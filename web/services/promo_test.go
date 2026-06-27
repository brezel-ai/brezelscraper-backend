package services

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/gosom/google-maps-scraper/models"
)

// TestPromoService_Redeem_EmptyCode verifies the whitespace-only / empty-code
// guard returns models.ErrPromoNotFound BEFORE the repository is touched.
//
// The nil repo passed below is safe ONLY because the empty/whitespace code
// short-circuits before s.repo.Redeem is ever called. A nil repo is NOT
// generally safe for this service — do not rely on it for any other path.
func TestPromoService_Redeem_EmptyCode(t *testing.T) {
	t.Parallel()

	discardLogger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := NewPromoService(nil, nil, discardLogger)

	_, err := svc.Redeem(context.Background(), "user_x", "   ", "manual")
	if !errors.Is(err, models.ErrPromoNotFound) {
		t.Fatalf("Redeem(empty code) = %v, want models.ErrPromoNotFound", err)
	}
}
