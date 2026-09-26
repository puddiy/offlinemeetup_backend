-- +goose Up
-- +goose StatementBegin

-- Жалобы пользователей на контент. Store-gate: без них App Store и Google Play
-- не пропускают приложение с пользовательским контентом.
--
-- target_id без внешнего ключа: цель полиморфна (митап, сообщение, пользователь).
-- target_owner_id тоже без FK, по той же причине, что admin_audit_log.admin_id:
-- жалоба — доказательство, и она обязана пережить любые манипуляции с целью.
CREATE TABLE reports (
    id                BIGSERIAL PRIMARY KEY,
    reporter_id       BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    target_type       VARCHAR(16) NOT NULL,
    target_id         BIGINT NOT NULL,
    target_owner_id   BIGINT NOT NULL,
    reason            VARCHAR(32) NOT NULL,
    comment           TEXT NOT NULL DEFAULT '',
    snapshot_text     TEXT NOT NULL DEFAULT '',
    snapshot_file_key TEXT,
    status            VARCHAR(16) NOT NULL DEFAULT 'open',
    resolution        TEXT,
    resolved_by       BIGINT,
    resolved_at       TIMESTAMP,
    created_at        TIMESTAMP NOT NULL DEFAULT current_timestamp,
    CONSTRAINT reports_target_type_check CHECK (target_type IN ('meetup', 'message', 'user')),
    CONSTRAINT reports_reason_check CHECK (reason IN ('spam', 'harassment', 'hate', 'sexual', 'violence', 'scam', 'other')),
    CONSTRAINT reports_status_check CHECK (status IN ('open', 'resolved', 'dismissed'))
);

-- Одна ОТКРЫТАЯ жалоба на цель от одного пользователя. Арбитр — индекс, а не
-- SELECT перед вставкой: проверка-перед-вставкой проиграла бы гонку двух
-- одновременных тапов. После закрытия можно пожаловаться снова.
CREATE UNIQUE INDEX uq_reports_open_per_reporter
    ON reports (reporter_id, target_type, target_id) WHERE status = 'open';

-- Очередь модератора: фильтр по статусу, сортировка по времени.
CREATE INDEX idx_reports_status_created ON reports (status, created_at DESC);

-- Действие модератора закрывает все открытые жалобы на ту же цель.
CREATE INDEX idx_reports_open_target ON reports (target_type, target_id) WHERE status = 'open';

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS reports;
-- +goose StatementEnd
