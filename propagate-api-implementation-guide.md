# Propagate API Implementation Guide

## 1. Purpose

This document gives implementation guidance for the Propagate backend API. It complements:

- `propagate-prd.md`, which defines product behavior.
- `propagate-technical-design.md`, which defines architecture and infrastructure.
- `propagate-cli-implementation-guide.md`, which defines CLI-facing contracts.

This guide focuses on the Go Cloud Run API: routing, request signing, endpoint contracts, stored function boundaries, error handling, testing, observability, and edge cases.

The selected backend stack is:

- API: Go HTTPS service deployed on Google Cloud Run.
- Database: Supabase Postgres.
- Data-local logic: Postgres stored functions and constraints.
- Infrastructure: Terraform.
- Database changes: versioned SQL migrations.

The API is the signed HTTPS boundary between the CLI and Supabase Postgres. It must never decrypt env values or plaintext scope keys.

## 2. API Principles

Implementation should follow these rules:

- Treat the API as a workflow boundary, not a raw table gateway.
- Verify request signatures before invoking protected stored functions.
- Reject replayed signed requests.
- Derive authorization from database state, never from client-provided access claims.
- Keep env values and plaintext scope keys out of request logs, API errors, audit metadata, and database plaintext columns.
- Require expected config revisions and secret version IDs for writes.
- Use operation IDs for idempotent mutating requests.
- Keep endpoint responses stable and versioned for older CLI compatibility.
- Push transactional data changes into stored functions where that improves atomicity and performance.
- Return safe remediation hints in structured errors.

## 3. Service Architecture

Recommended Go service layers:

| Layer | Responsibility |
| --- | --- |
| Router | Route `/v1` endpoints, method checks, path parameter decoding |
| Request ID middleware | Attach request ID to context, response envelope, and logs |
| Body limit middleware | Enforce maximum request body sizes per route |
| JSON layer | Decode/encode stable request and response envelopes |
| Signature middleware | Canonicalize signed requests, verify Ed25519 signatures, validate body digest and timestamp |
| Replay middleware | Reserve nonce in Postgres and reject duplicate signed requests |
| Auth context layer | Resolve team, actor, management access, scope, and effective permissions |
| Handler layer | Implement endpoint workflows and call stored functions |
| Database layer | Manage Postgres pool, call stored functions, map SQL errors |
| Error layer | Convert domain errors into HTTP statuses and stable API codes |
| Observability layer | Structured logs, safe metrics, latency, operation IDs, audit correlation |
| Config layer | Load runtime settings from environment variables and Secret Manager bindings |

Handlers should be thin. They validate request shape, call domain services or stored functions, and return response envelopes.

## 4. Runtime Configuration

Cloud Run runtime settings:

| Setting | Guidance |
| --- | --- |
| Minimum instances | `0` for MVP cost control |
| Maximum instances | Conservative cap to protect Supabase connection limits |
| Concurrency | Start moderate; tune with load tests and DB pool behavior |
| CPU/memory | Start small; increase only after measuring latency and crypto verification load |
| Request timeout | Long enough for config push/env push transactions, short enough to fail stuck requests |
| Database pool | Bounded pool with max connections below Supabase limits |
| Secrets | Load DB URL, service role key if used, and API runtime secrets from Secret Manager/Cloud Run secrets |
| Logs | Structured logs with redaction; no request bodies by default |

Environment variables should include:

| Variable | Purpose |
| --- | --- |
| `PROPAGATE_API_ENV` | `dev`, `staging`, or `prod` |
| `PROPAGATE_API_VERSION` | API build/version identifier |
| `PROPAGATE_MIN_CLI_VERSION` | Minimum supported CLI version |
| `PROPAGATE_RECOMMENDED_CLI_VERSION` | Optional upgrade hint |
| `PROPAGATE_DATABASE_URL` | Supabase Postgres connection string from secret binding |
| `PROPAGATE_REQUEST_SKEW_SECONDS` | Allowed signing timestamp skew |
| `PROPAGATE_MAX_BODY_BYTES` | Default request body limit |
| `PROPAGATE_LOG_LEVEL` | Runtime log level |

Secrets must not be committed to Terraform variables, Terraform state outputs, source control, or logs.

## 5. API Data Model

The API works with these logical model groups.

### 5.1 Actor Identity

Request actor fields:

| Field | Purpose |
| --- | --- |
| `public_key_sha` | Stable signing identity |
| `signing_public_key` | Used only during setup or membership metadata validation |
| `handle` | Display metadata |
| `team_id` | Team context for protected routes |
| `management` | Derived from database membership |
| `status` | Active, revoked, or replaced |

Only `public_key_sha` and signed request metadata identify the caller. Handles are never authorization inputs.

### 5.2 Config Snapshot

Config snapshot fields:

| Field | Purpose |
| --- | --- |
| `version` | Config format version |
| `team` | Team ID, name, revision |
| `scopes` | Scope names, env file mappings, variable declarations |
| `members` | Public identity material, management bit, and per-scope permissions |
| `pending` | Non-secret pending requests |
| `history` | Optional non-secret resolution metadata |

The API must validate that config snapshots contain no sensitive plaintext values, masked sensitive values, raw plaintext hashes, private keys, or tokens. Sensitive variable declarations must use an algorithm-prefixed keyed digest such as `hmac-sha-256:v1:...`. Explicit `non_sensitive` declarations may contain short `literal` values or truncated `preview` values.

### 5.3 Encrypted Secret Records

Encrypted secret write fields:

| Field | Purpose |
| --- | --- |
| `scope` | Scope name or ID |
| `env_file_path` | Repo-relative path |
| `name` | Variable name |
| `ciphertext` | Encrypted env value |
| `nonce` | Value encryption nonce |
| `algorithm` | Algorithm identifier |
| `scope_key_version` | Scope key version used |
| `expected_version_id` | Version precondition for update/removal |
| `operation_id` | Idempotency key |

The API should validate shape and metadata, but it cannot decrypt or inspect ciphertext.

### 5.4 Scope Key Envelopes

Envelope fields:

| Field | Purpose |
| --- | --- |
| `scope` | Scope name or ID |
| `recipient_key_sha` | Member receiving access |
| `scope_key_version` | Scope key version |
| `encrypted_scope_key` | Ciphertext encrypted to recipient |
| `algorithm` | Envelope algorithm |
| `created_by_key_sha` | Management actor |
| `config_revision` | Revision granting access |

The API stores and returns envelopes, but never decrypts them.

### 5.5 Audit Metadata

Allowed audit metadata:

- Actor public key SHA.
- Actor handle at event time.
- Team ID.
- Scope.
- Env file mapping.
- Config revision.
- CLI version.
- Client kind.
- Agent adapter name when known.
- Operation ID.
- Counts of variables added, changed, removed, pulled, skipped, approved, or declined.

Forbidden audit metadata:

- Sensitive plaintext env values.
- Masked env values.
- Raw plaintext hashes.
- Prompt text.
- Conversation content.
- Private keys.
- Access tokens.
- Absolute local paths outside repo-relative env mappings.

## 6. Request Signing And Replay Protection

### 6.1 Signed Request Inputs

All protected endpoints require a signature over:

| Field | Purpose |
| --- | --- |
| HTTP method | Prevent method substitution |
| Request path | Prevent path substitution |
| Canonical query | Prevent query tampering |
| Body digest | Bind signature to JSON body |
| Timestamp | Bound replay window |
| Nonce | One-time replay protection |
| Public key SHA | Actor identity |
| CLI version | Compatibility and audit context |
| Operation ID | Idempotency context when present |

Canonicalization must be deterministic and shared with CLI test fixtures. The server should reject requests when canonicalization inputs are missing, malformed, or inconsistent with the body.

### 6.2 Verification Flow

Protected endpoint flow:

1. Assign request ID.
2. Enforce method and body size limit.
3. Decode body where required, but do not log it.
4. Validate timestamp is within allowed skew.
5. Load signing public key for the actor/team, or use setup-specific public key validation for first team creation.
6. Verify body digest.
7. Verify Ed25519 signature.
8. Reserve nonce in Postgres.
9. Resolve actor membership and effective permission where required.
10. Invoke handler workflow.

Nonce reservation should be atomic through a stored function or direct insert with a unique constraint. Never do lookup-then-insert.

### 6.3 Nonce And Operation ID

Nonce and operation ID serve different purposes.

| Field | Purpose | Reuse |
| --- | --- | --- |
| Nonce | Blocks replay of one signed request | Never reuse |
| Operation ID | Makes retries idempotent | Reuse for same intended operation |

If a client retries after a network timeout, it should reuse the operation ID but sign a new request with a fresh nonce. The API must accept that pattern.

### 6.4 Clock Skew

If timestamp is outside the accepted window:

- Reject with `clock_skew`.
- Include server timestamp in safe error details.
- Do not reserve nonce.
- Do not call stored functions.

## 7. Authorization

Authorization is server-side and database-derived.

Evaluation order:

1. Verify signature and replay protection.
2. Load active team member by `team_id` and `public_key_sha`.
3. Resolve requested scope if route is scope-specific.
4. Apply the member's explicit scope permission for scope-specific routes.
5. Enforce required permission.

Permission requirements:

| Endpoint Area | Required Permission |
| --- | --- |
| Config status | Active member |
| Config pull | Active member |
| Config push | Management |
| Scope key envelope | Read for that scope |
| Pull bundle | Read for that scope |
| Env push | Write for that scope |
| Env status | Read for that scope |
| Pull event | Read for that scope |
| Team status | Active member |

The API may perform early permission checks in Go, but stored functions should re-check critical permissions for mutating operations.

## 8. Response And Error Contract

### 8.1 Response Envelope

Every API response should use a common envelope.

| Field | Success | Failure | Purpose |
| --- | --- | --- | --- |
| `ok` | True | False | Boolean status |
| `request_id` | Yes | Yes | Server correlation ID |
| `operation_id` | When present | When present | Client operation correlation |
| `data` | Yes | No | Endpoint payload |
| `error` | No | Yes | Structured API error |
| `warnings` | Optional | Optional | Safe non-fatal warnings |

### 8.2 Error Payload

Error fields:

| Field | Purpose |
| --- | --- |
| `code` | Stable symbolic code |
| `message` | Safe human-readable message |
| `retryable` | Whether retry may succeed without user changes |
| `details` | Safe structured details |
| `next_steps` | Safe remediation hints |

### 8.3 API Error Codes

| Code | HTTP | Retryable | Notes |
| --- | --- | --- | --- |
| `usage_error` | 400 | No | Invalid method, path, flags reflected by CLI, or malformed request |
| `validation_failed` | 422 | No | Request shape valid JSON but violates domain rules |
| `plaintext_rejected` | 422 | No | Snapshot or metadata appears to contain env values |
| `signature_missing` | 401 | No | Protected endpoint without signature metadata |
| `signature_invalid` | 401 | No | Signature or body digest mismatch |
| `clock_skew` | 401 | Maybe | Client clock likely wrong |
| `replay_rejected` | 401 | No | Nonce already used |
| `permission_denied` | 403 | No | Active actor lacks permission |
| `team_not_found` | 404 | No | Team unavailable or hidden from actor |
| `scope_not_found` | 404 | No | Scope unavailable or hidden from actor |
| `revision_conflict` | 409 | No | Expected config revision mismatch |
| `secret_version_conflict` | 409 | No | Expected secret version mismatch |
| `idempotency_conflict` | 409 | No | Operation ID reused with different payload |
| `rate_limited` | 429 | Yes | Include retry guidance |
| `cloud_unavailable` | 503 | Yes | Database or dependency unavailable |
| `server_error` | 500 | Maybe | Unexpected internal error |

Errors must not include raw SQL errors, stack traces, env values, private keys, signatures, tokens, or request bodies.

## 9. Endpoint Contracts

### 9.1 `GET /v1/version`

Authentication: not required.

Purpose: allow CLI compatibility checks.

Response data:

| Field | Purpose |
| --- | --- |
| `api_version` | Current API version |
| `min_cli_version` | Minimum supported CLI |
| `recommended_cli_version` | Optional upgrade hint |
| `server_time` | Timestamp for diagnostics |
| `features` | Safe feature flags |

Behavior:

- Do not expose deployment internals.
- Short cache is acceptable.
- Should remain available even when database is degraded if possible.

### 9.2 `POST /v1/teams/setup`

Used by: `propagate init` and `propagate quickstart`.

Authentication: signed request using the first management member's local identity.

Required permission: none, because this creates a new team. The request signature still proves control of the submitted signing private key.

Request data:

| Field | Purpose |
| --- | --- |
| `operation_id` | Idempotency |
| `team_name` | Display name |
| `first_admin` | Legacy API field name for the first management member: handle, public key SHA, signing public key, encryption public key |
| `config_snapshot` | Initial metadata-only config |
| `scopes` | Scope definitions and env file mappings |
| `encrypted_secret_versions` | Initial encrypted env values |
| `scope_key_envelopes` | First management member envelopes |
| `client` | CLI version and safe client metadata |

Validation:

- Verify signature against submitted first management member signing public key.
- Verify public key SHA matches signing public key.
- Reject snapshots containing env values.
- Verify env file paths are repo-relative and normalized.
- Verify ciphertext/envelope records have algorithm metadata.
- Verify operation ID is present.

Stored function:

- `create_team` or equivalent.

Response data:

| Field | Purpose |
| --- | --- |
| `team_id` | Created team ID |
| `config_revision` | First revision |
| `config_hash` | Hash of accepted metadata-only config |
| `scopes_created` | Accepted scopes |
| `encrypted_variables_count` | Stored encrypted value count |
| `envelopes_count` | Stored envelope count |

Failure cases:

- Duplicate operation ID with different payload returns `idempotency_conflict`.
- Invalid config returns `validation_failed`.
- Plaintext-like metadata returns `plaintext_rejected`.

### 9.3 `GET /v1/teams/{team_id}/config/status`

Authentication: signed.

Required permission: active team member.

Request inputs:

| Input | Purpose |
| --- | --- |
| `local_revision` | CLI's local config revision |
| `local_config_hash` | CLI's normalized local config hash |

Response data:

| Field | Purpose |
| --- | --- |
| `local_revision` | Echoed local revision |
| `cloud_revision` | Current cloud revision |
| `local_config_hash` | Echoed local hash |
| `cloud_config_hash` | Current cloud hash |
| `state` | `equal`, `local_ahead`, `cloud_ahead`, `conflict`, or `unknown` |
| `recommended_action` | `none`, `push`, `pull`, or `resolve_conflict` |
| `safe_summary` | Non-secret summary of differences where available |

Behavior:

- Do not return env values.
- Hide teams from non-members with a generic not found or permission error.

### 9.4 `GET /v1/teams/{team_id}/config`

Authentication: signed.

Required permission: active team member.

Response data:

| Field | Purpose |
| --- | --- |
| `config_revision` | Current cloud revision |
| `config_hash` | Current config hash |
| `config_snapshot` | Metadata-only config |
| `server_time` | Response timestamp |

Behavior:

- Validate snapshot before response.
- Reject and alert internally if stored snapshot contains env values.

### 9.5 `POST /v1/teams/{team_id}/config/push`

Authentication: signed.

Required permission: management on the team.

Request data:

| Field | Purpose |
| --- | --- |
| `operation_id` | Idempotency |
| `expected_config_revision` | Optimistic concurrency |
| `target_config_snapshot` | Metadata-only accepted config |
| `decisions` | Approved, declined, skipped summaries |
| `scope_key_envelopes` | Envelopes created by the management client |
| `client` | CLI and safe client metadata |

Validation:

- Actor must be an active management member.
- Expected revision must match current cloud revision.
- Snapshot must contain no env values.
- Approved access grants that require envelopes must include them.
- Envelope recipients must match approved members/scope access.
- Declined/skipped decisions must not grant access.

Stored function:

- `push_config_revision` or equivalent.

Response data:

| Field | Purpose |
| --- | --- |
| `old_revision` | Previous revision |
| `new_revision` | New revision |
| `config_hash` | Accepted snapshot hash |
| `applied_decisions` | Safe summary |
| `envelopes_count` | Envelopes inserted |
| `audit_events_count` | Events appended |

Conflict behavior:

- Return `revision_conflict` when expected revision is stale.
- Do not apply partial changes.

### 9.6 `GET /v1/teams/{team_id}/scopes/{scope}/pull-bundle`

Authentication: signed.

Required permission: read on scope.

Response data:

| Field | Purpose |
| --- | --- |
| `scope` | Scope name and ID |
| `config_revision` | Revision used for authorization |
| `env_file_mappings` | Repo-relative file paths |
| `scope_key_envelope` | Actor's active encrypted scope key |
| `variables` | Variable names, env file paths, current version IDs |
| `secret_versions` | Ciphertext, nonce, algorithm, scope key version |

Behavior:

- Return only encrypted values and safe metadata.
- Do not return bundle if actor has no active envelope.
- Use set-based stored function for efficient fetch.

### 9.7 `GET /v1/teams/{team_id}/scopes/{scope}/key-envelope`

Authentication: signed.

Required permission: read on scope.

Purpose: let a management client decrypt the current scope key before creating envelopes for newly approved members.

Response data:

| Field | Purpose |
| --- | --- |
| `scope` | Scope name and ID |
| `config_revision` | Revision used for authorization |
| `scope_key_version` | Current scope key version |
| `scope_key_envelope` | Actor's active encrypted scope key |
| `algorithm` | Envelope algorithm |

Behavior:

- Return no encrypted env values.
- Return permission denied if actor lacks read access.
- Return validation error if no active envelope exists for actor.

### 9.8 `POST /v1/teams/{team_id}/scopes/{scope}/env/push`

Authentication: signed.

Required permission: write on scope.

Used by: `propagate env push` and `propagate env set`.

Request data:

| Field | Purpose |
| --- | --- |
| `operation_id` | Idempotency |
| `expected_config_revision` | Config revision the CLI used |
| `target_config_snapshot` | Updated metadata snapshot when variable declarations changed |
| `upserts` | Encrypted new versions; `env set` sends exactly one upsert |
| `removals` | Tombstones with expected current version IDs |
| `safe_counts` | Added, changed, removed counts |
| `client` | CLI and safe client metadata |

Validation:

- Actor must have write access.
- Upserts must include ciphertext, nonce, algorithm, and expected current version where appropriate.
- A single-upsert request from `env set` is valid and should use the same transaction path as broader env push.
- A metadata-only env push is valid when it carries `target_config_snapshot`; this lets the CLI update sensitivity declarations without changing encrypted values.
- Removals must include expected current version.
- Env file paths must be repo-relative.
- Reject sensitive plaintext env values and raw plaintext hashes.

Stored function:

- `apply_env_push` or equivalent.

Response data:

| Field | Purpose |
| --- | --- |
| `created_versions` | Variable/version IDs for accepted upserts |
| `removed_variables` | Tombstone summaries |
| `conflicts` | Version conflicts, if any |
| `config_revision` | New config revision when declarations changed |
| `config_hash` | Hash of accepted config snapshot |
| `audit_events_count` | Events appended |

Conflict behavior:

- If any expected version mismatches, return `secret_version_conflict`.
- Prefer all-or-nothing for MVP.

### 9.9 `GET /v1/teams/{team_id}/scopes/{scope}/env/status`

Authentication: signed.

Required permission: read on scope.

Response data:

| Field | Purpose |
| --- | --- |
| `scope` | Scope name and ID |
| `config_revision` | Revision used for authorization |
| `variables` | Names, file paths, version IDs, update metadata |
| `encrypted_values` | Optional encrypted versions when CLI needs local masking |
| `scope_key_envelope` | Optional envelope when encrypted values are returned |

Behavior:

- JSON-focused clients should be able to request metadata-only status.
- Human masking requires encrypted values and local CLI decryption; the API still returns no plaintext.

### 9.10 `POST /v1/teams/{team_id}/events/pull`

Authentication: signed.

Required permission: read on scope.

Request data:

| Field | Purpose |
| --- | --- |
| `scope` | Pulled scope |
| `env_file_paths` | Repo-relative mappings |
| `config_revision` | Revision used during pull |
| `variables_count` | Count pulled/written |
| `client` | CLI and safe client metadata |

Behavior:

- Append audit event.
- Do not require or accept env values.
- If audit insert fails transiently, return retryable error. The CLI may warn after a successful local pull.

### 9.11 `GET /v1/teams/{team_id}/status`

Authentication: signed.

Required permission: active team member.

Response data:

| Field | Purpose |
| --- | --- |
| `team` | Team ID, name, revisions |
| `actor` | Current actor handle, public key SHA, management bit, scope permissions |
| `members` | Active and relevant revoked members |
| `pending_or_recent_access` | Safe metadata from config/audit |
| `last_pulls` | Last pull per member/scope |
| `never_pulled` | Active members with no pulls |

Behavior:

- Use stored function or indexed queries for audit summaries.
- Do not return env values, masked values, prompts, or private material.

### 9.12 `POST /v1/teams/{team_id}/invites` (planned)

Used by: `propagate team invite` and `propagate quickstart`.

Authentication: signed.

Required permission: management on the team.

Purpose: create a labeled PIN invite. Returns the **PIN once** in the response; persist only a verifier server-side.

Request data:

| Field | Purpose |
| --- | --- |
| `operation_id` | Idempotency |
| `label` | Management-entered invite name shown in joiner UI |
| `requested_management` | Optional management request default for pending join |
| `requested_scopes` | Optional default scope permissions for pending join |
| `client` | CLI metadata |

Response data:

| Field | Purpose |
| --- | --- |
| `invite_id` | Opaque id |
| `pin` | One-time human shareable PIN (`ddddL` pattern per PRD) |
| `expires_at` | Optional TTL |

Failure cases:

- Actor without management access.
- Too many active invites for team (optional policy).
- Invalid label (length, charset).

### 9.13 `GET /v1/teams/{team_id}/join/invites` (planned)

Authentication: not required; **strict rate limits** at edge and application layers.

Purpose: let a joiner discover **active** invites before they are team members. `team_id` is treated as an opaque capability leaked only to people who can read `propagate.yaml`.

Response data:

| Field | Purpose |
| --- | --- |
| `invites` | Array of `{ invite_id, label, created_at }` for `active` invites only |

Behavior:

- Do not return PINs, verifiers, or internal attempt counters.
- Hide teams that do not exist or apply the same generic error as other public lookups to avoid team-ID oracle behavior where desired.

### 9.14 `POST /v1/teams/{team_id}/join/invites/{invite_id}/pin` (planned)

Authentication: signed by a valid Propagate identity that is **not** required to be a team member yet. Replay protection applies.

Purpose: verify the PIN and record redemption for the calling public key.

Request data:

| Field | Purpose |
| --- | --- |
| `operation_id` | Idempotency |
| `pin` | Candidate PIN |
| `handle` | Joiner handle to echo into pending join metadata |
| `requested_management` | Optional management request override |
| `requested_scopes` | Optional override |
| `client` | CLI metadata |

Behavior:

- Compare PIN to stored verifier using constant-time comparison.
- Increment `failed_pin_attempts` on mismatch; on **third** failed attempt in the invite's lifetime, transition invite to terminal `invalidated_pin` (or delete) and return a stable error.
- On success, mark `redeemed` and bind `redeemed_by_key_sha` once; duplicate redemption attempts fail.

Response data:

| Field | Purpose |
| --- | --- |
| `redemption_id` | Optional server-issued proof the CLI can cite in `propagate.yaml` |
| `invite_id` | Echo |
| `server_time` | Timestamp |

### 9.15 `GET /v1/teams/{team_id}/invites` (planned)

Authentication: signed.

Required permission: management on the team.

Purpose: list invites including **non-active** rows for operational visibility (without PINs).

### 9.16 `POST /v1/teams/{team_id}/invites/{invite_id}/revoke` (planned)

Authentication: signed.

Required permission: management on the team.

Purpose: invalidate an invite before redemption or after policy.

## 10. Stored Function Contracts

The API should call stored functions for data-local transactions.

| Function Area | API Caller | Responsibility |
| --- | --- | --- |
| Reserve nonce | Replay middleware | Insert nonce atomically and reject duplicates |
| Resolve member access | Auth context and mutating functions | Return actor status, management bit, scope, effective permission |
| Create team | Team setup handler | Transactional team creation |
| Push config revision | Config push handler | Transactional revision update and access changes |
| Fetch config snapshot | Config handlers | Return current normalized config |
| Fetch pull bundle | Pull bundle/status handlers | Return envelope, mappings, encrypted versions |
| Apply env push | Env push and env set handlers | Transactional encrypted upserts/tombstones, including single-value updates |
| Record pull event | Pull event handler | Append audit event |
| Fetch team status | Team status handler | Membership and audit summaries |
| Create PIN invite | Team invite handler | Insert verifier, audit `invite_created` |
| Redeem PIN invite | Join handler | Atomic PIN check, attempt counter, redemption, audit |
| Revoke PIN invite | Team invite handler | Status flip and audit |

Stored function requirements:

- Use fixed search paths.
- Use tightly scoped execution privileges.
- Validate critical permissions for mutating operations.
- Avoid dynamic SQL unless absolutely necessary.
- Return typed errors or SQLSTATE/detail values that the Go API maps to stable API codes.
- Never accept sensitive plaintext env values or return decrypted env values or plaintext scope keys.

## 11. Database And Connection Handling

Connection rules:

- Use a bounded Postgres connection pool.
- Set connection max lifetime and idle lifetime to avoid stale serverless connections.
- Keep max open connections below Supabase limits and Cloud Run max instance/concurrency settings.
- Use context deadlines on every database call.
- Do not hold database connections while reading request bodies or writing large responses.

Transaction rules:

- Let stored functions own config push and env push transactions.
- Do not split one product mutation across multiple independent transactions in Go.
- Use operation ID uniqueness for idempotency.
- Treat idempotent replay with same operation ID and same payload as success or safe duplicate.
- Treat same operation ID with different payload as `idempotency_conflict`.

## 12. Validation And Redaction

### 12.1 Metadata Validation

Validate:

- Public key format and public key SHA.
- Scope names.
- Role and permission values.
- Repo-relative env file paths.
- Config snapshot version.
- Operation ID format.
- Ciphertext, nonce, and algorithm presence.
- Expected revision/version preconditions.
- Variable declaration sensitivity.
- Digest strings include an algorithm prefix.

Reject:

- Absolute env file paths.
- Path traversal.
- Unknown config versions.
- Sensitive env value fields in config snapshots.
- Raw plaintext hashes.
- Private key material.
- Access tokens in metadata.

### 12.2 Redaction

Redact before logging:

- Request bodies.
- Authorization/signature headers.
- Ciphertexts if logs would become too large or sensitive.
- Encrypted scope keys.
- Database connection strings.
- Any field name containing token, secret, private, password, or key, except public key fields that are explicitly safe.

Structured logs should prefer:

- Request ID.
- Operation ID.
- Team ID.
- Actor public key SHA.
- Endpoint.
- Status code.
- Error code.
- Latency.
- Safe counts.

## 13. Observability

Metrics:

| Metric | Purpose |
| --- | --- |
| Request count by endpoint/status | API health |
| Request latency by endpoint | Performance |
| Signature failure count | Security diagnostics |
| Replay rejection count | Replay detection |
| Permission denied count | Access diagnostics |
| Revision conflict count | Collaboration friction |
| Secret version conflict count | Env push contention |
| DB transaction failure count | Backend health |
| Cold start latency | Cloud Run tuning |

Logs:

- One structured access log per request.
- One structured error log per failed request.
- No request/response body logging by default.
- Include request ID and operation ID everywhere.

Tracing:

- Trace handler and database durations.
- Do not attach request bodies, env values, or ciphertext payloads as trace attributes.

## 14. Compatibility And Versioning

The API should be conservative about breaking CLI clients.

Versioning rules:

- All MVP endpoints live under `/v1`.
- Response fields may be added when old clients can safely ignore them.
- Existing field meanings should not change within `/v1`.
- Removing fields, changing enum values, or changing signing canonicalization requires a new API version or explicit compatibility gate.
- The version endpoint should return minimum and recommended CLI versions.
- When a CLI is too old, return a stable compatibility error with upgrade guidance.
- Feature flags may advertise optional behavior, but the server must not require a newly advertised feature until the minimum CLI version is raised.

Signing compatibility:

- Request canonicalization fixtures should be shared between CLI and API test suites.
- Any canonicalization change must support old and new formats during a migration window or require a new API version.
- Error responses for unsupported signing versions should be safe and explicit.

## 15. Testing Plan

### 15.1 Unit Tests

| Area | Required Tests |
| --- | --- |
| Request canonicalization | Stable canonical strings, query ordering, body digest binding |
| Signature verification | Valid signature, bad signature, wrong body, wrong path, wrong method |
| Timestamp validation | Accepted window, expired request, future request |
| Error mapping | Domain and SQL errors to HTTP/code/retryable |
| Redaction | Logs and errors exclude forbidden fields |
| Metadata validation | Keys, paths, management grants, scope permissions, operation IDs, plaintext rejection |
| Idempotency | Same operation ID same payload, same operation ID different payload |

### 15.2 Handler Tests

Use fake database interfaces for handler-level tests.

Required coverage:

- Every endpoint success path.
- Every endpoint permission denied path.
- Validation failure before database mutation.
- Signed route rejects missing signature.
- Version endpoint works without signature.
- Response envelopes include request IDs.
- Mutating endpoints require operation IDs.

### 15.3 Database Integration Tests

Run against disposable Postgres/Supabase-compatible database.

Required coverage:

- Nonce unique constraint and cleanup.
- Permission resolution order: explicit member scope grants before old access-rule rows during migrations.
- Config push revision conflict.
- Config push idempotent retry.
- Env push expected version conflict.
- Env push idempotent retry.
- Pull bundle returns only active envelope.
- Revoked member receives no envelope.
- Audit summary queries for team status.

### 15.4 End-To-End API Tests

Run Go API against disposable database with signed test clients.

Required flows:

- Team setup.
- Config status and pull.
- Config push approving member and uploading envelope.
- Pull bundle read.
- Env push write.
- Env set single-value write through env push endpoint.
- Env status metadata.
- Pull event audit.
- Team status.
- Replay rejection.
- Clock skew rejection.

### 15.5 Security Regression Tests

Use sentinel strings for env values, private keys, tokens, prompts, and conversations.

Assert sentinels never appear in:

- API logs.
- API errors.
- Response envelopes, except encrypted ciphertext sentinels should not be raw plaintext.
- Audit metadata.
- Config snapshots.
- Stored function inputs/outputs where plaintext is forbidden.
- Panic output.

## 16. Edge Case Checklist

| Edge Case | Required Behavior |
| --- | --- |
| Missing signature on protected route | 401 `signature_missing` |
| Invalid body digest | 401 `signature_invalid` |
| Reused nonce | 401 `replay_rejected` |
| Timestamp too old/new | 401 `clock_skew` with server time |
| Actor not active member | 403 or hidden 404 |
| Scope missing | 404 `scope_not_found` |
| Config revision mismatch | 409 `revision_conflict` |
| Secret version mismatch | 409 `secret_version_conflict` |
| Single-value env set payload | Treat as valid one-upsert env push |
| Operation ID reused with different payload | 409 `idempotency_conflict` |
| Plaintext-like config snapshot | 422 `plaintext_rejected` |
| Database unavailable | 503 `cloud_unavailable` |
| Stored function timeout | 503 or 500 with retryable flag based on outcome certainty |
| Audit insert fails after mutation | Prefer same transaction for mutation/audit; otherwise report warning only for non-critical audit |
| Supabase connection exhaustion | 503 retryable and emit metric |
| Large request body | 400 or 413 safe error before processing |
| Unknown API version | 400 `usage_error` or compatibility error |

## 17. Implementation Order

Recommended API implementation sequence:

1. Domain models, response envelopes, error codes.
2. Runtime config and structured logging with redaction.
3. Router and version endpoint.
4. Request canonicalization and signature verification with CLI-compatible fixtures.
5. Replay nonce reservation.
6. Database pool and stored function call layer.
7. Auth context and permission resolution.
8. Config status and config pull endpoints.
9. Team setup endpoint.
10. Config push endpoint.
11. Pull bundle and key envelope endpoints.
12. Env push endpoint.
13. Env set handler path using the env push endpoint.
14. Env status endpoint.
15. Pull event endpoint.
16. Team status endpoint.
17. Full integration and security regression tests.

This order establishes protocol safety and read-only paths before high-risk mutating operations.

## 18. Definition Of Done

The API MVP is implementation-ready when:

- Every protected endpoint verifies signatures, timestamps, body digests, and nonces.
- Every mutating endpoint requires operation ID and enforces idempotency.
- Every write uses expected revisions or version preconditions.
- The env push endpoint accepts one-upsert `env set` payloads without a separate backend endpoint.
- Stored functions own config push and env push transactions.
- Permission checks are derived from database state.
- Responses follow the common envelope.
- Errors use stable codes and safe details.
- Logs contain request IDs and operation IDs but no env values or private material.
- Sentinel security tests pass.
- End-to-end tests pass with a disposable database.
- The API can be deployed to Cloud Run with Terraform-managed infrastructure and migration-managed database schema.
