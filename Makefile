.PHONY: test build

test:
	go test ./...
	npm --workspace apps/web test -- --run

build:
	go build -o bin/yunling-api ./cmd/api
	npm --workspace apps/web run build
