package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/gosom/google-maps-scraper/models"
)

// PromoRepository owns promo_codes / promo_redemptions and the redemption
// transaction. It uses a raw *sql.DB (no ORM), mirroring the credit paths.
type PromoRepository struct {
	db     *sql.DB
	logger *slog.Logger
}

func NewPromoRepository(db *sql.DB, logger *slog.Logger) *PromoRepository {
	return &PromoRepository{db: db, logger: logger}
}

// GetByCode looks up a promo code case-insensitively. Returns
// models.ErrPromoNotFound when absent.
func (r *PromoRepository) GetByCode(ctx context.Context, code string) (models.PromoCode, error) {
	const q = `
		SELECT id, code, amount, description, status, max_redemptions,
		       current_redemptions, new_accounts_only, valid_from, valid_to,
		       created_by, created_at, updated_at
		FROM promo_codes WHERE upper(code) = upper($1)`
	var p models.PromoCode
	err := r.db.QueryRowContext(ctx, q, strings.TrimSpace(code)).Scan(
		&p.ID, &p.Code, &p.Amount, &p.Description, &p.Status, &p.MaxRedemptions,
		&p.CurrentRedemptions, &p.NewAccountsOnly, &p.ValidFrom, &p.ValidTo,
		&p.CreatedBy, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return models.PromoCode{}, models.ErrPromoNotFound
	}
	if err != nil {
		return models.PromoCode{}, fmt.Errorf("promo: get by code: %w", err)
	}
	return p, nil
}

// Redeem grants a promo code's credits to a user inside one transaction.
// source is "manual" or "signup_link". Returns the new balance (as text) on
// success; returns a typed sentinel (models.ErrPromo*) on any rejection.
func (r *PromoRepository) Redeem(ctx context.Context, userID, code, source string, newAccountWindowDays int) (amount float64, newBalance string, err error) {
	tx, err := r.db.BeginTx(ctx, nil) // READ COMMITTED; row locks + unique index give correctness
	if err != nil {
		return 0, "", fmt.Errorf("promo: begin tx: %w", err)
	}
	defer func() {
		if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
			r.logger.Error("promo_rollback_failed", slog.Any("error", rbErr))
		}
	}()

	// 1. Look up the code.
	var (
		codeID          string
		newAccountsOnly bool
	)
	switch err := tx.QueryRowContext(ctx,
		`SELECT id, new_accounts_only FROM promo_codes WHERE upper(code) = upper($1)`,
		strings.TrimSpace(code)).Scan(&codeID, &newAccountsOnly); {
	case errors.Is(err, sql.ErrNoRows):
		return 0, "", models.ErrPromoNotFound
	case err != nil:
		return 0, "", fmt.Errorf("promo: lookup: %w", err)
	}

	// 2. Lock the user row (lock order: users -> credit_transactions, matching
	//    the bonus/billing paths to avoid deadlock). Also read created_at for
	//    the new_accounts_only check.
	var (
		currentBalance float64
		createdAt      time.Time
	)
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(credit_balance, 0), created_at FROM users WHERE id = $1 FOR UPDATE`,
		userID).Scan(&currentBalance, &createdAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, "", fmt.Errorf("promo: user %q not found", userID)
		}
		return 0, "", fmt.Errorf("promo: lock user: %w", err)
	}

	// 3. Already-redeemed short-circuit (the unique index is the hard backstop).
	var already bool
	if err := tx.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM promo_redemptions WHERE user_id = $1 AND promo_code_id = $2)`,
		userID, codeID).Scan(&already); err != nil {
		return 0, "", fmt.Errorf("promo: check redeemed: %w", err)
	}
	if already {
		return 0, "", models.ErrPromoAlreadyRedeemed
	}

	// 4. new_accounts_only window (the service resolves the window from config and
	//    passes it explicitly; a 0/negative falls back to a sane 7d default).
	if newAccountsOnly {
		window := newAccountWindowDays
		if window <= 0 {
			window = 7
		}
		if createdAt.Before(time.Now().Add(-time.Duration(window) * 24 * time.Hour)) {
			return 0, "", models.ErrPromoNewAccountsOnly
		}
	}

	// 5. Atomically claim a slot: validates active/within-window/under-cap AND reserves.
	var grantAmount float64
	switch err := tx.QueryRowContext(ctx, `
		UPDATE promo_codes
		   SET current_redemptions = current_redemptions + 1, updated_at = now()
		 WHERE id = $1
		   AND status = 'active'
		   AND now() >= valid_from
		   AND (valid_to IS NULL OR now() < valid_to)
		   AND (max_redemptions IS NULL OR current_redemptions < max_redemptions)
		RETURNING amount`, codeID).Scan(&grantAmount); {
	case errors.Is(err, sql.ErrNoRows):
		return 0, "", r.classifyClaimFailure(ctx, tx, codeID)
	case err != nil:
		return 0, "", fmt.Errorf("promo: claim slot: %w", err)
	}

	// 6. Credit the balance.
	var balanceAfter float64
	if err := tx.QueryRowContext(ctx, `
		UPDATE users
		   SET credit_balance = credit_balance + $1::numeric, updated_at = now()
		 WHERE id = $2
		RETURNING credit_balance`, grantAmount, userID).Scan(&balanceAfter); err != nil {
		return 0, "", fmt.Errorf("promo: credit balance: %w", err)
	}

	// 7. Write the ledger row FIRST (promo_redemptions FKs it). App-side UUID.
	txID, err := uuid.NewV7()
	if err != nil {
		return 0, "", fmt.Errorf("promo: gen tx id: %w", err)
	}
	desc := "Promo code " + strings.ToUpper(strings.TrimSpace(code))
	refID := strings.ToUpper(strings.TrimSpace(code))
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO credit_transactions
			(id, user_id, type, amount, balance_before, balance_after, description, reference_id, reference_type, metadata)
		VALUES ($1, $2, 'bonus', $3, $4, $5, $6, $7, 'promo', jsonb_build_object('promo_code_id', $8::text, 'source', $9::text))`,
		txID.String(), userID, grantAmount, currentBalance, balanceAfter, desc, refID, codeID, source); err != nil {
		return 0, "", fmt.Errorf("promo: insert ledger: %w", err)
	}

	// 8. Insert the redemption. Unique (user_id, promo_code_id) is the backstop:
	//    a concurrent winner makes this fail -> already redeemed -> full rollback.
	redID, err := uuid.NewV7()
	if err != nil {
		return 0, "", fmt.Errorf("promo: gen redemption id: %w", err)
	}
	switch _, err := tx.ExecContext(ctx, `
		INSERT INTO promo_redemptions (id, promo_code_id, user_id, amount, credit_transaction_id, source)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		redID.String(), codeID, userID, grantAmount, txID.String(), source); {
	case isUniqueViolation(err):
		return 0, "", models.ErrPromoAlreadyRedeemed
	case err != nil:
		return 0, "", fmt.Errorf("promo: insert redemption: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return 0, "", fmt.Errorf("promo: commit: %w", err)
	}
	return grantAmount, fmt.Sprintf("%.6f", balanceAfter), nil
}

func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// classifyClaimFailure runs after the guarded claim UPDATE returned zero rows,
// to turn "ineligible" into a specific typed error.
func (r *PromoRepository) classifyClaimFailure(ctx context.Context, tx *sql.Tx, codeID string) error {
	var (
		status string
		validF time.Time
		validT *time.Time
		maxR   *int
		curR   int
	)
	if err := tx.QueryRowContext(ctx,
		`SELECT status, valid_from, valid_to, max_redemptions, current_redemptions
		   FROM promo_codes WHERE id = $1`, codeID).
		Scan(&status, &validF, &validT, &maxR, &curR); err != nil {
		return fmt.Errorf("promo: classify: %w", err)
	}
	now := time.Now()
	switch {
	case status != "active":
		return models.ErrPromoDisabled
	case now.Before(validF):
		return models.ErrPromoNotYetActive
	case validT != nil && !now.Before(*validT):
		return models.ErrPromoExpired
	case maxR != nil && curR >= *maxR:
		return models.ErrPromoExhausted
	default:
		return models.ErrPromoExhausted // safe default (e.g. lost race for the last slot)
	}
}

// Create inserts a new promo code (admin). created_by is the admin's user id.
func (r *PromoRepository) Create(ctx context.Context, req models.CreatePromoCodeRequest, createdBy string) (models.PromoCode, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return models.PromoCode{}, fmt.Errorf("promo: gen id: %w", err)
	}
	const q = `
		INSERT INTO promo_codes
			(id, code, amount, description, max_redemptions, new_accounts_only, valid_from, valid_to, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, COALESCE($7, now()), $8, $9)
		RETURNING id, code, amount, description, status, max_redemptions,
		          current_redemptions, new_accounts_only, valid_from, valid_to,
		          created_by, created_at, updated_at`
	var p models.PromoCode
	err = r.db.QueryRowContext(ctx, q,
		id.String(), strings.TrimSpace(req.Code), req.Amount, req.Description,
		req.MaxRedemptions, req.NewAccountsOnly, req.ValidFrom, req.ValidTo, createdBy).
		Scan(&p.ID, &p.Code, &p.Amount, &p.Description, &p.Status, &p.MaxRedemptions,
			&p.CurrentRedemptions, &p.NewAccountsOnly, &p.ValidFrom, &p.ValidTo,
			&p.CreatedBy, &p.CreatedAt, &p.UpdatedAt)
	if isUniqueViolation(err) {
		return models.PromoCode{}, models.ErrPromoCodeExists
	}
	if err != nil {
		return models.PromoCode{}, fmt.Errorf("promo: create: %w", err)
	}
	return p, nil
}

// List returns all promo codes, newest first (admin).
func (r *PromoRepository) List(ctx context.Context) ([]models.PromoCode, error) {
	const q = `
		SELECT id, code, amount, description, status, max_redemptions,
		       current_redemptions, new_accounts_only, valid_from, valid_to,
		       created_by, created_at, updated_at
		FROM promo_codes ORDER BY created_at DESC`
	rows, err := r.db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("promo: list: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []models.PromoCode
	for rows.Next() {
		var p models.PromoCode
		if err := rows.Scan(&p.ID, &p.Code, &p.Amount, &p.Description, &p.Status,
			&p.MaxRedemptions, &p.CurrentRedemptions, &p.NewAccountsOnly,
			&p.ValidFrom, &p.ValidTo, &p.CreatedBy, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, fmt.Errorf("promo: scan: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// SetStatus enables/disables a code (admin). status must be 'active' or 'disabled'.
func (r *PromoRepository) SetStatus(ctx context.Context, id, status string) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE promo_codes SET status = $1, updated_at = now() WHERE id = $2`, status, id)
	if err != nil {
		return fmt.Errorf("promo: set status: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return models.ErrPromoNotFound
	}
	return nil
}
