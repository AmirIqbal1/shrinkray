FROM golang:1.22-bookworm AS build

WORKDIR /src

COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal

RUN go test ./...

ENV CGO_ENABLED=0
RUN go build -trimpath \
    -ldflags="-s -w" \
    -o /out/shrinkray-server \
    ./cmd/shrinkray-server

FROM debian:bookworm-slim AS runtime

RUN apt-get update \
    && apt-get install --yes --no-install-recommends \
        bash \
        ca-certificates \
        curl \
        ffmpeg \
    && rm -rf /var/lib/apt/lists/*

COPY --from=build /out/shrinkray-server /usr/local/bin/shrinkray-server
COPY shrinkray /usr/local/bin/shrinkray

RUN chmod 0755 /usr/local/bin/shrinkray-server /usr/local/bin/shrinkray \
    && mkdir -p /var/lib/shrinkray

EXPOSE 8787

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD ["curl", "--fail", "--silent", "--show-error", "http://127.0.0.1:8787/api/health"]

ENTRYPOINT ["/usr/local/bin/shrinkray-server"]
