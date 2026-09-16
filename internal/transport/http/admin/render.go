package admin

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"

	"github.com/puddingtonnn/offlinemeetup_backend/internal/domain"
)

//go:embed templates/*.gohtml
var templatesFS embed.FS

//go:embed static
var staticFS embed.FS

// pages — страницы админки. Каждая парсится в ОТДЕЛЬНЫЙ template.Template
// вместе с layout: страницы переопределяют один и тот же блок "content", и
// в общем наборе последняя разобранная затёрла бы все предыдущие.
var pages = []string{"login", "dashboard"}

// PageData — общая форма данных для любой страницы. Admin пустой на странице
// входа (layout по нему решает, рисовать ли шапку).
type PageData struct {
	Title string
	Admin *domain.AdminUser
	Error string
	Flash string
	Data  any
}

type Renderer struct {
	tpl map[string]*template.Template
	log *slog.Logger
}

// NewRenderer разбирает все шаблоны один раз, на старте приложения.
// Ошибка шаблона обязана валить процесс при запуске, а не отдавать 500
// первому зашедшему модератору.
func NewRenderer(log *slog.Logger) (*Renderer, error) {
	tpl := make(map[string]*template.Template, len(pages))

	for _, page := range pages {
		t, err := template.New(page).ParseFS(templatesFS,
			"templates/layout.gohtml",
			"templates/"+page+".gohtml",
		)
		if err != nil {
			return nil, fmt.Errorf("parse admin template %q: %w", page, err)
		}
		tpl[page] = t
	}

	return &Renderer{tpl: tpl, log: log}, nil
}

// Render отрисовывает страницу.
//
// Рендер идёт В БУФЕР, и только удачный результат уходит в ResponseWriter:
// иначе ошибка на середине шаблона оставила бы клиенту половину страницы
// под кодом 200, который уже нельзя отозвать.
func (r *Renderer) Render(w http.ResponseWriter, status int, name string, data any) {
	t, ok := r.tpl[name]
	if !ok {
		r.log.Error("unknown admin template", slog.String("name", name))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, name, data); err != nil {
		r.log.Error("rendering admin template",
			slog.String("name", name), slog.Any("error", err))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if _, err := buf.WriteTo(w); err != nil {
		r.log.Error("writing admin response", slog.Any("error", err))
	}
}

// StaticFS отдаёт встроенную статику админки (HTMX).
func StaticFS() http.FileSystem {
	return http.FS(staticFS)
}
