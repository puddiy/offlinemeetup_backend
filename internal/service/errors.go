package service

import (
	"errors"
)

var (
	ErrNotFound             = errors.New("resource not found")
	ErrAlreadyExists        = errors.New("resource already exists")
	ErrForbidden            = errors.New("action forbidden")
	ErrUnauthorized         = errors.New("unauthorized")
	ErrInvalidInput         = errors.New("invalid input")
	ErrInternal             = errors.New("internal error")
	ErrMeetupFinished       = errors.New("meetup already finished")
	ErrOrganizerCannotLeave = errors.New("organizer cannot leave own meetup")
	ErrChatReadOnly         = errors.New("chat is read-only")
	// ErrTooManyRequests is a throttle the service itself applies (resend
	// cooldown/quota, confirmation-code attempt limit) as opposed to the
	// IP-based RateLimiter middleware. Maps to 429.
	ErrTooManyRequests = errors.New("too many requests")

	// ErrUserAlreadyDeleted — аккаунт уже удалён. Отдельно от ErrNotFound,
	// чтобы поддержка видела «уже удалён» вместо «не найден» на явно
	// существующем id, а мобильный клиент мог показать осмысленный текст
	// на повторный DELETE /v1/account.
	ErrUserAlreadyDeleted = errors.New("user already deleted")
)
