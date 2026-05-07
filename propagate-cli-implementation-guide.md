# Propagate CLI Implementation Guide

## 1. Purpose

This document gives implementation guidance for the Propagate CLI. It complements:

- `propagate-prd.md`, which defines product behavior.
- `propagate-technical-design.md`, which defines the overall architecture.

This guide focuses on CLI-facing data models, command contracts, API endpoint contracts, error handling, testing, and edge cases. It assumes the selected stack:

- CLI: Go
- TUI: Bubble Tea
- Backend API: Go HTTPS service on Google Cloud Run
- Database: Supabase Postgres
- Infrastructure: Terraform
- Database changes: versioned SQL migrations

The CLI is the product's primary user interface and the only component that handles sensitive plaintext env values. The backend receives encrypted env values, encrypted scope key envelopes, safe metadata, and signed requests; explicitly `non_sensitive` declarations may include short literals or previews in config metadata.

## 2. CLI Implementation Principles

Implementation should follow these rules consistently:

- Never write sensitive env values to `propagate.yaml`, agent instructions, docs, logs, JSON output, or panic output.
- Represent sensitive values in `propagate.yaml` only as scope-keyed, algorithm-prefixed digests such as `hmac-sha-256:v1:...`; never use raw SHA-256 plaintext hashes.
- Only write direct YAML literals for variables explicitly marked `non_sensitive` and short enough for one line; truncate longer non-sensitive values as previews.
- Never accept env values as normal positional command arguments; use secure no-echo prompts for `env set`.
- Treat public-looking env values as sensitive unless a human explicitly marks them `non_sensitive`.
- Keep all decryption and encryption of env values in the CLI.
- Use signed API requests for cloud reads and writes.
- Use expected revisions and expected secret version IDs for write operations.
- Prefer dry-run and status flows before mutation.
- Fail closed when identity, access, config revision, or encryption state is ambiguous.
- Preserve local user content in `propagate.yaml`, env files, and agent instruction files where practical.
- Keep Bubble Tea models pure: they collect decisions; command orchestration performs side effects.
- Keep command output stable enough for humans and tool-using agents.

## 3. Package Boundaries

Recommended Go package responsibilities:

| Package Area | Responsibility |
| --- | --- |
| `cmd` | Command tree, flags, output mode, exit code mapping |
| `app` | Command orchestration and workflow services |
| `identity` | Local key creation, loading, validation, permission checks |
| `config` | `propagate.yaml` parsing, normalization, validation, writing, diffing |
| `git` | Worktree detection, tracked/ignored file checks, repo-relative paths |
| `envfile` | Env scanning, parsing, masking, merge planning, atomic writes |
| `crypto` | Scope keys, value encryption, value decryption, envelopes, request signing |
| `api` | HTTP client, request signing middleware, API models, retries, error mapping |
| `tui` | Bubble Tea models for import, env push, config approval, agent guidance |
| `agents` | Agent target detection, managed block rendering, skill template writing |
| `output` | Human and JSON rendering with redaction guarantees |
| `domain` | Shared domain types and enums |

Avoid cycles between packages. The command layer should depend on app services; app services coordinate lower-level packages.

## 4. Local Data Model

### 4.1 Identity

Local identity is stored under `~/.propagate`. The CLI should support one default identity for MVP.

| Field | Purpose |
| --- | --- |
| `handle` | Human-readable display name |
| `public_key_sha` | Stable identity ID derived from canonical signing public key |
| `signing_public_key` | Public key used by the server to verify requests |
| `signing_private_key` | Local private key used to sign API requests |
| `encryption_public_key` | Recipient key used by admins to encrypt scope keys |
| `encryption_private_key` | Local private key used to decrypt scope key envelopes |
| `created_at` | Local identity creation timestamp |
| `format_version` | Local identity file version |

Implementation rules:

- Refuse to use private identity files with unsafe permissions.
- Keep signing and encryption keys separate.
- Public key SHA is derived from the signing public key, not the handle.
- Handles are display metadata and never authorization identifiers.
- If identity loading fails, commands must not fall back to anonymous behavior for cloud operations.

### 4.2 Local Profile

The local profile stores non-secret preferences.

| Field | Purpose |
| --- | --- |
| `default_api_url` | Cloud Run API base URL |
| `handle` | Current local display handle |
| `last_seen_cli_version` | Diagnostics and migration prompts |
| `preferred_output` | Optional default output mode |

The profile should not contain private keys, access tokens, env values, or decrypted data.

### 4.3 Project Config

`propagate.yaml` is the Git-backed team config.

Top-level logical fields:

| Field | Purpose |
| --- | --- |
| `version` | Config format version |
| `team` | Team ID, team name, cloud revision |
| `scopes` | Scope names, env file mappings, variable declarations, default role access |
| `members` | Active members and public identity material |
| `pending` | Join requests and access-change requests |
| `history` | Optional local non-secret resolution metadata |

Implementation rules:

- Config must never contain sensitive plaintext values, masked sensitive values, private material, raw plaintext hashes, or unmarked value-like placeholders/defaults/examples.
- Variables are sensitive by default. Sensitive declarations must use `digest: "hmac-sha-256:v1:..."`.
- Explicit `sensitivity: non_sensitive` declarations may use `literal` for short one-line values or `preview` plus `digest` for long values.
- Config validation must verify public key SHA matches the canonical signing public key.
- Env file paths must be repo-relative and normalized.
- Unknown config versions must fail with a clear upgrade message.
- Writes should be atomic.
- Preserve comments and ordering where feasible; if not feasible, use stable normalized formatting.

### 4.4 Domain Enums

Canonical values:

| Type | Values |
| --- | --- |
| Role | `admins`, `developers` |
| Permission | `none`, `read`, `write`, `admin` |
| Built-in scope | `dev`, `staging`, `prod`, `other` |
| Member status | `active`, `revoked`, `replaced` |
| Pending decision | `approve`, `decline`, `skip` |
| Output mode | `human`, `json` |

Comparisons should use normalized internal values. User-facing text may be friendlier, but serialized config and API payloads should use canonical values.

### 4.5 Env File Model

The env parser should preserve enough structure for safe rewrites.

Logical line types:

| Line Type | Handling |
| --- | --- |
| Assignment | Managed when variable belongs to selected scope/file |
| Export assignment | Preserve export marker |
| Comment | Preserve |
| Blank | Preserve |
| Unknown shell syntax | Preserve and warn if near managed variables |
| Duplicate variable | Warn and require confirmation before managing |

The parser must not evaluate shell expressions, expand variables, or execute anything.

### 4.6 Secret Material Model

Sensitive plaintext env values only exist in memory during:

- Env import.
- Env pull after decryption and before local write.
- Env push diffing and encryption.
- Env status display before masking.

Encrypted value records include:

| Field | Purpose |
| --- | --- |
| `name` | Variable name metadata |
| `env_file_path` | Repo-relative env file path |
| `ciphertext` | Encrypted env value |
| `nonce` | Value encryption nonce |
| `algorithm` | Value encryption algorithm version |
| `scope_key_version` | Scope key version |
| `expected_version_id` | Conflict detection for writes |

No raw plaintext hash should be uploaded. Variable declarations use a keyed HMAC construction where the key is the scope key and the serialized digest carries the algorithm prefix, for example `hmac-sha-256:v1:...`.

### 4.7 Command Result Model

Every command should produce an internal result object before rendering.

Common fields:

| Field | Purpose |
| --- | --- |
| `command` | Canonical command name |
| `status` | `success`, `no_change`, `canceled`, `failed` |
| `operation_id` | Present for mutating cloud operations |
| `team_id` | Present when known |
| `scope` | Present for scope-specific commands |
| `identity` | Safe identity summary: handle and public key SHA |
| `summary` | Counts and safe facts |
| `next_steps` | Safe remediation or Git workflow hints |
| `warnings` | Non-fatal safety warnings |

Render human and JSON output from the same result object. Do not build command behavior by parsing rendered text.

## 5. Global CLI Contract

### 5.1 Global Flags

Recommended global flags:

| Flag | Behavior |
| --- | --- |
| `--json` | Render stable machine-readable output |
| `--api-url` | Override Cloud Run API base URL |
| `--profile` | Select local profile when multi-profile support is added |
| `--no-color` | Disable terminal color |
| `--debug` | Enable safe debug diagnostics without env values |
| `--non-interactive` | Refuse prompts and require explicit flags for risky actions |

Command-specific write flags:

| Flag | Behavior |
| --- | --- |
| `--dry-run` | Plan without local or cloud mutation |
| `--yes` | Confirm safe non-interactive operations where allowed |
| `--force` | Reserved for explicit high-risk overrides; avoid in MVP unless necessary |

### 5.2 Exit Codes

Exit codes must be stable.

| Exit Code | Meaning |
| --- | --- |
| 0 | Success |
| 1 | Internal or unexpected error |
| 2 | Usage error or invalid flags |
| 3 | Validation error in local config, identity, env file, or request |
| 4 | Permission denied |
| 5 | Cloud unavailable or network failure |
| 6 | Conflict or revision mismatch |
| 7 | User canceled |
| 8 | Non-interactive confirmation required |
| 9 | Partial local failure after cloud success; recovery instructions required |

The JSON output should include the symbolic error code as well as the process exit code.

### 5.3 Output Safety

Output rules:

- stdout, stderr, JSON, logs, errors, and panics must not contain plaintext env values.
- Masked values may appear only in human TUI/status contexts where explicitly allowed.
- JSON should prefer counts, variable names, file paths, and metadata over masked values.
- Error messages should include safe next steps.
- Debug mode may include request IDs and operation IDs, but not env values, key material, signatures, or tokens.

### 5.4 Signing Contract

Every cloud API request should be signed except unauthenticated health/version endpoints.

Signed request metadata:

| Field | Purpose |
| --- | --- |
| `public_key_sha` | Signing identity |
| `timestamp` | Replay time window |
| `nonce` | One-time replay protection value |
| `method` | Bound into signature |
| `path` | Bound into signature |
| `body_digest` | Bound into signature |
| `signature` | Ed25519 signature |
| `cli_version` | Audit and compatibility |
| `operation_id` | Idempotency for mutating operations |

The nonce and operation ID are different:

- Nonce prevents replay of a signed request.
- Operation ID makes safe retries idempotent.

## 6. Command Contracts

### 6.1 `propagate init`

Purpose: create or load local identity, initialize project config when absent, upload initial encrypted values, and offer agent guidance.

Inputs:

| Input | Required | Notes |
| --- | --- | --- |
| Handle | Required only when creating identity | Prompt in interactive mode |
| Team name | Required only for new project setup | Prompt in interactive mode |
| Scope selections | Required only for new project setup | Collected via TUI |
| Env import selections | Required only for new project setup | Collected via TUI |
| Agent guidance targets | Optional | Collected via TUI |

Local reads:

- `~/.propagate/identity`
- `~/.propagate/profile`
- Git worktree metadata
- Existing `propagate.yaml`
- Candidate env files
- Existing agent instruction or skill files

Local writes:

- `~/.propagate/identity` if missing
- `~/.propagate/profile` if missing or updated
- `propagate.yaml` for new project setup
- Agent instruction or skill files when confirmed

API calls:

- New project: `POST /v1/teams/setup`
- Existing project: no cloud write required; may call identity/status endpoint if online validation is enabled

Success result:

| Field | Meaning |
| --- | --- |
| `identity_created` | Whether a new local identity was created |
| `project_created` | Whether `propagate.yaml` was created |
| `scopes_created` | Scope names created in cloud/config |
| `variables_uploaded_count` | Count of encrypted values uploaded |
| `agent_guidance` | Created, updated, skipped, unavailable, or failed |
| `next_steps` | Git add/commit guidance or team join guidance |

Failure behavior:

- If identity creation fails, do not modify project files.
- If env import is canceled, do not create cloud team.
- If cloud setup fails, do not write a normal complete config.
- If cloud setup succeeds but config write fails, return exit code 9 and recovery instructions.
- If agent guidance fails, project setup remains successful and the failure is reported as a warning unless the user explicitly requested guidance-only strict mode.

### 6.2 `propagate team join`

Purpose: add the current local identity as a pending join request in `propagate.yaml`.

Inputs:

| Input | Required | Notes |
| --- | --- | --- |
| Requested role | Optional | Defaults to `developers` |
| Requested scopes | Optional | Defaults to configured developer default |
| Handle | Required if identity/profile missing handle | Prompt or use existing profile |

Local reads:

- Local identity
- Local profile
- `propagate.yaml`

Local writes:

- `propagate.yaml` pending join request

API calls:

- None required for MVP.

Success result:

| Field | Meaning |
| --- | --- |
| `pending_join_added` | True when a new request was added |
| `public_key_sha` | Current identity |
| `requested_role` | Requested role |
| `requested_scopes` | Requested scope permissions |
| `next_steps` | Commit config diff, open PR, ask admin to run config push |

Failure behavior:

- Reject if no Git worktree.
- Reject invalid config.
- Reject duplicate pending join for same public key SHA.
- Reject pending join for an already active member.
- Make clear that the user does not have secret access yet.

### 6.3 `propagate config status`

Purpose: compare local config with cloud config state.

Inputs:

| Input | Required | Notes |
| --- | --- | --- |
| `--json` | Optional | Recommended for agents |

Local reads:

- Local identity when present
- `propagate.yaml`

API calls:

- `GET /v1/teams/{team_id}/config/status`

Local writes:

- None.

Success result:

| Field | Meaning |
| --- | --- |
| `local_revision` | Revision in `propagate.yaml` |
| `cloud_revision` | Current cloud revision |
| `local_config_hash` | Hash of normalized local config |
| `cloud_config_hash` | Hash of normalized cloud config |
| `local_only_changes` | Safe summary of local pending or divergent changes |
| `cloud_only_changes` | Safe summary of cloud changes not in local config |
| `recommended_action` | `none`, `push`, `pull`, or `resolve_conflict` |

Failure behavior:

- If cloud unavailable, show local config facts and return cloud unavailable.
- If config invalid, do not call cloud.

### 6.4 `propagate config pull`

Purpose: update local `propagate.yaml` from cloud config state.

Inputs:

| Input | Required | Notes |
| --- | --- | --- |
| `--dry-run` | Optional | Show what would change |
| `--yes` | Optional | Confirm overwrite in non-interactive mode where safe |

Local reads:

- Local identity
- `propagate.yaml`

API calls:

- `GET /v1/teams/{team_id}/config`

Local writes:

- `propagate.yaml`, only after confirmation when local changes exist.

Success result:

| Field | Meaning |
| --- | --- |
| `updated` | Whether config changed |
| `old_revision` | Previous local revision |
| `new_revision` | Pulled cloud revision |
| `changes` | Safe summary of member/scope/pending changes |

Failure behavior:

- Refuse to overwrite local unpushed changes without confirmation.
- In dry-run mode, never write.
- If pulled config contains env values or invalid public keys, reject it even if server sent it.

### 6.5 `propagate config push`

Purpose: push admin-approved config decisions to the cloud and update local config to match decisions.

Inputs:

| Input | Required | Notes |
| --- | --- | --- |
| Pending decisions | Required when pending items exist | TUI: approve, decline, skip |
| `--dry-run` | Optional | Validate and summarize without upload |
| `--yes` | Optional | Should not auto-approve pending joins in MVP |

Local reads:

- Local identity
- `propagate.yaml`
- Scope key envelopes needed for approved access grants

API calls:

- `GET /v1/teams/{team_id}/config/status`
- `GET /v1/teams/{team_id}/scopes/{scope}/key-envelope` or pull bundle equivalent when admin needs current scope keys
- `POST /v1/teams/{team_id}/config/push`

Local writes:

- `propagate.yaml` after successful push to remove approved/declined pending items, update members/access, leave skipped items pending, and update cloud revision.

Success result:

| Field | Meaning |
| --- | --- |
| `operation_id` | Idempotency key for config push |
| `old_revision` | Expected cloud revision |
| `new_revision` | New cloud revision |
| `approved` | Counts and safe summaries |
| `declined` | Counts and safe summaries |
| `skipped` | Counts and safe summaries |
| `envelopes_uploaded_count` | Number of encrypted scope key envelopes uploaded |
| `config_modified` | Whether local config changed |

Failure behavior:

- Non-admins may view diffs but cannot push privileged changes.
- If cloud revision differs, return conflict and do not upload changes.
- If admin cannot decrypt required scope key, fail only the affected approval path and require a new decision.
- If cloud push succeeds but local config write fails, return exit code 9 with recovery instructions.

### 6.6 `propagate env pull`

Purpose: fetch encrypted values, decrypt locally, and write configured env files.

Inputs:

| Input | Required | Notes |
| --- | --- | --- |
| `--scope` | Optional | Defaults to `dev` |
| `--dry-run` | Optional | Decrypt and plan without writing local files if allowed by policy |
| `--yes` | Optional | Confirm overwrites where safe |

Local reads:

- Local identity
- `propagate.yaml`
- Existing env files

API calls:

- `GET /v1/teams/{team_id}/scopes/{scope}/pull-bundle`
- `POST /v1/teams/{team_id}/events/pull`

Local writes:

- Configured env files, atomically, after merge plan confirmation when needed.

Success result:

| Field | Meaning |
| --- | --- |
| `scope` | Pulled scope |
| `files` | Updated file paths and safe counts |
| `variables_written_count` | Count of values written |
| `variables_preserved_count` | Unrelated local variables preserved |
| `conflicts_resolved_count` | Count of confirmed overwrites |
| `pull_event_recorded` | Whether audit event was recorded |

Failure behavior:

- If read access denied, write no files.
- If scope key envelope cannot decrypt, write no files.
- If env file merge has conflicts and no TTY/confirmation, write no files.
- For `prod`, require extra confirmation before local write.
- If pull event recording fails after successful local write, warn but do not roll back local files.

### 6.7 `propagate env push`

Purpose: read local env files, compare with cloud, encrypt approved changes, and upload.

Inputs:

| Input | Required | Notes |
| --- | --- | --- |
| `--scope` | Optional | Defaults to configured default or `dev` |
| `--dry-run` | Optional | Show encrypted upload plan without upload |
| `--yes` | Optional | May approve safe non-interactive upload only when policy permits |

Local reads:

- Local identity
- `propagate.yaml`
- Configured env files

API calls:

- `GET /v1/teams/{team_id}/scopes/{scope}/pull-bundle`
- `POST /v1/teams/{team_id}/scopes/{scope}/env/push`

Local writes:

- `propagate.yaml`, when variable declarations or config revision change after accepted upload.

Success result:

| Field | Meaning |
| --- | --- |
| `operation_id` | Idempotency key for env push |
| `scope` | Target scope |
| `added_count` | Approved added variables |
| `changed_count` | Approved changed variables |
| `removed_count` | Approved tombstones |
| `skipped_count` | Rejected or unselected changes |
| `new_versions_count` | Created encrypted versions |
| `new_config_revision` | Accepted config revision when declarations changed |

Failure behavior:

- If write access denied, upload nothing.
- If current cloud version IDs differ from expected values, return conflict and advise pull.
- If local env file has duplicate variables, require confirmation before managing.
- If no secret changes are approved but variable declarations changed, push the metadata snapshot and update `propagate.yaml`.

### 6.8 `propagate env set`

Purpose: securely set or update one variable in the encrypted cloud store without reading a whole env file.

Usage:

| Command Shape | Behavior |
| --- | --- |
| `propagate env set API_TOKEN --scope dev` | Securely prompts for the new value and updates one variable |

Inputs:

| Input | Required | Notes |
| --- | --- | --- |
| Variable name | Required | Positional name only, never the value |
| `--scope` | Optional | Defaults to configured default or `dev` |
| Secure prompt value | Required | No-echo terminal prompt |
| `--dry-run` | Optional | Validate and show add/change plan without upload |
| `--yes` | Optional | Must not bypass secure value prompt |

Local reads:

- Local identity
- `propagate.yaml`

API calls:

- `GET /v1/teams/{team_id}/scopes/{scope}/pull-bundle`
- `POST /v1/teams/{team_id}/scopes/{scope}/env/push`

Local writes:

- `propagate.yaml`, when the variable declaration and config revision change after accepted upload.

Success result:

| Field | Meaning |
| --- | --- |
| `operation_id` | Idempotency key for the single-value update |
| `scope` | Target scope |
| `variable` | Variable name |
| `change_type` | `added` or `changed` |
| `new_versions_count` | Should be one |
| `new_config_revision` | Accepted config revision when declaration changed |

Failure behavior:

- If no TTY is available for secure prompt, fail with confirmation/input-required guidance.
- If a plaintext value is passed as an extra positional argument, reject the command.
- If write access is denied, upload nothing.
- If the current cloud version ID differs from the expected value, return conflict and advise pull.
- For `prod`, require extra confirmation before prompting/uploading.
- Do not update local env files unless a future explicit flag requests it.

### 6.9 `propagate env status`

Purpose: show cloud env state for a scope without writing files, and compare local env values against the latest cloud YAML declarations.

Inputs:

| Input | Required | Notes |
| --- | --- | --- |
| `--scope` | Optional | Defaults to `dev` |
| `--json` | Optional | JSON should avoid masked values by default |

Local reads:

- Local identity
- `propagate.yaml`

API calls:

- `GET /v1/teams/{team_id}/config`
- `GET /v1/teams/{team_id}/scopes/{scope}/env/status`

Local writes:

- None.

Success result:

| Field | Meaning |
| --- | --- |
| `scope` | Scope checked |
| `variables` | Variable names and safe metadata |
| `config_stale` | Whether local `propagate.yaml` is behind the cloud revision |
| `local_state` | Per-variable comparison: equal, missing, different, or undeclared |
| `last_updated` | Last update metadata |
| `can_read` | Whether actor has read access |

Human output may display masked values after local decryption. JSON should default to variable names, declaration digests, local state, and metadata; it must not include plaintext or masked values. If the cloud config revision is newer, suggest `propagate config pull`. If local values are missing or digest-mismatched, suggest `propagate env pull`.

Failure behavior:

- If read access denied, show identity and requested scope, then write nothing.
- If decrypt fails, return a crypto/access error and do not show partial plaintext.

### 6.10 `propagate team status`

Purpose: show team membership, pending requests, access changes, and pull activity.

Inputs:

| Input | Required | Notes |
| --- | --- | --- |
| `--json` | Optional | Recommended for agents |

Local reads:

- Local identity
- `propagate.yaml`

API calls:

- `GET /v1/teams/{team_id}/status`

Local writes:

- None.

Success result:

| Field | Meaning |
| --- | --- |
| `team` | Team name, ID, revisions |
| `current_identity` | Handle, public key SHA, role |
| `members` | Members grouped by role |
| `pending` | Pending local config requests |
| `last_pulls` | Last pull by member and scope |
| `never_pulled` | Members with no pull activity |

Failure behavior:

- If cloud unavailable, show local config membership and mark audit activity unavailable.
- If identity is not a member, show pending/request guidance.

### 6.11 Agent Guidance Flow

Agent guidance is exposed through `propagate init` in MVP. A later `propagate agents setup` command can reuse the same service.

Inputs:

| Input | Required | Notes |
| --- | --- | --- |
| Target selection | Optional | TUI when multiple agent systems are detected |
| Diff approval | Required for existing files | Unless non-interactive mode explicitly approves safe changes |

Local reads:

- Git worktree metadata
- Known agent instruction and skill locations
- Existing instruction files

Local writes:

- Managed block or dedicated Propagate skill file.

API calls:

- None.

Failure behavior:

- Never write env values or private material into generated agent instructions.
- Refuse malformed managed blocks unless user confirms repair.
- Preserve unrelated content.
- Report skipped/failed targets without breaking project setup.

## 7. Endpoint Contracts

### 7.1 Common Request Rules

All API endpoints should use:

- HTTPS only.
- Signed requests except health/version endpoints.
- Stable JSON request and response envelopes.
- Request IDs generated by the API.
- Operation IDs supplied by the CLI for mutating operations.
- Versioned API paths under `/v1`.

Common response envelope:

| Field | Purpose |
| --- | --- |
| `ok` | Boolean success marker |
| `request_id` | Server-generated request ID |
| `operation_id` | Client operation ID when present |
| `data` | Successful response payload |
| `error` | Structured error payload when failed |
| `warnings` | Safe non-fatal warnings |

Common error payload:

| Field | Purpose |
| --- | --- |
| `code` | Stable symbolic code |
| `message` | Safe human-readable message |
| `retryable` | Whether retry may succeed without user changes |
| `details` | Safe structured metadata |
| `next_steps` | Safe remediation hints |

### 7.2 Endpoint Summary

| Endpoint | Used By | Responsibility |
| --- | --- | --- |
| `GET /v1/version` | All commands | API compatibility and server version |
| `POST /v1/teams/setup` | `init` | Create team, first admin, scopes, encrypted initial values, envelopes |
| `GET /v1/teams/{team_id}/config/status` | `config status`, `config push` | Return revision/hash comparison metadata |
| `GET /v1/teams/{team_id}/config` | `config pull` | Return current normalized config snapshot |
| `POST /v1/teams/{team_id}/config/push` | `config push` | Apply admin-approved config decisions and envelopes |
| `GET /v1/teams/{team_id}/scopes/{scope}/key-envelope` | `config push` | Return the actor's active encrypted scope key envelope for approval/envelope creation |
| `GET /v1/teams/{team_id}/scopes/{scope}/pull-bundle` | `env pull`, `env push`, `env set` | Return active envelope and encrypted current values |
| `POST /v1/teams/{team_id}/scopes/{scope}/env/push` | `env push`, `env set` | Apply encrypted upserts/removals, including single-value updates, with version checks |
| `GET /v1/teams/{team_id}/scopes/{scope}/env/status` | `env status` | Return safe env metadata and encrypted values if needed for masking |
| `POST /v1/teams/{team_id}/events/pull` | `env pull` | Record successful pull event |
| `GET /v1/teams/{team_id}/status` | `team status` | Return membership and audit summaries |

### 7.3 `GET /v1/version`

Response responsibilities:

- Return API version.
- Return minimum supported CLI version.
- Return recommended CLI version when available.
- Return server timestamp.
- Return feature flags that are safe for clients to know.

Server behavior:

- Does not require request signing.
- Must not return secrets, database details, or deployment internals.
- Should be cacheable for a short period.

### 7.4 `POST /v1/teams/setup`

Request responsibilities:

- Include first admin public identity material.
- Include normalized config snapshot with safe variable declarations.
- Include scopes and env file mappings.
- Include encrypted initial secret versions.
- Include first admin encrypted scope key envelopes.
- Include operation ID.

Response responsibilities:

- Return team ID.
- Return first cloud config revision.
- Return accepted scope IDs/names.
- Return uploaded encrypted variable counts.
- Return warnings for ignored metadata or non-fatal audit issues.

Server behavior:

- Verify request signature.
- Reserve replay nonce.
- Enforce idempotency by operation ID.
- Create team transactionally through stored functions.
- Reject any config snapshot containing sensitive plaintext values or raw plaintext hashes.

### 7.5 `GET /v1/teams/{team_id}/config/status`

Request responsibilities:

- Include local revision and local config hash as query parameters or signed request metadata.

Response responsibilities:

- Return cloud revision.
- Return cloud config hash.
- Return whether local revision is equal, behind, ahead, or conflicting.
- Return safe summary of cloud-only changes when available.

Server behavior:

- Verify signature.
- Allow active members to read status.
- Do not return plaintext or masked env values.

### 7.6 `GET /v1/teams/{team_id}/config`

Response responsibilities:

- Return current normalized config snapshot.
- Return cloud revision and config hash.
- Return server timestamp.

Server behavior:

- Verify signature.
- Allow active members to pull config.
- Validate that stored config snapshot contains no env values before returning it.

### 7.7 `POST /v1/teams/{team_id}/config/push`

Request responsibilities:

- Include expected cloud revision.
- Include normalized target config snapshot with safe variable declarations.
- Include approved, declined, and skipped decision summaries.
- Include encrypted scope key envelopes for newly approved scope access.
- Include operation ID.

Response responsibilities:

- Return new config revision.
- Return applied decisions.
- Return envelope counts.
- Return audit event IDs or safe counts.

Server behavior:

- Verify signature.
- Reserve replay nonce.
- Verify active admin permission.
- Verify expected revision.
- Enforce idempotency by operation ID.
- Apply decisions transactionally through stored functions.
- Reject sensitive plaintext env values and raw plaintext hashes in snapshots or metadata.

### 7.8 `GET /v1/teams/{team_id}/scopes/{scope}/pull-bundle`

Response responsibilities:

- Return active encrypted scope key envelope for the actor.
- Return env file mappings for the scope.
- Return current encrypted secret versions.
- Return variable metadata, current version IDs, algorithms, and nonces.
- Return config revision used for authorization.

Server behavior:

- Verify signature.
- Verify read access.
- Return no bundle if access denied or member revoked.
- Return only encrypted values and safe metadata.

### 7.9 `GET /v1/teams/{team_id}/scopes/{scope}/key-envelope`

Response responsibilities:

- Return active encrypted scope key envelope for the actor.
- Return scope key version.
- Return envelope algorithm and creation metadata.
- Return config revision used for authorization.

Server behavior:

- Verify signature.
- Verify read access.
- Return no envelope if access is denied, member is revoked, or the actor has no active envelope.
- Return no encrypted env values from this endpoint.

### 7.10 `POST /v1/teams/{team_id}/scopes/{scope}/env/push`

Request responsibilities:

- Include operation ID.
- Include expected config revision.
- Include expected current version IDs for changed/removed variables.
- Include encrypted new value versions. `env set` sends exactly one encrypted upsert.
- Include tombstones for approved removals.
- Include safe counts and env file paths.

Response responsibilities:

- Return created version IDs.
- Return conflict list if expected versions do not match.
- Return audit event count.
- Return final current version IDs for updated variables.

Server behavior:

- Verify signature.
- Reserve replay nonce.
- Verify write access.
- Verify expected versions.
- Apply encrypted versions and tombstones transactionally.
- Reject sensitive plaintext env values and raw plaintext hashes.

### 7.11 `GET /v1/teams/{team_id}/scopes/{scope}/env/status`

Response responsibilities:

- Return variable names, env file paths, current version IDs, last update metadata.
- Return encrypted values and envelope only if the CLI needs local masking for human output and the actor has read access.

Server behavior:

- Verify signature.
- Verify read access.
- Return safe metadata only for JSON mode unless encrypted values are explicitly needed by the CLI.

### 7.12 `POST /v1/teams/{team_id}/events/pull`

Request responsibilities:

- Include scope.
- Include env file paths.
- Include config revision.
- Include variable counts.
- Include CLI version and safe client kind metadata.

Response responsibilities:

- Return event ID or recorded count.

Server behavior:

- Verify signature.
- Verify read access.
- Append audit event.
- Do not require env values or masked values.

### 7.13 `GET /v1/teams/{team_id}/status`

Response responsibilities:

- Return team metadata.
- Return active and revoked members where appropriate.
- Return pending/recent access metadata.
- Return last pull per member/scope.
- Return members who have never pulled.
- Return current actor role.

Server behavior:

- Verify signature.
- Allow active members to view team status.
- Redact sensitive metadata.

## 8. Error Handling

### 8.1 Error Taxonomy

| Error Code | Meaning | Retryable |
| --- | --- | --- |
| `usage_error` | Invalid flags or command arguments | No |
| `identity_missing` | Local identity required but absent | No |
| `identity_corrupt` | Local identity cannot be parsed or validated | No |
| `unsafe_permissions` | Local identity permissions are too broad | No |
| `not_git_repo` | Command requires Git worktree | No |
| `config_missing` | `propagate.yaml` required but absent | No |
| `config_invalid` | Config failed validation | No |
| `env_parse_error` | Env file could not be safely parsed | No |
| `confirmation_required` | Non-interactive mode cannot proceed | No |
| `permission_denied` | Server or local access check denied operation | No |
| `revision_conflict` | Cloud config revision mismatch | No |
| `secret_version_conflict` | Env push expected version mismatch | No |
| `cloud_unavailable` | Network/API unavailable | Yes |
| `replay_rejected` | Nonce already used or request replayed | No |
| `clock_skew` | Request timestamp outside server window | Maybe, after clock fix |
| `crypto_error` | Decryption/encryption failed | No |
| `partial_local_failure` | Cloud succeeded but local write failed | Maybe, with recovery |
| `internal_error` | Unexpected failure | Maybe |

### 8.2 HTTP Mapping

| HTTP Status | API Code Examples | CLI Exit |
| --- | --- | --- |
| 400 | `usage_error`, `config_invalid` | 2 or 3 |
| 401 | `signature_invalid`, `clock_skew`, `replay_rejected` | 3 or 4 |
| 403 | `permission_denied` | 4 |
| 404 | `team_not_found`, `scope_not_found` | 3 |
| 409 | `revision_conflict`, `secret_version_conflict`, `idempotency_conflict` | 6 |
| 422 | `validation_failed`, `plaintext_rejected` | 3 |
| 429 | `rate_limited` | 5 |
| 500 | `server_error` | 1 |
| 503 | `cloud_unavailable` | 5 |

### 8.3 Error Message Rules

Error messages should:

- Name the command and safe target, such as team or scope.
- Include the current identity public key SHA when access is denied.
- State whether local files were written.
- State whether cloud state changed.
- Provide safe next steps.

Error messages should not:

- Include env values.
- Include masked values unless the command is already an approved masking context.
- Include private key paths beyond `~/.propagate`.
- Include raw signatures, tokens, database errors, or stack traces by default.

### 8.4 Recovery Rules

| Scenario | Recovery |
| --- | --- |
| Cloud setup succeeded but config write failed | Print team ID and operation ID; suggest retrying config pull/recovery flow |
| Config push succeeded but local config update failed | Print new cloud revision and local file write error; suggest rerunning config pull |
| Env pull wrote some files and then failed | Atomic writes should prevent partial files; report exact files updated or unchanged |
| Env push conflict | Pull latest, review diff, retry push |
| Lost private key | Create new identity and submit join request |
| Admin cannot decrypt scope key | Another admin with access must approve or recover scope key; do not fabricate a new key silently |

## 9. Testing Plan

### 9.1 Unit Tests

| Area | Required Tests |
| --- | --- |
| Identity | Create/load, corrupted file, unsafe permissions, public key SHA stability |
| Config | Valid examples, unknown versions, invalid keys, env value rejection, unsafe paths, duplicate members |
| Env parser | Quotes, comments, blank lines, export syntax, duplicate variables, unknown syntax preservation |
| Masking | Short values, empty values, Unicode, no accidental full reveal |
| Secure prompting | `env set` no-echo prompt, no positional value acceptance, no prompt leaks in errors |
| Crypto | Round trip, wrong recipient failure, associated data mismatch, nonce uniqueness |
| API signing | Canonicalization, body digest mismatch, timestamp skew, nonce inclusion |
| Error mapping | API errors to exit codes and messages |
| Agent guidance | Detection, managed block insertion/replacement, malformed markers, no secret leakage |

### 9.2 TUI Tests

Test Bubble Tea models as deterministic state machines.

| TUI | Required Tests |
| --- | --- |
| Env import | Include/exclude files, assign scopes, custom scope, cancel |
| Env push | Approve all, approve selected, reject selected, cancel |
| Config push | Approve, decline, skip, view details, cancel |
| Agent guidance | Select targets, preview diff, skip all, cancel |

TUI tests should assert returned decisions, not terminal styling.

### 9.3 API Client Tests

Use a fake HTTP server.

Required coverage:

- Signed request headers/metadata present.
- Retries preserve operation ID but use a fresh nonce.
- Retry only retryable errors.
- JSON error mapping.
- Cloud unavailable handling.
- Request timeout behavior.
- No env values in request logs.

### 9.4 Integration Tests

Recommended integration flows:

- First admin setup with one `.env`.
- Existing project `init` path with agent guidance offer.
- Developer join request.
- Admin config push approving join.
- Env pull into missing local file.
- Env pull with existing unrelated variables.
- Env push added/changed/removed variables.
- Env set adding one variable.
- Env set changing one variable.
- Env push conflict with stale version ID.
- Config pull with local pending changes.
- Team status with last pull and never-pulled members.

### 9.5 End-to-End Tests

End-to-end tests should run against disposable backend resources or a local API/Postgres stack.

Critical end-to-end assertions:

- `propagate.yaml` never contains sensitive plaintext values or raw plaintext hashes after any command.
- API receives encrypted env values only.
- Pull access denial writes no files.
- Write access denial uploads nothing.
- Replayed signed request is rejected.
- Idempotent retry does not duplicate versions or audit events.
- Agent guidance contains no env values, private keys, prompts, or tokens.

### 9.6 Security Regression Tests

Maintain a suite with sentinel secret strings.

The sentinel must not appear in:

- stdout
- stderr
- JSON output
- logs
- `propagate.yaml`
- generated agent instructions or skills
- audit event payloads
- API error bodies
- panic output

## 10. Edge Case Checklist

### 10.1 Local State

| Edge Case | Required Behavior |
| --- | --- |
| Missing `~/.propagate` | Create with restrictive permissions |
| Identity file unreadable | Fail with recovery guidance |
| Identity file permissions too broad | Refuse until fixed |
| Corrupted identity | Refuse cloud commands and suggest new identity/recovery |
| Missing Git repo | Refuse project setup |
| Multiple Git worktrees | Use current worktree root and repo-relative paths |
| Existing invalid config | Refuse mutation until repaired |
| Existing `propagate.yml` | Suggest canonical `propagate.yaml` rename |

### 10.2 Config And Membership

| Edge Case | Required Behavior |
| --- | --- |
| Duplicate pending join | Do not add another |
| Pending join for active member | Reject locally |
| Duplicate handle | Allow, but show public key SHA clearly |
| Public key SHA mismatch | Reject config |
| Pending item skipped | Leave pending in config and upload nothing for it |
| Declined item | Remove from pending and record safe history/audit |

### 10.3 Env Files

| Edge Case | Required Behavior |
| --- | --- |
| Missing env file during pull | Prompt to create or skip in non-interactive mode |
| Existing local value differs | Prompt before overwrite |
| Removed cloud variable exists locally | Preserve by default and warn |
| Duplicate variable in same file | Warn and require confirmation |
| Duplicate variable across files in same scope | Warn and require confirmation |
| Tracked env file | Warn strongly and offer `.gitignore` help |
| Unknown shell syntax | Preserve and avoid managing that line |

### 10.4 Cloud And Concurrency

| Edge Case | Required Behavior |
| --- | --- |
| Cloud unavailable | Do local-only work where safe; no mutation assumed |
| Config revision mismatch | Stop and instruct pull/resolve |
| Secret version mismatch | Stop affected env push or env set and instruct pull/retry |
| Replay rejection | Do not retry same signed request with same nonce |
| Clock skew | Show clock guidance |
| Rate limited | Respect retry metadata and return cloud unavailable/rate limited |
| Idempotent retry after timeout | Reuse operation ID with fresh nonce |

### 10.5 Agent And Non-Interactive Use

| Edge Case | Required Behavior |
| --- | --- |
| No TTY and confirmation required | Exit with confirmation-required code |
| Agent guidance file has malformed markers | Refuse automatic edit and explain repair |
| Multiple agent targets | Let user choose; in non-interactive mode require explicit target list |
| Generated guidance would include unsafe text | Refuse write and report validation error |
| `env set` receives value as extra positional arg | Reject command and instruct secure prompt usage |
| `env set` has no TTY | Fail unless a future explicit non-echo input channel is provided |

## 11. Implementation Order

Recommended implementation sequence:

1. Domain types, error taxonomy, output renderer.
2. Identity creation/loading and file permission checks.
3. Config parser/validator/writer with env value rejection.
4. Git worktree and env scanner.
5. Env parser and merge planner.
6. Crypto envelope/value operations.
7. API signing and client error mapping with fake server tests.
8. `config status` and `team status` read-only flows.
9. `init` identity-only path and existing-project path.
10. New project setup without agent guidance.
11. `team join`.
12. `config pull`.
13. `config push`.
14. `env pull`.
15. `env push`.
16. `env set`.
17. `env status`.
18. Agent guidance installer.
19. End-to-end and security regression tests.

This order front-loads data safety, validation, and read-only contracts before adding mutating flows.

## 12. Definition Of Done

The CLI MVP is implementation-ready when:

- Every command returns stable human and JSON results.
- Every mutating command supports dry-run where defined.
- Every API request is signed and replay-protected.
- Every cloud mutation carries an operation ID.
- `env set` uses secure no-echo prompting and rejects positional plaintext values.
- Config and env writes are atomic.
- `propagate.yaml` never contains sensitive plaintext values or raw plaintext hashes.
- No tests leak sentinel env values.
- Permission denied paths write no local env files and upload nothing.
- Conflict paths leave local and cloud state unchanged.
- Agent guidance is idempotent and safe.
- The end-to-end flows pass against a disposable backend environment.
