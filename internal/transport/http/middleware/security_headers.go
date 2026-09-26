package middleware

import "net/http"

// SecurityHeaders ставит консервативные защитные заголовки на каждый ответ.
// API обслуживает нативные мобильные клиенты плюс пару HTML-поверхностей
// (страница Telegram-логина, Swagger в dev), поэтому эти заголовки ничего не
// стоят и закрывают браузерные пути: nosniff запрещает MIME-sniffing, DENY —
// фрейминг (кликджекинг), no-referrer не утекает URL, no-store не даёт
// прокси/браузеру кешировать ответы, специфичные для пользователя.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// SameOriginReferrer ослабляет Referrer-Policy до same-origin для HTML-панели,
// поверх глобального no-referrer из SecurityHeaders.
//
// Без этого админка в браузере не работает вовсе: по спецификации Fetch POST
// со страницы с политикой no-referrer уходит с заголовком `Origin: null` —
// даже на свой же сайт. RequireSameOrigin справедливо отбивает null (его же
// шлют sandboxed iframe и data:-страницы, то есть ровно атакующие), и каждая
// форма — вход, выход, бан, удаление — получает 403. same-origin отдаёт
// настоящий Origin своему сайту и по-прежнему ничего — чужим.
//
// Ставится ПОСЛЕ SecurityHeaders: более поздний Set перетирает значение.
func SameOriginReferrer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Referrer-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}
