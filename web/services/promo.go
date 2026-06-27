package services

import (
	"context"
	"log/slog"
	"strconv"
	"strings"

	"github.com/gosom/google-maps-scraper/config"
	"github.com/gosom/google-maps-scraper/models"
	"github.com/gosom/google-maps-scraper/postgres"
)

// PromoService orchestrates promo redemption + admin management.
type PromoService struct {
	repo   *postgres.PromoRepository
	cfg    *config.Service // nil-safe; falls back to defaults
	logger *slog.Logger
}

func NewPromoService(repo *postgres.PromoRepository, cfg *config.Service, logger *slog.Logger) *PromoService {
	return &PromoService{repo: repo, cfg: cfg, logger: logger}
}

const defaultNewAccountWindowDays = 7

// Redeem validates and applies a promo code for a user. source is
// "manual" or "signup_link". Returns a RedeemResult or a typed models.ErrPromo*.
func (s *PromoService) Redeem(ctx context.Context, userID, code, source string) (models.RedeemResult, error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return models.RedeemResult{}, models.ErrPromoNotFound
	}

	// Resolve the new-account window (per-request; cheap due to the 1-min config cache).
	windowDays := defaultNewAccountWindowDays
	if s.cfg != nil {
		if v, err := s.cfg.GetInt(ctx, "promo.new_account_window_days", defaultNewAccountWindowDays); err == nil {
			windowDays = v
		}
	}

	amount, newBalance, err := s.repo.Redeem(ctx, userID, code, source, windowDays)
	if err != nil {
		// Sentinels are expected business outcomes — log at info, not error.
		s.logger.Info("promo_redeem_rejected",
			slog.String("user_id", userID), slog.String("code", strings.ToUpper(code)),
			slog.String("source", source), slog.String("reason", err.Error()))
		return models.RedeemResult{}, err
	}
	s.logger.Info("promo_redeemed",
		slog.String("user_id", userID), slog.String("code", strings.ToUpper(code)),
		slog.String("source", source), slog.Float64("amount", amount))
	return models.RedeemResult{
		CreditsAdded: strconv.FormatFloat(amount, 'f', 6, 64),
		NewBalance:   newBalance,
	}, nil
}

// Admin passthroughs.
func (s *PromoService) Create(ctx context.Context, req models.CreatePromoCodeRequest, adminID string) (models.PromoCode, error) {
	return s.repo.Create(ctx, req, adminID)
}
func (s *PromoService) List(ctx context.Context) ([]models.PromoCode, error) { return s.repo.List(ctx) }
func (s *PromoService) SetStatus(ctx context.Context, id, status string) error {
	return s.repo.SetStatus(ctx, id, status)
}
