package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gosom/google-maps-scraper/models"
	"github.com/gosom/google-maps-scraper/web/auth"
)

// newMeHandler builds an APIHandlers with auth configured (non-nil empty
// AuthMiddleware passes the "auth enabled" guard in GetMe).
func newMeHandler() *APIHandlers {
	return &APIHandlers{Deps: Dependencies{Auth: &auth.AuthMiddleware{}}}
}

// meContext layers the user identity keys the auth middleware would set onto a
// request context. Empty values are skipped so callers can model partial state
// (e.g. only a userID set).
func meContext(role, tier, apiKeyID string) context.Context {
	ctx := context.Background()
	if role != "" {
		ctx = context.WithValue(ctx, auth.UserRoleKey, role)
	}
	if tier != "" {
		ctx = context.WithValue(ctx, auth.UserTierKey, tier)
	}
	if apiKeyID != "" {
		ctx = context.WithValue(ctx, auth.APIKeyIDKey, apiKeyID)
	}
	return ctx
}

func TestGetMe_AdminPaidIdentity(t *testing.T) {
	ctx := context.WithValue(meContext(models.RoleAdmin, models.UserTierPaid, ""), auth.UserIDKey, "user-admin")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil).WithContext(ctx)
	w := httptest.NewRecorder()

	newMeHandler().GetMe(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", w.Code, w.Body.String())
	}

	var got models.MeResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("failed to unmarshal body: %v", err)
	}
	want := models.MeResponse{ID: "user-admin", Role: models.RoleAdmin, Tier: models.UserTierPaid}
	if got != want {
		t.Errorf("body mismatch: got %+v, want %+v", got, want)
	}
}

func TestGetMe_Unauthenticated(t *testing.T) {
	// No UserIDKey in context.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	w := httptest.NewRecorder()

	newMeHandler().GetMe(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized, got %d: %s", w.Code, w.Body.String())
	}

	var apiErr models.APIError
	if err := json.Unmarshal(w.Body.Bytes(), &apiErr); err != nil {
		t.Fatalf("failed to unmarshal body: %v", err)
	}
	if apiErr.Code != http.StatusUnauthorized {
		t.Errorf("expected error code 401, got %d", apiErr.Code)
	}
}

func TestGetMe_OnlyUserIDDefaults(t *testing.T) {
	// Only the userID is set; role/tier absent -> safe defaults.
	ctx := context.WithValue(context.Background(), auth.UserIDKey, "user-plain")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil).WithContext(ctx)
	w := httptest.NewRecorder()

	newMeHandler().GetMe(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", w.Code, w.Body.String())
	}

	var got models.MeResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("failed to unmarshal body: %v", err)
	}
	want := models.MeResponse{ID: "user-plain", Role: models.RoleUser, Tier: models.UserTierFree}
	if got != want {
		t.Errorf("body mismatch: got %+v, want %+v", got, want)
	}
}

func TestGetMe_APIKeyCallerStillAllowed(t *testing.T) {
	// /me is intentionally NOT session-only: an API-key caller (APIKeyIDKey set)
	// must still get a 200 with its identity.
	ctx := context.WithValue(meContext(models.RoleUser, models.UserTierFree, "api-key-123"), auth.UserIDKey, "user-apikey")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil).WithContext(ctx)
	w := httptest.NewRecorder()

	newMeHandler().GetMe(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for API-key caller, got %d: %s", w.Code, w.Body.String())
	}

	var got models.MeResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("failed to unmarshal body: %v", err)
	}
	want := models.MeResponse{ID: "user-apikey", Role: models.RoleUser, Tier: models.UserTierFree}
	if got != want {
		t.Errorf("body mismatch: got %+v, want %+v", got, want)
	}
}
