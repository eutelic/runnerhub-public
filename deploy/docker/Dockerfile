FROM golang:1.23-bookworm AS build
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/runnerhubd ./cmd/runnerhubd && \
    CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/runnerhub-agent ./cmd/runnerhub-agent

FROM gcr.io/distroless/static-debian12:nonroot AS control
COPY --from=build /out/runnerhubd /runnerhubd
VOLUME ["/data"]
ENTRYPOINT ["/runnerhubd"]

FROM debian:bookworm-slim AS agent
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates docker.io && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/runnerhub-agent /usr/local/bin/runnerhub-agent
ENTRYPOINT ["/usr/local/bin/runnerhub-agent"]
