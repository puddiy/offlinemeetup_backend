package admin

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/puddingtonnn/offlinemeetup_backend/internal/config"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/dto"
	"github.com/stretchr/testify/require"
)

type stubGeo struct {
	got   string
	items []dto.AddressSuggestion
	err   error
}

func (s *stubGeo) SuggestAddress(_ context.Context, q string) ([]dto.AddressSuggestion, error) {
	s.got = q
	return s.items, s.err
}

func newGeoHandler(t *testing.T, geo *stubGeo) *Handler {
	t.Helper()
	r, err := NewRenderer(slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	return NewHandler(Deps{Auth: &stubAuth{}, Geo: geo, Audit: &recordingAudit{}, Render: r,
		Cfg: &config.Config{Env: "local"}, Log: slog.New(slog.DiscardHandler)})
}

func TestGeoSuggestRendersPickLinks(t *testing.T) {
	geo := &stubGeo{items: []dto.AddressSuggestion{
		{Value: "г Москва, ул Тверская, д 1", Lat: 55.757, Lon: 37.615},
		{Value: "без координат", Lat: 0, Lon: 0},
	}}
	h := newGeoHandler(t, geo)

	rec := httptest.NewRecorder()
	h.GeoSuggest(rec, httptest.NewRequest(http.MethodGet, "/admin/geo/suggest?address=Tverskaya+1", nil))

	body := rec.Body.String()
	require.Equal(t, "Tverskaya 1", geo.got)
	require.Contains(t, body, "/admin/geo/pick?")
	require.Contains(t, body, "lat=55.757")
	require.NotContains(t, body, "без координат", "подсказка без координат бесполезна для митапа")
}

func TestGeoSuggestShortQueryDoesNotCallDaData(t *testing.T) {
	geo := &stubGeo{}
	h := newGeoHandler(t, geo)

	rec := httptest.NewRecorder()
	h.GeoSuggest(rec, httptest.NewRequest(http.MethodGet, "/admin/geo/suggest?address=ab", nil))

	require.Empty(t, geo.got)
	require.Contains(t, rec.Body.String(), "хотя бы 3 символа")
}

func TestGeoSuggestFailureFallsBackToManual(t *testing.T) {
	h := newGeoHandler(t, &stubGeo{err: errors.New("dadata down")})

	rec := httptest.NewRecorder()
	h.GeoSuggest(rec, httptest.NewRequest(http.MethodGet, "/admin/geo/suggest?address=Tverskaya", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "введите координаты вручную")
}

func TestGeoPickFillsFieldsEscaped(t *testing.T) {
	h := newGeoHandler(t, &stubGeo{})

	rec := httptest.NewRecorder()
	h.GeoPick(rec, httptest.NewRequest(http.MethodGet,
		`/admin/geo/pick?address=%22%3E%3Cscript%3Ex%3C%2Fscript%3E&lat=55.7&lng=37.6`, nil))

	body := rec.Body.String()
	require.Contains(t, body, `name="lat" value="55.7"`)
	require.NotContains(t, body, "<script>x</script>")
}
