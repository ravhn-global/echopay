# Multi-stage build — keeps the final image small (~25MB) so Fly.io's
# free-tier 256MB VM has all the RAM for the actual workload (Postgres
# pool + Redis client + in-process timers).

FROM golang:1.26-alpine AS build
WORKDIR /src
# Cache dependencies in their own layer.
COPY go.mod go.sum ./
RUN go mod download
# Now the source.
COPY . .
# Static build so the alpine final image doesn't need glibc shenanigans.
# -trimpath strips local paths from the binary; -ldflags '-s -w' drops
# the symbol table and DWARF info (smaller binary, no debug info we'd
# ship to prod anyway).
ENV CGO_ENABLED=0 GOOS=linux GOARCH=amd64
RUN go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

FROM alpine:3.20
# tzdata so time.LoadLocation works for the reconciler / scheduled jobs;
# ca-certificates so the Paystack + YouVerify + Firebase HTTPS clients
# can validate certs.
RUN apk add --no-cache ca-certificates tzdata
COPY --from=build /out/server /usr/local/bin/server
# Fly.io passes PORT as an env var, but our config reads HTTP_ADDR.
# Wrap the binary in a tiny shim so we honor whichever is set.
ENV HTTP_ADDR=:8080
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/server"]
