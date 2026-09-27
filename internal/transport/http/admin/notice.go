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

	noticeReportDismissed notice = "report_dismissed"
	noticeMeetupCancelled notice = "meetup_cancelled"
	noticeMessageDeleted  notice = "message_deleted"
	noticeAvatarRemoved   notice = "avatar_removed"
	noticeCoverRemoved    notice = "cover_removed"

	noticeStatusFailed   notice = "status_failed"
	noticeRevokeFailed   notice = "revoke_failed"
	noticeAlreadyDeleted notice = "already_deleted"
	noticeDeleteFailed   notice = "delete_failed"

	noticeReportClosed     notice = "report_closed"
	noticeTargetGone       notice = "target_gone"
	noticeWrongAction      notice = "wrong_action"
	noticeModerationFailed notice = "moderation_failed"
)

// Два каталога, а не один: ключ успеха в ?err= (или ошибки в ?flash=) не
// должен рисоваться в чужой плашке — «Пользователь заблокирован» красным
// читается как сбой.
var (
	flashTexts = map[notice]string{
		noticeBanned:          "Пользователь заблокирован, его активные митапы отменены",
		noticeUnbanned:        "Блокировка снята",
		noticeSessionsRevoked: "Сессии отозваны (access-токен живёт ещё до 15 минут)",
		noticeDeleted:         "Аккаунт удалён и анонимизирован",
		noticeReportDismissed: "Жалоба отклонена",
		noticeMeetupCancelled: "Митап отменён, чат переведён в режим чтения",
		noticeMessageDeleted:  "Сообщение удалено у всех участников чата",
		noticeAvatarRemoved:   "Аватар удалён",
		noticeCoverRemoved:    "Обложка удалена",
	}
	errorTexts = map[notice]string{
		noticeStatusFailed:   "Не удалось изменить статус",
		noticeRevokeFailed:   "Не удалось отозвать сессии",
		noticeAlreadyDeleted: "Аккаунт уже был удалён",
		noticeDeleteFailed:   "Не удалось удалить аккаунт",

		noticeReportClosed:     "Жалоба уже закрыта — возможно, её закрыло действие по другой жалобе на тот же контент",
		noticeTargetGone:       "Контента уже нет: его удалил автор или другой модератор. Отклоните жалобу",
		noticeWrongAction:      "Это действие не подходит для жалобы такого типа",
		noticeModerationFailed: "Не удалось выполнить действие",
	}
)
