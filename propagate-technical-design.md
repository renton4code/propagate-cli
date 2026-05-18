# Propagate Technical Design

## 1. Purpose

This document describes the technical implementation for Propagate, a CLI-first tool for securely sharing environment variables across development teams.

The product requirements are defined in `propagate-prd.md`. This document turns those requirements into an implementation plan covering architecture, technology choices, data flows, user flows, database schema, encryption, access control, and operational edge cases.

The MVP implementation choices are:

- CLI: Go
- TUI: Bubble Tea ecosystem
- Database: Supabase Postgres
- Cloud API: Go HTTPS API deployed on Google Cloud Run
- Infrastructure: Terraform for Google Cloud and Supabase project resources
- Database changes: versioned SQL migrations for schema, indexes, policies, and stored functions
- Secret model: end-to-end encrypted values, encrypted locally before upload
- Team configuration: Git-backed `propagate.yaml` with safe variable declarations

## 2. Design Principles

Propagate should be secure by default, predictable in Git workflows, and comfortable for developers who already use `.env` files.

Key principles:

- Sensitive plaintext env values never leave the user's machine during normal operation. Public-looking values are still sensitive by default; explicitly `non_sensitive` short literals or previews may be written to Git-backed config and cloud metadata.
- `propagate.yaml` is safe to commit because sensitive values are represented only by scope-keyed, algorithm-prefixed digests such as `hmac-sha-256:v1:...`. Explicitly non-sensitive short values may be stored as literals; longer non-sensitive values are stored as previews such as `aaa...zzz`.
- Cloud state is authoritative for encrypted secrets and audit history.
- Git state is authoritative for human-reviewed team membership proposals.
- Management approval requires an approving client because only clients can encrypt scope keys for newly approved members.
- The CLI should fail closed: if permissions, revisions, or encryption state are ambiguous, it should not write secrets.
- TUI screens should make risky operations explicit, especially imports, prod pulls, env overwrites, and access approvals.

## 3. High-Level Architecture

Propagate has five major components.

| Component | Responsibility |
| --- | --- |
| Go CLI | Command routing, local identity management, Git/config discovery, env file parsing, encryption/decryption, cloud API calls, non-interactive output |
| Bubble Tea TUI | Interactive setup, env import review, env push confirmation, config approval decisions, config variable metadata editing |
| Go Cloud Run API | HTTPS API, request signature verification, authorization checks, transaction orchestration, audit recording |
| Supabase Postgres | Persistent team metadata, config revisions, encrypted secret records, encrypted key envelopes, audit events, pull/update history |
| Terraform and migrations | Provision Google Cloud/Supabase infrastructure and apply versioned database changes |

The CLI must not connect to Supabase Postgres with privileged credentials. Shipping a Supabase service role key or database password inside a desktop CLI would compromise the entire system. Instead, the CLI calls the Go API on Cloud Run over HTTPS. The Cloud Run API verifies signed requests from Propagate identities and performs database operations using server-side credentials stored outside the CLI.

The Supabase database stores ciphertext and metadata. The cloud can enforce authorization decisions and retain audit events, but it cannot decrypt secrets in end-to-end encryption mode.

## 4. Go CLI Structure

The CLI should be organized around small packages with clear boundaries.

| Area | Responsibility |
| --- | --- |
| Command layer | Defines `init`, `team`, `scope`, `config`, and `env` command groups; handles flags, output mode, output style, and exit codes |
| TUI layer | Bubble Tea models for setup, env import, env push, config approval, and config variable edit flows |
| Identity layer | Creates, loads, validates, and stores local signing/encryption keys and handle metadata |
| Config layer | Reads, validates, normalizes, and writes `propagate.yaml` |
| Git layer | Detects worktree root, checks tracked/ignored files, computes config diff hints |
| Env layer | Scans candidate env files, parses env files, preserves unrelated local variables, writes updates atomically |
| Agent guidance layer | Detects agent instruction targets, renders Propagate skills or managed instruction blocks, previews diffs, and writes safe repo-local guidance |
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
| Agent guidance rendering | A small internal template and diff package; avoid shelling out to agent-specific tooling for MVP |

The CLI should expose `--json` for status-style commands and `--dry-run` for commands that would write cloud or local state. The initial implementation can keep JSON output limited to stable status summaries, but command internals should avoid formatting-dependent logic so machine output can grow later.

### 4.1 Human Output Rendering

The command layer should use a shared renderer for non-JSON terminal output. `propagate init` is the style baseline, and every command result renderer should use the same primitives rather than hand-formatting headings independently.

The shared output renderer should provide:

- A command title helper that prints a bold title and appends `(dry run)` when relevant.
- Semantic status helpers for success, note, and warning lines:
  - success: green `✓`
  - note: cyan `•`
  - warning: yellow `!`
- Section helpers for `Warnings:`, `Next steps:`, and repeated list sections.
- A single `--no-color` switch that removes ANSI color without changing symbols, wording, or layout.
- A guarantee that JSON output never includes ANSI color or decorative terminal formatting.

Human result renderers should use this shared style for the command groups:

- `propagate init`
- `propagate run`
- `propagate team join`
- `propagate team invite`
- `propagate team status`
- `propagate scope create`
- `propagate config status`
- `propagate config pull`
- `propagate config push`
- `propagate config edit`
- `propagate env pull`
- `propagate env push`
- `propagate env set`
- `propagate env status`

Shared error rendering should use the same warning marker and `Next steps:` section style. Help and version output may stay plain.

## 5. Local Files And Directories

Propagate stores local user identity and cache data under `~/.propagate`.

| Path | Contents | Secret? |
| --- | --- | --- |
| `~/.propagate/identity` | Local private identity bundle | Yes |
| `~/.propagate/profile` | Handle, default API URL, local preferences | No sensitive secrets, but should still be private |
| `~/.propagate/cache` | Non-secret cloud metadata cache, such as last seen revisions | No |
| Project `propagate.yaml` | Team config, scopes, public keys, pending requests, safe variable declarations | No sensitive plaintext or raw plaintext hashes |
| Project agent instruction or skill files | Repo-local guidance for AI coding agents | No env values, private keys, decrypted output, or tokens |

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
| Encryption public key | Recipient key used when granting access |
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

If a user loses their private key, the cloud cannot recover their access. The user must create a new identity, submit a new join request, and have a management member approve new access envelopes.

## 7. Cryptography And Secret Storage

Propagate uses envelope encryption.

Each team scope has a random symmetric scope key. Environment variable values for that scope are encrypted with the scope key. The scope key is then encrypted separately for each member who has read access to that scope.

### 7.1 Keys

| Key | Created By | Stored Locally | Stored In Cloud | Rotation |
| --- | --- | --- | --- | --- |
| User signing key | User CLI | Private key only | Public key only | User creates a new identity |
| User encryption key | User CLI | Private key only | Public key only | User creates a new identity |
| Scope key | Management member or first setup CLI | Plaintext only in memory during operations | Only encrypted envelopes | Rotate when revoking access or after incidents |
| Secret value nonce | CLI during encryption | No long-term local storage | Stored with ciphertext | New nonce per value version |

### 7.2 Env Value Encryption

For each environment variable version:

- The CLI generates a fresh nonce.
- The plaintext value is encrypted locally using the current scope key.
- Associated data binds the ciphertext to team ID, scope, variable name, env file path, secret version, and algorithm version.
- The CLI uploads ciphertext, nonce, algorithm metadata, safe variable declarations, and non-secret metadata.

All managed values are uploaded as encrypted secret versions for cloud storage. Variable declarations in `propagate.yaml` default to sensitive and store a scope-keyed HMAC digest of the value using the explicit prefix `hmac-sha-256:v1:`. The digest key is the scope key, so a committed YAML file is not useful for offline guessing without secret access. Users may explicitly mark a variable `non_sensitive`; short one-line values can then be stored as literals, while longer values are truncated to previews and retain a keyed digest.

Variable names and env file paths are treated as metadata. They are visible to the cloud because the product needs status screens, diffs, and file mapping. The documentation and UI should be honest about this. If a team considers variable names sensitive, a later version can add encrypted names, but that adds complexity to querying, diffs, and status output.

### 7.3 Scope Key Envelopes

When a member is granted read access:

- A management client obtains or decrypts the current scope key.
- The management client encrypts that scope key to the member's encryption public key.
- The encrypted envelope is uploaded to the cloud.
- The member can later download the envelope and decrypt it locally with their encryption private key.

When a member is revoked:

- Their existing envelope is marked revoked and no longer returned by the API.
- Existing secret versions that the user already pulled cannot be clawed back.
- For meaningful revocation, a management member should rotate the scope key and re-encrypt current env values for remaining authorized members.
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

The Go Cloud Run API validates the signature, rejects stale timestamps, rejects replayed nonces, loads the member record for the public key SHA, and evaluates permissions before touching team data.

### 7.5 Invite Relay Encryption

PIN invites grant immediate secret access via server-mediated re-encryption. This is the only flow where plaintext scope keys briefly exist in server memory.

**Relay key:**

- A server-side X25519 keypair stored in Google Cloud Secret Manager (never in the database).
- The relay public key is exposed through a public API endpoint so management CLIs can encrypt scope keys to it at invite creation time.
- The relay private key is loaded into Cloud Run memory only during invite redemption.
- Relay keys should be versioned (`relay_key_version`) to support rotation.

**Invite creation (management CLI):**

1. Management CLI fetches the server's current relay public key.
2. For each scope granted by the invite, CLI decrypts the scope key locally (using the management member's envelope).
3. CLI encrypts each scope key to the relay public key, producing a relay-encrypted scope key bundle.
4. CLI uploads the bundle alongside the invite record and PIN verifier.

**Invite redemption (server):**

1. Server validates the PIN against the stored verifier.
2. Server loads the relay private key from Secret Manager.
3. Server decrypts each scope key from the relay-encrypted bundle (plaintext exists only in memory, never persisted).
4. Server re-encrypts each scope key to the joiner's encryption public key (received in the redemption request).
5. Server stores the resulting scope key envelopes as active envelopes for the new member.
6. Server creates the active member record with the invite's pre-approved scopes.
7. Server deletes the relay-encrypted bundle from the invite record (no longer needed).
8. Server records audit events and returns the envelopes to the joiner's CLI.

**Trust model:**

- For invite flows only, the server briefly holds plaintext scope keys in memory during re-encryption. This is explicitly scoped and documented.
- Manual `config push` approval retains full end-to-end encryption (scope keys never touch the server).
- If both the relay private key and the database are compromised simultaneously, unredeemed invite bundles could be decrypted. Mitigations: short invite TTL, relay key in Secret Manager with restricted IAM, bundles deleted after redemption.

**Relay key rotation:**

1. Generate a new relay keypair; store in Secret Manager as the new active version.
2. Re-encrypt active (unredeemed) invite bundles: decrypt with old relay key, re-encrypt with new relay key, update `relay_key_version`.
3. Retire the old relay key after all active invites using it are redeemed, revoked, or expired.

## 8. Cloud API Design

The CLI talks to a small HTTPS API implemented as a Go service deployed on Google Cloud Run. The API should be coarse-grained around product workflows rather than exposing raw database tables.

Cloud Run is the signed API boundary. Supabase Postgres is the data store and transactional logic layer. The Go API owns HTTP routing, request canonicalization, signature verification, replay protection orchestration, API versioning, request/response schemas, and user-facing error mapping. Postgres stored functions own data-local transactions and invariants.

Recommended endpoints by responsibility:

| API Area | Responsibilities |
| --- | --- |
| Identity lookup | Resolve current public key SHA, verify team membership, return management bit and accessible scopes |
| Team setup | Create team, first management member, scopes, initial config revision, initial encrypted secrets and envelopes |
| Config sync | Fetch config snapshot, compare revisions, push management-approved config decisions |
| Secret read | Return encrypted scope key envelope and encrypted secret versions for an authorized member |
| Secret write | Accept encrypted secret upserts/deletions from authorized writers, including single-value updates from `env set` |
| Audit | Record pull, push, config, access, and error-relevant events |

The API must be idempotent where possible. Client-supplied operation IDs should be used for setup, config push, env push, and env set so retries do not duplicate audit events or create duplicate versions.

### 8.1 Go API Structure

The Cloud Run API should be a single Go service for the MVP.

| Area | Responsibility |
| --- | --- |
| HTTP layer | Route product endpoints, decode requests, encode responses, enforce API version headers |
| Signature middleware | Canonicalize requests, verify Ed25519 signatures, validate timestamps and body digests |
| Replay middleware | Reserve request nonces through Postgres and reject duplicate signed requests |
| Auth context layer | Resolve team, actor, management bit, scope, and effective permissions |
| Handler layer | Implement team setup, config sync, secret read, secret write, and audit workflows |
| Database layer | Call stored functions and map database errors to stable API errors |
| Observability layer | Structured logs, safe metrics, request IDs, operation IDs, and error categories |
| Configuration layer | Load environment-specific settings and secrets from Cloud Run environment variables or Secret Manager |

The Go API should avoid duplicating CLI crypto logic except for request signature verification. It must never decrypt env values or plaintext scope keys.

### 8.2 Cloud Run Runtime Configuration

Cloud Run should be configured for low-cost, low-maintenance MVP hosting.

Recommended defaults:

- Request-based billing.
- Minimum instances set to zero.
- Conservative maximum instance count to protect database connection limits and free-tier spend.
- Small memory and CPU allocation initially, with load testing before increasing.
- Container concurrency high enough to amortize cold starts, but low enough to keep Postgres connections bounded.
- No direct public database access from clients.
- Service account with only the permissions required to read runtime secrets and emit logs.
- Database connection string and service credentials stored in Secret Manager or Cloud Run secrets, not Terraform plaintext outputs.

## 9. Supabase Database Schema

The schema below is logical rather than SQL. Names can be adjusted during implementation, but the relationships and constraints should remain.

### 9.1 teams

Stores top-level team metadata.

| Column | Purpose |
| --- | --- |
| id | Stable team identifier |
| name | Display name |
| current_config_revision | Latest accepted config revision |
| created_by_key_sha | First management identity |
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
| config_hash | Hash of normalized config metadata and safe variable declarations |
| config_snapshot | Normalized config metadata, env file mappings, and safe variable declarations |
| pushed_by_key_sha | Management member who pushed the revision |
| pushed_at | Push timestamp |
| operation_id | Idempotency key |

Important constraints:

- One revision number per team.
- One operation ID per team for idempotent retries.
- Config snapshots must never include sensitive plaintext, masked sensitive values, raw plaintext hashes, private keys, or tokens.
- Sensitive variable declarations must use an algorithm-prefixed keyed digest such as `hmac-sha-256:v1:...`.
- Direct literals are allowed only for variables explicitly marked `non_sensitive` and short enough to fit on one line; longer non-sensitive values use previews.

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
| role | Legacy compatibility label, if present |
| management | Whether the member can approve joins, manage invites, and push config |
| status | Active, revoked, or replaced |
| approved_by_key_sha | Management member who approved the member |
| approved_at | Approval timestamp |
| revoked_by_key_sha | Management member who revoked the member |
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

Stores per-member scope access. Legacy role-level rules may be read for old snapshots, but new config writes member grants.

| Column | Purpose |
| --- | --- |
| id | Rule record ID |
| team_id | Parent team |
| scope_id | Scope the rule applies to |
| subject_type | Member, with old access-rule rows supported for migration |
| subject_value | Member public key SHA or old access-rule subject |
| permission | None, read, write, or admin |
| config_revision | Revision where this rule was accepted |
| active | Whether the rule is current |

Important constraints:

- Member-specific rules are authoritative for new config.
- Write implies read.
- Management is separate from scope access; it does not imply read or write unless a scope grant exists.

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
| created_by_key_sha | Management client that created the envelope |
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

### 9.10 team_pin_invites

Stores **management-created PIN invites** that grant immediate scope access on successful PIN redemption via server-mediated re-encryption. The database must never store plaintext PINs or plaintext scope keys.

| Column | Purpose |
| --- | --- |
| id | Opaque invite identifier returned to joiners after listing |
| team_id | Parent team |
| label | Management-entered display name for selection in `propagate team join` |
| pin_verifier | Slow hash or other verification material for the PIN; never reversible to plaintext |
| encrypted_scope_key_bundle | Scope keys encrypted to the server relay public key; one entry per granted scope |
| relay_key_version | Version of the relay key used for encryption (supports relay key rotation) |
| status | `active`, `redeemed`, `revoked`, `invalidated_pin` (or equivalent terminal states) |
| failed_pin_attempts | Count of rejected PIN submissions (implementation caps behavior before lockout) |
| requested_management | Optional management request applied on redemption |
| requested_scopes | Scope access grants applied on redemption |
| created_by_key_sha | Management member who created the invite |
| created_at | Creation timestamp |
| redeemed_at | When PIN was verified successfully |
| redeemed_by_key_sha | Joiner identity after successful PIN verification |
| expires_at | Optional TTL |
| revoked_at | When a management member revoked or system invalidated |

Important constraints:

- At most one successful redemption per invite.
- On successful redemption: the server creates active member records and scope key envelopes atomically, then **nullifies `encrypted_scope_key_bundle`** (the relay-encrypted material is no longer needed and should not persist).
- **PIN attempt policy**: allow **two** failed PIN checks; the **third** failed submission transitions the invite to a terminal failure state (`invalidated_pin` or equivalent) so it cannot be used again. Implementations should apply this atomically in the stored function that processes PIN attempts.
- Listing for joiners is **unauthenticated** in the product design: callers supply `team_id` from `propagate.yaml`. Rate limit aggressively and rely on invite TTL; teams must treat repository access as the primary confidentiality boundary for `team_id`. Optional hardening: a future listing token or signed URL model (PRD open questions).

### 9.11 audit_events

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

### 9.12 request_nonces

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
3. Determine member management bit.
4. Determine requested scope.
5. Apply member-specific scope rule if present.
6. Enforce command-specific permission.

Permission behavior:

| Permission | Capabilities |
| --- | --- |
| None | No secret read or write |
| Read | Pull env values and view env status |
| Write | Read plus push env changes and set individual env values |
| Management bit | Manage config, approve joins, approve access changes, and manage invites |

The client should also perform local permission checks for early, friendly errors. The server remains authoritative.

**PIN invite routes** are exceptions to the usual member-only reads:

- **Public invite listing** by `team_id` returns only non-secret metadata for `active` invites and must be rate limited at the edge and in application logic.
- **PIN redemption** grants immediate access: the request is signed by the joiner's Propagate key (not yet a member), and includes the joiner's encryption public key. On successful PIN verification, the server performs re-encryption and atomically creates the active member record with scope key envelopes. After redemption, the joiner is a full member and subsequent requests follow normal authorization.
- The API must validate signatures and reject requests that attempt to read encrypted env material before successful PIN verification, or bypass the two-failed-then-lockout rule.

## 11. Supabase Postgres Stored Functions

The Go Cloud Run API is the public HTTPS boundary, but selected data-local logic should live in Supabase Postgres stored functions for performance, consistency, and transactional safety.

Stored functions should not expose raw tables directly to the CLI. The Go API should call them after request parsing and signature verification.

### 11.1 Responsibilities That Belong In Stored Functions

| Logic | Why It Fits In Postgres |
| --- | --- |
| Replay nonce reservation | Atomic insert with a unique index prevents race conditions and avoids a read-before-write lookup |
| Actor and permission resolution | Authorization depends on indexed joins across members, scopes, and access rules |
| Config revision update | Config push needs optimistic concurrency, revision increments, member changes, access rule changes, envelope inserts, and audit events in one transaction |
| Env pull bundle fetch | The server can return the active envelope, env file mappings, variable metadata, and current encrypted versions with one set-based query |
| PIN invite redemption | Atomic PIN verification, attempt counter, member creation, scope access rules, scope key envelope inserts, config revision bump, bundle cleanup, and audit events must commit or roll back together |
| Env push apply | Version checks, immutable version inserts, current pointer updates, tombstones, idempotency, and audit events should commit or roll back together |
| Audit event append | Audit rows should be written in the same transaction as the operation they describe |
| Audit summaries | `team status` can efficiently compute last pull per member, members who never pulled, and recent activity near the data |
| Expired nonce cleanup | Short-lived replay records can be removed by a scheduled function using the `expires_at` index |
| PIN invite create/redeem/revoke | Atomic updates for attempt counters, redemption, and invalidation; same-transaction audit rows |

### 11.2 Responsibilities That Should Stay Outside Stored Functions

| Logic | Where It Belongs | Why |
| --- | --- | --- |
| Request canonicalization | Go Cloud Run API | It is protocol logic and should be easy to version with the API |
| Signature verification | Go Cloud Run API | Ed25519 verification and timestamp checks are easier to implement safely in Go and can share request-signing fixtures with the CLI |
| Encryption and decryption | CLI | The database must never see sensitive plaintext env values or plaintext scope keys |
| Scope key envelope creation | CLI | Management clients encrypt scope keys for recipients in the end-to-end encryption model |
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
| Resolve member access | Return active member, management bit, scope, and effective permission for a requested operation |
| Create team | Create team, first management member, scopes, initial config revision, encrypted secret records, first envelopes, and audit events |
| Push config revision | Apply management-approved config decisions with expected revision checks and idempotency |
| Fetch config snapshot | Return the current normalized config metadata and revision |
| Fetch env pull bundle | Return encrypted values, active envelope, env file mappings, and safe metadata for an authorized reader |
| Apply env push | Apply encrypted upserts and tombstones with expected version checks and idempotency |
| Record pull event | Append pull audit metadata after the CLI successfully receives the encrypted payload |
| Fetch team status | Return member list, pending/recent access metadata, and audit summaries |
| Redeem invite | Validate PIN verifier, enforce attempt limits, create active member with scope access rules, insert scope key envelopes (provided by Go API after re-encryption), bump config revision, nullify relay-encrypted bundle, and record audit events atomically |

The Go API should still enforce the public API contract. Stored functions should enforce data invariants and transaction semantics.

Note: the `Redeem invite` function receives already-re-encrypted scope key envelopes from the Go API layer (which performs the relay decryption and re-encryption in memory). The stored function never handles plaintext scope keys.

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

Config push and env push should be single database transactions. `env set` uses the env push transaction path with a single encrypted upsert.

Config push transaction requirements:

- Verify the actor has management access.
- Verify expected cloud config revision.
- Enforce idempotency through the operation ID.
- Apply approved member and access changes.
- Insert encrypted scope key envelopes supplied by the management client.
- Insert a new config revision snapshot with safe variable declarations.
- Update the team current revision.
- Append audit events.

Env push transaction requirements:

- Verify the actor has write access to the scope.
- Verify expected current secret version IDs for changed and removed variables.
- Insert immutable encrypted secret versions.
- Update current version pointers.
- Mark approved removals as tombstones.
- Update the config snapshot and bump the config revision when variable declarations change.
- Enforce idempotency through the operation ID.
- Append audit events.

If any step fails, the transaction must roll back. The CLI can retry with the same operation ID when the failure is transient.

### 11.6 Stored Function Security

Stored functions are part of the trusted backend, not a public client API.

Security rules:

- The CLI should never call stored functions directly.
- The Go API should verify request signatures before invoking stored functions.
- Stored functions should derive management and permissions from database state, not from client-provided claims.
- Functions that need elevated privileges should use tightly scoped execution privileges and a fixed search path.
- Function inputs should use team IDs, public key SHAs, scope IDs, operation IDs, ciphertext, envelopes, and metadata only.
- Function inputs and outputs must never contain sensitive plaintext env values or plaintext scope keys.
- Raw table access should not be granted to anonymous or end-user Supabase roles.

## 12. Config File Design

`propagate.yaml` is committed to Git and contains team metadata plus safe variable declarations. Sensitive values must never appear directly. Non-sensitive literals require an explicit `sensitivity: non_sensitive` declaration.

Config edits to variable declarations are local metadata changes. The CLI may change a declaration's sensitivity, move it between scopes, add an env file mapping needed by that move, or remove the declaration, but it must not read env file values, decrypt cloud values, or mutate encrypted secret versions during config edit.

Scope creation is also a local metadata change. `propagate scope create` may add an empty scope with optional env file mappings and grant write access to existing management members, but it must not read local env values, generate encrypted secret versions, decrypt cloud values, or publish the scope directly. Publication happens through `propagate config push`.

Logical sections:

| Section | Contents |
| --- | --- |
| Version | Config format version |
| Team | Team ID, name, cloud revision |
| Scopes | Scope names, env file mappings, variable declarations |
| Members | Active members, handles, public key SHA, public keys, management bit, per-scope permissions |
| Pending | Join requests, management changes, scope access changes |
| History | Optional local history of declined or resolved requests, without env values |

Config validation should check:

- Known config version.
- Team ID and cloud revision format.
- Sensitive variables use keyed digest declarations with an algorithm prefix.
- Non-sensitive literals are explicitly marked and short; long non-sensitive values use previews.
- Sensitive declarations do not retain literal or preview metadata.
- Each variable declaration references an env file path listed by its containing scope.
- No raw plaintext hashes, private keys, access tokens, or unmarked value-like literals in scope, variable, pending, member, history, or metadata sections.
- Valid public keys and public key SHA matches.
- Scope names are valid and unique.
- Env file paths are relative and inside the Git worktree.
- Pending requests do not duplicate active members.
- Management and permission values are known.

Config writes should preserve comments and ordering where possible. If round-trip preservation is not reliable, the CLI should generate a stable normalized file and clearly report that it updated formatting.

## 13. Core User Flows

### 13.1 First Management Member Setup

1. User runs `propagate init`.
2. CLI creates or loads local identity.
3. CLI verifies the current directory is inside a Git worktree.
4. CLI checks for existing `propagate.yaml`.
5. If none exists, CLI starts project setup.
6. CLI asks for team name.
7. CLI scans candidate env files inside the Git worktree.
8. Bubble Tea import UI shows files, variable names, masked values, detected source, and proposed scopes.
9. User selects scopes and confirms import.
10. CLI creates team metadata, first management member, scopes, and env mappings.
11. CLI creates scope keys for selected scopes.
12. CLI encrypts selected env values locally.
13. CLI creates first management member scope key envelopes.
14. CLI sends a signed setup request to the cloud API.
15. Go API calls stored functions that create the team transactionally and record audit events.
16. CLI writes `propagate.yaml` with the returned team ID, cloud revision, env file mappings, and safe variable declarations.
17. CLI detects supported AI agent instruction or skill targets in the repository.
18. CLI offers to add or update Propagate agent guidance.
19. If confirmed, CLI writes a managed instruction block or Propagate skill template without env values or private material.
20. CLI prints a summary with identity path, created scopes, uploaded variable count, agent guidance status, and next Git steps.

Failure handling:

- If cloud setup fails, no config should be written unless it is clearly marked as incomplete.
- If config writing fails after cloud setup succeeds, the CLI should print recovery instructions and allow `config pull` by team ID if possible.
- If env import is canceled, no cloud team should be created.
- If agent guidance writing fails, project setup should remain successful and the CLI should report how to retry the guidance step later.

### 13.1.1 Management PIN Invite

1. A management member runs `propagate team invite` with label and scopes to grant.
2. CLI fetches the server's current relay public key.
3. CLI decrypts relevant scope keys locally (using the management member's envelopes).
4. CLI encrypts each scope key to the relay public key, producing a relay-encrypted scope key bundle.
5. CLI sends signed `create_invite` request with the relay-encrypted bundle; API generates random PIN, stores verifier and bundle, returns PIN once in the response body.
6. CLI prints PIN exactly once. Documentation should warn about shell history and screen shoulder-surfing.
7. The management member communicates PIN out of band.
8. Management members can list or revoke invites before redemption using `propagate team invite list` or `revoke`.

### 13.2 Developer Join

1. Developer can run `propagate init` to create or load identity, then run `propagate team join`.
2. For an already configured repository, developer can instead run `propagate team join --init --handle bob@example.com --scope dev=read` to combine existing-project init and the join request.
3. When `--init` is present, CLI runs the existing-project init path first: create or load identity, verify `propagate.yaml` exists, and offer or apply agent guidance. It must not create a new project config in the join path.
4. CLI reads `propagate.yaml` for validation and `team_id`.
5. CLI queries the cloud for **active invite codes** for the team. **If none**, proceed directly to the git-mediated pending join (step 7). **If one or more exist**, the Bubble Tea UI offers **Request to join** (git-mediated only) or **Join by invite code** (invite subflow below).

**Invite-code subflow (immediate access):**

6a. CLI lists active invites, joiner selects the matching row (or auto-selects if only one), then enters the PIN.
6b. CLI sends a signed PIN redemption request including the joiner's encryption public key.
6c. Server validates PIN, performs relay re-encryption (see §7.5), creates active member and scope key envelopes atomically.
6d. Server returns the scope key envelopes to the CLI.
6e. CLI writes the join entry to `propagate.yaml` with `pre_approved: true` and invite correlation fields.
6f. CLI notifies the developer that access is active and they can immediately run `propagate env pull` or `propagate run`.
6g. Developer commits the config diff for audit.

**Git-mediated subflow (pending access):**

7. CLI adds a pending join request with handle, signing public key, encryption public key, public key SHA, requested management bit, requested scopes, and timestamp.
8. CLI writes `propagate.yaml`.
9. CLI explains that no secret access has been granted.
10. Developer commits the config diff and opens a pull request for management review.

For **git-mediated** join requests, the cloud does not need to know about the pending row until a management member pushes the approved config. **Invite-code redemption** creates an active member server-side immediately; the `propagate.yaml` entry serves as a Git audit trail. `config push` recognizes pre-approved members and skips re-prompting.

### 13.3 Management Config Push

1. A management member pulls the latest Git branch containing pending config changes.
2. The management member runs `propagate config push`.
3. CLI loads local identity and `propagate.yaml`.
4. CLI fetches cloud config revision.
5. CLI compares local revision and cloud revision.
6. If revisions conflict, CLI refuses to push until the management member pulls or resolves the conflict.
7. Bubble Tea approval UI shows each pending join and access change.
8. The management member approves, declines, or skips each item.
9. For approvals, CLI prepares updated config metadata.
10. For approved scope access, CLI decrypts the relevant scope key and encrypts a new envelope for the target member.
11. If the target config contains scopes that do not exist in the current cloud config, CLI generates a fresh random scope key for each new scope.
12. For each new scope, CLI encrypts the new scope key for every active member whose per-member scope map grants read or write access.
13. CLI sends a signed config push with expected cloud revision, decisions, updated config snapshot, and new envelopes.
14. Go API validates management permission and applies the transaction through stored functions.
15. CLI updates `propagate.yaml` to reflect approved and declined decisions while leaving skipped items pending.
16. CLI prints a decision summary and whether the config file changed.

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

### 13.5 Scope Create

`propagate scope create` creates an empty local scope declaration in `propagate.yaml`.

Flow:

1. User runs `propagate scope create NAME`, optionally with one or more `--env-file PATH` mappings.
2. CLI verifies the current directory is inside a Git worktree.
3. CLI loads `propagate.yaml` and validates the requested scope name.
4. CLI rejects duplicate scope names.
5. CLI validates optional env file mappings as repository-relative paths inside the worktree.
6. CLI adds a new scope with empty variables, optional env file mappings, and write grants for current management members.
7. CLI validates the edited config.
8. With `--dry-run`, CLI prints the safe summary and does not write.
9. Without `--dry-run`, CLI writes `propagate.yaml` atomically.
10. CLI does not prompt for source scopes and does not copy env file mappings or declaration metadata from existing scopes.
11. CLI recommends `propagate config status`, `propagate config edit`, `propagate config push`, and existing env push commands for setting metadata and seeding the new scope.

Safety rules:

- No API call is required.
- No local env file values are read.
- No encrypted cloud values are pulled or decrypted.
- No encrypted secret records are created, updated, or deleted.
- No plaintext scope key is generated during `scope create`; scope keys are generated only when a new scope is published with `config push`.
- No env file mappings or variable declarations are copied from other scopes during `scope create`.

### 13.6 Config Edit

`propagate config edit` opens an interactive local editor for variable declaration metadata.

Flow:

1. User runs `propagate config edit`.
2. CLI loads `propagate.yaml` and validates it before editing.
3. TUI lists declarations by scope, env file path, variable name, and sensitivity.
4. User chooses metadata edits:
   - Toggle sensitivity.
   - Move a declaration to another existing scope.
   - Remove a declaration from config metadata.
5. If a move targets a scope that does not list the declaration's env file path, CLI adds that env file mapping to the target scope.
6. CLI validates the edited config.
7. With `--dry-run`, CLI prints the safe summary and does not write.
8. Without `--dry-run`, CLI writes `propagate.yaml` atomically.
9. CLI recommends `propagate config status` and `propagate config push` for review and publication.

Safety rules:

- No API call is required.
- No local env file values are read.
- No encrypted cloud values are pulled or decrypted.
- No encrypted secret records are created, updated, or deleted.
- Moving a declaration changes config metadata only; it does not re-encrypt the underlying value.
- Removing a declaration removes metadata only. Secret removals continue through env push or single-value env workflows.
- Switching from `non_sensitive` to `sensitive` must clear literal and preview fields.
- Non-interactive mode should fail instead of waiting for TUI input.

### 13.7 Env Pull

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

### 13.8 Process Injection

1. User runs `propagate run --scope dev -- COMMAND [args...]`.
2. CLI loads identity and config.
3. CLI pulls the latest cloud config and updates `propagate.yaml`; if local config changes would be overwritten, the CLI requires confirmation or `--yes`.
4. CLI sends the same signed read request used by env pull.
5. Server verifies read access and returns encrypted secret versions plus the member's active scope key envelope.
6. CLI decrypts the scope key locally.
7. CLI decrypts env values locally.
8. CLI flattens values into `NAME=value` process environment entries.
9. If two configured env files in the selected scope contain the same variable name, CLI refuses before starting the child process because process environments cannot preserve file-path identity.
10. CLI records a safe injection audit event through the pull-event endpoint with client kind `cli_run`.
11. CLI starts the child process with inherited stdin, stdout, stderr, working directory, and environment plus injected values. Injected values override inherited variables with the same name.
12. CLI exits with the child process exit code.

`propagate run` must not write local env files or print decrypted values in Propagate-owned output. It also cannot guarantee child output safety: once values are injected, the child process can read, log, print, or pass them to descendants.

For `prod`, the CLI should require confirmation before process injection. Non-interactive prod injection requires `--yes`.

### 13.9 Env Push

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
11. CLI updates local variable declarations with keyed digests or explicit non-sensitive literals/previews.
12. CLI sends encrypted upserts, tombstones, expected current versions, and the updated config snapshot.
13. Go API validates write permission and version preconditions through stored functions.
14. Database transaction creates immutable secret versions, updates current pointers, stores the new config revision, and records audit events.
15. CLI updates local `propagate.yaml` with the accepted revision and prints a masked summary.

If the user lacks write access, the CLI should refuse before upload and explain how to request access.

### 13.10 Env Set

1. User runs `propagate env set NAME --scope dev` or omits `--scope` for interactive selection.
2. CLI validates the variable name. `--value-stdin` is validated before config loading so missing piped input fails early.
3. CLI loads identity and config.
4. CLI resolves the target scope: explicit `--scope`, the only configured scope, or an interactive prompt when multiple scopes exist.
5. In non-interactive mode, CLI requires `--scope` when multiple scopes exist.
6. CLI asks for production confirmation before reading a value when the target scope is `prod`.
7. CLI prompts for the value using a secure no-echo prompt unless `--value-stdin` was used.
8. CLI requests current encrypted cloud values and the user's scope envelope.
9. CLI decrypts the scope key locally.
10. CLI determines the target env file mapping and whether the variable is added or changed.
11. CLI verifies write access before upload.
12. CLI encrypts the new value locally.
13. CLI updates the variable declaration in the target scope.
14. CLI sends one encrypted upsert, expected current version metadata, and the updated config snapshot through the env push API.
15. Go API validates write permission and version preconditions through stored functions.
16. Database transaction creates one immutable secret version, updates the current pointer, stores the new config revision, and records audit events.
17. CLI updates local `propagate.yaml` with the accepted revision and prints a safe summary with scope, variable name, add/change status, and operation ID.

The plaintext value must never be accepted as a positional command argument, shown in output, written as a sensitive literal in `propagate.yaml`, or logged. `env set` should not update local env files unless a future explicit flag requests it.

### 13.11 Env Status

`propagate env status` should fetch the latest cloud config snapshot and the encrypted env status bundle. The CLI decrypts the scope key locally, hashes local env file values with the declaration algorithm, and compares those local digests to the latest cloud declarations.

The command should report:

- Whether local `propagate.yaml` is behind the cloud config revision.
- Variable names and env file paths from the latest cloud declarations.
- Local state per variable: equal, missing, different, or undeclared.
- Last updated metadata from cloud secret versions.
- Next steps: `propagate config pull` when YAML is stale, and `propagate env pull` when values differ or are missing.

### 13.12 Team Status

`propagate team status` should combine local config and cloud audit summaries:

- Team name.
- Current identity, management bit, and scope permissions.
- Current public key SHA.
- Members grouped by management vs non-management access.
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
- Interactive command screens that print plain terminal headings, such as the config edit metadata editor, should reuse the shared human output style for titles and section headings where practical.

TUI models should be deterministic state machines. They should receive domain data, produce user decisions, and avoid direct database or filesystem writes. The command layer performs side effects after the TUI returns a decision.

The Config Edit TUI should receive parsed config declarations and return metadata edit decisions only. It should display variable name, scope, env file path, sensitivity, and pending env file mapping additions. It must not display, request, infer, or persist env values.

## 17. AI Agent Integration

Propagate should treat AI coding agents as first-class repository collaborators without treating them as trusted secret readers. The CLI should make the safe path obvious to agents by installing repo-local guidance and exposing predictable, machine-readable command behavior.

### 17.1 Agent Guidance Architecture

The agent guidance layer should have three responsibilities:

| Responsibility | Description |
| --- | --- |
| Detect | Find known agent instruction files and skill directories inside the Git worktree |
| Render | Produce Propagate-specific guidance from internal templates with no env values or private material |
| Apply | Preview and write managed blocks or skill files idempotently while preserving user-authored content |

Detection should be adapter-based. Each adapter should know how to detect, render, and update one target type without coupling the rest of the CLI to agent-specific file layouts.

Initial adapter targets:

| Target | Behavior |
| --- | --- |
| Generic repository instructions | Create or update a managed Propagate section in `AGENTS.md` when selected |
| Codex-style skill | Create or update a Propagate skill file in the repository's configured skill location when detected or selected |
| Cursor rules | Update a Propagate-managed rule file when a Cursor rules directory is present |
| Claude instructions | Update a Propagate-managed section when a Claude instruction file is present |
| GitHub Copilot instructions | Update a Propagate-managed section when repository Copilot instructions are present |

MVP can ship with generic instructions and one skill adapter first, as long as the adapter interface can support more targets later.

### 17.2 Init Flow

`propagate init` should run agent guidance setup after project config setup. Existing projects should also get the offer when `propagate init` detects an existing `propagate.yaml`.

Flow:

1. Detect agent guidance targets inside the Git worktree.
2. If no target exists, offer to create generic repository instructions.
3. If multiple targets exist, show a Bubble Tea selection screen.
4. For existing files, show a diff preview containing only the managed block changes.
5. Write selected guidance atomically after user confirmation.
6. Report which targets were created, updated, skipped, or failed.

Agent guidance setup should be re-runnable and idempotent. A future `propagate agents setup` command can reuse the same implementation, but MVP can expose the flow through `propagate init`.

### 17.3 Managed Block And Skill Rules

Generated guidance should be managed through explicit markers or full-file ownership, depending on the target type.

Managed block rules:

- Preserve all content outside the managed block.
- Replace only the Propagate-managed block on subsequent runs.
- Include a template version for future migrations.
- Avoid rewriting unrelated whitespace or formatting.
- Refuse to update if managed markers are malformed unless the user confirms a repair.

Skill file rules:

- Prefer a dedicated Propagate skill file over mixing long instructions into unrelated skills.
- Keep the skill focused on safe Propagate command usage.
- Do not include project-specific env values, masked values, decrypted output, private key material, or cloud credentials.
- Include safe command categories, approval requirements, and failure handling guidance.
- Keep generated text deterministic so diffs are easy to review.

### 17.4 Agent-Friendly Command Contracts

Tool-using agents need command results that are stable enough to parse and safe enough to quote back to users.

Command contract requirements:

| Area | Requirement |
| --- | --- |
| JSON output | Status and dry-run commands should expose stable field names and schema versions |
| Exit codes | Distinguish success, validation failure, permission denied, cloud unavailable, conflict, user cancellation, and internal error |
| Non-interactive mode | Commands should fail with a clear error instead of waiting forever when confirmation is required and no TTY is available |
| Dry runs | Write-capable commands should support dry-run summaries without changing local files or cloud state |
| Human output | Non-JSON output should use the shared Propagate style: bold command title, semantic status marker, consistent list sections, and `--no-color` support |
| Output safety | stdout, stderr, JSON, logs, and panic paths must never contain plaintext env values |
| Operation IDs | Mutating commands should return operation IDs in JSON so agents can report and retry safely |
| Next steps | Errors should include safe remediation, such as requesting access or asking a management member to approve a pending join |

For `propagate run`, output safety applies to Propagate-owned output only. The child process receives plaintext values by design, so its stdout and stderr are outside Propagate's ability to sanitize.

Agent guidance should recommend discovery commands first: config status, team status, env status, and dry-run variants. It should discourage direct `.env` inspection unless the user explicitly asks and local policy allows it.

### 17.5 Agent Audit Metadata

The CLI may detect agent execution through explicit flags, environment variables, or known adapter contexts. Detection should be best-effort and non-security-critical.

Allowed audit metadata:

- Client kind.
- Agent adapter name.
- CLI version.
- Command name.
- Operation ID.
- Config revision.

Disallowed audit metadata:

- User prompts or conversation text.
- Env values or masked values.
- Private keys or access tokens.
- Absolute local paths outside repository-relative env mappings.

Agent metadata should help teams understand whether a change was made from a human terminal, script, or AI-assisted tool, but authorization must still be based on Propagate identity and scope permissions.

### 17.6 Security Boundaries

Agent guidance is guardrail text, not a sandbox. AI agents may have access to the local repository, terminal output, and files the user allows them to inspect.

Security rules:

- The CLI and cloud API must enforce permissions regardless of agent guidance.
- Generated guidance must never grant access, create identities, or approve joins by itself.
- Agent-driven commands use the current user's local Propagate identity unless a future explicit agent identity model is added.
- Sensitive commands should require human confirmation unless the user intentionally passes an explicit non-interactive approval flag.
- Agent templates should instruct agents not to paste decrypted env values into chat, docs, tests, commit messages, or issue trackers.
- Audit events should identify agent-assisted execution when known, but should not treat it as a separate trusted identity in MVP.

## 18. Revision And Conflict Handling

Propagate has two important revision systems:

- Config revision: monotonic team-level revision for `propagate.yaml` cloud sync.
- Secret version: immutable per-variable version for encrypted env values.

Config push must include the expected cloud config revision. If the cloud revision has changed, the server rejects the push. The CLI then instructs the management member to run `propagate config pull`, review the diff, and retry.

Env push should include expected current secret version IDs for changed or removed variables. If another user updated the same variable first, the server rejects only the conflicting variables where possible. The CLI should show a conflict summary and ask the user to pull before retrying.

This prevents accidental overwrites without requiring pessimistic locks.

## 19. Security Requirements

### 19.1 Plaintext Handling

The CLI should treat plaintext env values as sensitive by default.

Rules:

- Do not log plaintext values.
- Do not include plaintext values in errors.
- Do not include plaintext values in analytics or audit metadata.
- Do not write sensitive env values to `propagate.yaml`.
- Do not write raw hashes of plaintext values. Use scope-keyed, algorithm-prefixed digests such as `hmac-sha-256:v1:...`.
- Do not write direct literals unless the variable is explicitly marked `non_sensitive` and the value fits on one short line; truncate longer non-sensitive values as previews.
- Do not use `config edit` to infer or populate literals from local env files. It edits declaration metadata only.
- Do not use `scope create` to read, infer, validate, or copy env values. It creates scope metadata only; use `config edit` to set env file mappings or move declaration metadata.
- Do not accept env values as positional CLI arguments; `env set` must use secure no-echo prompting or an explicit non-echo input channel.
- Do not write env values to generated agent instructions, skills, prompts, tool logs, or machine-readable output.
- Do not include private key material, access tokens, cloud credentials, prompt text, or conversation text in generated agent guidance or audit metadata.
- Mask values in all TUI and terminal output.
- Keep plaintext in memory only as long as needed.
- Avoid writing temporary plaintext files.
- Ensure panic and crash paths do not dump secret-containing structures.

Go cannot guarantee immediate memory zeroization for all strings. The implementation should still minimize copies and use byte slices for sensitive crypto operations where practical.

### 19.2 Cloud Trust Boundary

In end-to-end encryption mode, Supabase is trusted for availability, metadata storage, authorization checks, and audit history. It is not trusted with sensitive plaintext env values or plaintext scope keys.

**Exception: invite relay re-encryption.** During PIN invite redemption, the Cloud Run API briefly holds plaintext scope keys in memory while re-encrypting them for the new member (see §7.5). This is explicitly scoped to the invite flow only. The scope keys are never persisted in plaintext, never written to logs, and exist in memory only for the duration of the re-encryption operation.

A compromised cloud database could expose:

- Team names.
- Handles.
- Public keys.
- Scope names.
- Env file paths.
- Variable names.
- Ciphertexts.
- Audit metadata.
- Relay-encrypted scope key bundles for unredeemed invites (requires relay private key to decrypt).

It should not expose:

- Sensitive plaintext env values.
- User private keys.
- Plaintext scope keys (except transiently in Cloud Run memory during invite redemption).

A compromised database **combined with** a compromised relay private key could expose scope keys for unredeemed invites. Mitigations:

- Short invite TTL (recommended 7 days default).
- Relay private key stored in Secret Manager with restricted IAM (not in the database).
- Relay-encrypted bundles nullified immediately after successful redemption.
- Relay key rotation when compromise is suspected.

This threat model should be stated in user-facing security documentation.

### 19.3 Server-Side Authorization

Server authorization must not rely on client-provided access claims. The server derives management access and scope permissions from the database.

Every API handler should verify:

- Valid signature.
- Non-expired timestamp.
- Non-replayed nonce.
- Active team membership where required.
- Required permission for the specific scope and operation.
- Expected revision or version preconditions for writes.

### 19.4 Management Approval Safety

Management approval is security-critical.

The Config Push TUI should show:

- Full handle.
- Public key SHA.
- Requested management access.
- Requested scopes.
- Whether the same key or handle already exists.
- Whether the request came from local config changes not yet committed.
- Whether the request includes **PIN invite** metadata (`source_invite_id` / label) when present.

The CLI should encourage management members to review the Git diff before approving. It should not auto-approve pending joins.

### 19.5 Revocation Limits

Revocation cannot erase values already pulled to a developer's machine. The CLI and docs should say this plainly.

After revoking access, the product should recommend:

- Rotate affected scope keys.
- Update all env values in the affected scope if exposure is suspected.
- Re-encrypt current values only for remaining authorized members.
- Commit the updated config after config push.

### 19.6 Production Scope Guardrails

`prod` should be treated as high-risk.

Guardrails:

- Extra confirmation before importing prod env values.
- Extra confirmation before pulling prod env values to local files.
- Extra confirmation before injecting prod env values into a child process.
- Clear display of identity and target file path.
- Refuse non-interactive prod writes unless an explicit flag or config setting is present.
- Refuse non-interactive prod process injection unless `--yes` is present.
- Prefer management-only write access by default.

## 20. Edge Cases And Expected Behavior

| Edge Case | Expected Behavior |
| --- | --- |
| No Git repository | `init` refuses project setup and explains that MVP requires Git |
| Existing invalid `propagate.yaml` | Command refuses to continue until repaired or backed up |
| `team join --init` without `propagate.yaml` | Command refuses to create a new project and tells the user to get the repository config first |
| Corrupted local identity | CLI refuses to use it and gives recovery options |
| Lost private key | User must create a new identity and request access again |
| Duplicate handle | Allowed, but UI highlights public key SHA because handle is not identity |
| Duplicate public key in pending joins | CLI warns and avoids adding duplicate request |
| Pending join for existing member | CLI rejects the pending request locally |
| Duplicate scope name | CLI rejects scope creation locally and leaves `propagate.yaml` unchanged |
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
| Approver cannot decrypt scope key | Config approval for that scope fails; the approver must pull access or recover key |
| Supabase transaction partially fails | Transaction rolls back; CLI retry uses same operation ID |
| Agent instruction file has malformed managed block | CLI refuses automatic update and offers a repair or manual instructions |
| Multiple agent systems detected | CLI lets the user choose targets and reports each created, updated, skipped, or failed target |
| Non-interactive agent invokes mutating command without approval | CLI fails with a stable exit code and suggests dry-run or explicit human-approved mode |
| PIN invite: third failed PIN attempt | Server marks invite invalid; CLI tells joiner to contact a management member for a new invite |
| PIN invite: redeem after invite expired or revoked | Server rejects; CLI explains invite is no longer valid |
| PIN invite: successful redemption | Server creates active member and envelopes atomically; CLI receives envelopes and notifies developer that access is active |
| PIN invite: relay key version mismatch | Server detects stale relay key version on bundle; management member must reissue the invite |
| PIN invite: list empty but management member said PIN was issued | Often means lockout, redemption, expiry, or wrong `team_id`; CLI points to status commands for management members |
| `team join` with active invites but `--non-interactive` and no join-mode flag | CLI fails with stable error; user must pick **Request to join** or **Join by invite code** in interactive mode or pass explicit flags |
| Agent guidance write fails | Propagate project setup remains valid; CLI reports the failed target and retry path |

## 21. Observability And Auditing

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
- Client kind, such as human terminal, script, or AI agent when known.
- Agent adapter name when known and safe.
- Operation ID.
- Counts of variables added, changed, removed, pulled, or injected into a process.

Audit metadata should not include:

- Plaintext values.
- Masked values if the mask might leak too much for short secrets.
- Raw hashes of plaintext values.
- Prompt text or conversation content.
- Local filesystem absolute paths beyond the repository-relative env file mapping.

## 22. Testing Strategy

Testing should cover both product behavior and security invariants.

Recommended coverage:

| Test Area | What To Verify |
| --- | --- |
| Config validation | Invalid management grants, invalid permissions, invalid keys, duplicate scopes, unsafe paths, env value fields, value-like literals |
| Identity | Key creation, permission checks, corrupted files, public key SHA stability |
| Crypto | Round-trip encryption, wrong recipient failure, associated data mismatch failure, nonce uniqueness |
| Env parsing | Quoting, comments, empty values, duplicate names, preserve unrelated values |
| TUI decisions | Approval, decline, skip, config edit metadata changes, cancel, selection state, masked values |
| API authorization | Scope read/write permissions, management-only actions, revoked members, replay rejection, revision preconditions |
| Supabase transactions | Config push atomicity, env push conflicts, env set partial updates, idempotent retries |
| Agent guidance | Target detection, managed block replacement, idempotent reruns, malformed marker handling, no env value leakage |
| Agent command contracts | JSON schema stability, exit codes, non-interactive failure, dry-run summaries |
| End-to-end flows | First setup, join request, management approval, env pull, process injection, env push, env set, revoke access |

Security-specific tests should assert that sensitive plaintext env values never appear in logs, command output, audit rows, config snapshots, `propagate.yaml`, or API error bodies. Public-looking and placeholder values are sensitive by default unless explicitly marked `non_sensitive`; sentinel fixtures should cover both default-sensitive and explicit non-sensitive cases.

## 23. Deployment And Operations

Infrastructure should be managed with Terraform, while database schema and stored function changes should be managed with versioned SQL migrations committed to the repository.

Terraform responsibilities:

- Google Cloud project service enablement needed for Cloud Run, Artifact Registry, Secret Manager, IAM, logging, and builds.
- Artifact Registry repository for the Go API container image.
- Cloud Run service, service account, IAM bindings, environment variables, secret mounts, scaling limits, CPU, memory, and concurrency.
- Secret Manager secret containers for API runtime configuration, without committing secret values to source control.
- Budget alerts and basic operational guardrails.
- Supabase project resources and settings where the Supabase Terraform provider supports them.

Migration responsibilities:

- Supabase Postgres tables, indexes, constraints, and enum-like checks.
- Stored functions for replay nonce reservation, permission resolution, config push, env pull bundle fetch, env push/env set, and audit summaries.
- Row-level security policies if direct Supabase access is ever introduced for internal tooling.
- Safe forward migrations and rollback notes where practical.

Terraform should not be the primary mechanism for routine database schema changes. Database migrations are easier to review, test, apply in order, and roll back independently from infrastructure changes.

Operational recommendations:

- Separate development, staging, and production Google Cloud and Supabase environments.
- Keep Supabase service role keys and database credentials only in Secret Manager or Cloud Run secret bindings.
- Enable database backups and point-in-time recovery for production.
- Use database constraints to reinforce application invariants.
- Use scheduled cleanup for expired request nonces.
- Monitor Cloud Run request latency, cold starts, error rates, database transaction failures, and authorization failure rates.
- Version API responses so older CLIs can fail gracefully when the server requires an upgrade.
- Keep Cloud Run minimum instances at zero for low-cost MVP environments unless latency requirements justify paying for warm instances.
- Cap Cloud Run maximum instances initially to protect Supabase connection limits and control spend.
- Use a bounded database connection pool in the Go API.
- Run migrations from CI/CD or an explicit release command before deploying API code that depends on them.
- Keep Terraform state out of the repository and avoid storing plaintext secrets in Terraform variables or outputs.

## 24. MVP Implementation Phases

### Phase 1: Local Foundations

- Go command skeleton.
- Local identity creation and loading.
- `propagate.yaml` parser and validator.
- Git worktree detection.
- Env scanner and parser.
- Basic masked output helpers.
- Agent guidance target detection and template rendering.

### Phase 2: Cryptography

- Scope key generation.
- Env value encryption and decryption.
- Scope key envelopes.
- Request signing and verification test fixtures.
- Local key permission checks.

### Phase 3: Cloud Backend

- Terraform for Google Cloud, Cloud Run, Artifact Registry, Secret Manager, IAM, and Supabase project resources.
- Supabase Postgres SQL migrations for schema, indexes, policies, and stored functions.
- Go Cloud Run API for team setup, config sync, secret read, secret write, and audit events.
- Transaction and idempotency handling.
- Permission enforcement.

### Phase 4: Core CLI Flows

- `propagate init`.
- `propagate team join`.
- `propagate scope create`.
- `propagate config status`.
- `propagate config pull`.
- `propagate config push`.
- `propagate config edit`.
- `propagate env pull`.
- `propagate run`.
- `propagate env push`.
- `propagate env set`.
- `propagate env status`.
- `propagate team status`.

### Phase 5: TUI Polish And Safety

- Env import TUI.
- Env push TUI.
- Config push TUI.
- Config edit TUI.
- Agent guidance target selection and diff preview.
- Prod guardrails.
- Clear summaries and error messages.
- JSON and dry-run support for selected commands.

### Phase 6: Hardening

- End-to-end test suite.
- Security tests for no plaintext leakage.
- Agent guidance security tests for no env values, private keys, tokens, prompts, or conversations in generated files or audit metadata.
- Conflict handling polish.
- Cross-platform filesystem behavior.
- Documentation for recovery, revocation, and team workflows.

## 25. Open Technical Decisions

The PRD leaves several decisions open. Recommended MVP answers:

| Question | Recommendation |
| --- | --- |
| `propagate.yaml` or `propagate.yml` | Use `propagate.yaml` as canonical; detect `propagate.yml` and suggest rename |
| Backend API stack | Use a Go HTTPS API on Google Cloud Run, not Supabase Edge Functions, so CLI and backend can share Go domain and signing code |
| Infrastructure management | Use Terraform for infrastructure and SQL migrations for database schema/stored functions |
| Require Git repository | Yes for MVP, because access review depends on Git workflow |
| SSH key or dedicated key format | Use a dedicated Propagate identity bundle with Ed25519 signing and X25519 or age encryption keys; display it as one identity |
| First management member | The user who successfully creates the team during `propagate init` gets management access |
| Scope key granularity | One key per scope for MVP; reconsider per env file if teams need finer isolation |
| Initial scope access | New members request explicit per-scope access; write access should be explicit or management-configured |
| Pull overwrite behavior | Prompt before overwriting existing differing values; non-interactive mode preserves unless explicitly told to overwrite |
| Removed variables during pull | Preserve locally by default and warn; deletion requires explicit confirmation |
| Revocation rotation | Record revocation in MVP; add guided or automated scope key rotation as soon as practical |
| Agent guidance targets | Support generic `AGENTS.md` and one Propagate skill adapter first; add Cursor, Claude, and Copilot adapters after the interface settles |
| Agent identity model | AI agents operate through the current human user's local identity in MVP; dedicated AI agent identities are later work |
| Agent guidance default | Offer setup during `propagate init`; auto-select detected targets but require confirmation before writing |

## 26. Summary

The safest MVP architecture is a Go CLI that performs all encryption locally, a Bubble Tea TUI for high-risk human decisions, a Go API on Google Cloud Run as the signed API boundary, and Supabase Postgres as the encrypted metadata and audit store.

The most important implementation detail is preserving the end-to-end encryption boundary: Supabase can store and authorize access to encrypted material, but only local Propagate clients with valid private keys can decrypt scope keys and env values.

The second most important detail is revision discipline. Config pushes, env pushes, and single-value env set operations should always carry expected revisions or versions so Propagate does not silently overwrite team decisions or secret updates.
