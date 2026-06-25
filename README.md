# Propagate

Propagate is organized as a Go monorepo.

## Product documentation

- [propagate-prd.md](internal-docs/propagate-prd.md) — requirements, including **PIN-backed team invites**.
- [propagate-technical-design.md](internal-docs/propagate-technical-design.md) — architecture and data model.
- [propagate-api-implementation-guide.md](internal-docs/propagate-api-implementation-guide.md) — HTTP contracts.
- [propagate-cli-implementation-guide.md](internal-docs/propagate-cli-implementation-guide.md) — CLI command behaviors.
- [docs/whitepaper.mdx](docs/whitepaper.mdx) — public security model and trust boundaries.

## Layout

- `packages/backend/`: Backend API (Go, Cloud Run).
- `packages/cli/`: Propagate CLI (Go, Bubble Tea).
- `packages/landing/`: Marketing site (Astro, deployed to Netlify).
- `go.work`: Go workspace tying backend and CLI together.

## Common Commands

```bash
go test ./packages/cli/... ./packages/backend/...
go run ./packages/cli/cmd/propagate version
go run ./packages/cli/cmd/propagate quickstart --help
```

## Local Docker Stack

Run a local Supabase Postgres container, apply the Propagate schema, and start
the backend API:

```bash
cp packages/backend/.env.example packages/backend/.env
docker compose up --build
```

The CLI defaults to the free hosted API at `https://api.propagatecli.com/`.
For local development, `PROPAGATE_API_URL` in `packages/backend/.env`
overrides that default before commands contact the API, and the backend loads
its runtime config from the same file when run on the host. You do not need to
export values manually during local development.

```bash
curl http://localhost:8080/v1/version
go run ./packages/cli/cmd/propagate version
```

Supabase Studio is available at `http://localhost:54323`.

Postgres is exposed on `localhost:55432`:

```bash
psql 'postgres://postgres:postgres@localhost:55432/postgres?sslmode=disable'
```

Override host ports with `PROPAGATE_API_PORT`, `PROPAGATE_DB_PORT`, and
`SUPABASE_STUDIO_PORT`.
For example, use `PROPAGATE_DB_PORT=54322` if you want the usual Supabase
local Postgres port.

Reset the local database with:

```bash
docker compose down -v
```

## License

Propagate is free software released under the [MIT License](LICENSE). The
project has no paid tier, subscription plan, or commercial offering in this
repo.
