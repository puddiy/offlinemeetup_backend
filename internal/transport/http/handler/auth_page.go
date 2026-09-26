package handler

import "net/http"

// ServeTelegramLoginPage
// @Summary      Вход через Telegram
// @Description  Возвращает HTML-страницу с Telegram Login Widget для авторизации пользователя через Telegram
// @Tags         Auth
// @Produce      html
// @Success      200 {string} string "HTML page with Telegram login widget"
// @Router       /v1/auth/telegram/login [get]
func (h *AuthHandler) ServeTelegramLoginPage(w http.ResponseWriter, r *http.Request) {
	html := `
    <!DOCTYPE html>
    <html>
    <head><title>Login</title></head>
    <body>
        <div style="display:flex; justify-content:center; align-items:center; height:100vh;">
            <script async src="https://telegram.org/js/telegram-widget.js?22" 
                data-telegram-login="meetuperbot" 
                data-size="large" 
                data-auth-url="https://api.meetuper.site/v1/auth/telegram/callback" 
                data-request-access="write"></script>
        </div>
    </body>
    </html>`

	w.Header().Set("Content-Type", "text/html")
	_, _ = w.Write([]byte(html))
}
