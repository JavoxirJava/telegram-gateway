FROM docker.io/library/golang:1.27.1-bookworm AS builder
ARG VCS_REF=unknown
ARG BUILD_DATE=unknown
WORKDIR /src
COPY go.mod go.sum VERSION ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY migrations ./migrations
RUN CGO_ENABLED=1 go build -trimpath -ldflags="-s -w -X github.com/JavoxirJava/telegram-gateway/internal/buildinfo.Revision=${VCS_REF} -X github.com/JavoxirJava/telegram-gateway/internal/buildinfo.BuiltAt=${BUILD_DATE}" -o /out/telegram-gateway ./cmd/api \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X github.com/JavoxirJava/telegram-gateway/internal/buildinfo.Revision=${VCS_REF} -X github.com/JavoxirJava/telegram-gateway/internal/buildinfo.BuiltAt=${BUILD_DATE}" -o /out/gateway-migrate ./cmd/migrate \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X github.com/JavoxirJava/telegram-gateway/internal/buildinfo.Revision=${VCS_REF} -X github.com/JavoxirJava/telegram-gateway/internal/buildinfo.BuiltAt=${BUILD_DATE}" -o /out/gateway-repair-media ./cmd/repair-media \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X github.com/JavoxirJava/telegram-gateway/internal/buildinfo.Revision=${VCS_REF} -X github.com/JavoxirJava/telegram-gateway/internal/buildinfo.BuiltAt=${BUILD_DATE}" -o /out/gateway-mcp ./cmd/mcp-stdio \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X github.com/JavoxirJava/telegram-gateway/internal/buildinfo.Revision=${VCS_REF} -X github.com/JavoxirJava/telegram-gateway/internal/buildinfo.BuiltAt=${BUILD_DATE}" -o /out/audit-verify ./cmd/audit-verify \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X github.com/JavoxirJava/telegram-gateway/internal/buildinfo.Revision=${VCS_REF} -X github.com/JavoxirJava/telegram-gateway/internal/buildinfo.BuiltAt=${BUILD_DATE}" -o /out/gateway-health ./cmd/health
FROM localhost/telegram-gateway-tdlib:d1085f9
ARG VCS_REF=unknown
ARG BUILD_DATE=unknown
LABEL org.opencontainers.image.version="2.0.0" \
      org.opencontainers.image.revision="${VCS_REF}" \
      org.opencontainers.image.created="${BUILD_DATE}" \
      org.opencontainers.image.source="https://github.com/JavoxirJava/telegram-gateway"
RUN apt-get update && apt-get install -y --no-install-recommends ffmpeg && rm -rf /var/lib/apt/lists/*
RUN groupadd --gid 10001 app && useradd --uid 10001 --gid 10001 --no-create-home app \
 && mkdir -p /data/tdlib && chown -R app:app /data
COPY --from=builder /out/ /usr/local/bin/
WORKDIR /app
USER 10001:10001
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/telegram-gateway"]
