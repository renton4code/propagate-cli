FROM golang:1.22 AS build

WORKDIR /src
COPY packages/backend/go.mod packages/backend/go.sum ./packages/backend/
COPY packages/shared/go.mod ./packages/shared/
WORKDIR /src/packages/backend
RUN GOWORK=off go mod download

WORKDIR /src
COPY packages/backend/cmd ./packages/backend/cmd
COPY packages/backend/internal ./packages/backend/internal
COPY packages/shared ./packages/shared

WORKDIR /src/packages/backend
RUN GOWORK=off CGO_ENABLED=0 GOOS=linux go build -o /out/propagate-api ./cmd/api

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/propagate-api /propagate-api

USER nonroot:nonroot
ENTRYPOINT ["/propagate-api"]
