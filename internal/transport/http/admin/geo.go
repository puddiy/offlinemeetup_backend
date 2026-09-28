package admin

import (
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"
)

// minAddressQuery — короче DaData отвечает шумом, а каждый запрос платный.
const minAddressQuery = 3

// AddressPick — одна подсказка: текст и ссылка, заполняющая поля формы.
type AddressPick struct {
	Value string
	Link  string
}

// AddressSuggestionsData — фрагмент со списком подсказок.
type AddressSuggestionsData struct {
	Items   []AddressPick
	Message string
}

// GeoSuggest отдаёт HTMX-фрагмент с подсказками адреса. Сбой DaData — не
// ошибка страницы: админ вводит координаты вручную, поля для них всегда
// видны.
func (h *Handler) GeoSuggest(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("address"))
	data := AddressSuggestionsData{}

	switch {
	case utf8.RuneCountInString(q) < minAddressQuery:
		data.Message = "Введите хотя бы 3 символа адреса"
	default:
		items, err := h.geo.SuggestAddress(r.Context(), q)
		if err != nil {
			h.log.Warn("address suggestions failed", slog.Any("error", err))
			data.Message = "Подсказки недоступны — введите координаты вручную"
			break
		}
		for _, s := range items {
			// Без координат подсказка бесполезна: митапу нужна точка на карте.
			if s.Lat == 0 && s.Lon == 0 {
				continue
			}
			v := url.Values{}
			v.Set("address", s.Value)
			v.Set("lat", strconv.FormatFloat(s.Lat, 'f', -1, 64))
			v.Set("lng", strconv.FormatFloat(s.Lon, 'f', -1, 64))
			data.Items = append(data.Items, AddressPick{Value: s.Value, Link: "/admin/geo/pick?" + v.Encode()})
		}
		if len(data.Items) == 0 {
			data.Message = "Ничего не найдено — уточните адрес или введите координаты вручную"
		}
	}

	h.render.RenderPartial(w, http.StatusOK, "address_suggestions", data)
}

// GeoPick возвращает поля адреса, заполненные выбранной подсказкой.
// Значения приходят из query и экранируются шаблоном.
func (h *Handler) GeoPick(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	h.render.RenderPartial(w, http.StatusOK, "address_fields", MeetupFormValues{
		Address: q.Get("address"),
		Lat:     q.Get("lat"),
		Lng:     q.Get("lng"),
	})
}
