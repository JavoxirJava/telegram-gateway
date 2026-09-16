FROM docker.io/library/golang:1.27.1-bookworm AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY migrations ./migrations
RUN CGO_ENABLED=1 go build -trimpath -ldflags="-s -w" -o /out/telegram-gateway ./cmd/api \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/gateway-migrate ./cmd/migrate \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/gateway-repair-media ./cmd/repair-media \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/gateway-mcp ./cmd/mcp-stdio \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/audit-verify ./cmd/audit-verify \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/gateway-health ./cmd/health
FROM localhost/telegram-gateway-tdlib:d1085f9
RUN groupadd --gid 10001 app && useradd --uid 10001 --gid 10001 --no-create-home app \
 && mkdir -p /data/tdlib && chown -R app:app /data
COPY --from=builder /out/ /usr/local/bin/
WORKDIR /app
USER 10001:10001
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/telegram-gateway"]
