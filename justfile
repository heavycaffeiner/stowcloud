set shell := ["bash", "-euo", "pipefail", "-c"]

default:
    @just --list

dev:
    bash scripts/dev.sh

build:
    cd frontend && pnpm build
    cd backend && CGO_ENABLED=0 go build -tags "embed_ui compat_nc" -o sc-engine ./cmd/sc-engine

test:
    cd frontend && pnpm test && pnpm check
    cd backend && CGO_ENABLED=0 go test -count=1 ./...
    cd backend && CGO_ENABLED=0 go test -tags compat_nc -count=1 ./internal/nextcloud/... ./test/...

e2e:
    bash scripts/e2e.sh

lint:
    cd frontend && pnpm format:check && pnpm lint && pnpm lint:i18n
    cd backend && CGO_ENABLED=0 go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.13.1 run ./...

clean:
    python3 scripts/clean.py
