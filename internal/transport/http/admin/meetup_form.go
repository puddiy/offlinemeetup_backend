package admin

import (
	"errors"
	"log/slog"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/dto"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/service"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/transport/http/middleware"
)

// datetimeLocalLayout — формат значения <input type="datetime-local">: без
// секунд и БЕЗ часового пояса. Пояс подставляет adminLocation.
const datetimeLocalLayout = "2006-01-02T15:04"

// MeetupFormValues — сырые строки формы. Храним строки, а не типы: при
// ошибке форма возвращается с ровно тем, что ввёл админ, а не с нулями.
type MeetupFormValues struct {
	Title       string
	Description string
	Start       string
	End         string
	Address     string
	Lat         string
	Lng         string
	IsPublic    bool
	TagIDs      map[int64]bool
}

// parseMeetupForm читает форму. Нечисловые id тегов отбрасываются молча:
// их может прислать только подделанная форма.
func parseMeetupForm(r *http.Request) (MeetupFormValues, error) {
	if err := r.ParseForm(); err != nil {
		return MeetupFormValues{}, err
	}
	v := MeetupFormValues{
		Title:       strings.TrimSpace(r.PostFormValue("title")),
		Description: strings.TrimSpace(r.PostFormValue("description")),
		Start:       strings.TrimSpace(r.PostFormValue("start_time")),
		End:         strings.TrimSpace(r.PostFormValue("end_time")),
		Address:     strings.TrimSpace(r.PostFormValue("address")),
		Lat:         strings.TrimSpace(r.PostFormValue("lat")),
		Lng:         strings.TrimSpace(r.PostFormValue("lng")),
		IsPublic:    r.PostFormValue("is_public") == "1",
		TagIDs:      map[int64]bool{},
	}
	for _, s := range r.PostForm["tags"] {
		if id, err := strconv.ParseInt(s, 10, 64); err == nil {
			v.TagIDs[id] = true
		}
	}
	return v, nil
}

// formValuesFromDetail заполняет форму правки из карточки митапа.
func formValuesFromDetail(d *dto.AdminMeetupDetail, loc *time.Location) MeetupFormValues {
	v := MeetupFormValues{
		Title:       d.Title,
		Description: d.Description,
		Start:       d.StartTime.In(loc).Format(datetimeLocalLayout),
		End:         d.EndTime.In(loc).Format(datetimeLocalLayout),
		Address:     d.Address,
		Lat:         strconv.FormatFloat(d.Lat, 'f', -1, 64),
		Lng:         strconv.FormatFloat(d.Lng, 'f', -1, 64),
		IsPublic:    d.IsPublic,
		TagIDs:      map[int64]bool{},
	}
	for _, id := range d.TagIDs {
		v.TagIDs[id] = true
	}
	return v
}

// meetupFields — строки формы, разобранные в типы.
type meetupFields struct {
	start, end time.Time
	lat, lng   float64
	tagIDs     []int64
}

// fields разбирает общие для создания и правки поля. Ключи ошибок —
// "start_time", "end_time", "coordinates".
func (v MeetupFormValues) fields(loc *time.Location) (meetupFields, map[string]string) {
	errs := map[string]string{}
	var f meetupFields
	var err error

	if f.start, err = time.ParseInLocation(datetimeLocalLayout, v.Start, loc); err != nil {
		errs["start_time"] = "укажите дату и время начала"
	}
	if f.end, err = time.ParseInLocation(datetimeLocalLayout, v.End, loc); err != nil {
		errs["end_time"] = "укажите дату и время окончания"
	}

	lat, latErr := strconv.ParseFloat(v.Lat, 64)
	lng, lngErr := strconv.ParseFloat(v.Lng, 64)
	// ParseFloat принимает "NaN" и "Inf", а проверка диапазона в Validate
	// их пропускает (любое сравнение с NaN — false) — PostGIS ответил бы 500.
	if latErr != nil || lngErr != nil || !isFinite(lat) || !isFinite(lng) {
		errs["coordinates"] = "выберите адрес из подсказок или введите координаты"
	} else {
		f.lat, f.lng = lat, lng
	}

	for id := range v.TagIDs {
		f.tagIDs = append(f.tagIDs, id)
	}
	slices.Sort(f.tagIDs)
	return f, errs
}

func isFinite(x float64) bool { return !math.IsNaN(x) && !math.IsInf(x, 0) }

// mergeErrors добавляет ошибки Validate, не затирая ошибки разбора: при
// пустой дате Validate скажет «в прошлом», а человеку нужно «укажите дату».
func mergeErrors(dst, src map[string]string) {
	for k, msg := range src {
		if _, ok := dst[k]; !ok {
			dst[k] = msg
		}
	}
}

// toCreateRequest собирает запрос создания и проверяет его тем же
// Validate, что и мобильный POST /v1/meetups.
func (v MeetupFormValues) toCreateRequest(loc *time.Location) (dto.CreateMeetupRequest, map[string]string) {
	f, errs := v.fields(loc)
	req := dto.CreateMeetupRequest{
		Title:       v.Title,
		Description: v.Description,
		IsPublic:    v.IsPublic,
		StartTime:   f.start,
		EndTime:     f.end,
		Coordinates: dto.Coordinates{Lat: f.lat, Lng: f.lng},
		Address:     v.Address,
		TagIDs:      f.tagIDs,
	}
	mergeErrors(errs, req.Validate())
	return req, errs
}

// toUpdateRequest собирает запрос правки. Форма отправляет ВСЕ поля, поэтому
// заполнены все указатели; теги — указатель даже на пустой срез (все галочки
// сняты = снять теги). Обложку форма не трогает (CoverFileID = nil).
func (v MeetupFormValues) toUpdateRequest(loc *time.Location) (dto.UpdateMeetupRequest, map[string]string) {
	f, errs := v.fields(loc)
	title, desc, addr, public := v.Title, v.Description, v.Address, v.IsPublic
	tags := f.tagIDs
	if tags == nil {
		tags = []int64{}
	}
	req := dto.UpdateMeetupRequest{
		Title:       &title,
		Description: &desc,
		Address:     &addr,
		IsPublic:    &public,
		TagIDs:      &tags,
	}
	if _, bad := errs["start_time"]; !bad {
		req.StartTime = &f.start
	}
	if _, bad := errs["end_time"]; !bad {
		req.EndTime = &f.end
	}
	if _, bad := errs["coordinates"]; !bad {
		c := dto.Coordinates{Lat: f.lat, Lng: f.lng}
		req.Coordinates = &c
	}
	mergeErrors(errs, req.Validate())
	return req, errs
}

// TagOption — галочка тега в форме.
type TagOption struct {
	ID      int64
	Name    string
	Hidden  bool
	Checked bool
}

// MeetupFormData — данные страницы формы.
type MeetupFormData struct {
	MeetupID int64 // 0 — создание
	Values   MeetupFormValues
	Tags     []TagOption
	Errors   map[string]string
	Timezone string
}

// Action — адрес отправки формы.
func (d MeetupFormData) Action() string {
	if d.MeetupID == 0 {
		return meetupsPath
	}
	return meetupsPath + "/" + strconv.FormatInt(d.MeetupID, 10) + "/edit"
}

// tagOptions — галочки: все видимые теги плюс СКРЫТЫЕ, но уже отмеченные
// (правило «старые митапы не меняются», см. repo.checkMeetupTagsTx).
func (h *Handler) tagOptions(r *http.Request, checked map[int64]bool) []TagOption {
	rows, err := h.tags.List(r.Context())
	if err != nil {
		h.log.Error("listing tags for meetup form", slog.Any("error", err))
		return nil
	}
	opts := make([]TagOption, 0, len(rows))
	for _, t := range rows {
		if t.IsHidden && !checked[t.ID] {
			continue
		}
		opts = append(opts, TagOption{ID: t.ID, Name: t.Name, Hidden: t.IsHidden, Checked: checked[t.ID]})
	}
	return opts
}

// renderMeetupForm рисует форму с данными и ошибками.
func (h *Handler) renderMeetupForm(w http.ResponseWriter, r *http.Request, status int, id int64, v MeetupFormValues, errs map[string]string) {
	admin, _ := middleware.GetAdminFromContext(r.Context())
	title := "Новый митап"
	if id != 0 {
		title = "Редактирование митапа"
	}
	h.render.Render(w, status, "meetup_form", PageData{
		Title: title,
		Admin: admin,
		Data: MeetupFormData{
			MeetupID: id,
			Values:   v,
			Tags:     h.tagOptions(r, v.TagIDs),
			Errors:   errs,
			Timezone: h.adminLocation().String(),
		},
	})
}

// rejectedByService — текст, когда форма прошла проверку, а сервис её
// отверг: на практике это тег, скрытый, пока форма была открыта.
var rejectedByService = map[string]string{"form": "Проверьте теги: один из выбранных скрыт или удалён"}

// MeetupNew рисует пустую форму.
func (h *Handler) MeetupNew(w http.ResponseWriter, r *http.Request) {
	h.renderMeetupForm(w, r, http.StatusOK, 0, MeetupFormValues{IsPublic: true, TagIDs: map[int64]bool{}}, nil)
}

// MeetupCreate публикует официальный митап.
func (h *Handler) MeetupCreate(w http.ResponseWriter, r *http.Request) {
	admin, ok := middleware.GetAdminFromContext(r.Context())
	if !ok || admin == nil {
		http.Redirect(w, r, loginPath, http.StatusSeeOther)
		return
	}
	v, err := parseMeetupForm(r)
	if err != nil {
		h.renderMeetupForm(w, r, http.StatusBadRequest, 0, MeetupFormValues{TagIDs: map[int64]bool{}},
			map[string]string{"form": "не удалось прочитать форму"})
		return
	}
	req, errs := v.toCreateRequest(h.adminLocation())
	if len(errs) > 0 {
		h.renderMeetupForm(w, r, http.StatusBadRequest, 0, v, errs)
		return
	}

	id, err := h.meetups.CreateOfficial(r.Context(), admin.ID, req, h.clientIP(r))
	switch {
	case err == nil:
		h.redirectToMeetup(w, r, id, noticeMeetupCreated, "")
	case errors.Is(err, service.ErrInvalidInput):
		h.renderMeetupForm(w, r, http.StatusBadRequest, 0, v, rejectedByService)
	default:
		h.log.Error("creating official meetup", slog.Any("error", err))
		h.renderMeetupForm(w, r, http.StatusInternalServerError, 0, v,
			map[string]string{"form": "не удалось опубликовать митап"})
	}
}

// MeetupEdit рисует форму правки официального митапа.
func (h *Handler) MeetupEdit(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		redirectWithNotice(w, r, meetupsPath, "", noticeMeetupNotFound)
		return
	}
	d, err := h.meetups.Get(r.Context(), id)
	if err != nil {
		if !errors.Is(err, service.ErrNotFound) {
			h.log.Error("loading meetup for edit", slog.Int64("meetup_id", id), slog.Any("error", err))
		}
		redirectWithNotice(w, r, meetupsPath, "", noticeMeetupNotFound)
		return
	}
	if !d.IsOfficial {
		h.redirectToMeetup(w, r, id, "", noticeNotOfficial)
		return
	}
	h.renderMeetupForm(w, r, http.StatusOK, id, formValuesFromDetail(d, h.adminLocation()), nil)
}

// MeetupUpdate сохраняет правку официального митапа.
func (h *Handler) MeetupUpdate(w http.ResponseWriter, r *http.Request) {
	admin, ok := middleware.GetAdminFromContext(r.Context())
	if !ok || admin == nil {
		http.Redirect(w, r, loginPath, http.StatusSeeOther)
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		redirectWithNotice(w, r, meetupsPath, "", noticeMeetupNotFound)
		return
	}
	v, err := parseMeetupForm(r)
	if err != nil {
		h.redirectToMeetup(w, r, id, "", noticeModerationFailed)
		return
	}
	req, errs := v.toUpdateRequest(h.adminLocation())
	if len(errs) > 0 {
		h.renderMeetupForm(w, r, http.StatusBadRequest, id, v, errs)
		return
	}

	switch err := h.meetups.UpdateOfficial(r.Context(), admin.ID, id, req, h.clientIP(r)); {
	case err == nil:
		h.redirectToMeetup(w, r, id, noticeMeetupUpdated, "")
	case errors.Is(err, service.ErrForbidden):
		h.redirectToMeetup(w, r, id, "", noticeNotOfficial)
	case errors.Is(err, service.ErrNotFound):
		redirectWithNotice(w, r, meetupsPath, "", noticeMeetupNotFound)
	case errors.Is(err, service.ErrInvalidInput):
		h.renderMeetupForm(w, r, http.StatusBadRequest, id, v, rejectedByService)
	default:
		h.log.Error("updating official meetup", slog.Int64("meetup_id", id), slog.Any("error", err))
		h.renderMeetupForm(w, r, http.StatusInternalServerError, id, v,
			map[string]string{"form": "не удалось сохранить изменения"})
	}
}
