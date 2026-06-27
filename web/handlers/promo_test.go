package handlers

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" sql driver (never connected here)

	"github.com/gosom/google-maps-scraper/models"
	"github.com/gosom/google-maps-scraper/postgres"
	"github.com/gosom/google-maps-scraper/web/auth"
	webservices "github.com/gosom/google-maps-scraper/web/services"
)

// discardLogger returns a logger that swallows all output for test noise.
func discardLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

// redeemDeps builds Dependencies whose DB and PromoSvc are NON-nil but are
// never actually queried: every guard path exercised below returns before
// PromoService.Redeem runs. The DSN is parsed lazily by the pgx stdlib driver;
// sql.Open does not dial, so the "invalid" host is irrelevant.
func redeemDeps(t *testing.T) Dependencies {
	t.Helper()
	db, err := sql.Open("pgx", "postgres://invalid")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	log := discardLogger()
	return Dependencies{
		DB:       db,
		Logger:   log,
		PromoSvc: webservices.NewPromoService(postgres.NewPromoRepository(db, log), nil, log),
	}
}

// ---------------------------------------------------------------------------
// RedeemPromoCode guard tests (DB-free: Redeem is never reached)
// ---------------------------------------------------------------------------

func TestRedeemPromoCode_Guards(t *testing.T) {
	h := &BillingHandlers{Deps: redeemDeps(t)}

	tests := []struct {
		name       string
		body       string
		userID     string
		apiKeyID   string
		wantStatus int
	}{
		// API-key identity present → rejected before any DB access. The fact
		// that this returns 403 (not a panic / 500) proves the handler bailed
		// out before touching the never-connected DB.
		{"api key identity rejected", `{"code":"WELCOME10"}`, "user_1", "key-uuid", http.StatusForbidden},
		// Authenticated session, malformed JSON body → 422.
		{"malformed body", `{"code":`, "user_1", "", http.StatusUnprocessableEntity},
		// Authenticated session, empty code → 400.
		{"empty code", `{"code":""}`, "user_1", "", http.StatusBadRequest},
		// No userID and no API key → unauthenticated → 401.
		{"unauthenticated", `{"code":"WELCOME10"}`, "", "", http.StatusUnauthorized},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/credits/redeem", bytes.NewReader([]byte(tt.body)))

			ctx := req.Context()
			if tt.userID != "" {
				ctx = context.WithValue(ctx, auth.UserIDKey, tt.userID)
			}
			if tt.apiKeyID != "" {
				ctx = context.WithValue(ctx, auth.APIKeyIDKey, tt.apiKeyID)
			}
			req = req.WithContext(ctx)

			rr := httptest.NewRecorder()
			h.RedeemPromoCode(rr, req)

			if rr.Code != tt.wantStatus {
				t.Fatalf("expected %d, got %d: %s", tt.wantStatus, rr.Code, rr.Body.String())
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Admin promo handler rejection tests (mirror admin_test.go) — these return in
// requireAdminSession before PromoSvc is touched, so an empty Dependencies is
// sufficient and no DB is needed.
// ---------------------------------------------------------------------------

func TestAdminPromoHandlers_Reject(t *testing.T) {
	h := &AdminHandlers{Deps: Dependencies{}}

	body := []byte(`{"code":"X","amount":100}`)

	tests := []struct {
		name       string
		handler    func(http.ResponseWriter, *http.Request)
		method     string
		path       string
		body       []byte
		userID     string
		role       string
		apiKeyID   string
		wantStatus int
	}{
		// Non-admin role → 403.
		{"CreatePromoCode rejects non-admin", h.CreatePromoCode, http.MethodPost, "/api/v1/admin/promo-codes", body, "user_1", models.RoleUser, "", http.StatusForbidden},
		{"ListPromoCodes rejects non-admin", h.ListPromoCodes, http.MethodGet, "/api/v1/admin/promo-codes", nil, "user_1", models.RoleUser, "", http.StatusForbidden},
		{"UpdatePromoCode rejects non-admin", h.UpdatePromoCode, http.MethodPatch, "/api/v1/admin/promo-codes/id", body, "user_1", models.RoleUser, "", http.StatusForbidden},

		// Admin role but authenticated via API key → 403.
		{"CreatePromoCode rejects API key", h.CreatePromoCode, http.MethodPost, "/api/v1/admin/promo-codes", body, "user_2", models.RoleAdmin, "key-uuid", http.StatusForbidden},
		{"ListPromoCodes rejects API key", h.ListPromoCodes, http.MethodGet, "/api/v1/admin/promo-codes", nil, "user_2", models.RoleAdmin, "key-uuid", http.StatusForbidden},
		{"UpdatePromoCode rejects API key", h.UpdatePromoCode, http.MethodPatch, "/api/v1/admin/promo-codes/id", body, "user_2", models.RoleAdmin, "key-uuid", http.StatusForbidden},

		// Missing auth (admin role asserted but no user ID) → 401.
		{"CreatePromoCode rejects no auth", h.CreatePromoCode, http.MethodPost, "/api/v1/admin/promo-codes", body, "", models.RoleAdmin, "", http.StatusUnauthorized},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var bodyReader io.Reader
			if tt.body != nil {
				bodyReader = bytes.NewReader(tt.body)
			}
			req := httptest.NewRequest(tt.method, tt.path, bodyReader)

			ctx := req.Context()
			if tt.userID != "" {
				ctx = context.WithValue(ctx, auth.UserIDKey, tt.userID)
			}
			ctx = context.WithValue(ctx, auth.UserRoleKey, tt.role)
			if tt.apiKeyID != "" {
				ctx = context.WithValue(ctx, auth.APIKeyIDKey, tt.apiKeyID)
			}
			req = req.WithContext(ctx)

			rr := httptest.NewRecorder()
			tt.handler(rr, req)

			if rr.Code != tt.wantStatus {
				t.Errorf("expected %d, got %d: %s", tt.wantStatus, rr.Code, rr.Body.String())
			}
		})
	}
}

// ---------------------------------------------------------------------------
// promoErrorStatus pure unit table
// ---------------------------------------------------------------------------

func TestPromoErrorStatus(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"not found", models.ErrPromoNotFound, http.StatusNotFound},
		{"expired", models.ErrPromoExpired, http.StatusGone},
		{"not yet active", models.ErrPromoNotYetActive, http.StatusConflict},
		{"exhausted", models.ErrPromoExhausted, http.StatusConflict},
		{"already redeemed", models.ErrPromoAlreadyRedeemed, http.StatusConflict},
		{"disabled", models.ErrPromoDisabled, http.StatusConflict},
		{"new accounts only", models.ErrPromoNewAccountsOnly, http.StatusUnprocessableEntity},
		{"non-sentinel", errors.New("boom"), http.StatusInternalServerError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, msg := promoErrorStatus(tt.err)
			if got != tt.want {
				t.Errorf("promoErrorStatus(%v) status = %d, want %d", tt.err, got, tt.want)
			}
			if msg == "" {
				t.Errorf("promoErrorStatus(%v) returned empty message", tt.err)
			}
		})
	}
}
