ARG VERSION=dev

FROM golang:1.26.6-bookworm AS build
ARG VERSION
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/runnerhubd ./cmd/runnerhubd && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/runnerhub-agent ./cmd/runnerhub-agent && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/runnerhub-deployer ./cmd/runnerhub-deployer

FROM golang:1.26.6-bookworm AS scaleset-build
WORKDIR /src
COPY scaleset/go.mod scaleset/go.sum ./
RUN go mod download
COPY scaleset/ .
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/runnerhub-scaleset ./cmd/runnerhub-scaleset

# Keep the non-root service identity stable across image/base-package updates so
# persistent bind mounts do not become unreadable when Debian's automatic
# system-user allocation changes. 10001 is reserved for RunnerHub containers.
FROM debian:bookworm-slim AS control
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl openssh-client sshpass util-linux && rm -rf /var/lib/apt/lists/* && \
    groupadd --gid 10001 runnerhub && useradd --uid 10001 --gid runnerhub --home-dir /nonexistent --shell /usr/sbin/nologin --no-create-home runnerhub && \
    mkdir -p /data && chown runnerhub:runnerhub /data
COPY --from=build /out/runnerhubd /usr/local/bin/runnerhubd
COPY deploy/docker/secret-file.sh /usr/local/lib/runnerhub/secret-file.sh
COPY deploy/docker/control-entrypoint.sh /usr/local/bin/runnerhub-control-entrypoint
RUN chmod 0755 /usr/local/bin/runnerhub-control-entrypoint
VOLUME ["/data"]
ENTRYPOINT ["/usr/local/bin/runnerhub-control-entrypoint"]

# Keep the deployment authority outside the main RunnerHub app so RunnerHub can
# safely redeploy itself without terminating the coordinator performing the update.
FROM debian:bookworm-slim AS deployer
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl && rm -rf /var/lib/apt/lists/* && \
    groupadd --gid 10001 runnerhub && useradd --uid 10001 --gid runnerhub --home-dir /nonexistent --shell /usr/sbin/nologin --no-create-home runnerhub && \
    mkdir -p /data && chown runnerhub:runnerhub /data
COPY --from=build /out/runnerhub-deployer /usr/local/bin/runnerhub-deployer
USER runnerhub
VOLUME ["/data"]
ENTRYPOINT ["/usr/local/bin/runnerhub-deployer"]

# The agent talks to the host Docker daemon through /var/run/docker.sock.
# Use Docker's maintained CLI image so the client API keeps pace with current
# Docker Engine releases instead of Debian Bookworm's older docker.io client.
FROM docker:29.8.1-cli@sha256:018edbc908e08fcc9dbf029c812c34251e9b4719e6f71ca0e5eae2a987d014ca AS agent
RUN apk add --no-cache ca-certificates
COPY --from=build /out/runnerhub-agent /usr/local/bin/runnerhub-agent
COPY deploy/docker/secret-file.sh /usr/local/lib/runnerhub/secret-file.sh
COPY deploy/docker/agent-entrypoint.sh /usr/local/bin/runnerhub-agent-entrypoint
RUN chmod 0755 /usr/local/bin/runnerhub-agent-entrypoint
ENTRYPOINT ["/usr/local/bin/runnerhub-agent-entrypoint"]

FROM debian:bookworm-slim AS bootstrap
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl jq openssl && rm -rf /var/lib/apt/lists/*
COPY deploy/docker/secret-file.sh /usr/local/lib/runnerhub/secret-file.sh
COPY deploy/docker/bootstrap-entrypoint.sh /usr/local/bin/runnerhub-bootstrap
COPY deploy/docker/init-secrets.sh /usr/local/bin/runnerhub-init-secrets
RUN chmod 0755 /usr/local/bin/runnerhub-bootstrap /usr/local/bin/runnerhub-init-secrets
ENTRYPOINT ["/usr/local/bin/runnerhub-bootstrap"]

FROM debian:bookworm-slim AS scaleset
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates util-linux && rm -rf /var/lib/apt/lists/* && \
    groupadd --gid 10001 runnerhub && useradd --uid 10001 --gid runnerhub --home-dir /nonexistent --shell /usr/sbin/nologin --no-create-home runnerhub
COPY --from=scaleset-build /out/runnerhub-scaleset /usr/local/bin/runnerhub-scaleset
COPY deploy/docker/secret-file.sh /usr/local/lib/runnerhub/secret-file.sh
COPY deploy/docker/scaleset-entrypoint.sh /usr/local/bin/runnerhub-scaleset-entrypoint
RUN chmod 0755 /usr/local/bin/runnerhub-scaleset-entrypoint
ENTRYPOINT ["/usr/local/bin/runnerhub-scaleset-entrypoint"]
