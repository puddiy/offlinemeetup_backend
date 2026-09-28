package domain

import "github.com/uptrace/bun"

type Tag struct {
	bun.BaseModel `bun:"table:tags" swaggerignore:"true"`

	ID   int64  `bun:",pk,autoincrement" json:"id"`
	Name string `bun:",unique,notnull" json:"name"`
	// IsHidden — тег скрыт из каталога и выбора (см. миграцию
	// 20260928100000). Наружу не сериализуется: мобильный клиент видит
	// теги только через dto.TagResponse.
	IsHidden bool `bun:",notnull,default:false" json:"-"`
}

type UserTag struct {
	bun.BaseModel `bun:"table:user_tags" swaggerignore:"true"`

	UserID int64 `bun:",pk" json:"user_id"`
	TagID  int64 `bun:",pk" json:"tag_id"`

	User *User `bun:"rel:belongs-to,join:user_id=id" json:"-"`
	Tag  *Tag  `bun:"rel:belongs-to,join:tag_id=id" json:"-"`
}

type MeetupTag struct {
	bun.BaseModel `bun:"table:meetup_tags" swaggerignore:"true"`
	MeetupID      int64 `bun:",pk"`
	TagID         int64 `bun:",pk"`

	Meetup *Meetup `bun:"rel:belongs-to"`
	Tag    *Tag    `bun:"rel:belongs-to"`
}
