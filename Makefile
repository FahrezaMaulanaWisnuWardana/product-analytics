.PHONY: run demo test integration-test build db db-stop migrate
run:
	./scripts/run.sh

demo:
	./scripts/run.sh --demo

db:
	./scripts/dev-db.sh start

db-stop:
	./scripts/dev-db.sh stop

migrate:
	./scripts/run.sh --migrate-only

test:
	go test ./...
	go vet ./...
	node --test web/sdk.test.cjs

integration-test:
	@test -n "$$TEST_DATABASE_URL" || (echo 'Set TEST_DATABASE_URL to a disposable PostgreSQL database'; exit 1)
	go test -race ./...

build:
	mkdir -p bin
	go build -trimpath -o bin/pulse ./cmd/pulse
