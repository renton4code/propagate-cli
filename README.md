# Propagate

Propagate is organized as a Go monorepo.

## Layout

- `cli/`: Propagate CLI implementation.
- `backend/`: Backend API module placeholder. The current implementation pass is CLI-only.
- `go.work`: Go workspace tying both modules together.

## Common Commands

```bash
go test ./cli/... ./backend/...
go run ./cli/cmd/propagate version
```

