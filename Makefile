.PHONY: run test fmt vet build deploy infra-up infra-down infra-logs migrate-up
GO ?= go
run:
	systemctl --user start tgw-api.service tgw-tunnel.service
test:
	$(GO) test -race -count=1 ./...
fmt:
	$(GO) fmt ./...
vet:
	$(GO) vet ./...
build:
	podman build --layers -f Dockerfile.tdlib -t localhost/telegram-gateway-tdlib:d1085f9 .
	podman build --layers -t localhost/telegram-gateway:local .
deploy:
	python3 deploy/install.py
	python3 deploy/tunnel.py
infra-up:
	python3 deploy/install.py
infra-down:
	systemctl --user stop tgw-tunnel tgw-api tgw-minio tgw-nats tgw-redis tgw-postgres
infra-logs:
	journalctl --user -u tgw-api -u tgw-postgres -u tgw-redis -u tgw-nats -u tgw-minio -f
migrate-up:
	podman run --rm --network tgw --env-file deploy/runtime/migrate.env --entrypoint /usr/local/bin/gateway-migrate localhost/telegram-gateway:local
