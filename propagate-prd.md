# Propagate Product Requirements Document

## 1. Overview

Propagate is a CLI-first tool for sharing environment variables across development teams. It lets a team initialize a project, encrypt environment variables locally, store encrypted values in the cloud, and manage access through a Git-backed team configuration file.

The initial product focuses on developer teams using `.env` files. Future releases may add runtime injection, CI support, production agents, and hosted dashboard workflows.

## 2. Product Goals

- Make team `.env` sharing safer than sending files through Slack, email, or docs.
- Keep the primary workflow inside the CLI.
- Use public/private key identity rather than password-based accounts for the MVP.
- Store env values encrypted in the cloud, with decryption controlled by local user keys.
- Use a project-level config file in Git to make team membership and access changes reviewable.
- Support common local development layouts, including monorepos and multiple env files.
- Give admins visibility into pending joins, scope changes, and last secret pulls.

## 3. Non-Goals For MVP

- CI/CD integration.
- `propagate run` or process-level secret injection.
- Production runtime agent.
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

Users may define custom scopes under `other`.

Each scope has:

- One or more env files.
- A set of encrypted variables.
- Access rules by role or member.
- Pull history.

### 4.4 Config File

The project config file is `propagate.yaml`.

It is committed to Git and contains non-secret project/team metadata, member public keys, pending requests, scopes, and env file mappings.

It must never contain environment variable values of any kind. This includes secrets, public values, placeholder values, defaults, masked values, and example values. The config may contain variable names and env file mappings, but values belong only in local env files and encrypted cloud records.

## 5. User Roles

### 5.1 Admin

Admins can:

- Initialize a project team.
- Approve pending joins.
- Approve role and scope changes.
- Push local config state to the cloud.
- Pull cloud config state.
- Push environment variable updates for scopes they can write.
- View team status and last pull events.

### 5.2 Developer

Developers can:

- Initialize their local Propagate identity.
- Request to join a project team.
- Pull environment variables for scopes they can read.
- Request access changes through config diffs.
- Push environment variable changes only for scopes they can write.

### 5.3 Future Roles

Potential future roles:

- Viewer: read-only access to selected scopes.
- Maintainer: can approve dev/staging changes but not prod.
- CI identity: non-human identity for CI workflows.
- Production agent: non-human identity for runtime secret retrieval.

## 6. MVP Commands

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
   - Save team config to `propagate.yaml` without writing any env values to the config.

#### Success Output

The command should clearly report:

- Whether a new local identity was created or an existing one was used.
- Where local identity is stored.
- Whether project config was created or already existed.
- Which scopes were created.
- How many variables were encrypted and uploaded.

#### Error Cases

- Cannot write to `~/.propagate`.
- Invalid or corrupted local keypair.
- No Git repository detected.
- Existing `propagate.yaml` is invalid.
- `.env` cannot be read.
- Cloud API is unavailable.
- User does not confirm env import.

### 6.2 `propagate team join`

Adds the current user as a pending invite/request in `propagate.yaml`.

#### Behavior

1. Ensure local identity exists. If not, run the identity portion of `propagate init`.
2. Read `propagate.yaml`.
3. Add a pending join request containing:
   - Public key SHA.
   - Full public key.
   - Handle.
   - Requested role: `developers`.
   - Requested scopes, if specified.
   - Timestamp.
4. Save the config file.
5. Notify the user explicitly that this only creates a Git-reviewed access request.
6. Tell the user to commit the config diff, open a pull request, and ask an admin to approve it.

#### Notes

The join request is Git-mediated. This lets teams review membership changes in pull requests.

The CLI output must make this workflow clear. It should not imply that the user has joined the team or received secret access yet.

Example output:

```text
Join request added to propagate.yaml.
You do not have secret access yet.

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
8. For declined items:
   - Do not grant cloud access.
   - Remove the item from the pending section of `propagate.yaml`.
   - Record the decline in local config history or cloud audit events.
9. For skipped items:
   - Leave the item pending in `propagate.yaml`.
   - Do not push any access change for that item.
10. Push accepted and declined config decisions to the cloud.
11. Update local `propagate.yaml` so it reflects the actual decisions made.
12. Update local config revision metadata.

#### Output Requirements

The command output must explicitly summarize all decisions:

- Approved joins.
- Declined joins.
- Approved role changes.
- Declined role changes.
- Approved scope changes.
- Declined scope changes.
- Skipped items that remain pending.
- Encrypted access envelopes uploaded.
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

### 6.6 `propagate env pull`

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

### 6.7 `propagate env push`

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
10. Upload encrypted values to the cloud.
11. Record update event.

#### Access Errors

If the user lacks write access, the CLI should:

- Show the scope.
- Show the current identity.
- Refuse the push before upload.
- Suggest requesting a role or scope change.

### 6.8 `propagate env status`

Shows masked values currently stored in the cloud.

#### Behavior

1. Read selected scope or default to `dev`.
2. Check read access.
3. Fetch and decrypt current cloud values.
4. Display variable names and masked values.
5. Show last updated metadata if available.

#### Example Output

```text
Scope: dev

DATABASE_URL=p***************2
API_TOKEN=s***********9
STRIPE_KEY=s***********x

Last updated: 2026-04-30 10:24 by alice@example.com
```

### 6.9 `propagate team status`

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

## 8. Config File Shape

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
    default_role_access:
      developers: read
      admins: write
  staging:
    env_files:
      - .env.staging
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

## 9. Cloud Data Model

### 9.1 Stored In Cloud

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
- Plaintext env values.
- Plaintext scope keys, if using end-to-end encryption.

`propagate.yaml` also does not store plaintext env values, even when a value is considered public or non-secret. Treating all env values as config-external prevents accidental leakage and keeps the Git-reviewed file metadata-only.

### 9.2 Secret Storage Model

Recommended model:

1. Each scope has a symmetric scope key.
2. Each env value is encrypted with the scope key.
3. The scope key is encrypted for each authorized member public key.
4. When access is granted, an admin client uploads a new encrypted envelope for that member.
5. When access is revoked, future versions are no longer encrypted for that member.

### 9.3 Audit Events

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

## 10. Permissions

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
- `write`: can read and push env changes.
- `admin`: can manage config, approve joins, approve role changes, and write all scopes unless restricted later.

## 11. Monorepo Support

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

## 12. Security Requirements

- Never write env values to `propagate.yaml`, including public, non-secret, placeholder, masked, or example values.
- Never upload plaintext env values to cloud when end-to-end encryption mode is enabled.
- Store private keys under `~/.propagate` with restrictive filesystem permissions.
- Warn if `.env` files are tracked by Git.
- Warn before writing `prod` env values to a local `.env` file.
- Mask env values in all TUI and command output.
- Avoid logging plaintext env values.
- Include config revision in cloud writes to prevent accidental overwrite.
- Use HTTPS for all cloud communication.
- Use modern cryptography:
  - Prefer Ed25519 for identity signatures.
  - Prefer X25519 or age-style recipients for encryption.
  - Avoid raw RSA encryption for new designs unless compatibility requires it.

## 13. Git Workflow

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
propagate init
propagate team join
git add propagate.yaml
git commit -m "Request Propagate access"
git push
```

Admin approval flow:

```bash
git pull
propagate config push
git add propagate.yaml
git commit -m "Approve Propagate access"
git push
```

## 14. Success Metrics

MVP success metrics:

- Time from install to first encrypted env upload.
- Time for a new developer to request access.
- Time for admin to approve a join.
- Percentage of teams that successfully pull envs after setup.
- Number of `.env` files replaced or managed by Propagate.
- Number of secret pushes per team per week.
- Number of failed pulls due to permission issues.
- Number of users with stale pulls.

## 15. Release Scope

### MVP

- Local keypair creation.
- Handle setup.
- `propagate init`.
- `propagate team join`.
- `propagate config push`.
- `propagate config pull`.
- `propagate config status`.
- `propagate env pull`.
- `propagate env push`.
- `propagate env status`.
- `propagate team status`.
- Basic TUI flows.
- Cloud encrypted secret storage.
- Git-backed `propagate.yaml`.
- Monorepo env file mapping.

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

## 16. Suggestions, Design Issues, And Open Questions

### Suggestions

- Use `propagate.yaml` instead of `propagate.yalm`. The latter appears to be a typo and will create unnecessary friction.
- Prefer the term "scope" in the CLI but explain it as "environment" in user-facing setup prompts.
- Make `dev` the default scope, but require an extra confirmation before importing or writing `prod`.
- Treat `propagate env pull` as the compatibility path for MVP, but design the data model so `propagate run` can be added cleanly later.
- Add automatic `.gitignore` checks for managed env files.
- Treat every env value as confidential for storage purposes, even when a user describes it as public.
- Add a `--dry-run` option to `env push`, `env pull`, and `config push`.
- Add a `--json` output mode for status commands so future CI and scripts can consume them.
- Store enough audit metadata now to support future dashboard and CI workflows.

### Design Issues

- Git-mediated joins are transparent, but they may feel unusual because a user modifies project config before having access. The CLI must make this explicit by saying the user has created an access request, not joined the team.
- Admin approval requires the admin client to perform encryption for the new member. The server cannot complete approval alone in an end-to-end encrypted design. `propagate config push` must ask for an explicit decision on each pending item and update `propagate.yaml` to reflect approved, declined, and skipped changes.
- Revocation cannot erase secrets already pulled to a developer machine. The product should make this clear and eventually support rotation.
- Pulling into `.env` is familiar but less safe than runtime injection. The MVP should include warnings and guardrails.
- Monorepo env discovery can produce noisy results. The scanner must only inspect Git project directories, and the TUI must let users choose which discovered env files belong to each scope.
- Public key identity is simple and CLI-native, but account recovery is hard if the private key is lost.
- If handles are not verified emails, duplicate or misleading handles are possible.
- Some env vars may appear public, such as feature flags or local service URLs, but storing them in `propagate.yaml` creates inconsistent rules and accidental leakage risk. The MVP should keep all env values out of Git-backed config.

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
