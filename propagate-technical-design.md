# Propagate Technical Design

## 1. Purpose

This document describes the technical implementation for Propagate, a CLI-first tool for securely sharing environment variables across development teams.

The product requirements are defined in `propagate-prd.md`. This document turns those requirements into an implementation plan covering architecture, technology choices, data flows, user flows, database schema, encryption, access control, and operational edge cases.

The MVP implementation choices are:

- CLI: Go
- TUI: Bubble Tea ecosystem
- Database: Supabase Postgres
- Cloud API: Supabase Edge Functions in front of Supabase Postgres
- Secret model: end-to-end encrypted values, encrypted locally before upload
- Team configuration: Git-backed `propagate.yaml` that never stores env values

## 2. Design Principles

Propagate should be secure by default, predictable in Git workflows, and comfortable for developers who already use `.env` files.

Key principles:

- Plaintext env values never leave the user's machine during normal operation, whether they are secret, public, or local-only.
- `propagate.yaml` is safe to commit because it contains metadata and public keys only, never env values.
- Cloud state is authoritative for encrypted secrets and audit history.
- Git state is authoritative for human-reviewed team membership proposals.
- Admin approval requires an admin client because only clients can encrypt scope keys for newly approved members.
- The CLI should fail closed: if permissions, revisions, or encryption state are ambiguous, it should not write secrets.
- TUI screens should make risky operations explicit, especially imports, prod pulls, env overwrites, and access approvals.

## 3. High-Level Architecture

Propagate has four major components.

| Component | Responsibility |
| --- | --- |
| Go CLI | Command routing, local identity management, Git/config discovery, env file parsing, encryption/decryption, cloud API calls, non-interactive output |
| Bubble Tea TUI | Interactive setup, env import review, env push confirmation, config approval decisions |
| Supabase Edge Functions | HTTPS API, request signature verification, authorization checks, transaction orchestration, audit recording |
| Supabase Postgres | Persistent team metadata, config revisions, encrypted secret records, encrypted key envelopes, audit events, pull/update history |

The CLI must not connect to Supabase Postgres with privileged credentials. Shipping a Supabase service role key inside a desktop CLI would compromise the entire system. Instead, the CLI calls Edge Functions over HTTPS. Edge Functions verify signed requests from Propagate identities and perform database operations using server-side credentials.

The Supabase database stores ciphertext and metadata. The cloud can enforce authorization decisions and retain audit events, but it cannot decrypt secrets in end-to-end encryption mode.

## 4. Go CLI Structure

The CLI should be organized around small packages with clear boundaries.

| Area | Responsibility |
| --- | --- |
| Command layer | Defines `init`, `team`, `config`, and `env` command groups; handles flags, output mode, and exit codes |
| TUI layer | Bubble Tea models for setup, env import, env push, and config approval flows |
| Identity layer | Creates, loads, validates, and stores local signing/encryption keys and handle metadata |
| Config layer | Reads, validates, normalizes, and writes `propagate.yaml` |
| Git layer | Detects worktree root, checks tracked/ignored files, computes config diff hints |
| Env layer | Scans candidate env files, parses env files, preserves unrelated local variables, writes updates atomically |
| Crypto layer | Creates scope keys, encrypts/decrypts env values, encrypts/decrypts member access envelopes, signs API requests |
| API client | Sends signed HTTPS requests, handles retries, maps API errors into user-facing messages |
| Domain layer | Shared types for teams, scopes, members, permissions, config revisions, secret versions, and audit summaries |

Recommended Go libraries:

| Need | Recommendation |
| --- | --- |
| Command routing | Cobra or an equivalent command framework with stable flag handling |
| TUI | Bubble Tea, Bubbles, and Lip Gloss |
| YAML | A YAML parser that preserves enough structure for safe round-tripping where possible |
| Cryptography | Go standard cryptography packages plus an audited age-compatible library for recipient encryption |
| HTTPS | Go standard HTTP client with explicit timeout and retry policy |
| Keychain integration | A cross-platform keyring library, used when local key encryption is enabled |

The CLI should expose `--json` for status-style commands and `--dry-run` for commands that would write cloud or local state. The initial implementation can keep JSON output limited to stable status summaries, but command internals should avoid formatting-dependent logic so machine output can grow later.

## 5. Local Files And Directories

Propagate stores local user identity and cache data under `~/.propagate`.

| Path | Contents | Secret? |
| --- | --- | --- |
| `~/.propagate/identity` | Local private identity bundle | Yes |
| `~/.propagate/profile` | Handle, default API URL, local preferences | No sensitive secrets, but should still be private |
| `~/.propagate/cache` | Non-secret cloud metadata cache, such as last seen revisions | No |
| Project `propagate.yaml` | Team config, scopes, public keys, pending requests | No env values of any kind |

Filesystem permissions:

- `~/.propagate` should be created with owner-only permissions.
- Private identity files should be owner-read/write only.
- The CLI should warn and refuse to use private key files that are group- or world-readable unless the user explicitly repairs permissions.
- Writes to identity, config, and env files should be atomic: write a temporary file in the same directory, fsync where supported, then rename.

## 6. Identity Model

The PRD identifies a user by their Propagate public key. For the technical design, identity should separate signing from encryption while keeping one stable user identifier.

Each local identity contains:

| Field | Purpose |
| --- | --- |
| Signing private key | Signs API requests and config decisions |
| Signing public key | Primary public identity key |
| Encryption private key | Decrypts scope key envelopes |
| Encryption public key | Recipient key used by admins when granting access |
| Public key SHA | Stable identifier derived from the canonical signing public key |
| Handle | Human-readable display metadata |
| Created timestamp | Local diagnostics and audit context |

Why separate keys:

- Ed25519 is well suited for signatures and identity.
- X25519 or age-style recipients are well suited for encryption.
- Converting signing keys into encryption keys creates avoidable coupling and implementation risk.

`propagate.yaml` should include the signing public key, encryption public key, public key SHA, and handle for each member and pending join. The UI can continue to present this as one Propagate identity.

Local private key protection should support two modes:

- MVP baseline: owner-only file permissions, clear warnings on weak permissions, no cloud backup.
- Recommended secure mode: encrypt the local identity file using an OS keychain-protected secret where available, with passphrase fallback for unsupported environments.

If a user loses their private key, the cloud cannot recover their access. The user must create a new identity, submit a new join request, and have an admin approve new access envelopes.

## 7. Cryptography And Secret Storage

Propagate uses envelope encryption.

Each team scope has a random symmetric scope key. Environment variable values for that scope are encrypted with the scope key. The scope key is then encrypted separately for each member who has read access to that scope.

### 7.1 Keys

| Key | Created By | Stored Locally | Stored In Cloud | Rotation |
| --- | --- | --- | --- | --- |
| User signing key | User CLI | Private key only | Public key only | User creates a new identity |
| User encryption key | User CLI | Private key only | Public key only | User creates a new identity |
| Scope key | Admin or first setup CLI | Plaintext only in memory during operations | Only encrypted envelopes | Rotate when revoking access or after incidents |
| Secret value nonce | CLI during encryption | No long-term local storage | Stored with ciphertext | New nonce per value version |

### 7.2 Env Value Encryption

For each environment variable version:

- The CLI generates a fresh nonce.
- The plaintext value is encrypted locally using the current scope key.
- Associated data binds the ciphertext to team ID, scope, variable name, env file path, secret version, and algorithm version.
- The CLI uploads only ciphertext, nonce, algorithm metadata, and non-secret metadata.

All env values are handled through this encrypted path. The implementation should not special-case "public" env values into `propagate.yaml`, because that creates inconsistent behavior and makes accidental leakage more likely.

Variable names and env file paths are treated as metadata. They are visible to the cloud because the product needs status screens, diffs, and file mapping. The documentation and UI should be honest about this. If a team considers variable names sensitive, a later version can add encrypted names, but that adds complexity to querying, diffs, and status output.

### 7.3 Scope Key Envelopes

When a member is granted read access:

- An admin client obtains or decrypts the current scope key.
- The admin client encrypts that scope key to the member's encryption public key.
- The encrypted envelope is uploaded to the cloud.
- The member can later download the envelope and decrypt it locally with their encryption private key.

When a member is revoked:

- Their existing envelope is marked revoked and no longer returned by the API.
- Existing secret versions that the user already pulled cannot be clawed back.
- For meaningful revocation, an admin should rotate the scope key and re-encrypt current env values for remaining authorized members.
- MVP should support revocation metadata and warnings; automated rotation can be a follow-up if it is too large for the first release.

### 7.4 Request Signing

Every mutating cloud request should be signed by the local signing key.

The signed request should include:

- HTTP method
- Request path
- Body digest
- Timestamp
- Random nonce
- Public key SHA
- CLI version

The Edge Function validates the signature, rejects stale timestamps, rejects replayed nonces, loads the member record for the public key SHA, and evaluates permissions before touching team data.

## 8. Cloud API Design

The CLI talks to a small HTTPS API implemented as Supabase Edge Functions. The API should be coarse-grained around product workflows rather than exposing raw database tables.

Recommended endpoints by responsibility:

| API Area | Responsibilities |
| --- | --- |
| Identity lookup | Resolve current public key SHA, verify team membership, return role and accessible scopes |
| Team setup | Create team, first admin, scopes, initial config revision, initial encrypted secrets and envelopes |
| Config sync | Fetch config snapshot, compare revisions, push admin-approved config decisions |
| Secret read | Return encrypted scope key envelope and encrypted secret versions for an authorized member |
| Secret write | Accept encrypted secret upserts/deletions from authorized writers |
| Audit | Record pull, push, config, access, and error-relevant events |

The API must be idempotent where possible. Client-supplied operation IDs should be used for setup, config push, and env push so retries do not duplicate audit events or create duplicate versions.

## 9. Supabase Database Schema

The schema below is logical rather than SQL. Names can be adjusted during implementation, but the relationships and constraints should remain.

### 9.1 teams

Stores top-level team metadata.

| Column | Purpose |
| --- | --- |
| id | Stable team identifier |
| name | Display name |
| current_config_revision | Latest accepted config revision |
| created_by_key_sha | First admin identity |
| created_at | Creation timestamp |
| updated_at | Last metadata update |
| archived_at | Soft-delete marker for future use |

Important constraints:

- Team ID is globally unique.
- Team name does not need to be globally unique.
- Current config revision must match the latest accepted revision.

### 9.2 team_config_revisions

Stores canonical cloud revisions of the Git-backed config.

| Column | Purpose |
| --- | --- |
| id | Revision record ID |
| team_id | Parent team |
| revision_number | Monotonic team-local revision |
| config_hash | Hash of normalized non-secret config |
| config_snapshot | Normalized config metadata snapshot, without env values |
| pushed_by_key_sha | Admin who pushed the revision |
| pushed_at | Push timestamp |
| operation_id | Idempotency key |

Important constraints:

- One revision number per team.
- One operation ID per team for idempotent retries.
- Config snapshots must never include plaintext, masked, example, placeholder, public, or default env values.

### 9.3 members

Stores active and historical member identities.

| Column | Purpose |
| --- | --- |
| id | Member record ID |
| team_id | Parent team |
| handle | Display handle |
| public_key_sha | Stable identity identifier |
| signing_public_key | Public signing key |
| encryption_public_key | Public encryption recipient |
| role | Admin or developer for MVP |
| status | Active, revoked, or replaced |
| approved_by_key_sha | Admin who approved the member |
| approved_at | Approval timestamp |
| revoked_by_key_sha | Admin who revoked the member |
| revoked_at | Revocation timestamp |

Important constraints:

- Public key SHA is unique within a team.
- Active member signing public keys are unique within a team.
- Handles are not unique and must not be used for authorization.

### 9.4 scopes

Stores team scopes such as `dev`, `staging`, `prod`, or custom names.

| Column | Purpose |
| --- | --- |
| id | Scope record ID |
| team_id | Parent team |
| name | Scope name |
| kind | Built-in or custom |
| created_at | Creation timestamp |
| archived_at | Soft-delete marker |

Important constraints:

- Scope name is unique within a team.
- Built-in scopes should be normalized to lowercase reserved names.

### 9.5 env_file_mappings

Stores configured env files for each scope.

| Column | Purpose |
| --- | --- |
| id | Mapping record ID |
| team_id | Parent team |
| scope_id | Parent scope |
| path | Repository-relative env file path |
| config_revision | Revision where this mapping was accepted |
| active | Whether the mapping is current |

Important constraints:

- Paths must be relative, normalized, and inside the Git worktree.
- A scope can have multiple env file mappings.

### 9.6 scope_access_rules

Stores role-level and member-level access.

| Column | Purpose |
| --- | --- |
| id | Rule record ID |
| team_id | Parent team |
| scope_id | Scope the rule applies to |
| subject_type | Role or member |
| subject_value | Role name or member public key SHA |
| permission | None, read, write, or admin |
| config_revision | Revision where this rule was accepted |
| active | Whether the rule is current |

Important constraints:

- Member-specific rules override role rules.
- Write implies read.
- Admin implies write unless later policy introduces restrictions.

### 9.7 scope_key_envelopes

Stores encrypted scope keys for authorized members.

| Column | Purpose |
| --- | --- |
| id | Envelope record ID |
| team_id | Parent team |
| scope_id | Scope protected by this key |
| recipient_key_sha | Member public key SHA |
| scope_key_version | Scope key version |
| encrypted_scope_key | Ciphertext encrypted to recipient |
| algorithm | Envelope encryption algorithm |
| created_by_key_sha | Admin client that created the envelope |
| config_revision | Config revision that granted access |
| created_at | Creation timestamp |
| revoked_at | Revocation timestamp |

Important constraints:

- Only active envelopes for active members are returned.
- A member can have multiple historical envelopes, but only the newest non-revoked envelope for the current scope key version should be used.

### 9.8 secret_variables

Stores variable identity and current version pointers.

| Column | Purpose |
| --- | --- |
| id | Variable record ID |
| team_id | Parent team |
| scope_id | Parent scope |
| env_file_path | Repository-relative file path |
| name | Variable name |
| current_version_id | Current encrypted value version |
| deleted_at | Marker for variables removed from cloud state |
| created_at | Creation timestamp |
| updated_at | Last metadata update |

Important constraints:

- Variable name plus env file path is unique within a scope.
- Deleted variables remain as tombstones so pulls can explain removals and audits stay intact.

### 9.9 secret_versions

Stores immutable encrypted value versions.

| Column | Purpose |
| --- | --- |
| id | Secret version ID |
| variable_id | Parent variable |
| version_number | Monotonic variable-local version |
| ciphertext | Encrypted env value |
| nonce | Nonce used for value encryption |
| algorithm | Value encryption algorithm |
| scope_key_version | Scope key version used |
| plaintext_fingerprint | Optional keyed fingerprint for local diff support, never a raw hash |
| created_by_key_sha | Member who pushed this version |
| created_at | Creation timestamp |
| operation_id | Idempotency key |

Important constraints:

- Secret versions are immutable.
- The cloud must never store raw plaintext hashes of env values, because low-entropy values can be guessed offline.
- If diff optimization needs fingerprints, use a keyed construction where the key is unavailable to the cloud, or skip server-side value fingerprints for MVP.

### 9.10 audit_events

Stores append-only product audit history.

| Column | Purpose |
| --- | --- |
| id | Event ID |
| team_id | Parent team |
| actor_key_sha | Acting identity |
| actor_handle | Handle at event time |
| event_type | Created, joined, approved, declined, pulled, pushed, revoked, and related event types |
| scope_id | Optional scope |
| target_key_sha | Optional target member |
| config_revision | Relevant config revision |
| metadata | Structured non-secret event details |
| created_at | Event timestamp |

Important constraints:

- Audit events are append-only.
- Metadata must be scrubbed to prevent secret leakage.
- Pull and push events should include CLI version and env file paths.

### 9.11 request_nonces

Stores short-lived replay protection records for signed API requests.

| Column | Purpose |
| --- | --- |
| public_key_sha | Request signer |
| nonce | Random request nonce |
| expires_at | Expiration timestamp |
| first_seen_at | Timestamp when accepted |

Important constraints:

- A nonce can be accepted only once for a given public key SHA.
- Expired nonces should be cleaned up regularly.

## 10. Authorization Model

Authorization is evaluated on the server before returning encrypted data or accepting writes.

Evaluation order:

1. Verify request signature and replay protection.
2. Load active team member by public key SHA.
3. Determine member role.
4. Determine requested scope.
5. Apply member-specific scope rule if present.
6. Otherwise apply role-level scope rule.
7. Enforce command-specific permission.

Permission behavior:

| Permission | Capabilities |
| --- | --- |
| None | No secret read or write |
| Read | Pull env values and view env status |
| Write | Read plus push env changes |
| Admin | Manage config, approve joins, approve access changes, and write all scopes unless future policy restricts it |

The client should also perform local permission checks for early, friendly errors. The server remains authoritative.

## 11. Supabase Stored Functions

Supabase Edge Functions remain the public HTTPS boundary, but selected data-local logic should live in Postgres stored functions for performance, consistency, and transactional safety.

Stored functions should not expose raw tables directly to the CLI. Edge Functions should call them after request parsing and signature verification.

### 11.1 Responsibilities That Belong In Stored Functions

| Logic | Why It Fits In Postgres |
| --- | --- |
| Replay nonce reservation | Atomic insert with a unique index prevents race conditions and avoids a read-before-write lookup |
| Actor and permission resolution | Authorization depends on indexed joins across members, scopes, and access rules |
| Config revision update | Config push needs optimistic concurrency, revision increments, member changes, access rule changes, envelope inserts, and audit events in one transaction |
| Env pull bundle fetch | The server can return the active envelope, env file mappings, variable metadata, and current encrypted versions with one set-based query |
| Env push apply | Version checks, immutable version inserts, current pointer updates, tombstones, idempotency, and audit events should commit or roll back together |
| Audit event append | Audit rows should be written in the same transaction as the operation they describe |
| Audit summaries | `team status` can efficiently compute last pull per member, members who never pulled, and recent activity near the data |
| Expired nonce cleanup | Short-lived replay records can be removed by a scheduled function using the `expires_at` index |

### 11.2 Responsibilities That Should Stay Outside Stored Functions

| Logic | Where It Belongs | Why |
| --- | --- | --- |
| Request canonicalization | Edge Function | It is protocol logic and should be easy to version with the API |
| Signature verification | Edge Function | Ed25519 verification and timestamp checks are easier to implement safely in the API runtime |
| Encryption and decryption | CLI | The database must never see plaintext env values or plaintext scope keys |
| Scope key envelope creation | CLI | Admin clients encrypt scope keys for recipients in the end-to-end encryption model |
| YAML parsing and validation | CLI, with server-side metadata validation | Git config is local file state; server should only validate normalized metadata snapshots |
| Env file parsing and merging | CLI | This depends on local filesystem state and user confirmation |
| TUI approval decisions | CLI | Human interaction belongs in the Bubble Tea client |
| Masking values for display | CLI | Plaintext should never reach the server just to produce masked output |

### 11.3 Recommended Stored Function Boundaries

The MVP should define stored functions around product operations rather than low-level table access.

Recommended functions:

| Function Area | Responsibility |
| --- | --- |
| Reserve request nonce | Insert a nonce for a signer and reject duplicates atomically |
| Resolve member access | Return active member, role, scope, and effective permission for a requested operation |
| Create team | Create team, first admin, scopes, initial config revision, encrypted secret records, first envelopes, and audit events |
| Push config revision | Apply admin-approved config decisions with expected revision checks and idempotency |
| Fetch config snapshot | Return the current normalized config metadata and revision |
| Fetch env pull bundle | Return encrypted values, active envelope, env file mappings, and safe metadata for an authorized reader |
| Apply env push | Apply encrypted upserts and tombstones with expected version checks and idempotency |
| Record pull event | Append pull audit metadata after the CLI successfully receives the encrypted payload |
| Fetch team status | Return member list, pending/recent access metadata, and audit summaries |

The Edge Function should still enforce the public API contract. Stored functions should enforce data invariants and transaction semantics.

### 11.4 Performance And Indexing

Stored functions must be set-based and index-backed. Hot paths should avoid loops over large result sets, dynamic SQL, and repeated scans of JSON snapshots.

Required indexes:

| Table | Index |
| --- | --- |
| `request_nonces` | Unique index on `public_key_sha` and `nonce` |
| `request_nonces` | Index on `expires_at` for cleanup |
| `members` | Unique index on `team_id` and `public_key_sha` |
| `scopes` | Unique index on `team_id` and `name` |
| `scope_access_rules` | Index on `team_id`, `scope_id`, `subject_type`, and `subject_value` |
| `scope_key_envelopes` | Index on `team_id`, `scope_id`, `recipient_key_sha`, `scope_key_version`, and `revoked_at` |
| `secret_variables` | Unique index on `team_id`, `scope_id`, `env_file_path`, and `name` |
| `secret_variables` | Index on `team_id`, `scope_id`, and `deleted_at` for pulls |
| `secret_versions` | Unique index on `variable_id` and `version_number` |
| `team_config_revisions` | Unique index on `team_id` and `revision_number` |
| `team_config_revisions` | Unique index on `team_id` and `operation_id` for idempotent config pushes |
| `secret_versions` | Unique index on `variable_id` and `operation_id` where operation IDs are used for retries |
| `audit_events` | Index on `team_id` and `created_at` |
| `audit_events` | Index on `team_id`, `actor_key_sha`, and `created_at` |
| `audit_events` | Index on `team_id`, `event_type`, and `created_at` for status summaries |

Replay protection should use atomic insert semantics. The server should not perform a separate lookup and then insert, because concurrent duplicate requests could pass the lookup at the same time. A unique constraint on `public_key_sha` and `nonce` lets Postgres reject replays safely.

Nonce storage is expected to stay small because rows are short-lived. The cleanup window should be slightly longer than the accepted timestamp skew, so delayed duplicates are still rejected until they are no longer useful.

### 11.5 Transaction Rules

Config push and env push should be single database transactions.

Config push transaction requirements:

- Verify the actor is an active admin.
- Verify expected cloud config revision.
- Enforce idempotency through the operation ID.
- Apply approved member and access changes.
- Insert encrypted scope key envelopes supplied by the admin client.
- Insert a new config revision snapshot without env values.
- Update the team current revision.
- Append audit events.

Env push transaction requirements:

- Verify the actor has write access to the scope.
- Verify expected current secret version IDs for changed and removed variables.
- Insert immutable encrypted secret versions.
- Update current version pointers.
- Mark approved removals as tombstones.
- Enforce idempotency through the operation ID.
- Append audit events.

If any step fails, the transaction must roll back. The CLI can retry with the same operation ID when the failure is transient.

### 11.6 Stored Function Security

Stored functions are part of the trusted backend, not a public client API.

Security rules:

- The CLI should never call stored functions directly.
- Edge Functions should verify request signatures before invoking stored functions.
- Stored functions should derive roles and permissions from database state, not from client-provided claims.
- Functions that need elevated privileges should use tightly scoped execution privileges and a fixed search path.
- Function inputs should use team IDs, public key SHAs, scope IDs, operation IDs, ciphertext, envelopes, and metadata only.
- Function inputs and outputs must never contain plaintext env values or plaintext scope keys.
- Raw table access should not be granted to anonymous or end-user Supabase roles.

## 12. Config File Design

`propagate.yaml` is committed to Git and contains non-secret metadata. It must never contain env values of any kind, including values that look public, harmless, masked, placeholder-only, or default-like.

Logical sections:

| Section | Contents |
| --- | --- |
| Version | Config format version |
| Team | Team ID, name, cloud revision |
| Scopes | Scope names, env file mappings, default role access |
| Members | Active members, handles, public key SHA, public keys, roles |
| Pending | Join requests, role changes, scope access changes |
| History | Optional local history of declined or resolved requests, without env values |

Config validation should check:

- Known config version.
- Team ID and cloud revision format.
- No env value fields or value-like literals in scope, variable, pending, member, history, or metadata sections.
- Valid public keys and public key SHA matches.
- Scope names are valid and unique.
- Env file paths are relative and inside the Git worktree.
- Pending requests do not duplicate active members.
- Role and permission values are known.

Config writes should preserve comments and ordering where possible. If round-trip preservation is not reliable, the CLI should generate a stable normalized file and clearly report that it updated formatting.

## 13. Core User Flows

### 13.1 First Admin Setup

1. User runs `propagate init`.
2. CLI creates or loads local identity.
3. CLI verifies the current directory is inside a Git worktree.
4. CLI checks for existing `propagate.yaml`.
5. If none exists, CLI starts project setup.
6. CLI asks for team name.
7. CLI scans candidate env files inside the Git worktree.
8. Bubble Tea import UI shows files, variable names, masked values, detected source, and proposed scopes.
9. User selects scopes and confirms import.
10. CLI creates team metadata, first admin member, scopes, and env mappings.
11. CLI creates scope keys for selected scopes.
12. CLI encrypts selected env values locally.
13. CLI creates first admin scope key envelopes.
14. CLI sends a signed setup request to the cloud API.
15. Edge Function creates the team transactionally and records audit events.
16. CLI writes `propagate.yaml` with the returned team ID and cloud revision, but without env values.
17. CLI prints a summary with identity path, created scopes, uploaded variable count, and next Git steps.

Failure handling:

- If cloud setup fails, no config should be written unless it is clearly marked as incomplete.
- If config writing fails after cloud setup succeeds, the CLI should print recovery instructions and allow `config pull` by team ID if possible.
- If env import is canceled, no cloud team should be created.

### 13.2 Developer Join

1. Developer runs `propagate init` to create or load identity.
2. Developer runs `propagate team join`.
3. CLI reads `propagate.yaml`.
4. CLI adds a pending join request with handle, signing public key, encryption public key, public key SHA, requested role, requested scopes, and timestamp.
5. CLI writes `propagate.yaml`.
6. CLI explains that no secret access has been granted.
7. Developer commits the config diff and opens a pull request.

The cloud does not need to know about the request until an admin pushes the approved config. This keeps the access request reviewable in Git.

### 13.3 Admin Config Push

1. Admin pulls the latest Git branch containing pending config changes.
2. Admin runs `propagate config push`.
3. CLI loads local identity and `propagate.yaml`.
4. CLI fetches cloud config revision.
5. CLI compares local revision and cloud revision.
6. If revisions conflict, CLI refuses to push until the admin pulls or resolves the conflict.
7. Bubble Tea approval UI shows each pending join and access change.
8. Admin approves, declines, or skips each item.
9. For approvals, CLI prepares updated config metadata.
10. For approved scope access, CLI decrypts the relevant scope key and encrypts a new envelope for the target member.
11. CLI sends a signed config push with expected cloud revision, decisions, updated config snapshot, and new envelopes.
12. Edge Function validates admin permission and applies the transaction.
13. CLI updates `propagate.yaml` to reflect approved and declined decisions while leaving skipped items pending.
14. CLI prints a decision summary and whether the config file changed.

Skipped items remain local and reviewable. Declined items are removed from pending and recorded in audit events.

### 13.4 Config Pull And Status

`propagate config status` compares local config hash and revision against cloud state and reports:

- Local revision.
- Cloud revision.
- Local-only changes.
- Cloud-only changes.
- Recommended next action.

`propagate config pull` fetches the cloud config snapshot and updates local `propagate.yaml`.

If local unpushed changes exist, the CLI should warn before overwriting and offer a dry-run summary. The default should be conservative: do not overwrite local pending requests without explicit confirmation.

### 13.5 Env Pull

1. User runs `propagate env pull`, optionally selecting a scope.
2. CLI loads identity and config.
3. CLI determines env file mappings for the scope.
4. CLI sends a signed read request.
5. Server verifies read access and returns encrypted secret versions plus the member's active scope key envelope.
6. CLI decrypts the scope key locally.
7. CLI decrypts env values locally.
8. CLI merges values into configured env files.
9. CLI preserves unrelated variables and comments where possible.
10. CLI records a pull event through the API.
11. CLI prints which files were updated and how many variables changed.

For `prod`, the CLI should require an additional confirmation before writing to a local env file unless the user has configured a trusted non-interactive mode.

### 13.6 Env Push

1. User runs `propagate env push`, optionally selecting a scope.
2. CLI loads identity and config.
3. CLI reads configured env files for the selected scope.
4. CLI requests current encrypted cloud values and the user's scope envelope.
5. CLI decrypts current values locally.
6. CLI computes added, changed, and removed variables.
7. Bubble Tea confirmation UI shows masked old and new values.
8. User approves all or selected changes.
9. CLI confirms server-side write access before upload.
10. CLI encrypts approved new values locally.
11. CLI sends encrypted upserts and tombstones with expected current versions.
12. Edge Function validates write permission and version preconditions.
13. Database transaction creates immutable secret versions, updates current pointers, and records audit events.
14. CLI prints a masked summary.

If the user lacks write access, the CLI should refuse before upload and explain how to request access.

### 13.7 Team Status

`propagate team status` should combine local config and cloud audit summaries:

- Team name.
- Current identity and role.
- Current public key SHA.
- Members grouped by role.
- Pending joins and access changes from local config.
- Last pull per member and scope from cloud events.
- Members with no recorded pulls.

The command should work partially offline by showing local config data and clearly marking cloud audit data as unavailable.

## 14. Env File Handling

Env file parsing must be careful because `.env` formats vary.

Supported behavior:

- Preserve unrelated variables during pull.
- Preserve comments and ordering where practical.
- Support quoted values.
- Support empty values.
- Support common `export NAME=value` lines.
- Avoid evaluating shell expressions.
- Avoid expanding variables during parsing.
- Detect duplicate variable names within a file and warn.
- Detect duplicate variable names across files in the same scope and warn.

Write behavior:

- Pull should update managed variables and preserve unrelated local variables.
- Removed cloud variables should not be deleted locally by default in MVP; instead, the CLI should warn and offer deletion confirmation.
- Push should represent approved removals as cloud tombstones.
- Writes should be atomic and should create a short-lived backup only if it can be stored securely and cleaned up.

The CLI must warn if selected env files are tracked by Git. It should also offer to add common env file patterns to `.gitignore`, but only after explicit user confirmation.

## 15. Monorepo Discovery

The scanner should only inspect directories that belong to the Git worktree.

Discovery approach:

1. Find the Git worktree root.
2. Read tracked project paths from Git.
3. Identify likely project roots such as root, apps, packages, and services directories.
4. Search for known env file names only within those project roots.
5. Include ignored env files if their parent directory is part of the tracked project.
6. Exclude dependency folders, build outputs, caches, fixtures, examples, and directories outside the worktree.

The scanner should be intentionally conservative. It is better to miss an unusual env file and let the user add it manually than to scan arbitrary untracked folders.

The TUI should show:

- Candidate file path.
- Whether the parent path is Git-tracked.
- Variables found.
- Masked values.
- Proposed scope.
- Include/exclude state.

## 16. TUI Design

Bubble Tea should be used for interactive flows only. Non-interactive command output should remain clear and script-friendly.

Common TUI requirements:

- Keyboard-first navigation.
- Clear focused item.
- Safe defaults.
- Cancel path available from every screen.
- No plaintext env value display.
- Confirmation screens for cloud writes and local env writes.
- Final summary that can be copied into PR descriptions or team messages.

TUI models should be deterministic state machines. They should receive domain data, produce user decisions, and avoid direct database or filesystem writes. The command layer performs side effects after the TUI returns a decision.

## 17. Revision And Conflict Handling

Propagate has two important revision systems:

- Config revision: monotonic team-level revision for `propagate.yaml` cloud sync.
- Secret version: immutable per-variable version for encrypted env values.

Config push must include the expected cloud config revision. If the cloud revision has changed, the server rejects the push. The CLI then instructs the admin to run `propagate config pull`, review the diff, and retry.

Env push should include expected current secret version IDs for changed or removed variables. If another user updated the same variable first, the server rejects only the conflicting variables where possible. The CLI should show a conflict summary and ask the user to pull before retrying.

This prevents accidental overwrites without requiring pessimistic locks.

## 18. Security Requirements

### 18.1 Plaintext Handling

The CLI should treat plaintext env values as sensitive.

Rules:

- Do not log plaintext values.
- Do not include plaintext values in errors.
- Do not include plaintext values in analytics or audit metadata.
- Do not write env values to `propagate.yaml`, including public, placeholder, default, example, or masked values.
- Mask values in all TUI and terminal output.
- Keep plaintext in memory only as long as needed.
- Avoid writing temporary plaintext files.
- Ensure panic and crash paths do not dump secret-containing structures.

Go cannot guarantee immediate memory zeroization for all strings. The implementation should still minimize copies and use byte slices for sensitive crypto operations where practical.

### 18.2 Cloud Trust Boundary

In end-to-end encryption mode, Supabase is trusted for availability, metadata storage, authorization checks, and audit history. It is not trusted with plaintext env values or plaintext scope keys.

A compromised cloud database could expose:

- Team names.
- Handles.
- Public keys.
- Scope names.
- Env file paths.
- Variable names.
- Ciphertexts.
- Audit metadata.

It should not expose:

- Plaintext env values.
- User private keys.
- Plaintext scope keys.

This threat model should be stated in user-facing security documentation.

### 18.3 Server-Side Authorization

Server authorization must not rely on client-provided role claims. The server derives roles and permissions from the database.

Every API handler should verify:

- Valid signature.
- Non-expired timestamp.
- Non-replayed nonce.
- Active team membership where required.
- Required permission for the specific scope and operation.
- Expected revision or version preconditions for writes.

### 18.4 Admin Approval Safety

Admin approval is security-critical.

The Config Push TUI should show:

- Full handle.
- Public key SHA.
- Requested role.
- Requested scopes.
- Whether the same key or handle already exists.
- Whether the request came from local config changes not yet committed.

The CLI should encourage admins to review the Git diff before approving. It should not auto-approve pending joins.

### 18.5 Revocation Limits

Revocation cannot erase values already pulled to a developer's machine. The CLI and docs should say this plainly.

After revoking access, the product should recommend:

- Rotate affected scope keys.
- Update all env values in the affected scope if exposure is suspected.
- Re-encrypt current values only for remaining authorized members.
- Commit the updated config after config push.

### 18.6 Production Scope Guardrails

`prod` should be treated as high-risk.

Guardrails:

- Extra confirmation before importing prod env values.
- Extra confirmation before pulling prod env values to local files.
- Clear display of identity and target file path.
- Refuse non-interactive prod writes unless an explicit flag or config setting is present.
- Prefer admin-only write access by default.

## 19. Edge Cases And Expected Behavior

| Edge Case | Expected Behavior |
| --- | --- |
| No Git repository | `init` refuses project setup and explains that MVP requires Git |
| Existing invalid `propagate.yaml` | Command refuses to continue until repaired or backed up |
| Corrupted local identity | CLI refuses to use it and gives recovery options |
| Lost private key | User must create a new identity and request access again |
| Duplicate handle | Allowed, but UI highlights public key SHA because handle is not identity |
| Duplicate public key in pending joins | CLI warns and avoids adding duplicate request |
| Pending join for existing member | CLI rejects the pending request locally |
| Cloud unavailable | Local-only commands can proceed; cloud writes fail with retry guidance |
| Config revision mismatch | Push rejected; user must pull and resolve |
| Secret version conflict | Env push rejected for conflicting variables; user must pull latest |
| User lacks read access | No files written; error names scope and current identity |
| User lacks write access | No upload attempted; error names scope and current identity |
| Env file missing during pull | CLI asks to create it or skips in non-interactive mode |
| Env file has duplicate names | CLI warns and requires confirmation before managing that file |
| Existing local value differs from cloud during pull | CLI prompts before overwrite unless a merge mode is specified |
| Removed cloud variable exists locally | CLI warns; default is preserve local value in MVP |
| Tracked `.env` file | CLI warns strongly and offers `.gitignore` help |
| File permissions too broad | CLI warns for env files and refuses for private identity files |
| Interrupted write | Atomic writes should leave either old or new file, not a partial file |
| Clock skew | Server allows small skew window and returns a clear clock error if exceeded |
| Replay attempt | Server rejects reused nonce and records security-relevant audit metadata |
| Admin cannot decrypt scope key | Config approval for that scope fails; admin must pull access or recover key |
| Supabase transaction partially fails | Transaction rolls back; CLI retry uses same operation ID |

## 20. Observability And Auditing

The product needs auditability without leaking secrets.

Audit events should record:

- Team creation.
- Config pushes and pulls.
- Join requests observed during config push.
- Join approvals and declines.
- Scope access grants and revocations.
- Env pulls.
- Env pushes.
- Failed authorization attempts where useful and safe.

Audit metadata should include:

- Actor public key SHA.
- Actor handle at event time.
- Scope.
- Env file path.
- Config revision.
- CLI version.
- Operation ID.
- Counts of variables added, changed, removed, or pulled.

Audit metadata should not include:

- Plaintext values.
- Masked values if the mask might leak too much for short secrets.
- Raw hashes of plaintext values.
- Local filesystem absolute paths beyond the repository-relative env file mapping.

## 21. Testing Strategy

Testing should cover both product behavior and security invariants.

Recommended coverage:

| Test Area | What To Verify |
| --- | --- |
| Config validation | Invalid roles, invalid keys, duplicate scopes, unsafe paths, env value fields, value-like literals |
| Identity | Key creation, permission checks, corrupted files, public key SHA stability |
| Crypto | Round-trip encryption, wrong recipient failure, associated data mismatch failure, nonce uniqueness |
| Env parsing | Quoting, comments, empty values, duplicate names, preserve unrelated values |
| TUI decisions | Approval, decline, skip, cancel, selection state, masked values |
| API authorization | Read/write/admin permissions, revoked members, replay rejection, revision preconditions |
| Supabase transactions | Config push atomicity, env push conflicts, idempotent retries |
| End-to-end flows | First setup, join request, admin approval, env pull, env push, revoke access |

Security-specific tests should assert that plaintext env values never appear in logs, command output, audit rows, config snapshots, `propagate.yaml`, or API error bodies. This applies to public and placeholder env values as well as secrets.

## 22. Deployment And Operations

Supabase should be managed with migrations committed to the repository. Each schema change should have a forward migration and a rollback strategy where practical.

Operational recommendations:

- Separate development, staging, and production Supabase projects.
- Keep service role keys only in Supabase Edge Function secrets or deployment environment variables.
- Enable database backups and point-in-time recovery for production.
- Use database constraints to reinforce application invariants.
- Use scheduled cleanup for expired request nonces.
- Monitor Edge Function errors, database transaction failures, and authorization failure rates.
- Version API responses so older CLIs can fail gracefully when the server requires an upgrade.

## 23. MVP Implementation Phases

### Phase 1: Local Foundations

- Go command skeleton.
- Local identity creation and loading.
- `propagate.yaml` parser and validator.
- Git worktree detection.
- Env scanner and parser.
- Basic masked output helpers.

### Phase 2: Cryptography

- Scope key generation.
- Env value encryption and decryption.
- Scope key envelopes.
- Request signing and verification test fixtures.
- Local key permission checks.

### Phase 3: Cloud Backend

- Supabase migrations.
- Edge Functions for team setup, config sync, secret read, secret write, and audit events.
- Transaction and idempotency handling.
- Permission enforcement.

### Phase 4: Core CLI Flows

- `propagate init`.
- `propagate team join`.
- `propagate config status`.
- `propagate config pull`.
- `propagate config push`.
- `propagate env pull`.
- `propagate env push`.
- `propagate env status`.
- `propagate team status`.

### Phase 5: TUI Polish And Safety

- Env import TUI.
- Env push TUI.
- Config push TUI.
- Prod guardrails.
- Clear summaries and error messages.
- JSON and dry-run support for selected commands.

### Phase 6: Hardening

- End-to-end test suite.
- Security tests for no plaintext leakage.
- Conflict handling polish.
- Cross-platform filesystem behavior.
- Documentation for recovery, revocation, and team workflows.

## 24. Open Technical Decisions

The PRD leaves several decisions open. Recommended MVP answers:

| Question | Recommendation |
| --- | --- |
| `propagate.yaml` or `propagate.yml` | Use `propagate.yaml` as canonical; detect `propagate.yml` and suggest rename |
| Require Git repository | Yes for MVP, because access review depends on Git workflow |
| SSH key or dedicated key format | Use a dedicated Propagate identity bundle with Ed25519 signing and X25519 or age encryption keys; display it as one identity |
| First admin | The user who successfully creates the team during `propagate init` becomes first admin |
| Scope key granularity | One key per scope for MVP; reconsider per env file if teams need finer isolation |
| Developer write access | Developers can read `dev` by default; write access should be explicit or admin-configured |
| Pull overwrite behavior | Prompt before overwriting existing differing values; non-interactive mode preserves unless explicitly told to overwrite |
| Removed variables during pull | Preserve locally by default and warn; deletion requires explicit confirmation |
| Revocation rotation | Record revocation in MVP; add guided or automated scope key rotation as soon as practical |

## 25. Summary

The safest MVP architecture is a Go CLI that performs all encryption locally, a Bubble Tea TUI for high-risk human decisions, Supabase Edge Functions as the signed API boundary, and Supabase Postgres as the encrypted metadata and audit store.

The most important implementation detail is preserving the end-to-end encryption boundary: Supabase can store and authorize access to encrypted material, but only local Propagate clients with valid private keys can decrypt scope keys and env values.

The second most important detail is revision discipline. Config pushes and env pushes should always carry expected revisions or versions so Propagate does not silently overwrite team decisions or secret updates.
