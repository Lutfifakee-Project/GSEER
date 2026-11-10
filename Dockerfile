# GSEER — internet-asset search engine.
# Multi-stage build: compile with the Go toolchain, ship on a minimal
# alpine runtime.  Non-root, no build tools in the final image, no
# collector/egress capability beyond what the operator explicitly runs
# (`gseer collect run` is manual by design).
#
# Build:
#   docker build --build-arg VERSION=v0.1.0 --build-arg COMMIT=$(git rev-parse HEAD) -t gseer:local .
#
# The updater (`gseer update`) is for bare-metal installs; container
# deployments upgrade by building/pulling a new image (the container's
# binary is root-owned and read-only for the runtime user by design).

# --- build stage -----------------------------------------------------------
FROM golang:1.26-alpine AS build
WORKDIR /src

# Cache the module graph first.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG VERSION=dev
ARG COMMIT=unknown
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags "-X github.com/Lutfifakee-Project/GSEER/internal/version.Version=${VERSION} -X github.com/Lutfifakee-Project/GSEER/internal/version.Commit=${COMMIT}" \
      -o /out/gseer ./cmd/gseer

# --- runtime stage ---------------------------------------------------------
FROM alpine:3.20

# ca-certificates for HTTPS (passive connectors + update manifest fetch);
# busybox wget (already in alpine) serves the container healthcheck.
RUN apk add --no-cache ca-certificates \
 && adduser -D -H -u 10001 gseer

COPY --from=build /out/gseer /usr/local/bin/gseer

# Non-root runtime user; the data lives in PostgreSQL (external volume),
# so the container itself needs no writable state.
USER gseer

# The API + dashboard listen address (GSEER_LISTEN overrides).
EXPOSE 8443

# Foreground server (graceful SIGTERM shutdown; migrations run on start).
ENTRYPOINT ["gseer"]
CMD ["server", "start"]
