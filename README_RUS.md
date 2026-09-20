# Meetuper Backend

Бэкенд для мобильного приложения **Meetuper** — организация, поиск и участие в оффлайн-встречах. Go-сервис с **REST API** и **real-time WebSocket**-слоем (чат + presence), построенный по слоистой («чистой») архитектуре: гео-поиск на **PostGIS**, **Redis** для кэширования / pub-sub / presence и **S3-совместимое** хранилище для медиа.

## Стек технологий

* **Язык:** Go 1.26
* **HTTP-роутер:** [chi](https://github.com/go-chi/chi)
* **База данных:** PostgreSQL 16 + **PostGIS** (пространственные запросы)
* **ORM / Query Builder:** [Bun](https://github.com/uptrace/bun)
* **Кэш · Pub/Sub · Presence:** Redis
* **Хранилище объектов:** S3-совместимое (MinIO в dev)
* **Real-time:** WebSocket
* **Миграции:** [Goose](https://github.com/pressly/goose) — встроены, накатываются автоматически при старте
* **Метрики:** Prometheus
* **Документация:** Swagger (swaggo)
* **Инфраструктура:** Docker, Docker Compose, GitHub Actions (CI/CD)

## Архитектура

Проект построен по слоистой архитектуре; зависимости связываются вручную в `internal/app`. Поток запроса:

```
transport/http/handler  →  service           →  repo         →  Bun / Postgres
   (DTO ↔ domain)          (бизнес-логика)       (SQL)
```

* `cmd/app/` — Точка входа: загрузка конфига, подключение БД/Redis/S3, миграции, запуск HTTP-сервера и WebSocket-хаба.
* `internal/transport/http/` — REST-слой: роутинг, хендлеры, middleware (auth, rate limiting, security headers), DTO.
* `internal/transport/websocket/` — Real-time слой hub-and-client: чат, presence, межинстансный broadcast.
* `internal/service/` — Бизнес-логика. Каждый сервис объявляет нужный ему интерфейс репозитория (шов для моков).
* `internal/repo/` — Слой данных через Bun.
* `internal/domain/` — Доменные модели.
* `internal/cache/` — Хелперы кэширования на Redis (cache-aside, presence store).
* `internal/config/` — Конфигурация из env.

## Основные возможности

### Гео-поиск
* Поиск в радиусе с ранжированием по расстоянию через PostGIS `ST_DWithin` / `ST_Distance`.
* Интеграция с **DaData** — подсказки адресов и геокодирование.

### Аутентификация и авторизация
* Короткоживущие **JWT access-токены** и **ротируемые opaque refresh-токены** (в БД хранится только SHA-256-хэш) с **детекцией повторного использования** — предъявление уже отозванного токена отзывает все сессии пользователя.
* OAuth через **Google ID Token** и **Telegram Login Widget**.
* Fail-fast при отсутствии секретов (`JWT_SECRET_KEY`, `TELEGRAM_BOT_TOKEN`) во всех окружениях.
* Авторизация запроса (владение, членство, capability-токены) проверяется **внутри транзакций репозитория**.

### Real-time чат (WebSocket)
* Модель hub-and-client: отправка / редактирование / удаление сообщений, ответы, отметки о прочтении, индикатор набора.
* **Горизонтально масштабируется между инстансами** через Redis Pub/Sub — источник правды это БД, а клиент добирает историю по REST при реконнекте.
* Обработка входящих событий по одному воркеру на соединение и строго по порядку; каждая горутина изолирована от паник.
* **Идемпотентная отправка** по клиентскому `request_id`.

### Presence
* Статус online / last-seen на Redis. Переходы offline↔online вычисляются **атомарно через Lua**, чтобы конкурирующие инстансы не слали дубли; TTL-heartbeat переводит соединения упавшего инстанса в offline.

### Загрузка медиа
* Схема upload-then-reference для обложек, аватаров и вложений; файлы хранятся в S3.
* MIME определяется **по реальным байтам** (не по имени файла клиента), с whitelist медиа-типов.
* **Серверное извлечение метаданных**: длительность (аудио/видео) и размеры (изображения/видео); аудио-only MP4 корректируется из `video/mp4` в `audio/mp4`.
* Обложки/аватары — только изображения; максимальный размер настраивается (по умолчанию 100 МБ).

### Кэширование и устойчивость
* Redis cache-aside с **singleflight** (против stampede), **джиттером TTL** (против avalanche) и операциями с таймаутом, деградирующими к загрузчику при медленном Redis — всё с метриками Prometheus.
* **Распределённый rate limiter** (fixed-window на Redis) на роутах авторизации и загрузки.
* Security-заголовки, лимит тела запроса и настраиваемый allowlist Origin для WebSocket.

### Наблюдаемость и эксплуатация
* `GET /health` (liveness) и `GET /metrics` (Prometheus).
* Миграции встроены и накатываются автоматически при старте — отдельного шага миграции нет.
* CI/CD через GitHub Actions (сборка/пуш Docker-образа, деплой).

## Установка и запуск

### Предварительные требования
* Docker & Docker Compose
* Go 1.26 (для запуска без Docker)
* Make

### Конфигурация
Создайте файл `.env` в корне проекта. `DB_DSN`, `JWT_SECRET_KEY` и `TELEGRAM_BOT_TOKEN` **обязательны** — без них приложение не стартует.

```env
APP_PORT=9090
APP_ENV=local            # local | dev | prod

# База данных (PostGIS)
DB_DSN=postgres://user:password@localhost:5432/meetuper_db?sslmode=disable
POSTGRES_USER=user
POSTGRES_PASSWORD=password
POSTGRES_DB=meetuper_db

# Redis
REDIS_ADDR=localhost:6379
REDIS_PASSWORD=
REDIS_DB=0

# Хранилище объектов (S3 / MinIO)
S3_ENDPOINT=http://localhost:9000
S3_REGION=us-east-1
S3_BUCKET=meetuper
S3_ACCESS_KEY=minioadmin
S3_SECRET_KEY=minioadmin
S3_PUBLIC_URL=http://localhost:9000/meetuper

# Авторизация
JWT_SECRET_KEY=your_secret_key
JWT_ACCESS_TTL=15m
JWT_REFRESH_TTL=720h
GOOGLE_WEB_CLIENT_ID=your_google_client_id
TELEGRAM_BOT_TOKEN=your_telegram_bot_token

# Внешние API
DADATA_TOKEN=your_dadata_api_key

# Опционально (значения по умолчанию)
MAX_UPLOAD_SIZE=104857600         # 100 МБ
PRESENCE_TTL=2m
CACHE_TIMEOUT=200ms
TRUST_PROXY_HEADERS=false          # доверять X-Forwarded-For/X-Real-IP только за доверенным прокси
WS_ALLOWED_ORIGINS=http://localhost:3000

# Админ-панель
ADMIN_SESSION_TTL=8h              # окно простоя сессии (продлевается каждым запросом)
ADMIN_SESSION_MAX_TTL=24h         # абсолютный потолок от момента входа
```

### Запуск в Docker (рекомендуемый)
Разворачивает PostgreSQL (PostGIS), Redis, MinIO и приложение:

```bash
make up
```

Миграции накатываются автоматически при старте. API доступен по адресу `http://localhost:9090` (Swagger — `/swagger/index.html`).

### Локальный запуск (для разработки)
1. Поднять только инфраструктуру:
   ```bash
   docker-compose -f docker-compose.dev.yml up -d meetuper_db meetuper_redis minio
   ```
2. Запустить приложение (миграции накатятся автоматически при старте):
   ```bash
   REDIS_ADDR=localhost:6379 make run
   ```

> `make migration NAME=...` только **создаёт** файл новой миграции — он их не применяет. Применение происходит при каждом старте.

## API-документация

Swagger UI (включён в `local`/`dev`): `http://localhost:9090/swagger/index.html`. Перегенерация после изменения хендлеров:

```bash
make swag
```

WebSocket-эндпоинт — `GET /v1/ws` (аутентификация по JWT `Bearer`); по нему идут события чата и presence.

## Тестирование

```bash
make test   # unit-тесты с race-детектором (код плотно конкурентный)
make lint   # golangci-lint
```

Тесты сервисного слоя используют gomock и miniredis (in-memory Redis).

## Деплой

В репозитории настроен CI/CD через GitHub Actions (`.github/workflows/deploy.yml`):

1. Сборка Docker-образа.
2. Пуш в Docker Hub.
3. Подключение по SSH к VPS.
4. Обновление контейнеров через `docker compose`.

Для продакшена используется `docker-compose.yml` с Traefik (метки `traefik.enable=true`, хост `api.meetuper.site`, TLS через Let's Encrypt).

## Особенности

* **PostGIS обязателен.** Нужен образ `postgis/postgis:16-3.4-alpine` — обычный Postgres не подойдёт, используются гео-типы `GEOGRAPHY(POINT, 4326)`.
* **DaData:** для подсказок адресов (`/v1/geo/suggest`) нужен валидный токен; без него эндпоинт вернёт ошибку.
* **Redis — жёсткая зависимость real-time слоя:** доставка WebSocket-сообщений между инстансами идёт через Redis Pub/Sub, presence и кэш тоже на Redis.

## Полезные команды (Makefile)

| Команда | Описание |
|---|---|
| `make up` / `down` / `restart` | Запуск / остановка / перезапуск dev-окружения в Docker |
| `make logs` / `logs-app` | Логи контейнеров (все / только приложение) |
| `make run` | Локальный запуск приложения (нужны доступные БД/Redis) |
| `make build` | Сборка бинарника в `bin/meetuper` |
| `make test` | Unit-тесты с race-детектором |
| `make lint` | golangci-lint |
| `make swag` | Перегенерация Swagger-документации |
| `make migration NAME=xxx` | Создать новую SQL-миграцию |
| `make clean` | Остановка контейнеров и удаление томов (данные БД теряются) |
