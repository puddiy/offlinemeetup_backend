package admin

// notice — ключ сообщения о результате действия, который уходит в
// POST-redirect-GET через query (?flash=… / ?err=…).
//
// В URL кладётся КЛЮЧ, а не текст, и текст берётся только из каталогов ниже.
// Раньше в query ехал сам текст, и ссылка на настоящий домен панели вида
// /admin/users/1?flash=«Сессия истекла, позвоните …» рисовала модератору
// поддельное сообщение в «официальной» плашке. XSS там не было — html/template
// экранирует, — но для фишинга хватает и текста.
//
// Неизвестный ключ даёт пустую строку, и плашка не рисуется вовсе.
type notice string

const (
	noticeBanned          notice = "banned"
	noticeUnbanned        notice = "unbanned"
	noticeSessionsRevoked notice = "sessions_revoked"
	noticeDeleted         notice = "deleted"

	noticeStatusFailed   notice = "status_failed"
	noticeRevokeFailed   notice = "revoke_failed"
	noticeAlreadyDeleted notice = "already_deleted"
	noticeDeleteFailed   notice = "delete_failed"
)

// Два каталога, а не один: ключ успеха в ?err= (или ошибки в ?flash=) не
// должен рисоваться в чужой плашке — «Пользователь заблокирован» красным
// читается как сбой.
var (
	flashTexts = map[notice]string{
		noticeBanned:          "Пользователь заблокирован",
		noticeUnbanned:        "Блокировка снята",
		noticeSessionsRevoked: "Сессии отозваны (access-токен живёт ещё до 15 минут)",
		noticeDeleted:         "Аккаунт удалён и анонимизирован",
	}
	errorTexts = map[notice]string{
		noticeStatusFailed:   "Не удалось изменить статус",
		noticeRevokeFailed:   "Не удалось отозвать сессии",
		noticeAlreadyDeleted: "Аккаунт уже был удалён",
		noticeDeleteFailed:   "Не удалось удалить аккаунт",
	}
)
