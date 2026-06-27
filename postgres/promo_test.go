package postgres

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"math"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/gosom/google-maps-scraper/models"
)

// discardLogger returns a *slog.Logger that throws away all output, so the
// repository's rollback/error logging doesn't pollute test output.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// uniqueCode builds a collision-free promo code so concurrent runs (and the
// unique index on upper(code)) never trip over each other.
func uniqueCode(prefix string) string {
	return prefix + "_" + uuid.NewString()[:8]
}

func intPtr(n int) *int { return &n }

// promoSeed describes a promo_codes row to insert. Zero values get sane
// defaults (active status, amount 1.0, valid_from = 1h ago, no expiry).
type promoSeed struct {
	code            string
	amount          float64
	status          string     // default "active"
	maxRedemptions  *int       // nil => unlimited cap (NULL)
	newAccountsOnly bool       // default false
	validFrom       *time.Time // default now()-1h
	validTo         *time.Time // nil => no expiry
}

// seedPromoCode inserts a promo_codes row directly (bypassing repo.Create so
// tests control status / window / cap) and registers FK-ordered cleanup:
// promo_redemptions.promo_code_id is ON DELETE RESTRICT, so the code's
// redemptions must be deleted before the code itself. Returns the code id.
func seedPromoCode(t *testing.T, db *sql.DB, p promoSeed) string {
	t.Helper()
	ctx := context.Background()

	id := uuid.NewString()
	if p.status == "" {
		p.status = "active"
	}
	if p.amount == 0 {
		p.amount = 1.0
	}
	validFrom := time.Now().Add(-time.Hour)
	if p.validFrom != nil {
		validFrom = *p.validFrom
	}

	_, err := db.ExecContext(ctx, `
		INSERT INTO promo_codes
			(id, code, amount, status, max_redemptions, current_redemptions,
			 new_accounts_only, valid_from, valid_to)
		VALUES ($1, $2, $3, $4, $5, 0, $6, $7, $8)`,
		id, p.code, p.amount, p.status, p.maxRedemptions,
		p.newAccountsOnly, validFrom, p.validTo)
	if err != nil {
		t.Fatalf("seed promo code %q: %v", p.code, err)
	}

	t.Cleanup(func() {
		// ON DELETE RESTRICT: clear redemptions before the parent code.
		_, _ = db.ExecContext(ctx, `DELETE FROM promo_redemptions WHERE promo_code_id = $1`, id)
		_, _ = db.ExecContext(ctx, `DELETE FROM promo_codes WHERE id = $1`, id)
	})
	return id
}

// TestPromoRedeem_LastSlotClaimedExactlyOnce is the core race: a code with
// max_redemptions=1 and two DISTINCT users redeeming concurrently. Exactly one
// must win; the other must get ErrPromoExhausted; the counter must never
// exceed the cap.
func TestPromoRedeem_LastSlotClaimedExactlyOnce(t *testing.T) {
	db := openUserTestDB(t)
	ctx := context.Background()
	repo := NewPromoRepository(db, discardLogger())

	code := uniqueCode("LASTSLOT")
	promoID := seedPromoCode(t, db, promoSeed{
		code:           code,
		amount:         1.0,
		maxRedemptions: intPtr(1),
	})

	users := []string{seedTestUser(t, db), seedTestUser(t, db)}

	var wg sync.WaitGroup
	errs := make([]error, len(users))
	start := make(chan struct{}) // released after both goroutines are parked

	for i, uid := range users {
		wg.Add(1)
		go func(i int, uid string) {
			defer wg.Done()
			<-start
			_, _, err := repo.Redeem(ctx, uid, code, "manual", 7)
			errs[i] = err
		}(i, uid)
	}
	close(start)
	wg.Wait()

	var nilCount, exhaustedCount int
	for i, err := range errs {
		switch {
		case err == nil:
			nilCount++
		case errors.Is(err, models.ErrPromoExhausted):
			exhaustedCount++
		default:
			t.Fatalf("user %d: unexpected error: %v", i, err)
		}
	}
	if nilCount != 1 {
		t.Errorf("successful redemptions = %d, want exactly 1", nilCount)
	}
	if exhaustedCount != 1 {
		t.Errorf("ErrPromoExhausted count = %d, want exactly 1", exhaustedCount)
	}

	var cur, max int
	if err := db.QueryRowContext(ctx,
		`SELECT current_redemptions, max_redemptions FROM promo_codes WHERE id = $1`, promoID).
		Scan(&cur, &max); err != nil {
		t.Fatalf("query promo counters: %v", err)
	}
	if cur != 1 {
		t.Errorf("current_redemptions = %d, want 1 (counter must not exceed cap)", cur)
	}
	if max != 1 {
		t.Errorf("max_redemptions = %d, want 1", max)
	}
}

// TestPromoRedeem_SameUserNoDoubleRedeem covers the per-user uniqueness path:
// an unlimited code redeemed concurrently N times by the SAME user must credit
// exactly once. The rest must get ErrPromoAlreadyRedeemed, and there must be
// exactly one redemption row and one promo ledger entry (no double-credit).
func TestPromoRedeem_SameUserNoDoubleRedeem(t *testing.T) {
	db := openUserTestDB(t)
	ctx := context.Background()
	repo := NewPromoRepository(db, discardLogger())

	code := uniqueCode("DOUBLE")
	promoID := seedPromoCode(t, db, promoSeed{
		code:           code,
		amount:         3.0,
		maxRedemptions: nil, // unlimited cap
	})
	userID := seedTestUser(t, db)

	const n = 8
	var wg sync.WaitGroup
	errs := make([]error, n)
	start := make(chan struct{})

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, _, err := repo.Redeem(ctx, userID, code, "manual", 7)
			errs[i] = err
		}(i)
	}
	close(start)
	wg.Wait()

	var nilCount, alreadyCount int
	for i, err := range errs {
		switch {
		case err == nil:
			nilCount++
		case errors.Is(err, models.ErrPromoAlreadyRedeemed):
			alreadyCount++
		default:
			t.Fatalf("goroutine %d: unexpected error: %v", i, err)
		}
	}
	if nilCount != 1 {
		t.Errorf("successful redemptions = %d, want exactly 1", nilCount)
	}
	if alreadyCount != n-1 {
		t.Errorf("ErrPromoAlreadyRedeemed count = %d, want %d", alreadyCount, n-1)
	}

	var redemptionRows int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM promo_redemptions WHERE user_id = $1 AND promo_code_id = $2`,
		userID, promoID).Scan(&redemptionRows); err != nil {
		t.Fatalf("count promo_redemptions: %v", err)
	}
	if redemptionRows != 1 {
		t.Errorf("promo_redemptions rows = %d, want 1 (no double redeem)", redemptionRows)
	}

	var ledgerRows int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM credit_transactions WHERE user_id = $1 AND reference_type = 'promo'`,
		userID).Scan(&ledgerRows); err != nil {
		t.Fatalf("count credit_transactions: %v", err)
	}
	if ledgerRows != 1 {
		t.Errorf("credit_transactions(promo) rows = %d, want 1 (no double credit)", ledgerRows)
	}
}

// TestPromoRedeem_Validation table-drives every rejection sentinel plus the
// happy path. Each case seeds its own user + code so subtests are independent.
func TestPromoRedeem_Validation(t *testing.T) {
	db := openUserTestDB(t)
	ctx := context.Background()
	repo := NewPromoRepository(db, discardLogger())

	const happyAmount = 2.5

	tests := []struct {
		name    string
		setup   func(t *testing.T) (userID, code string)
		wantErr error // nil => happy path
	}{
		{
			name: "nonexistent code",
			setup: func(t *testing.T) (string, string) {
				return seedTestUser(t, db), uniqueCode("MISSING")
			},
			wantErr: models.ErrPromoNotFound,
		},
		{
			name: "disabled",
			setup: func(t *testing.T) (string, string) {
				uid := seedTestUser(t, db)
				code := uniqueCode("DISABLED")
				seedPromoCode(t, db, promoSeed{code: code, status: "disabled"})
				return uid, code
			},
			wantErr: models.ErrPromoDisabled,
		},
		{
			name: "expired (valid_to in the past)",
			setup: func(t *testing.T) (string, string) {
				uid := seedTestUser(t, db)
				code := uniqueCode("EXPIRED")
				from := time.Now().Add(-2 * time.Hour)
				to := time.Now().Add(-1 * time.Hour)
				seedPromoCode(t, db, promoSeed{code: code, validFrom: &from, validTo: &to})
				return uid, code
			},
			wantErr: models.ErrPromoExpired,
		},
		{
			name: "not yet active (valid_from in the future)",
			setup: func(t *testing.T) (string, string) {
				uid := seedTestUser(t, db)
				code := uniqueCode("FUTURE")
				from := time.Now().Add(time.Hour)
				seedPromoCode(t, db, promoSeed{code: code, validFrom: &from})
				return uid, code
			},
			wantErr: models.ErrPromoNotYetActive,
		},
		{
			name: "new accounts only with old user",
			setup: func(t *testing.T) (string, string) {
				uid := seedTestUser(t, db)
				// Age the seeded user well beyond the 7-day window.
				if _, err := db.ExecContext(ctx,
					`UPDATE users SET created_at = NOW() - INTERVAL '30 days' WHERE id = $1`, uid); err != nil {
					t.Fatalf("age user created_at: %v", err)
				}
				code := uniqueCode("NEWONLY")
				seedPromoCode(t, db, promoSeed{code: code, newAccountsOnly: true})
				return uid, code
			},
			wantErr: models.ErrPromoNewAccountsOnly,
		},
		{
			name: "happy path",
			setup: func(t *testing.T) (string, string) {
				uid := seedTestUser(t, db)
				code := uniqueCode("HAPPY")
				seedPromoCode(t, db, promoSeed{code: code, amount: happyAmount})
				return uid, code
			},
			wantErr: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			userID, code := tt.setup(t)

			// Capture prior balance for the happy-path balance assertion.
			var priorBalance float64
			if err := db.QueryRowContext(ctx,
				`SELECT COALESCE(credit_balance, 0) FROM users WHERE id = $1`, userID).
				Scan(&priorBalance); err != nil {
				t.Fatalf("read prior balance: %v", err)
			}

			amount, newBalance, err := repo.Redeem(ctx, userID, code, "manual", 7)

			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("Redeem error = %v, want %v", err, tt.wantErr)
				}
				return
			}

			// Happy path.
			if err != nil {
				t.Fatalf("happy path Redeem failed: %v", err)
			}
			if math.Abs(amount-happyAmount) > 1e-6 {
				t.Errorf("granted amount = %v, want %v", amount, happyAmount)
			}
			gotBalance, perr := strconv.ParseFloat(newBalance, 64)
			if perr != nil {
				t.Fatalf("parse newBalance %q: %v", newBalance, perr)
			}
			want := priorBalance + amount
			if math.Abs(gotBalance-want) > 1e-6 {
				t.Errorf("newBalance = %v, want %v (prior %v + amount %v)",
					gotBalance, want, priorBalance, amount)
			}
		})
	}
}
