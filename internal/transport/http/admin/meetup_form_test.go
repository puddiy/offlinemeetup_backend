package admin

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/config"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/domain"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/dto"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/service"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/transport/http/middleware"
	"github.com/stretchr/testify/require"
)

func moscow(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Moscow")
	require.NoError(t, err)
	return loc
}

// futureLocal — дата через неделю в формате datetime-local.
func futureLocal(hour int) string {
	d := time.Now().AddDate(0, 0, 7)
	return time.Date(d.Year(), d.Month(), d.Day(), hour, 0, 0, 0, time.UTC).Format(datetimeLocalLayout)
}

func validForm() url.Values {
	return url.Values{
		"title":       {"Официальная встреча"},
		"description": {"Описание"},
		"start_time":  {futureLocal(19)},
		"end_time":    {futureLocal(21)},
		"address":     {"Москва, Тверская, 1"},
		"lat":         {"55.757"},
		"lng":         {"37.615"},
		"is_public":   {"1"},
		"tags":        {"3", "1", "3", "junk"},
	}
}

func formValues(v url.Values) MeetupFormValues {
	req := httptest.NewRequest(http.MethodPost, "/admin/meetups", strings.NewReader(v.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	fv, err := parseMeetupForm(req)
	if err != nil {
		panic(err)
	}
	return fv
}

// Review Focus #3: «19:00» из формы — это 19:00 по Москве, то есть 16:00 UTC.
func TestMeetupFormParsesTimeInAdminTimezone(t *testing.T) {
	loc := moscow(t)
	req, errs := formValues(validForm()).toCreateRequest(loc)

	require.Empty(t, errs)
	require.Equal(t, 16, req.StartTime.UTC().Hour())
	require.Equal(t, 18, req.EndTime.UTC().Hour())
	require.Equal(t, []int64{1, 3}, req.TagIDs, "повторы и мусор отброшены, порядок стабилен")
	require.True(t, req.IsPublic)
	require.InDelta(t, 55.757, req.Coordinates.Lat, 1e-9)
}

func TestMeetupFormFieldErrors(t *testing.T) {
	cases := []struct {
		name  string
		patch func(url.Values)
		field string
	}{
		{"no start", func(v url.Values) { v.Set("start_time", "") }, "start_time"},
		{"garbage end", func(v url.Values) { v.Set("end_time", "завтра") }, "end_time"},
		{"no coordinates", func(v url.Values) { v.Set("lat", "") }, "coordinates"},
		{"NaN latitude", func(v url.Values) { v.Set("lat", "NaN") }, "coordinates"},
		{"Inf longitude", func(v url.Values) { v.Set("lng", "+Inf") }, "coordinates"},
		{"latitude out of range", func(v url.Values) { v.Set("lat", "95") }, "lat"},
		{"short title", func(v url.Values) { v.Set("title", "ab") }, "title"},
		{"end before start", func(v url.Values) { v.Set("end_time", futureLocal(10)) }, "end_time"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := validForm()
			tc.patch(v)
			_, errs := formValues(v).toCreateRequest(moscow(t))
			require.Contains(t, errs, tc.field)
		})
	}
}

// Правка отправляет ВСЕ поля формы. Снятые все галочки тегов — это
// пустой список (снять теги), а не nil (не трогать).
func TestMeetupFormUpdateRequestSetsAllFields(t *testing.T) {
	v := validForm()
	v.Del("tags")
	v.Del("is_public")

	req, errs := formValues(v).toUpdateRequest(moscow(t))

	require.Empty(t, errs)
	require.NotNil(t, req.TagIDs)
	require.Empty(t, *req.TagIDs)
	require.False(t, *req.IsPublic)
	require.Equal(t, "Официальная встреча", *req.Title)
	require.NotNil(t, req.Coordinates)
	require.Nil(t, req.CoverFileID, "обложку форма не трогает")
}

func TestMeetupFormValuesFromDetailRoundTrip(t *testing.T) {
	loc := moscow(t)
	start := time.Date(2030, 1, 2, 16, 0, 0, 0, time.UTC)
	d := &dto.AdminMeetupDetail{
		AdminMeetupRow: dto.AdminMeetupRow{Title: "T", IsPublic: true, StartTime: start, EndTime: start.Add(time.Hour)},
		Lat:            55.5, Lng: 37.25, TagIDs: []int64{2},
	}

	v := formValuesFromDetail(d, loc)

	require.Equal(t, "2030-01-02T19:00", v.Start)
	require.Equal(t, "55.5", v.Lat)
	require.True(t, v.TagIDs[2])
}

// ---- хендлеры ----

func newFormHandler(t *testing.T, meetups *stubMeetupSvc, tags *stubTagSvc) *Handler {
	t.Helper()
	r, err := NewRenderer(slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	return NewHandler(Deps{Auth: &stubAuth{}, Meetups: meetups, Tags: tags, Audit: &recordingAudit{}, Render: r,
		Cfg: &config.Config{Env: "local", AdminTimezone: moscow(t)}, Log: slog.New(slog.DiscardHandler)})
}

var formAdmin = &domain.AdminUser{ID: 3, Email: "root@x.io", Role: domain.AdminRoleAdmin}

func formPost(target, id string, v url.Values) *http.Request {
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(v.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	ctx := context.WithValue(req.Context(), middleware.AdminKey, formAdmin)
	if id != "" {
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", id)
		ctx = context.WithValue(ctx, chi.RouteCtxKey, rctx)
	}
	return req.WithContext(ctx)
}

func TestMeetupCreateInvalidRerendersWithValues(t *testing.T) {
	meetups := &stubMeetupSvc{}
	h := newFormHandler(t, meetups, &stubTagSvc{rows: []dto.AdminTagRow{{ID: 1, Name: "Спорт"}}})
	v := validForm()
	v.Set("title", "ab")

	rec := httptest.NewRecorder()
	h.MeetupCreate(rec, formPost("/admin/meetups", "", v))

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Zero(t, meetups.createCall, "невалидная форма не доходит до сервиса")
	require.Contains(t, rec.Body.String(), `value="Москва, Тверская, 1"`, "введённое не теряется")
}

func TestMeetupCreateSuccessRedirectsToCard(t *testing.T) {
	meetups := &stubMeetupSvc{createdID: 42}
	h := newFormHandler(t, meetups, &stubTagSvc{})

	rec := httptest.NewRecorder()
	h.MeetupCreate(rec, formPost("/admin/meetups", "", validForm()))

	require.Equal(t, http.StatusSeeOther, rec.Code)
	require.Equal(t, "/admin/meetups/42?flash=meetup_created", rec.Header().Get("Location"))
	require.Equal(t, int64(3), meetups.gotActor)
}

// Сервис отверг теги (скрытый тег, выбранный в устаревшей вкладке):
// форма возвращается с сообщением, а не 500.
func TestMeetupCreateRejectedByServiceRerenders(t *testing.T) {
	meetups := &stubMeetupSvc{createErr: service.ErrInvalidInput}
	h := newFormHandler(t, meetups, &stubTagSvc{})

	rec := httptest.NewRecorder()
	h.MeetupCreate(rec, formPost("/admin/meetups", "", validForm()))

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "Проверьте теги")
}

func TestMeetupUpdateForeignMeetupRedirects(t *testing.T) {
	meetups := &stubMeetupSvc{updateErr: service.ErrForbidden}
	h := newFormHandler(t, meetups, &stubTagSvc{})

	rec := httptest.NewRecorder()
	h.MeetupUpdate(rec, formPost("/admin/meetups/40/edit", "40", validForm()))

	require.Equal(t, http.StatusSeeOther, rec.Code)
	require.Equal(t, "/admin/meetups/40?err=not_official", rec.Header().Get("Location"))
}

func TestMeetupEditForeignMeetupRedirects(t *testing.T) {
	meetups := &stubMeetupSvc{detail: &dto.AdminMeetupDetail{AdminMeetupRow: dto.AdminMeetupRow{ID: 40, IsOfficial: false, Status: "active"}}}
	h := newFormHandler(t, meetups, &stubTagSvc{})

	req := formPost("/admin/meetups/40/edit", "40", nil)
	req.Method = http.MethodGet
	rec := httptest.NewRecorder()
	h.MeetupEdit(rec, req)

	require.Equal(t, "/admin/meetups/40?err=not_official", rec.Header().Get("Location"))
}

// В форме правки скрытый тег, уже стоящий на митапе, остаётся видимым и
// отмеченным; скрытый и не стоящий — не предлагается вовсе.
func TestMeetupEditTagOptions(t *testing.T) {
	meetups := &stubMeetupSvc{detail: &dto.AdminMeetupDetail{
		AdminMeetupRow: dto.AdminMeetupRow{ID: 41, IsOfficial: true, Status: "active", StartTime: time.Now(), EndTime: time.Now()},
		TagIDs:         []int64{2},
	}}
	tags := &stubTagSvc{rows: []dto.AdminTagRow{
		{ID: 1, Name: "Спорт"},
		{ID: 2, Name: "Старое", IsHidden: true},
		{ID: 3, Name: "Архив", IsHidden: true},
	}}
	h := newFormHandler(t, meetups, tags)

	req := formPost("/admin/meetups/41/edit", "41", nil)
	req.Method = http.MethodGet
	rec := httptest.NewRecorder()
	h.MeetupEdit(rec, req)

	body := rec.Body.String()
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, body, "Спорт")
	require.Contains(t, body, "Старое (скрыт)")
	require.NotContains(t, body, "Архив")
}
