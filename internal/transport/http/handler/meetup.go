package handler

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/dto"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/service"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/transport/http/middleware"
	"github.com/puddingtonnn/offlinemeetup_backend/internal/transport/http/response"
)

type MeetupHandler struct {
	service *service.MeetupService
	log     *slog.Logger
}

func NewMeetupHandler(service *service.MeetupService, log *slog.Logger) *MeetupHandler {
	return &MeetupHandler{service: service, log: log}
}

// CreateMeetup
// @Summary Создать митап
// @Security BearerAuth
// @Tags 	Meetups
// @Accept	json
// @Produce	json
// @Param input body dto.CreateMeetupRequest true "Данные митапа"
// @Success 201
// @Failure 400 {object} response.ErrorResponse
// @Router /v1/meetups [post]
func (h *MeetupHandler) CreateMeetup(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r, h.log)
	if !ok {
		return
	}

	var req dto.CreateMeetupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.RespondError(w, service.ErrInvalidInput, h.log)
		return
	}

	if errs := req.Validate(); len(errs) > 0 {
		response.RespondValidation(w, errs)
		return
	}

	resp, err := h.service.CreateMeetup(r.Context(), userID, req)
	if err != nil {
		response.RespondError(w, err, h.log)
		return
	}

	response.JSON(w, http.StatusCreated, resp)
}

// GetByID
// @Summary     Получить митап по ID
// @Description Возвращает детальную информацию о митапе.
// @Tags        Meetups
// @Produce     json
// @Param       id   path      int  true  "Meetup ID"
// @Success     200  {object}  dto.MeetupResponse
// @Failure     400  {object}  response.ErrorResponse
// @Failure     404  {object}  response.ErrorResponse
// @Router      /v1/meetups/{id} [get]
func (h *MeetupHandler) GetByID(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt64(w, r, "id", h.log)
	if !ok {
		return
	}

	userID, _ := middleware.GetUserIDFromContext(r.Context())

	resp, err := h.service.GetMeetup(r.Context(), id, userID)
	if err != nil {
		response.RespondError(w, err, h.log)
		return
	}

	response.JSON(w, http.StatusOK, resp)
}

// List
// @Summary     Поиск и список митапов
// @Description Возвращает список митапов. Если переданы lat/lng/radius — ищет ближайшие. Иначе сортирует по времени.
// @Tags        Meetups
// @Produce     json
// @Param       lat     query     number  false  "Широта (Latitude)"
// @Param       lng     query     number  false  "Долгота (Longitude)"
// @Param       radius  query     int     false  "Радиус поиска (в метрах)"
// @Param       limit   query     int     false  "Лимит записей (default: 20)"
// @Param       offset  query     int     false  "Смещение (pagination)"
// @Param 		tags	query	  string  false  "ID тегов через запятую (например: 1,2,5)"
// @Success     200     {array}   dto.MeetupResponse
// @Failure     500     {object}  response.ErrorResponse
// @Router      /v1/meetups [get]
func (h *MeetupHandler) List(w http.ResponseWriter, r *http.Request) {
	userID, _ := middleware.GetUserIDFromContext(r.Context())
	query := r.URL.Query()

	lat, _ := strconv.ParseFloat(query.Get("lat"), 64)
	lng, _ := strconv.ParseFloat(query.Get("lng"), 64)
	radius, _ := strconv.Atoi(query.Get("radius"))
	limit, _ := strconv.Atoi(query.Get("limit"))
	offset, _ := strconv.Atoi(query.Get("offset"))

	if lat != 0 && lng != 0 && radius == 0 {
		radius = 5000 // 5 км
	}

	if limit <= 0 || limit > 100 {
		limit = 20
	}

	tagsStr := query.Get("tags") // "1,3,5"
	var tagIDs []int64

	if tagsStr != "" {
		for p := range strings.SplitSeq(tagsStr, ",") {
			id, err := strconv.ParseInt(strings.TrimSpace(p), 10, 64)
			if err == nil && id > 0 {
				tagIDs = append(tagIDs, id)
			}
		}
	}

	filter := dto.MeetupFilter{
		Lat:    lat,
		Lng:    lng,
		Radius: radius,
		Limit:  limit,
		Offset: offset,
		Tags:   tagIDs,
	}

	if userID != 0 {
		filter.ExcludeOwn = true
	}

	list, err := h.service.ListMeetups(r.Context(), userID, filter)
	if err != nil {
		response.RespondError(w, err, h.log)
		return
	}

	response.JSON(w, http.StatusOK, list)
}

// Update
// @Summary     Обновить митап
// @Description Частичное обновление полей митапа. Требует прав создателя (owner).
// @Security    BearerAuth
// @Tags        Meetups
// @Accept      json
// @Produce     json
// @Param       id     path      int                      true  "Meetup ID"
// @Param       input  body      dto.UpdateMeetupRequest  true  "Поля для обновления (nil поля игнорируются)"
// @Success     200    {object}  dto.MeetupResponse
// @Failure     400    {object}  response.ErrorResponse
// @Failure     403    {object}  response.ErrorResponse
// @Failure     404    {object}  response.ErrorResponse
// @Failure     409    {object}  response.ErrorResponse  "митап отменён — правка запрещена"
// @Router      /v1/meetups/{id} [patch]
func (h *MeetupHandler) Update(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r, h.log)
	if !ok {
		return
	}

	meetupID, ok := pathInt64(w, r, "id", h.log)
	if !ok {
		return
	}

	var req dto.UpdateMeetupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		response.RespondError(w, service.ErrInvalidInput, h.log)
		return
	}

	if errs := req.Validate(); len(errs) > 0 {
		response.RespondValidation(w, errs)
		return
	}

	resp, err := h.service.UpdateMeetup(r.Context(), userID, meetupID, req)
	if err != nil {
		response.RespondError(w, err, h.log)
		return
	}

	response.JSON(w, http.StatusOK, resp)
}

// Delete
// @Summary     Удалить митап
// @Description Удаляет митап (Soft Delete). Требует прав создателя (owner).
// @Security    BearerAuth
// @Tags        Meetups
// @Produce     json
// @Param       id   path      int  true  "Meetup ID"
// @Success     204  {object}  nil  "No Content"
// @Failure     400  {object}  response.ErrorResponse
// @Failure     403  {object}  response.ErrorResponse
// @Failure     404  {object}  response.ErrorResponse
// @Router      /v1/meetups/{id} [delete]
func (h *MeetupHandler) Delete(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r, h.log)
	if !ok {
		return
	}

	meetupID, ok := pathInt64(w, r, "id", h.log)
	if !ok {
		return
	}

	if err := h.service.DeleteMeetup(r.Context(), userID, meetupID); err != nil {
		response.RespondError(w, err, h.log)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Join
// @Summary      Вступить в митап
// @Description  Добавляет текущего пользователя в список участников публичного митапа.
// @Tags         Meetups
// @Security     BearerAuth
// @Produce      json
// @Param        id   path      int  true  "Meetup ID"
// @Success      200  {object}  map[string]string "Сообщение об успехе или пустой JSON"
// @Failure      400  {object}  response.ErrorResponse
// @Failure      401  {object}  response.ErrorResponse
// @Failure      403  {object}  response.ErrorResponse "Митап приватный"
// @Failure      404  {object}  response.ErrorResponse "Митап не найден"
// @Failure      409  {object}  response.ErrorResponse "Уже участник"
// @Failure      500  {object}  response.ErrorResponse
// @Router       /v1/meetups/{id}/join [post]
func (h *MeetupHandler) Join(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r, h.log)
	if !ok {
		return
	}
	meetupID, ok := pathInt64(w, r, "id", h.log)
	if !ok {
		return
	}

	if err := h.service.JoinMeetup(r.Context(), userID, meetupID); err != nil {
		response.RespondError(w, err, h.log)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// JoinByToken
// @Summary      Вступить в приватный митап
// @Description  Вступление по инвайт-ссылке/токену
// @Tags         Meetups
// @Security     BearerAuth
// @Produce      json
// @Param        token   path      string  true  "Invite Token (UUID)"
// @Success      200  {object}  map[string]string
// @Failure      400  {object}  response.ErrorResponse
// @Failure      401  {object}  response.ErrorResponse
// @Failure      404  {object}  response.ErrorResponse
// @Router       /v1/meetups/join/{token} [post]
func (h *MeetupHandler) JoinByToken(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r, h.log)
	if !ok {
		return
	}

	token := chi.URLParam(r, "token")

	if err := h.service.JoinMeetupByToken(r.Context(), userID, token); err != nil {
		response.RespondError(w, err, h.log)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// Leave
// @Summary      Покинуть митап
// @Description  Удаляет текущего пользователя из участников.
// @Tags         Meetups
// @Security     BearerAuth
// @Produce      json
// @Param        id   path      int  true  "Meetup ID"
// @Success      200  {object}  map[string]string
// @Failure      400  {object}  response.ErrorResponse
// @Failure      401  {object}  response.ErrorResponse
// @Failure      404  {object}  response.ErrorResponse
// @Failure      500  {object}  response.ErrorResponse
// @Router       /v1/meetups/{id}/leave [post]
func (h *MeetupHandler) Leave(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r, h.log)
	if !ok {
		return
	}
	meetupID, ok := pathInt64(w, r, "id", h.log)
	if !ok {
		return
	}

	if err := h.service.LeaveMeetup(r.Context(), userID, meetupID); err != nil {
		response.RespondError(w, err, h.log)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// My
// @Summary      Мои митапы
// @Description  Возвращает митапы текущего пользователя. По умолчанию (или filter=joined) — куда записан. Если filter=created — которые организовал.
// @Tags         Meetups
// @Security     BearerAuth
// @Produce      json
// @Param        filter     query     string  false  "Фильтр роли: 'joined' (участник) или 'created' (организатор)" Enums(joined, created)
// @Param       limit   query     int     false  "Лимит записей (default: 20)"
// @Param       offset  query     int     false  "Смещение (pagination)"
// @Param		show_past	query	bool	false	"Показать посещенные митапы (прошедшие)"
// @Success      200  {array}   dto.MeetupResponse
// @Failure      401  {object}  response.ErrorResponse
// @Router       /v1/meetups/my [get]
func (h *MeetupHandler) My(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r, h.log)
	if !ok {
		return
	}
	query := r.URL.Query()

	limit, _ := strconv.Atoi(query.Get("limit"))
	offset, _ := strconv.Atoi(query.Get("offset"))
	var showPast bool
	if query.Get("show_past") == "true" {
		showPast = true
	}

	if limit <= 0 || limit > 100 {
		limit = 20
	}

	filter := dto.MeetupFilter{
		Limit:    limit,
		Offset:   offset,
		ShowPast: showPast,
	}

	if query.Get("filter") == "created" {
		filter.OnlyCreated = true
	} else {
		filter.OnlyMy = true
	}

	list, err := h.service.ListMeetups(r.Context(), userID, filter)
	if err != nil {
		response.RespondError(w, err, h.log)
		return
	}

	response.JSON(w, http.StatusOK, list)
}
