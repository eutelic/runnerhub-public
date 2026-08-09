FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/runnerhubd ./cmd/runnerhubd && \
    CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/runnerhub-agent ./cmd/runnerhub-agent

FROM debian:bookworm-slim AS control
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && rm -rf /var/lib/apt/lists/* && \
    groupadd --system runnerhub && useradd --system --gid runnerhub --home-dir /nonexistent --shell /usr/sbin/nologin runnerhub && \
    mkdir -p /data && chown runnerhub:runnerhub /data
COPY --from=build /out/runnerhubd /usr/local/bin/runnerhubd
USER runnerhub:runnerhub
VOLUME ["/data"]
ENTRYPOINT ["/usr/local/bin/runnerhubd"]

FROM debian:bookworm-slim AS agent
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates docker.io && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/runnerhub-agent /usr/local/bin/runnerhub-agent
ENTRYPOINT ["/usr/local/bin/runnerhub-agent"]
