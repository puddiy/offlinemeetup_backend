-- +goose Up
-- +goose StatementBegin

-- Скрытый тег пропадает из каталога и из выбора, но остаётся на митапах и в
-- профилях, где уже стоит (решение продукта от 2026-09-27). Удалять теги
-- нельзя: meetup_tags/user_tags ссылаются на них с ON DELETE CASCADE, и
-- удаление молча сняло бы тег со всех митапов без возможности вернуть.
ALTER TABLE tags ADD COLUMN is_hidden BOOLEAN NOT NULL DEFAULT FALSE;

-- Регистронезависимая уникальность: иначе админ заведёт «спорт» рядом со
-- «Спорт», и в каталоге окажутся два неотличимых тега. Старый
-- tags_name_key (регистрозависимый) остаётся — он строже не бывает.
CREATE UNIQUE INDEX uq_tags_name_lower ON tags (lower(name));

-- Служебный аккаунт «Meetuper» — создатель официальных митапов из админки.
-- email NULL, пароля и соцпривязок нет: войти под ним невозможно ни одним
-- способом (dev-login, OAuth-линковка и сброс пароля идут через email).
ALTER TABLE users ADD COLUMN is_system BOOLEAN NOT NULL DEFAULT FALSE;

-- Служебный аккаунт ровно один: приложение находит его по флагу на старте,
-- и второй сделал бы выбор создателя неоднозначным.
CREATE UNIQUE INDEX uq_users_single_system ON users (is_system) WHERE is_system;

INSERT INTO users (email, status, is_system) VALUES (NULL, 'active', TRUE);

-- username 'meetuper' может быть уже занят реальным пользователем (он мог
-- зарегистрироваться до этой миграции) — тогда берём 'meetuper_<id>', а не
-- роняем старт приложения. uq_profile_username_lower регистронезависим.
INSERT INTO profile (user_id, username, display_name, bio)
SELECT u.id,
       CASE WHEN EXISTS (SELECT 1 FROM profile p WHERE lower(p.username) = 'meetuper')
            THEN 'meetuper_' || u.id
            ELSE 'meetuper' END,
       'Meetuper',
       'Официальные встречи команды Meetuper'
FROM users u
WHERE u.is_system;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

-- ВНИМАНИЕ: удаление служебного аккаунта каскадом (meetups.creator_id
-- ON DELETE CASCADE) удаляет и все официальные митапы. Down — только для
-- отката в dev.
DELETE FROM profile WHERE user_id IN (SELECT id FROM users WHERE is_system);
DELETE FROM users WHERE is_system;
DROP INDEX IF EXISTS uq_users_single_system;
ALTER TABLE users DROP COLUMN IF EXISTS is_system;

DROP INDEX IF EXISTS uq_tags_name_lower;
ALTER TABLE tags DROP COLUMN IF EXISTS is_hidden;

-- +goose StatementEnd
