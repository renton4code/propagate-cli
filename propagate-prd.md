# Propagate Product Requirements Document

## 1. Overview

Propagate is a CLI-first tool for sharing environment variables across development teams. It lets a team initialize a project, encrypt environment variables locally, store encrypted values in the cloud, and manage access through a Git-backed team configuration file.

The initial product focuses on developer teams using `.env` files. Future releases may add runtime injection, CI support, production agents, and hosted dashboard workflows.

MVP implementation stack:

- CLI: Go with Bubble Tea for interactive flows.
- Backend API: Go HTTPS service deployed on Google Cloud Run.
- Database: Supabase Postgres.
- Infrastructure: Terraform for cloud resources.
- Database changes: versioned SQL migrations for schema, indexes, policies, and stored functions.

## 2. Product Goals

- Make team `.env` sharing safer than sending files through Slack, email, or docs.
- Keep the primary workflow inside the CLI.
- Use public/private key identity rather than password-based accounts for the MVP.
- Store env values encrypted in the cloud, with decryption controlled by local user keys.
- Use a project-level config file in Git to make team membership and access changes reviewable.
- Support common local development layouts, including monorepos and multiple env files.
- Give admins visibility into pending joins, scope changes, and last secret pulls.
- Make Propagate safe and predictable for AI coding agents that operate through terminal tools and repository instructions.

## 3. Non-Goals For MVP

- CI/CD integration.
- `propagate run` or process-level secret injection.
- Production runtime agent.
- Dedicated AI agent identities or autonomous secret access separate from a human user's local Propagate identity.
- Web dashboard.
- SSO or enterprise identity providers.
- Secret rotation automation.
- Browser-based signup/login.
- Complex policy language beyond roles and environment scopes.

## 4. Core Concepts

### 4.1 User Identity

A user is identified by their Propagate public key.

The user handle is human-readable metadata only. It may be a name, email, or team alias, but it is not the primary identity.

Identity material:

- Private key: stored locally in `~/.propagate`.
- Public key: shared through `propagate.yaml` and the cloud API.
- Public key SHA: used as a compact stable identifier in config and UI.
- Handle: used for readability in TUI screens, status output, and audit views.

### 4.2 Team

A team is the top-level collaboration boundary for a project. A project can be associated with one team in the MVP.

Team metadata includes:

- Team name.
- Team ID.
- Environments/scopes.
- Members.
- Roles.
- Pending invites and access changes.
- Cloud sync revision.

### 4.3 Scope

A scope represents a logical environment. Default supported scopes:

- `dev`
- `staging`
- `prod`
- `other`

Users may define additional custom scopes. Scope names should be lowercase and CLI-safe, such as `qa`, `preview`, or `demo`.

Each scope has:

- One or more env files.
- A set of encrypted variables.
- Access rules by role or member.
- Pull history.

A scope may initially be empty. Teams can create the scope metadata first, review and push it, then move existing variable declarations into it or add new variables later.

### 4.4 Config File

The project config file is `propagate.yaml`.

It is committed to Git and contains project/team metadata, member public keys, pending requests, scopes, env file mappings, and variable declarations.

Each variable declaration records the env file path, variable name, sensitivity, and a safe representation of the current value. Variables are `sensitive` by default and must be represented by a keyed digest such as `hmac-sha-256:v1:...`; raw SHA-256 is not acceptable because low-entropy secrets can be cracked offline. A variable may be explicitly marked `non_sensitive`. Non-sensitive values may be stored directly only when they fit on one short line; otherwise the config stores a short preview such as `aaa...zzz` plus the keyed digest.

Variable declaration edits are metadata-only. Changing a declaration's sensitivity, scope, or presence in `propagate.yaml` must not read local env values, decrypt cloud values, or write plaintext values into the config.

## 5. User Roles

### 5.1 Admin

Admins can:

- Initialize a project team.
- Approve pending joins.
- Approve role and scope changes.
- Push local config state to the cloud.
- Pull cloud config state.
- Create empty scope metadata for review and config push.
- Edit local variable declaration metadata before review and config push.
- Push environment variable updates for scopes they can write.
- Set individual environment variable values for scopes they can write.
- View team status and last pull events.

### 5.2 Developer

Developers can:

- Initialize their local Propagate identity.
- Request to join a project team.
- Pull environment variables for scopes they can read.
- Request access changes through config diffs.
- Propose new scope metadata through reviewable config diffs.
- Edit local variable declaration metadata for reviewable config diffs.
- Push environment variable changes and set individual values only for scopes they can write.

### 5.3 Future Roles

Potential future roles:

- Viewer: read-only access to selected scopes.
- Maintainer: can approve dev/staging changes but not prod.
- CI identity: non-human identity for CI workflows.
- Production agent: non-human identity for runtime secret retrieval.

## 6. MVP Commands

### 6.0 Shared Human Output Requirements

All non-JSON command output should use one consistent Propagate style so users and tool agents can scan any command the same way.

Human-readable output should:

- Start with a bold command title, such as `Propagate init`, `Propagate config status`, or `Propagate env pull`.
- Add `(dry run)` to the title for dry-run executions.
- Use semantic status markers consistently:
  - `✓` for completed or successful actions.
  - `•` for informational notes, skipped actions, no-op states, and dry-run summaries.
  - `!` for warnings and recoverable degraded states.
- Use colored markers and section headings by default.
- Respect `--no-color` by removing ANSI color while keeping the same text, markers, and layout.
- Keep `--json` output machine-readable and free of ANSI color, decorative symbols, or terminal-only layout.
- Render `Warnings:` and `Next steps:` with the same section style across commands and errors.
- Keep common list sections visually consistent, including `Files:`, `Changes:`, `Members:`, `Variables:`, `Requested scopes:`, `Env files:`, and `Default access:`.
- Never print plaintext env values in human output, JSON output, errors, warnings, next steps, or panic paths.

Help and version output may remain plain and script-friendly.

### 6.1 `propagate init`

Initializes the local user identity and, if the repo is not already configured, initializes Propagate for the project.

#### Behavior

1. Check for an existing Propagate keypair in `~/.propagate`.
2. If no keypair exists:
   - Generate a new SSH-compatible keypair for Propagate.
   - Store it in `~/.propagate`.
   - Ask the user for a handle, such as name or email.
   - Save the handle locally.
   - Notify the user that signup credentials were created.
3. If a keypair exists:
   - Load the local identity.
   - Notify the user that an existing Propagate identity was found.
4. Scan the repository for `propagate.yaml`.
5. If `propagate.yaml` exists:
   - Notify the user that the project already has Propagate configured.
   - Suggest `propagate team join`.
   - Do not overwrite the existing config.
6. If `propagate.yaml` does not exist:
   - Start project setup.
   - Ask user for team name.
   - Show a TUI for selecting environments:
     - `dev`
     - `staging`
     - `prod`
     - `other`
   - Default to reading `.env` and assigning discovered variables to `dev`.
   - Show discovered variables in a TUI confirmation screen.
   - Mask values, for example `password` as `p*****d`.
   - Let the user assign each variable to `dev`, `staging`, `prod`, or a custom scope.
   - Encrypt values locally for the selected scope.
   - Store encrypted secrets in the cloud.
   - Save team config to `propagate.yaml` with env file mappings and variable declarations.
   - Mark discovered variables as sensitive by default and write keyed `hmac-sha-256:v1:` digests, not plaintext values.
7. Offer to add or update AI agent guidance for the repository:
   - Detect known agent instruction and skill locations.
   - Explain that this creates agent-facing instructions only, not secret access.
   - Add a Propagate skill or managed instruction block when the user confirms.
   - Never include env values, masked values, private keys, access tokens, or decrypted output in agent guidance.
   - Preserve existing user-authored agent instructions.

#### Success Output

The command should clearly report:

- Whether a new local identity was created or an existing one was used.
- Where local identity is stored.
- Whether project config was created or already existed.
- Which scopes were created.
- How many variables were encrypted and uploaded.
- Whether AI agent guidance was added, updated, skipped, or unavailable.

#### Error Cases

- Cannot write to `~/.propagate`.
- Invalid or corrupted local keypair.
- No Git repository detected.
- Existing `propagate.yaml` is invalid.
- `.env` cannot be read.
- Cloud API is unavailable.
- User does not confirm env import.
- Cannot safely update detected agent instruction or skill files.

### 6.2 `propagate team join`

Adds the current user as a pending invite/request in `propagate.yaml`.

#### Behavior

1. Ensure local identity exists. If not, create the identity using the same local identity behavior as `propagate init`.
2. If `--init` is provided, run the existing-project portion of `propagate init` first:
   - Create or load the local identity.
   - Confirm that `propagate.yaml` already exists.
   - Offer or apply AI agent guidance using the same guidance behavior as `propagate init`.
   - Do not create a new project config or overwrite an existing config.
3. Read `propagate.yaml`.
4. Add a pending join request containing:
   - Public key SHA.
   - Full public key.
   - Handle.
   - Requested role: `developers`.
   - Requested scopes, if specified.
   - Timestamp.
5. Save the config file.
6. Notify the user explicitly that this only creates a Git-reviewed access request.
7. Tell the user to commit the config diff, open a pull request, and ask an admin to approve it.

#### Notes

The join request is Git-mediated. This lets teams review membership changes in pull requests.

The CLI output must make this workflow clear. It should not imply that the user has joined the team or received secret access yet.

The existing two-command flow remains supported:

```bash
propagate init --handle bob@example.com
propagate team join --scope dev=read
```

For an already configured repository, a developer can combine existing-project init and the join request in one command:

```bash
propagate team join --init --handle bob@example.com --scope dev=read
```

Example output:

```text
Propagate team join

✓ Join request added to propagate.yaml.
• You do not have secret access yet.

Next steps:
1. Commit this config change.
2. Open a pull request.
3. Ask a Propagate admin to run propagate config push after approval.
```

### 6.3 `propagate config push`

Synchronizes the local `propagate.yaml` state with the cloud.

#### Behavior

1. Read local `propagate.yaml`.
2. Fetch current cloud config revision.
3. Compare local config against cloud config.
4. If pending items exist, show a TUI approval menu.
5. Pending items may include:
   - Join requests.
   - Role changes.
   - Scope access changes.
6. Admin must make an explicit decision for each pending item:
   - Approve.
   - Decline.
   - Skip for later.
7. For approved members/scopes:
   - Encrypt relevant scope keys for the member public key.
   - Upload encrypted access envelopes to the cloud.
8. If the config adds new scopes:
   - Generate a fresh scope key for each new scope.
   - Encrypt that scope key for each active member who should have read access under the target config's default role access.
   - Upload encrypted access envelopes with the config push.
9. For declined items:
   - Do not grant cloud access.
   - Remove the item from the pending section of `propagate.yaml`.
   - Record the decline in local config history or cloud audit events.
10. For skipped items:
   - Leave the item pending in `propagate.yaml`.
   - Do not push any access change for that item.
11. Push accepted and declined config decisions to the cloud.
12. Update local `propagate.yaml` so it reflects the actual decisions made.
13. Update local config revision metadata.

#### Output Requirements

The command output must explicitly summarize all decisions:

- Approved joins.
- Declined joins.
- Approved role changes.
- Declined role changes.
- Approved scope changes.
- Declined scope changes.
- Skipped items that remain pending.
- Encrypted access envelopes uploaded, including envelopes for newly created scopes.
- Whether `propagate.yaml` was modified.

#### Access Control

Only admins can approve joins and role changes.

If a non-admin runs this command, the CLI should show which diffs exist but refuse to push privileged changes.

### 6.4 `propagate config pull`

Fetches current team config from the cloud and updates local `propagate.yaml`.

#### Behavior

1. Fetch cloud config.
2. Compare with local config.
3. If local unpushed changes exist, warn the user before overwriting.
4. Update `propagate.yaml`.
5. Notify the user about pulled changes.

### 6.5 `propagate config status`

Shows whether local config differs from cloud config.

#### Output Should Include

- Current local revision.
- Current cloud revision.
- Pending local-only changes.
- Cloud-only changes.
- Whether `propagate config push` or `propagate config pull` is recommended.

### 6.6 `propagate scope create`

Creates an empty scope in local `propagate.yaml` for Git review and later config push.

#### Usage

```bash
propagate scope create staging
propagate scope create preview --env-file .env.preview
propagate scope create qa --dry-run
```

#### Behavior

1. Read local `propagate.yaml`.
2. Validate that the requested scope name is supported and not already present.
3. Validate any provided `--env-file` mappings as repository-relative paths inside the worktree.
4. Add a scope with:
   - The requested scope name.
   - Empty `env_files` unless `--env-file` is supplied.
   - No variable declarations.
   - Default role access: `admins: write`; non-`prod` scopes also default to `developers: read`.
5. Validate the resulting config before writing it.
6. Save the edited `propagate.yaml`, unless `--dry-run` is used.
7. Do not prompt for source scopes or clone metadata during scope creation.
8. Suggest `propagate config status`, `propagate config edit`, `propagate config push`, and `propagate env push`.

#### Safety Requirements

- Must not read local env file values.
- Must not prompt for or store env values.
- Must not copy env file mappings or variable declarations from another scope.
- Must not decrypt cloud env values.
- Must not upload or delete encrypted cloud values directly.
- Must not write plaintext env values, masked values, private keys, or raw plaintext hashes to `propagate.yaml`.
- Must reject duplicate scope names.
- If no env file mapping is supplied, output must explain that an env file mapping is needed before seeding values into the new scope.

#### Publishing Requirements

`propagate scope create` is local metadata only. The new scope becomes cloud-visible after an admin runs `propagate config push`.

When publishing a new scope, `propagate config push` must create a fresh scope key and upload encrypted scope key envelopes for authorized active members. The server must never receive the plaintext scope key.

Users should run `propagate config edit` to add env file mappings or move declaration metadata into the new scope before publishing. Users seed values after publication through existing env workflows.

#### Output Requirements

- Scope name.
- Env file mappings, if any.
- Default role access.
- Whether `propagate.yaml` was modified.
- Next steps for publishing metadata and seeding values through existing env workflows.

### 6.7 `propagate config edit`

Opens an interactive local editor for safe variable declaration metadata in `propagate.yaml`.

#### Usage

```bash
propagate config edit
propagate config edit --dry-run
```

#### Behavior

1. Read local `propagate.yaml`.
2. List existing variable declarations by scope, env file path, variable name, and sensitivity.
3. Let the user edit each declaration's metadata:
   - Toggle sensitivity between `sensitive` and `non_sensitive`.
   - Move the declaration to another existing scope.
   - Remove the declaration from config metadata.
4. If a declaration is moved to a scope that does not yet list the declaration's env file path, add that env file mapping to the target scope and show it in the summary.
5. Validate the resulting config before writing it.
6. Save the edited `propagate.yaml`, unless `--dry-run` is used.
7. Suggest `propagate config status` and `propagate config push` after review.

#### Safety Requirements

- Must not read local env file values.
- Must not decrypt cloud env values.
- Must not upload or delete encrypted cloud values.
- Must not write plaintext env values, masked values, or raw plaintext hashes to `propagate.yaml`.
- Switching a declaration from `non_sensitive` back to `sensitive` must remove any literal or preview metadata.
- Removing a declaration removes only config metadata. Secret value deletion should continue to use the env update flow.
- Non-interactive mode must fail instead of hanging.

#### Output Requirements

- Variables before and after editing.
- Sensitivity changes.
- Scope changes.
- Removed declarations.
- Env file mappings added.
- Whether `propagate.yaml` was modified.
- Next steps for config status and push.

### 6.8 `propagate env pull`

Pulls encrypted env values from the cloud and writes them to local env files.

#### Usage

```bash
propagate env pull
propagate env pull --scope dev
propagate env pull --scope staging
propagate env pull --scope prod
```

Default scope is `dev`.

#### Behavior

1. Read `propagate.yaml`.
2. Determine env file mappings for the selected scope.
3. Check whether the user has read access to the scope.
4. Fetch encrypted variables from the cloud.
5. Decrypt locally with the user's private key.
6. Write values to the configured env file or files.
7. Preserve unrelated local variables when possible.
8. Record pull event in the cloud:
   - Public key SHA.
   - Handle.
   - Team ID.
   - Scope.
   - Env file path.
   - Timestamp.
   - Config revision.

#### Access Errors

If the user lacks read access, the CLI should say:

- Which scope was requested.
- Which identity is being used.
- That no values were written.
- How to request access.

### 6.9 `propagate env push`

Pushes local env file changes to the encrypted cloud store.

#### Usage

```bash
propagate env push
propagate env push --scope dev
propagate env push --scope staging
propagate env push --scope prod
```

Default scope is inferred from config or defaults to `dev`.

#### Behavior

1. Read configured env file or files for the scope.
2. Fetch current encrypted cloud values for comparison.
3. Decrypt current values locally if the user has access.
4. Compute added, changed, and removed variables.
5. Show a TUI confirmation dialog.
6. Mask old and new values:
   - `p*****d -> x***z`
7. Ask user to confirm updates.
8. Check whether the user has write access to the scope.
9. Encrypt new values locally.
10. Update the scope's variable declarations in `propagate.yaml`.
11. Upload encrypted values and the updated metadata snapshot to the cloud.
12. Record update event.

#### Access Errors

If the user lacks write access, the CLI should:

- Show the scope.
- Show the current identity.
- Refuse the push before upload.
- Suggest requesting a role or scope change.

### 6.10 `propagate env set`

Sets or updates one environment variable value in the encrypted cloud store.

#### Usage

```bash
propagate env set API_TOKEN --scope dev
propagate env set DATABASE_URL --scope staging
```

The value must not be passed as a positional CLI argument. The CLI should prompt for the value using a secure no-echo prompt.

If `--scope` is omitted, the CLI should choose the only configured scope automatically. If more than one scope exists, it should prompt the user to select a scope before asking for the value. In non-interactive mode, `--scope` is required when multiple scopes exist.

#### Behavior

1. Read `propagate.yaml`.
2. Determine the target scope from `--scope`, the only configured scope, or an interactive scope selection prompt.
3. Ask for confirmation when setting a `prod` value.
4. Prompt securely for the new value without echoing it.
5. Fetch current encrypted cloud values and the user's scope envelope.
6. Decrypt the scope key locally.
7. Determine the target env file mapping.
8. Determine whether the variable is added or changed.
9. Check whether the user has write access to the scope.
10. Encrypt the new value locally.
11. Update the variable declaration in `propagate.yaml`.
12. Upload a single encrypted value update and updated metadata snapshot through the same cloud path as `propagate env push`.
13. Record update event.
14. Do not update local env files unless a future explicit flag requests it.

#### Output Requirements

The command output must show:

- Scope.
- Variable name.
- Whether the value was added or changed.
- Current identity.
- Operation ID, when available.

The command output must never show the plaintext value.

#### Access Errors

If the user lacks write access, the CLI should:

- Show the scope.
- Show the current identity.
- Refuse before upload.
- Suggest requesting a role or scope change.

### 6.11 `propagate env status`

Shows masked values currently stored in the cloud and compares local env files against the latest cloud `propagate.yaml` declarations.

#### Behavior

1. Read selected scope or default to `dev`.
2. Check read access.
3. Fetch the latest cloud config snapshot.
4. Fetch and decrypt current cloud values.
5. Hash local env file values with the same scope-keyed digest algorithm used by the cloud declarations.
6. Compare local values against the latest cloud declarations.
7. Display variable names, masked cloud values, local state, and last updated metadata.
8. Suggest `propagate config pull` if local YAML is stale.
9. Suggest `propagate env pull` if local values are missing or differ from the latest cloud declarations.

#### Example Output

```text
Scope: dev

DATABASE_URL=p***************2
API_TOKEN=s***********9
STRIPE_KEY=s***********x

Last updated: 2026-04-30 10:24 by alice@example.com
```

### 6.12 `propagate team status`

Shows team membership, pending requests, access changes, and pull activity.

#### Output Should Include

- Team name.
- Current user's role.
- Current user's public key SHA.
- Members by role.
- Pending join requests.
- Pending role changes.
- Pending scope access changes.
- Last pull by member and scope.
- Members who have never pulled.

## 7. TUI Requirements

The TUI should be keyboard-first and safe by default.

### 7.1 Env Import TUI

Used during `propagate init`.

Must show:

- Candidate env files grouped by scope.
- Whether each candidate file is inside a Git-tracked project folder.
- Variable name.
- Masked value.
- Detected source file.
- Selected scope.
- Include/exclude toggle.

Actions:

- Include or exclude env files.
- Confirm import.
- Change scope.
- Exclude variable.
- Add custom scope.
- Cancel setup.

### 7.2 Env Push TUI

Used during `propagate env push`.

Must show:

- Added variables.
- Changed variables.
- Removed variables.
- Masked old value.
- Masked new value.
- Target scope.

Actions:

- Approve all.
- Approve selected.
- Reject selected.
- Cancel push.

### 7.3 Config Push TUI

Used during `propagate config push`.

Must show:

- Pending joins.
- Public key SHA.
- Handle.
- Requested role.
- Requested scopes.
- Pending role changes.
- Pending scope changes.

Actions:

- Approve item.
- Decline item.
- Skip item for later.
- Approve selected.
- Decline selected.
- View public key details.
- Cancel push.

### 7.4 Config Edit TUI

Used during `propagate config edit`.

Must show:

- Variable name.
- Current scope.
- Current env file path.
- Current sensitivity.
- Whether moving the variable will add an env file mapping to the target scope.

Actions:

- Toggle sensitivity.
- Move variable declaration to another scope.
- Remove variable declaration from config metadata.
- Save edits.
- Dry-run edits without writing `propagate.yaml`.
- Cancel without saving.

The TUI must show declaration metadata only. It must not show, request, or infer env values.

## 8. First-Class AI Agent Support

AI coding agents are expected to work inside repositories, edit files, and call terminal tools. Propagate should make those agents safer by giving them explicit repository-local instructions and machine-friendly command behavior.

### 8.1 Agent Guidance During Init

`propagate init` should offer to add or update agent guidance after project setup or after detecting an existing `propagate.yaml`.

Supported guidance targets should include:

- Generic repository instructions, such as `AGENTS.md`, when present or selected by the user.
- Codex-style repo skills, when the repository uses a skill directory.
- Other known agent instruction files, such as Cursor rules, Claude instructions, or GitHub Copilot instructions, when detected.

The MVP should not require every ecosystem to be supported perfectly. It should start with a generic instruction file and a Propagate skill template, then allow future adapters.

The generated guidance should tell agents:

- Use Propagate commands instead of reading, copying, or inventing env values.
- Treat variables as sensitive by default, including public-looking values, unless a human explicitly marks them `non_sensitive`.
- Never write sensitive plaintext values or raw plaintext hashes to `propagate.yaml`, agent instructions, docs, prompts, test fixtures, or commits.
- Never write any env values into generated agent instructions, prompts, or tool logs.
- Prefer `propagate config status`, `propagate team status`, and `propagate env status` for discovery.
- Prefer `--json` for machine-readable status output.
- Prefer `--dry-run` before any command that writes local files or cloud state.
- Require human confirmation before running `propagate config edit`, `propagate env pull`, `propagate env push`, `propagate env set`, or `propagate config push`.
- Report permission errors and pending join requirements clearly instead of attempting workarounds.

Agent guidance is not an access-control system. Agents operate with the user's local filesystem and identity. Propagate must still enforce permissions in the CLI and cloud API.

### 8.2 Skill And Instruction File Behavior

When Propagate edits an agent instruction or skill file, it should:

- Use a clearly marked managed block.
- Preserve user-authored content outside the managed block.
- Be idempotent when run multiple times.
- Include the Propagate CLI version or template version used to generate the block.
- Avoid writing any env values, private key paths beyond `~/.propagate`, decrypted output, or cloud tokens.
- Show a diff preview before modifying existing files.
- Support a skip path for teams that do not want generated agent instructions.

If a repository has multiple agent systems configured, the TUI should let the user choose which targets to update.

### 8.3 Tool-Agent-Friendly CLI Behavior

Propagate commands should be easy for tool-using agents to call safely.

Agent-friendly behavior includes:

- Stable exit codes for success, validation failure, permission denied, cloud unavailable, conflict, and canceled operation.
- Stable `--json` output for status and dry-run commands.
- Non-interactive failure instead of hanging when stdin is not a TTY.
- Clear separation between human-readable summaries and machine-readable JSON.
- Consistent human-readable output with command titles, semantic status markers, styled sections, and `--no-color` support.
- No plaintext env values in stdout, stderr, logs, JSON, or panic output.
- Operation IDs included in JSON responses for traceability.
- Error messages that include safe next steps, such as "run `propagate team join`" or "ask an admin to approve access."

Agent-friendly behavior must not bypass human approval. Commands that write env files, upload encrypted env values, approve access, or publish config should require explicit confirmation unless a user intentionally passes a non-interactive approval flag. Local metadata-only commands such as `propagate team join` and `propagate scope create` may run non-interactively because their changes remain Git-reviewable until an admin publishes them.

### 8.4 Agent Audit Metadata

When the CLI can detect that it is being run by an AI tool agent, it should include safe agent metadata in cloud audit events.

Allowed metadata:

- CLI version.
- Client kind, such as human terminal, script, or AI agent.
- Agent adapter name, when known.
- Operation ID.
- Command name.

Disallowed metadata:

- Prompt text.
- Conversation content.
- Env values.
- Masked env values.
- Private key material.
- Absolute local paths outside the repository-relative env file mapping.

Agent metadata should help teams understand how changes were made without exposing user prompts or secrets.

## 9. Config File Shape

Example `propagate.yaml`:

```yaml
version: 1
team:
  id: team_abc123
  name: Acme API
  cloud_revision: rev_00012

scopes:
  dev:
    env_files:
      - .env
    variables:
      - name: DATABASE_URL
        env_file_path: .env
        sensitivity: sensitive
        digest: "hmac-sha-256:v1:3YV..."
      - name: PUBLIC_BASE_URL
        env_file_path: .env
        sensitivity: non_sensitive
        literal: "https://api.example.com"
    default_role_access:
      developers: read
      admins: write
  staging:
    env_files:
      - .env.staging
    default_role_access:
      developers: read
      admins: write
  qa:
    env_files: []
    default_role_access:
      developers: read
      admins: write
  prod:
    env_files:
      - .env.production
    default_role_access:
      admins: write

members:
  - handle: alice@example.com
    public_key_sha: sha256:abc123
    public_key: ssh-ed25519 AAAA...
    role: admins
  - handle: bob@example.com
    public_key_sha: sha256:def456
    public_key: ssh-ed25519 BBBB...
    role: developers

pending:
  joins:
    - handle: carol@example.com
      public_key_sha: sha256:ghi789
      public_key: ssh-ed25519 CCCC...
      requested_role: developers
      requested_scopes:
        dev: read
      created_at: "2026-04-30T10:00:00Z"
  access_changes: []
```

## 10. Cloud Data Model

### 10.1 Stored In Cloud

Cloud stores:

- Team metadata.
- Config revision.
- Member public keys.
- Encrypted env values.
- Encrypted access envelopes.
- Audit events.
- Last pull timestamps.

Cloud does not store:

- User private keys.
- Sensitive plaintext env values.
- Plaintext scope keys, if using end-to-end encryption.

`propagate.yaml` stores sensitive values only as scope-keyed digest declarations. Explicitly non-sensitive values may appear as direct short literals or truncated previews. The cloud stores the same metadata snapshot plus encrypted secret versions; it never stores plaintext sensitive values or plaintext scope keys.

### 10.2 Secret Storage Model

Recommended model:

1. Each scope has a symmetric scope key.
2. Each env value is encrypted with the scope key.
3. The scope key is encrypted for each authorized member public key.
4. When access is granted, an admin client uploads a new encrypted envelope for that member.
5. When access is revoked, future versions are no longer encrypted for that member.

### 10.3 Audit Events

Events to record:

- Team created.
- Config pushed.
- Config pulled.
- Join requested.
- Join approved.
- Join rejected.
- Scope access granted.
- Scope access revoked.
- Env pulled.
- Env pushed.

Pull events should include:

- Public key SHA.
- Handle.
- Scope.
- Env file mapping.
- Timestamp.
- CLI version.
- Config revision.

## 11. Permissions

MVP permission model:

```text
none
read
write
admin
```

Access is evaluated by:

1. Team membership.
2. Role.
3. Scope.
4. Explicit member override, if present.

Expected behavior:

- `read`: can pull and view env status.
- `write`: can read, push env changes, and set individual env values.
- `admin`: can manage config, approve joins, approve role changes, and write all scopes unless restricted later.

## 12. Monorepo Support

Propagate should support multiple env files per project and per scope.

Example:

```yaml
scopes:
  dev:
    env_files:
      - apps/api/.env
      - apps/web/.env.local
      - packages/worker/.env
```

During `propagate init`, the CLI should scan only directories that belong to the Git worktree and appear to be part of the project. The scanner should derive candidate directories from Git-tracked files and known project roots, then look for env files inside those directories. Ignored env files are allowed as candidates if their parent directory is part of the Git project.

The scanner must not recursively scan arbitrary untracked folders, dependency folders, build outputs, caches, or directories outside the Git worktree.

Common env file candidates:

- `.env`
- `.env.local`
- `.env.development`
- `.env.dev`
- `apps/*/.env`
- `apps/*/.env.local`
- `packages/*/.env`
- `services/*/.env`

The TUI should list discovered env files and let the user choose which files belong to each scope before importing variables.

Recommended defaults:

- Select root `.env` for `dev` if present.
- Select env files under tracked app/service/package directories only after confirmation.
- Exclude files under `node_modules`, `dist`, `build`, `coverage`, `.next`, `.turbo`, cache folders, fixtures, and examples.
- Warn when multiple selected files contain the same variable name in the same scope.

## 13. Security Requirements

- Never write sensitive env values to `propagate.yaml`.
- Use scope-keyed digest declarations with an algorithm prefix, for example `hmac-sha-256:v1:...`; never use raw plaintext hashes for sensitive values.
- Only write direct values to `propagate.yaml` when a variable is explicitly marked `non_sensitive` and the value fits on one short line. Long non-sensitive values must be truncated as a preview such as `aaa...zzz`.
- Never upload sensitive plaintext env values to cloud when end-to-end encryption mode is enabled.
- Never accept plaintext env values as positional CLI arguments; single-value updates must use secure no-echo prompting or an explicit non-echo input channel.
- Store private keys under `~/.propagate` with restrictive filesystem permissions.
- Warn if `.env` files are tracked by Git.
- Warn before writing `prod` env values to a local `.env` file.
- Mask env values in all TUI and command output.
- Avoid logging plaintext env values.
- Never write env values into generated agent instructions, skills, prompts, or tool logs.
- Generated agent guidance must not contain private keys, access tokens, decrypted output, or cloud service credentials.
- Include config revision in cloud writes to prevent accidental overwrite.
- Use HTTPS for all cloud communication.
- Use modern cryptography:
  - Prefer Ed25519 for identity signatures.
  - Prefer X25519 or age-style recipients for encryption.
  - Avoid raw RSA encryption for new designs unless compatibility requires it.

## 14. Git Workflow

`propagate.yaml` is intended to be committed.

Typical flow:

```bash
propagate init
git add propagate.yaml
git commit -m "Set up Propagate"
git push
```

New developer flow:

```bash
propagate team join --init --handle bob@example.com --scope dev=read
git add propagate.yaml
git commit -m "Request Propagate access"
git push
```

The separate `propagate init` then `propagate team join` flow remains available for users who want to initialize identity or agent guidance separately.

Admin approval flow:

```bash
git pull
propagate config push
git add propagate.yaml
git commit -m "Approve Propagate access"
git push
```

## 15. Success Metrics

MVP success metrics:

- Time from install to first encrypted env upload.
- Time for a new developer to request access.
- Time for admin to approve a join.
- Percentage of teams that successfully pull envs after setup.
- Number of `.env` files replaced or managed by Propagate.
- Number of secret pushes per team per week.
- Number of failed pulls due to permission issues.
- Number of users with stale pulls.
- Percentage of initialized repositories with Propagate agent guidance installed.
- Number of successful dry-run operations initiated from agent-friendly JSON flows.

## 16. Release Scope

### MVP

- Local keypair creation.
- Handle setup.
- `propagate init`.
- `propagate team join`.
- `propagate scope create`.
- `propagate config push`.
- `propagate config pull`.
- `propagate config status`.
- `propagate config edit`.
- `propagate env pull`.
- `propagate env push`.
- `propagate env set`.
- `propagate env status`.
- `propagate team status`.
- Basic TUI flows.
- Cloud encrypted secret storage.
- Git-backed `propagate.yaml`.
- Monorepo env file mapping.
- Agent guidance prompt during `propagate init`.
- Generated Propagate skill or managed instruction block for supported agent systems.
- Stable JSON output and exit codes for status and dry-run commands.

### Later

- `propagate run -- command`.
- CI identities.
- GitHub Actions OIDC.
- Runtime production agents.
- Secret rotation.
- Web dashboard.
- SSO.
- Hardware-backed keys.
- Policy-as-code.
- Automatic leak scanning on commit.
- Dedicated non-human AI agent identities with explicit scopes.
- Agent-specific approval policies and richer audit dashboards.

## 17. Suggestions, Design Issues, And Open Questions

### Suggestions

- Use `propagate.yaml` instead of `propagate.yalm`. The latter appears to be a typo and will create unnecessary friction.
- Prefer the term "scope" in the CLI but explain it as "environment" in user-facing setup prompts.
- Make `dev` the default scope, but require an extra confirmation before importing or writing `prod`.
- Treat `propagate env pull` as the compatibility path for MVP, but design the data model so `propagate run` can be added cleanly later.
- Add automatic `.gitignore` checks for managed env files.
- Treat every env value as confidential for storage purposes, even when a user describes it as public.
- Add a `--dry-run` option to `env push`, `env set`, `env pull`, `config push`, `config edit`, and `scope create`.
- Add a `--json` output mode for status commands so future CI and scripts can consume them.
- Add an agent guidance installer to `propagate init` and make it re-runnable without changing unrelated instruction content.
- Store enough audit metadata now to support future dashboard and CI workflows.

### Design Issues

- Git-mediated joins are transparent, but they may feel unusual because a user modifies project config before having access. The CLI must make this explicit by saying the user has created an access request, not joined the team.
- Admin approval requires the admin client to perform encryption for the new member. The server cannot complete approval alone in an end-to-end encrypted design. `propagate config push` must ask for an explicit decision on each pending item and update `propagate.yaml` to reflect approved, declined, and skipped changes.
- Revocation cannot erase secrets already pulled to a developer machine. The product should make this clear and eventually support rotation.
- Pulling into `.env` is familiar but less safe than runtime injection. The MVP should include warnings and guardrails.
- Monorepo env discovery can produce noisy results. The scanner must only inspect Git project directories, and the TUI must let users choose which discovered env files belong to each scope.
- Public key identity is simple and CLI-native, but account recovery is hard if the private key is lost.
- If handles are not verified emails, duplicate or misleading handles are possible.
- Some env vars may appear public, such as feature flags or local service URLs. The MVP must default them to sensitive; users must explicitly mark a variable `non_sensitive` before Propagate stores a direct literal or preview in Git-backed config.
- AI agents may be able to read local files and terminal output. Propagate can make the safe path obvious, but it cannot guarantee an agent will not misuse access outside Propagate. Sensitive operations still need CLI and server enforcement.

### Open Questions

- Should the canonical config file be `propagate.yaml`, `propagate.yml`, or should both be supported?
- Should `propagate init` require a Git repository, or allow standalone local projects?
- Should local identity use SSH keys directly, or a dedicated age/Ed25519 key format stored under `~/.propagate`?
- Should the first admin be the user who runs `propagate init`, or should the team require explicit admin confirmation?
- Should scope keys be one key per scope or one key per env file?
- Should developers be allowed to push `dev` secrets by default, or should write access be admin-only until granted?
- How should conflicts be handled when local `propagate.yaml` and cloud config both changed?
- Should `propagate env pull` overwrite existing local values, merge only missing values, or prompt every time?
- Should removed variables in cloud delete local `.env` entries during pull?
- Should `propagate env status` reveal masked values to all readers, or only variable names and metadata?
- How should custom `other` scopes be named and validated?
- Should pending joins live indefinitely, or expire after a fixed time?
- Should the product support verified email handles later, or stay purely key-based?
- What is the minimum cloud API needed for MVP, and can the cloud service stay stateless for some operations?
- Which agent instruction targets should be first-class in MVP: generic `AGENTS.md`, Codex skills, Cursor rules, Claude instructions, GitHub Copilot instructions, or a smaller initial set?
- Should agent guidance be enabled by default during `propagate init`, or only when a known agent configuration is detected?
- Should AI agents ever receive their own Propagate identities, or should MVP agents always operate through the human user's local identity?
