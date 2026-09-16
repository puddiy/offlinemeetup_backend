-- +goose Up
-- +goose StatementBegin

-- Админы живут отдельно от users сознательно: мобильный access-токен,
-- утёкший с устройства, не должен открывать панель. Отсюда же своя
-- сессия (Redis) вместо переиспользования JWT.
CREATE TABLE admin_users (
    id            BIGSERIAL PRIMARY KEY,
    email         TEXT NOT NULL,
    password_hash TEXT NOT NULL,
    role          VARCHAR(20) NOT NULL,
    is_active     BOOLEAN NOT NULL DEFAULT TRUE,
    created_at    TIMESTAMP NOT NULL DEFAULT current_timestamp,
    updated_at    TIMESTAMP NOT NULL DEFAULT current_timestamp,
    CONSTRAINT admin_users_role_check CHECK (role IN ('moderator', 'admin'))
);

-- Уникальность по lower(email) — тем же приёмом, что uq_users_email_lower
-- в 20260810033338_email_password_auth.sql: логин регистронезависим.
CREATE UNIQUE INDEX uq_admin_users_email_lower ON admin_users (lower(email));

-- Журнал действий. admin_id БЕЗ внешнего ключа на admin_users:
-- запись журнала обязана пережить удаление самой учётки, иначе исчезнут
-- следы ровно тех действий, ради которых журнал и заводится.
CREATE TABLE admin_audit_log (
    id          BIGSERIAL PRIMARY KEY,
    admin_id    BIGINT NOT NULL,
    action      TEXT NOT NULL,
    target_type TEXT,
    target_id   TEXT,
    details     JSONB,
    ip          TEXT,
    created_at  TIMESTAMP NOT NULL DEFAULT current_timestamp
);

-- Основной сценарий чтения — «последние действия», плюс «что делал вот этот
-- админ» и «что происходило с вот этим объектом».
CREATE INDEX idx_admin_audit_created_at ON admin_audit_log (created_at DESC);
CREATE INDEX idx_admin_audit_admin_id ON admin_audit_log (admin_id, created_at DESC);
CREATE INDEX idx_admin_audit_target ON admin_audit_log (target_type, target_id);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP TABLE IF EXISTS admin_audit_log;
DROP TABLE IF EXISTS admin_users;

-- +goose StatementEnd
