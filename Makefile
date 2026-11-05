.PHONY: build test race vet fmt-check lint audit generate generate-check tidy mod-verify check

build:
	go build -trimpath -o bin/acpboss ./cmd/acpboss
test:
	go test ./...
race:
	go test -race ./...
vet:
	go vet ./...
fmt-check:
	@test -z "$$(gofmt -l cmd internal)"
lint:
	./scripts/lint.sh
audit:
	./scripts/audit.sh
generate:
	go generate ./internal/openai/api
generate-check:
	./scripts/generate-check.sh
tidy:
	go mod tidy
	go -C tools mod tidy
mod-verify:
	go mod verify
	go -C tools mod verify
check: mod-verify generate-check fmt-check test race vet lint audit build
