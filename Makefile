.PHONY: test test-integration test-e2e build compose-check

test:
	go test ./...
	npm --workspace apps/web test -- --run

test-integration:
	go test ./tests/integration -count=1

test-e2e:
	npm --workspace apps/web run test:e2e

build:
	mkdir -p bin
	go build -o bin/yunling-api ./cmd/api
	go build -o bin/yunling-scheduler ./cmd/scheduler
	go build -o bin/yunling-agent ./cmd/agent
	go build -o bin/yunling-bootstrap ./cmd/bootstrap
	npm --workspace apps/web run build

compose-check:
	docker compose --env-file deploy/.env.example -f deploy/docker-compose.yml config --quiet
