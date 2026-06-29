package handlers

import (
	"net/http"

	"github.com/gosom/google-maps-scraper/models"
	"github.com/gosom/google-maps-scraper/web/auth"
)

// GetMe returns the authenticated caller's own identity (id, DB-authoritative
// role, tier), read from the request context set by the auth middleware. No DB
// read; no userId param (returns only the caller — never another user).
func (h *APIHandlers) GetMe(w http.ResponseWriter, r *http.Request) {
	if h.Deps.Auth == nil {
		renderJSON(w, http.StatusUnauthorized, models.APIError{Code: http.StatusUnauthorized, Message: "Authentication not configured"})
		return
	}
	userID, err := auth.GetUserID(r.Context())
	if err != nil || userID == "" {
		renderJSON(w, http.StatusUnauthorized, models.APIError{Code: http.StatusUnauthorized, Message: "User not authenticated"})
		return
	}
	renderJSON(w, http.StatusOK, models.MeResponse{
		ID:   userID,
		Role: auth.GetUserRole(r.Context()),
		Tier: auth.GetUserTier(r.Context()),
	})
}
