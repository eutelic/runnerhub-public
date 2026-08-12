FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/runnerhubd ./cmd/runnerhubd && \
    CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/runnerhub-agent ./cmd/runnerhub-agent

FROM golang:1.26-bookworm AS scaleset-build
WORKDIR /src
COPY scaleset/go.mod scaleset/go.sum ./
RUN go mod download
COPY scaleset/ .
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/runnerhub-scaleset ./cmd/runnerhub-scaleset

FROM debian:bookworm-slim AS control
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl util-linux && rm -rf /var/lib/apt/lists/* && \
    groupadd --system runnerhub && useradd --system --gid runnerhub --home-dir /nonexistent --shell /usr/sbin/nologin runnerhub && \
    mkdir -p /data && chown runnerhub:runnerhub /data
COPY --from=build /out/runnerhubd /usr/local/bin/runnerhubd
COPY deploy/docker/secret-file.sh /usr/local/lib/runnerhub/secret-file.sh
COPY deploy/docker/control-entrypoint.sh /usr/local/bin/runnerhub-control-entrypoint
RUN chmod 0755 /usr/local/bin/runnerhub-control-entrypoint
VOLUME ["/data"]
ENTRYPOINT ["/usr/local/bin/runnerhub-control-entrypoint"]

FROM debian:bookworm-slim AS agent
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates docker.io && rm -rf /var/lib/apt/lists/*
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
    groupadd --system runnerhub && useradd --system --gid runnerhub --home-dir /nonexistent --shell /usr/sbin/nologin runnerhub
COPY --from=scaleset-build /out/runnerhub-scaleset /usr/local/bin/runnerhub-scaleset
COPY deploy/docker/secret-file.sh /usr/local/lib/runnerhub/secret-file.sh
COPY deploy/docker/scaleset-entrypoint.sh /usr/local/bin/runnerhub-scaleset-entrypoint
RUN chmod 0755 /usr/local/bin/runnerhub-scaleset-entrypoint
ENTRYPOINT ["/usr/local/bin/runnerhub-scaleset-entrypoint"]
