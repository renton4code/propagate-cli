#!/usr/bin/env sh
set -eu

PROPAGATE_API_URL=http://localhost:8080 go run ./packages/cli/cmd/propagate "$@"
