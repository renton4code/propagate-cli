# Propagate

**Share environment variables across your team without ever sending a `.env` file through Slack, email, or a ticket.**

Propagate is a CLI-first, end-to-end encrypted tool for managing team secrets. Values are encrypted locally before they leave your machine, the cloud only ever stores ciphertext, and who-can-access-what lives in a Git-backed `propagate.yaml` so every access change is a reviewable pull request.

- Website: [propagatecli.com](https://propagatecli.com)
- Security model: [docs/whitepaper.mdx](docs/whitepaper.mdx)
- License: free software under the [MIT License](LICENSE) — no paid tier, no commercial offering.

## Why Propagate

- **End-to-end encrypted.** Variables are encrypted locally with your keypair before upload. The server never sees plaintext values or plaintext scope keys (with one documented exception: PIN invite redemption).
- **CLI-first.** Everything happens in the terminal. No browser, no dashboard, no account to log into.
- **Git-backed config.** Team membership and per-scope access live in `propagate.yaml`. Access changes are diffable, reviewable, and auditable as ordinary PRs.
- **Key-based identity.** No passwords, no OAuth. Your identity is a keypair in `~/.propagate`. Works for both humans and AI coding agents.
- **Process-only injection.** `propagate run` injects secrets straight into a child process — nothing written to disk — with `.env` files available as a fallback when your tooling needs them.
- **Team access control.** Invite by public key or PIN, grant read/write per environment, and separate config management from secret access.

## How it works

1. Each scope (e.g. `dev`, `staging`, `prod`) gets a random symmetric **scope key**.
2. Env values are encrypted locally with the scope key (AES-256-GCM).
3. The scope key is wrapped (X25519 + AES-256-GCM) into one **envelope per authorized member**.
4. The cloud API stores only ciphertext, envelopes, metadata, config revisions, and audit events.
5. A member decrypts values locally only when their private key can open the relevant envelope.

`propagate.yaml` is the committed, reviewable record of accepted team and access state — scopes, env-file mappings, variable declarations (as keyed digests, never raw values), and member public keys. See the [whitepaper](docs/whitepaper.mdx) for the full trust model and limitations.

## Install

```bash
curl -fsSL https://propagatecli.com/install.sh | sh
```

This downloads the latest release, verifies its SHA-256 checksum, and installs the `propagate` binary to `~/.propagate/bin`. Add that directory to your `PATH` if the installer prompts you to.

Prefer to inspect first? Download release artifacts directly from [GitHub Releases](https://github.com/renton4code/propagate-cli/releases) and verify `propagate_checksums.txt`. Verify the install with:

```bash
propagate version
```

## Quickstart

### Set up a new project (team lead)

```bash
propagate quickstart
```

This creates (or loads) your local identity, scans the repo for `.env` files, encrypts the values you choose, writes `propagate.yaml`, and can create teammate PIN invites in the same run. Share each printed **PIN** through a trusted channel — it is shown only once.

### Join an existing project (developer)

```bash
propagate quickstart
```

In a repo that already has `propagate.yaml`, `quickstart` switches to the onboarding flow. Choose **Join by invite code** and enter the PIN for immediate access, or **Request to join** to submit a Git-reviewed join request a manager approves later.

### Run your app with secrets injected (recommended)

```bash
propagate run --scope dev -- npm run dev
```

Secrets are decrypted locally and passed only to the child process environment — no `.env` file is written.

### Or pull a `.env` file when tooling requires one

```bash
propagate env pull --scope dev
```

## Common commands

| Command | What it does |
| --- | --- |
| `propagate quickstart` | Set up a new project, or join an existing one, in one flow. |
| `propagate init` | Create local identity and initialize project metadata. |
| `propagate status` | Read-only view of config, team, and scoped env state. |
| `propagate run --scope <s> -- <cmd>` | Inject secrets into a child process (no files written). |
| `propagate env pull` / `env push` | Sync encrypted values to/from local `.env` files. |
| `propagate env set NAME --scope <s>` | Set one encrypted value (prompts without echo). |
| `propagate team join` / `team approve` | Request access, or approve a pending join. |
| `propagate config pull` / `config push` | Sync `propagate.yaml` with the cloud. |
| `propagate scope create <name>` | Add a new scope to local metadata. |

Use `--help` on any command, `--json` for machine-readable output, `--non-interactive` to fail instead of prompting, and `--dry-run` to preview write operations before confirming with `--yes`.

The CLI defaults to the free hosted API at `https://api.propagatecli.com/`. Override it with `--api-url VALUE` or `PROPAGATE_API_URL` to self-host.

Full references:

- [docs/cli-command-reference.mdx](docs/cli-command-reference.mdx) — every command, its writes, and non-interactive usage.
- [docs/cli-scenarios.mdx](docs/cli-scenarios.mdx) — task-oriented workflows for humans and agents.

## Repository layout

Propagate is a Go monorepo.

- `packages/backend/` — Backend API (Go, Cloud Run).
- `packages/cli/` — Propagate CLI (Go, Bubble Tea).
- `packages/landing/` — Marketing site (Astro, deployed to Netlify).
- `docs/` — Public docs: [whitepaper](docs/whitepaper.mdx), [command reference](docs/cli-command-reference.mdx), [scenarios](docs/cli-scenarios.mdx).
- `internal-docs/` — Internal design docs (PRD, technical design, API & CLI implementation guides).
- `go.work` — Go workspace tying backend and CLI together.

## Development

Build, test, and run the CLI from source:

```bash
go test ./packages/cli/... ./packages/backend/...
go run ./packages/cli/cmd/propagate version
go run ./packages/cli/cmd/propagate quickstart --help
```

### Local Docker stack

Run a local Supabase Postgres container, apply the Propagate schema, and start the backend API:

```bash
cp packages/backend/.env.example packages/backend/.env
docker compose up --build
```

For local development, set `PROPAGATE_API_URL` in `packages/backend/.env` to point the CLI at your local backend; the backend loads its runtime config from the same file. You do not need to export values manually.

```bash
curl http://localhost:8080/v1/version
go run ./packages/cli/cmd/propagate version
```

- Supabase Studio: `http://localhost:54323`
- Postgres: `localhost:55432`

```bash
psql 'postgres://postgres:postgres@localhost:55432/postgres?sslmode=disable'
```

Override host ports with `PROPAGATE_API_PORT`, `PROPAGATE_DB_PORT`, and `SUPABASE_STUDIO_PORT` (e.g. `PROPAGATE_DB_PORT=54322` for the usual Supabase local Postgres port). Reset the local database with:

```bash
docker compose down -v
```

## License

Propagate is free software released under the [MIT License](LICENSE). The project has no paid tier, subscription plan, or commercial offering in this repo.
