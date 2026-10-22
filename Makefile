.PHONY: help build test test-integration fmt vet check up up-build down logs ps db-shell rabbit-shell restart-api restart-worker restart-publisher diagrams clean

APP_PORT ?= 8000
BASE_URL ?= http://localhost:$(APP_PORT)

help: ## Tampilkan daftar perintah
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-20s\033[0m %s\n", $$1, $$2}'

build: ## Build ketiga binary
	GOPROXY=off go build ./...

test: ## Jalankan unit test
	GOPROXY=off go test ./...

test-integration: ## Jalankan integration test (butuh stack docker yang hidup)
	GOPROXY=off go test -tags integration ./internal/integration/... -v

fmt: ## Format kode
	gofmt -w .

vet: ## Jalankan go vet
	GOPROXY=off go vet ./...
	GOPROXY=off go vet -tags integration ./internal/integration/...

check: fmt vet build test ## Format + vet + build + unit test

up: ## Nyalakan seluruh stack
	docker compose up -d --build

down: ## Matikan seluruh stack (data tetap tersimpan)
	docker compose down

logs: ## Lihat log seluruh service
	docker compose logs -f

ps: ## Status seluruh container
	docker compose ps

db-shell: ## Buka shell psql
	docker exec -it transjakarta_postgres psql -U postgres -d transjakarta

rabbit-shell: ## Buka RabbitMQ management API (localhost:15672)
	@echo "http://localhost:15672  (guest/guest)"

restart-api: ## Restart service API
	docker compose restart api

restart-worker: ## Restart service worker
	docker compose restart worker

restart-publisher: ## Restart service publisher
	docker compose restart publisher

diagrams: ## Render ulang diagram Mermaid -> PNG + tulis docs/FLOWCHART.md
	./docs/diagrams/render.sh

clean: ## Hapus container DAN volume (data hilang)
	docker compose down -v
