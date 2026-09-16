APP_NAME=meetuper
CMD=cmd/app/main.go
DC_FILE=docker-compose.dev.yml

.PHONY: help
help:
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-20s\033[0m %s\n", $$1, $$2}'

# Go

.PHONY: run
run: ## Запустить Go приложение локально (без Докера)
	go run $(CMD)

.PHONY: build
build: ## Сбилдить бинарник
	go build -o bin/$(APP_NAME) $(CMD)

.PHONY: deps
deps: ## Обновить зависимости
	go mod tidy

.PHONY: test
test: ## Запустить Unit-тесты (с race-детектором — код плотно конкурентный)
	go test -race ./...

.PHONY: lint
lint: ## Запустить линтер (требуется golangci-lint)
	golangci-lint run

.PHONY: ci
ci: ## Гейт: что должно пройти, прежде чем считать работу законченной
	@test -z "$$(gofmt -l .)" || { echo "gofmt needed:"; gofmt -l .; exit 1; }
	go vet ./...
	@if command -v golangci-lint >/dev/null 2>&1; then golangci-lint run; \
	 else echo "(golangci-lint не установлен — шаг пропущен)"; fi
	go test -race ./...

.PHONY: swag
swag: ## Сгенерировать Swagger документацию
	go run github.com/swaggo/swag/cmd/swag@latest init -g $(CMD) -d ./

.PHONY: migration
migration: ## Создать новую миграцию: make migration NAME=create_meetups_table
	go run github.com/pressly/goose/v3/cmd/goose@latest -dir migrations create $(NAME) sql

# Docker

.PHONY: up
up: ## Поднять локальное окружение (БД, Redis, App) в фоне
	docker-compose -f $(DC_FILE) up -d --build

.PHONY: down
down: ## Остановить контейнеры
	docker-compose -f $(DC_FILE) down

.PHONY: logs
logs: ## Смотреть логи всех контейнеров
	docker-compose -f $(DC_FILE) logs -f

.PHONY: logs-app
logs-app: ## Смотреть логи только приложения
	docker-compose -f $(DC_FILE) logs -f meetuper_backend

.PHONY: restart
restart: down up ## Перезапустить всё окружение

.PHONY: clean
clean: ## Полная очистка: остановка контейнеров + удаление томов (данных БД)
	rm -rf bin/
	docker-compose -f $(DC_FILE) down -v