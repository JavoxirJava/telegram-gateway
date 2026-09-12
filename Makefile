.PHONY: run test fmt vet infra-up infra-down infra-logs migrate-up migrate-down

run:
	go run ./cmd/api

test:
	go test ./...

fmt:
	gofmt -w ./cmd ./internal

vet:
	go vet ./...

infra-up:
	docker compose up -d

infra-down:
	docker compose down

infra-logs:
	docker compose logs -f --tail=200

# Uses host-accessible PostgreSQL configuration. Records migration checksums.
migrate-up:
	go run ./cmd/gateway-migrate

migrate-down:
	@for file in $$(find migrations -maxdepth 1 -name '*.down.sql' | sort -r); do \
		echo "Rolling back $$file"; \
		docker compose exec -T postgres sh -c 'psql -v ON_ERROR_STOP=1 -U "$$POSTGRES_USER" -d "$$POSTGRES_DB"' < "$$file" || exit 1; \
	done
