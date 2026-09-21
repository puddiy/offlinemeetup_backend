package dto

// Page — конверт постраничного ответа.
//
// Заведён ДЛЯ АДМИНКИ и только для неё. Мобильные списки исторически
// возвращают голый JSON-массив (`[]MeetupResponse`, `[]ChatResponse`, …);
// обернуть их сейчас значит сломать клиент на руках у пользователей.
// Админским же экранам без Total не нарисовать «страница 3 из 47» —
// отсюда отдельный тип, а не переделка существующих ответов.
type Page[T any] struct {
	Items  []T `json:"items"`
	Total  int `json:"total"`
	Limit  int `json:"limit"`
	Offset int `json:"offset"`
}

// NewPage собирает конверт, нормализуя nil-слайс в пустой: encoding/json
// сериализует nil как `null`, и вместо пустого списка клиент получил бы
// `"items": null`, на котором спотыкается любой наивный разбор.
func NewPage[T any](items []T, total, limit, offset int) Page[T] {
	if items == nil {
		items = []T{}
	}
	return Page[T]{Items: items, Total: total, Limit: limit, Offset: offset}
}

// Pages — всего страниц при текущем Limit. 0 при Limit<=0 или пустой выборке:
// «ноль страниц» честнее, чем деление на ноль или выдуманная единица.
func (p Page[T]) Pages() int {
	if p.Limit <= 0 || p.Total <= 0 {
		return 0
	}
	return (p.Total + p.Limit - 1) / p.Limit
}

// CurrentPage — номер текущей страницы, начиная с 1.
func (p Page[T]) CurrentPage() int {
	if p.Limit <= 0 {
		return 1
	}
	return p.Offset/p.Limit + 1
}

// HasPrev — есть ли страница назад.
func (p Page[T]) HasPrev() bool {
	return p.Offset > 0
}

// HasNext — есть ли страница вперёд. При Limit<=0 страниц нет вовсе, иначе
// «offset+0 < total» дало бы вечное «вперёд» и NextOffset == Offset.
func (p Page[T]) HasNext() bool {
	return p.Limit > 0 && p.Offset+p.Limit < p.Total
}

// PrevOffset — offset предыдущей страницы, не уходящий ниже нуля.
func (p Page[T]) PrevOffset() int {
	prev := p.Offset - p.Limit
	if prev < 0 {
		return 0
	}
	return prev
}

// NextOffset — offset следующей страницы. Когда следующей нет, возвращает
// текущий: шаблон всё равно не нарисует ссылку (HasNext false), а отдавать
// offset за пределами выборки — приглашение к пустой странице.
func (p Page[T]) NextOffset() int {
	if !p.HasNext() {
		return p.Offset
	}
	return p.Offset + p.Limit
}
