package handlers

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/gorilla/mux"
	"github.com/gosom/google-maps-scraper/models"
	"github.com/gosom/google-maps-scraper/web/auth"
)

// promoErrorStatus maps redemption sentinels to HTTP status codes.
func promoErrorStatus(err error) (int, string) {
	switch {
	case errors.Is(err, models.ErrPromoNotFound):
		return http.StatusNotFound, "Promo code not found"
	case errors.Is(err, models.ErrPromoExpired):
		return http.StatusGone, "This promo code has expired"
	case errors.Is(err, models.ErrPromoNotYetActive):
		return http.StatusConflict, "This promo code is not active yet"
	case errors.Is(err, models.ErrPromoExhausted):
		return http.StatusConflict, "This promo code has been fully redeemed"
	case errors.Is(err, models.ErrPromoAlreadyRedeemed):
		return http.StatusConflict, "You have already redeemed this code"
	case errors.Is(err, models.ErrPromoDisabled):
		return http.StatusConflict, "This promo code is no longer active"
	case errors.Is(err, models.ErrPromoNewAccountsOnly):
		return http.StatusUnprocessableEntity, "This promo code is only for new accounts"
	default:
		return http.StatusInternalServerError, "Failed to redeem promo code"
	}
}

type redeemPromoRequest struct {
	Code string `json:"code"`
}

// RedeemPromoCode handles POST /api/v1/credits/redeem. Session-auth only:
// API-key identities are rejected (defense against scripted code-farming).
func (h *BillingHandlers) RedeemPromoCode(w http.ResponseWriter, r *http.Request) {
	if h.Deps.PromoSvc == nil || h.Deps.DB == nil {
		renderJSON(w, http.StatusServiceUnavailable, models.APIError{Code: http.StatusServiceUnavailable, Message: "promo redemption not available"})
		return
	}
	if auth.GetAPIKeyID(r.Context()) != "" {
		renderJSON(w, http.StatusForbidden, models.APIError{Code: http.StatusForbidden, Message: "promo redemption requires session authentication, not API keys"})
		return
	}
	userID, err := auth.GetUserID(r.Context())
	if err != nil || userID == "" {
		renderJSON(w, http.StatusUnauthorized, models.APIError{Code: http.StatusUnauthorized, Message: "User not authenticated"})
		return
	}
	var req redeemPromoRequest
	if err := decodeStrict(r, &req); err != nil {
		renderJSON(w, http.StatusUnprocessableEntity, models.APIError{Code: http.StatusUnprocessableEntity, Message: "invalid payload"})
		return
	}
	if strings.TrimSpace(req.Code) == "" {
		renderJSON(w, http.StatusBadRequest, models.APIError{Code: http.StatusBadRequest, Message: "code is required"})
		return
	}

	result, err := h.Deps.PromoSvc.Redeem(r.Context(), userID, req.Code, "manual")
	if err != nil {
		status, msg := promoErrorStatus(err)
		if status == http.StatusInternalServerError {
			internalError(w, h.Deps.Logger, err, msg, slog.String("user_id", userID), slog.String("path", r.URL.Path))
			return
		}
		renderJSON(w, status, models.APIError{Code: status, Message: msg})
		return
	}
	renderJSON(w, http.StatusOK, result)
}

// CreatePromoCode handles POST /api/v1/admin/promo-codes (admin session only).
func (h *AdminHandlers) CreatePromoCode(w http.ResponseWriter, r *http.Request) {
	adminID, ok := requireAdminSession(w, r)
	if !ok {
		return
	}
	if h.Deps.PromoSvc == nil {
		renderJSON(w, http.StatusServiceUnavailable, models.APIError{Code: http.StatusServiceUnavailable, Message: "promo service not available"})
		return
	}
	var req models.CreatePromoCodeRequest
	if err := decodeStrict(r, &req); err != nil {
		renderJSON(w, http.StatusUnprocessableEntity, models.APIError{Code: http.StatusUnprocessableEntity, Message: "invalid payload"})
		return
	}
	if strings.TrimSpace(req.Code) == "" || req.Amount <= 0 {
		renderJSON(w, http.StatusBadRequest, models.APIError{Code: http.StatusBadRequest, Message: "code and positive amount are required"})
		return
	}
	code, err := h.Deps.PromoSvc.Create(r.Context(), req, adminID)
	if err != nil {
		if errors.Is(err, models.ErrPromoCodeExists) {
			renderJSON(w, http.StatusConflict, models.APIError{Code: http.StatusConflict, Message: "a promo code with that code already exists"})
			return
		}
		internalError(w, h.Deps.Logger, err, "Failed to create promo code", slog.String("admin_id", adminID))
		return
	}
	// Admin actions are logged at Warn level (not Info) so they stand out in
	// log aggregation dashboards and can be filtered for audit review.
	if h.Deps.Logger != nil {
		h.Deps.Logger.Warn("admin_promo_code_created",
			slog.String("admin_id", adminID),
			slog.String("code", code.Code),
			slog.Float64("amount", code.Amount))
	}
	renderJSON(w, http.StatusCreated, code)
}

// ListPromoCodes handles GET /api/v1/admin/promo-codes (admin session only).
func (h *AdminHandlers) ListPromoCodes(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdminSession(w, r); !ok {
		return
	}
	if h.Deps.PromoSvc == nil {
		renderJSON(w, http.StatusServiceUnavailable, models.APIError{Code: http.StatusServiceUnavailable, Message: "promo service not available"})
		return
	}
	codes, err := h.Deps.PromoSvc.List(r.Context())
	if err != nil {
		internalError(w, h.Deps.Logger, err, "Failed to list promo codes")
		return
	}
	renderJSON(w, http.StatusOK, codes)
}

type setPromoStatusRequest struct {
	Status string `json:"status"`
}

// UpdatePromoCode handles PATCH /api/v1/admin/promo-codes/{id} (enable/disable).
func (h *AdminHandlers) UpdatePromoCode(w http.ResponseWriter, r *http.Request) {
	adminID, ok := requireAdminSession(w, r)
	if !ok {
		return
	}
	if h.Deps.PromoSvc == nil {
		renderJSON(w, http.StatusServiceUnavailable, models.APIError{Code: http.StatusServiceUnavailable, Message: "promo service not available"})
		return
	}
	id := mux.Vars(r)["id"]
	var req setPromoStatusRequest
	if err := decodeStrict(r, &req); err != nil {
		renderJSON(w, http.StatusUnprocessableEntity, models.APIError{Code: http.StatusUnprocessableEntity, Message: "invalid payload"})
		return
	}
	if req.Status != "active" && req.Status != "disabled" {
		renderJSON(w, http.StatusBadRequest, models.APIError{Code: http.StatusBadRequest, Message: "status must be 'active' or 'disabled'"})
		return
	}
	if err := h.Deps.PromoSvc.SetStatus(r.Context(), id, req.Status); err != nil {
		if errors.Is(err, models.ErrPromoNotFound) {
			renderJSON(w, http.StatusNotFound, models.APIError{Code: http.StatusNotFound, Message: "promo code not found"})
			return
		}
		internalError(w, h.Deps.Logger, err, "Failed to update promo code")
		return
	}
	if h.Deps.Logger != nil {
		h.Deps.Logger.Warn("admin_promo_code_status_changed",
			slog.String("admin_id", adminID),
			slog.String("promo_code_id", id),
			slog.String("status", req.Status))
	}
	renderJSON(w, http.StatusNoContent, nil)
}
