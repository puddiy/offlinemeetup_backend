package dto

// AdminTagRow — строка справочника тегов в админке.
type AdminTagRow struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	IsHidden bool   `json:"is_hidden"`
	Meetups  int    `json:"meetups"`
	Users    int    `json:"users"`
}
