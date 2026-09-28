package repo

import (
	"context"
	"fmt"

	"github.com/uptrace/bun"
)

// uniqueIDs убирает повторы, сохраняя порядок первых вхождений. Клиент,
// приславший [3,3], иначе получал 500 на нарушении первичного ключа
// meetup_tags/user_tags.
func uniqueIDs(ids []int64) []int64 {
	if ids == nil {
		return nil
	}
	seen := make(map[int64]struct{}, len(ids))
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// checkMeetupTagsTx проверяет, что каждый тег из tagIDs можно оставить на
// митапе: он существует и либо не скрыт, либо УЖЕ стоит на этом митапе.
// meetupID = 0 — митап ещё не создан, допустимы только видимые теги.
//
// Исключение для уже стоящих скрытых тегов — решение продукта «старые
// митапы не меняются»: мобильный клиент шлёт полный список тегов при
// любой правке, и без исключения митап со скрытым тегом стал бы
// нередактируемым.
//
// tagIDs обязан быть без повторов (uniqueIDs): проверка сравнивает число
// найденных строк с длиной списка. Вызывать ДО удаления старых связей —
// иначе «уже стоит» никогда не сработает.
func checkMeetupTagsTx(ctx context.Context, idb bun.IDB, meetupID int64, tagIDs []int64) error {
	return checkTagsTx(ctx, idb, tagIDs,
		"EXISTS (SELECT 1 FROM meetup_tags mt WHERE mt.meetup_id = ? AND mt.tag_id = t.id)", meetupID)
}

// checkUserTagsTx — то же правило для тегов-интересов профиля.
func checkUserTagsTx(ctx context.Context, idb bun.IDB, userID int64, tagIDs []int64) error {
	return checkTagsTx(ctx, idb, tagIDs,
		"EXISTS (SELECT 1 FROM user_tags ut WHERE ut.user_id = ? AND ut.tag_id = t.id)", userID)
}

// checkTagsTx — общая часть. alreadyAttached — SQL-условие «тег уже стоит у
// владельца»; это константа из кода выше, не пользовательский ввод.
func checkTagsTx(ctx context.Context, idb bun.IDB, tagIDs []int64, alreadyAttached string, ownerID int64) error {
	if len(tagIDs) == 0 {
		return nil
	}
	var n int
	err := idb.NewSelect().
		TableExpr("tags AS t").
		ColumnExpr("count(*)").
		Where("t.id IN (?)", bun.In(tagIDs)).
		WhereGroup(" AND ", func(q *bun.SelectQuery) *bun.SelectQuery {
			return q.Where("NOT t.is_hidden").WhereOr(alreadyAttached, ownerID)
		}).
		Scan(ctx, &n)
	if err != nil {
		return fmt.Errorf("check tags: %w", err)
	}
	if n != len(tagIDs) {
		return ErrTagUnavailable
	}
	return nil
}
