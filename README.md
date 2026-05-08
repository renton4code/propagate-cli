# Propagate

Propagate is organized as a Go monorepo.

## Layout

- `packages/backend/`: Backend API (Go, Cloud Run).
- `packages/cli/`: Propagate CLI (Go, Bubble Tea).
- `packages/landing/`: Marketing site (Astro, deployed to Netlify).
- `go.work`: Go workspace tying backend and CLI together.

## Common Commands

```bash
go test ./packages/cli/... ./packages/backend/...
go run ./packages/cli/cmd/propagate version
```

