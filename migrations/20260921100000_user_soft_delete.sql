-- +goose Up
-- +goose StatementBegin

-- Жёсткое удаление пользователя в этой схеме невозможно: messages.sender_id
-- ссылается на users БЕЗ ON DELETE (то есть NO ACTION), поэтому DELETE FROM
-- users падает на нарушении FK у любого, кто хоть раз писал в чат. А если бы
-- прошёл — meetups.creator_id ON DELETE CASCADE снёс бы все созданные им
-- митапы вместе с участниками. Отсюда soft-delete: строка остаётся,
-- идентифицирующие поля обнуляются (см. UserAdminRepo.SoftDeleteTx).
ALTER TABLE users ADD COLUMN deleted_at TIMESTAMP;

-- Частичный индекс: удалённых заведомо меньшинство, и единственный запрос,
-- которому он нужен, — админский фильтр «показать удалённых».
CREATE INDEX idx_users_deleted_at ON users (deleted_at) WHERE deleted_at IS NOT NULL;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP INDEX IF EXISTS idx_users_deleted_at;
ALTER TABLE users DROP COLUMN IF EXISTS deleted_at;

-- +goose StatementEnd
