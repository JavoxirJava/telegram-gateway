FROM golang:1.27.1-alpine AS builder

WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal

RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/telegram-gateway ./cmd/api

FROM alpine:3.22

RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S app \
    && adduser -S -G app app

WORKDIR /app
COPY --from=builder /out/telegram-gateway /usr/local/bin/telegram-gateway

USER app
EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/telegram-gateway"]
