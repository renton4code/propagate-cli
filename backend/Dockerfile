FROM golang:1.22 AS build

WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal

RUN CGO_ENABLED=0 GOOS=linux go build -o /out/propagate-api ./cmd/api

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/propagate-api /propagate-api

USER nonroot:nonroot
ENTRYPOINT ["/propagate-api"]
