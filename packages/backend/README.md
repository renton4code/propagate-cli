# Propagate Backend

Backend API module for the Propagate monorepo.

## Implemented

- `GET /v1/version`
- `POST /v1/teams/setup`
- `GET /v1/teams/{team_id}/config/status`
- `GET /v1/teams/{team_id}/config`
- `POST /v1/teams/{team_id}/config/push`
- `GET /v1/teams/{team_id}/scopes/{scope}/key-envelope`
- `GET /v1/teams/{team_id}/scopes/{scope}/pull-bundle`
- `POST /v1/teams/{team_id}/scopes/{scope}/env/push`
- `GET /v1/teams/{team_id}/scopes/{scope}/env/status`
- `POST /v1/teams/{team_id}/events/pull`
- `GET /v1/teams/{team_id}/status`
- Schema migration in `migrations/0001_init_schema.sql`

`POST /v1/teams/setup` expects an Ed25519 signed request from the first admin identity. It validates that the submitted config snapshot is metadata-only, reserves a replay nonce, and records encrypted setup metadata through the storage layer.
Protected team endpoints verify the active member's Ed25519 signature, reserve a replay nonce, and enforce role/scope permissions through the storage layer.

## Request Signing Headers

- `X-Propagate-Public-Key-SHA`
- `X-Propagate-Timestamp`
- `X-Propagate-Nonce`
- `X-Propagate-CLI-Version`
- `X-Propagate-Operation-ID`
- `X-Propagate-Signature`

By default the server uses an in-memory store so endpoint contracts and validation can be tested without a local Supabase/Postgres instance. Set `PROPAGATE_DATABASE_URL` to use the Postgres-backed store.

## Run

```bash
go run ./backend/cmd/api
```

The API listens on `PORT`, defaulting to `8080`.
