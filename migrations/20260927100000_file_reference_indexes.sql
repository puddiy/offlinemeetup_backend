-- +goose Up
-- +goose StatementBegin

-- Индексы на ссылки на files. Нужны дважды:
--   - удаление строки files запускает ON DELETE SET NULL, и без индекса
--     каждое удаление сканирует профили и митапы целиком;
--   - уборка осиротевших файлов (FileRepo.ListOrphans) проверяет эти ссылки
--     через NOT EXISTS для каждого кандидата.
-- messages.file_id уже проиндексирован (idx_messages_file_id).
CREATE INDEX IF NOT EXISTS idx_profile_avatar_file_id ON profile (avatar_file_id) WHERE avatar_file_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_meetups_cover_file_id ON meetups (cover_file_id) WHERE cover_file_id IS NOT NULL;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_meetups_cover_file_id;
DROP INDEX IF EXISTS idx_profile_avatar_file_id;
-- +goose StatementEnd
