.PHONY: run demo test integration-test build db db-stop migrate backup restore test-ci
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
	node --test web/sdk.test.cjs web/app.test.cjs

integration-test:
	@test -n "$$TEST_DATABASE_URL" || (echo 'Set TEST_DATABASE_URL to a disposable PostgreSQL database'; exit 1)
	go test -race ./...

build:
	mkdir -p bin
	go build -trimpath -o bin/pulse ./cmd/pulse

backup:
	./scripts/backup.sh

restore:
	@test -n "$(BACKUP_FILE)" || (echo 'Usage: make restore BACKUP_FILE=backups/<file> CONFIRM=RESTORE'; exit 1)
	./scripts/restore.sh "$(BACKUP_FILE)" "$(CONFIRM)"

# Mirrors CI locally; pass TEST_DATABASE_URL to include the PostgreSQL integration tests.
test-ci:
	@if [ -n "$(TEST_DATABASE_URL)" ]; then export TEST_DATABASE_URL="$(TEST_DATABASE_URL)"; fi; \
	go test ./... && \
	go vet ./... && \
	node --test web/sdk.test.cjs web/app.test.cjs && \
	go test -race ./...
