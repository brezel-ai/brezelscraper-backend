# Promo Code Redemption Implementation Plan

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let users redeem shared campaign promo codes (e.g. `REDDIT5`) for free credits — via a manual card on the Credits page and via an auto-apply signup link — stacking on top of the existing $2 signup bonus.

**Architecture:** Two new Postgres tables (`promo_codes`, `promo_redemptions`) plus a widened `credit_transactions.reference_type` CHECK. Redemption reuses the existing immutable `credit_transactions` ledger and clones the proven `grantSignupBonus` transaction recipe (READ COMMITTED + `SELECT … FOR UPDATE` on the user row + `EXISTS` short-circuit + a guarded atomic counter + a unique index). Layering follows the codebase: raw SQL repository (`postgres/promo.go`) → service (`web/services/promo.go`) → handlers (`web/handlers/promo.go`) → routes (`web/web.go`). Redeem is session-auth only (API keys rejected, reusing `Idempotency` + `PerUserRateLimit` middleware); admin management lives on the existing `RequireRole(RoleAdmin)` router. The signup link is captured on the frontend, carried via Clerk `unsafeMetadata`, and redeemed server-side from the Clerk `user.created` webhook handler after provisioning.

**Tech Stack:** Go 1.x, `database/sql` + `pgx` (no ORM), `gorilla/mux`, golang-migrate, `google/uuid` (v7), `golang.org/x/time/rate`; Next.js App Router, TypeScript, Tailwind, SWR, sonner, Clerk.

**Spec:** `docs/superpowers/specs/2026-06-25-promo-code-redemption-design.md` (read it before starting).

**Branch:** `feat/promo-codes` (already created; the spec commit is its first commit).

---

## Conventions used throughout (read once)

- **Run all backend commands from** `brezelscraper-backend/`.
- **Build:** `go build ./...`  **Test:** `go test ./...`  (single package: `go test ./web/services/ -run TestPromo -v`).
- **DB-backed tests** need a Postgres DSN. Before Task 4, run `grep -rn "func.*testing.T" postgres/*_test.go | head` and `grep -rln "sql.Open\|DATABASE_URL\|TEST_DSN\|pgx" postgres/*_test.go` to find the existing integration-test bootstrap (helper like `newTestDB(t)` / a `TEST_DATABASE_URL` env gate) and **mirror it exactly**. If none exists, gate the test with `t.Skip` when the env DSN is unset. Do not invent a new harness.
- **Error/JSON helpers** (declared in package `handlers` — referenced elsewhere via the import alias `webhandlers`; do NOT redefine):
  - `renderJSON(w, code, payload)` — success or error JSON.
  - `models.APIError{Code: <int>, Message: "<str>"}` — error body shape.
  - `internalError(w, log, err, userMsg, ...slog.Attr)` — sanitized 500.
  - `decodeStrict(r, &req)` — strict JSON body decode (returns err → respond 422 `"invalid payload"`).
  - `auth.GetUserID(ctx) (string, error)`, `auth.GetAPIKeyID(ctx) string` (non-empty ⇒ API-key auth), `auth.IsAdmin(ctx) bool`.
- **UUIDs:** generate app-side with `uuid.NewV7()` (matches `grantSignupBonus`).
- Apply the **@golang-database**, **@golang-error-handling**, **@golang-testing**, and **@golang-security** skills while writing Go.
- **Commit after every task** (frequent commits). Use Conventional Commits and end each message with:
  `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`

---

## File Structure

**Backend (`brezelscraper-backend/`)**
- Create `scripts/migrations/000042_promo_codes.up.sql` / `.down.sql` — schema + CHECK widening.
- Create `models/promo.go` — `PromoCode`, `PromoRedemption`, request/response DTOs, sentinel errors.
- Create `postgres/promo.go` — `PromoRepository` (lookup, redemption transaction, admin CRUD).
- Create `postgres/promo_test.go` — concurrency/integration tests.
- Create `web/services/promo.go` — `PromoService` (Redeem + admin ops + per-request config read).
- Create `web/services/promo_test.go` — service unit tests (error mapping).
- Create `web/handlers/promo.go` — `RedeemPromoCode` (on `BillingHandlers`) + admin handlers (on `AdminHandlers`) + `requireSessionUser` helper.
- Create `web/handlers/promo_test.go` — handler tests (auth rejection, status codes).
- Modify `web/web.go` — construct `PromoService`, inject into deps + webhook handler, register routes with middleware.
- Modify `web/handlers/handlers.go` — add `PromoSvc *webservices.PromoService` field to `Dependencies`.
- Modify `web/handlers/clerk_webhook.go` — parse `unsafe_metadata`, redeem after provisioning; hold a `PromoService`.

**Frontend (`brezelscraper-frontend/`)**
- Create `src/components/credits/PromoCodeRedemption.tsx` — the redeem card.
- Modify `src/app/dashboard/credits/CreditsClient.tsx` — mount the card.
- Modify the sign-up route + landing capture for `?promo=` → Clerk `unsafeMetadata`.

---

## Chunk 1: Schema & domain types

### Task 1: Migration `000042_promo_codes`

**Files:**
- Create: `scripts/migrations/000042_promo_codes.up.sql`
- Create: `scripts/migrations/000042_promo_codes.down.sql`

- [ ] **Step 1: Write the up-migration**

`scripts/migrations/000042_promo_codes.up.sql`:
```sql
-- Promo code redemption. Shared campaign codes (one code, many users, each
-- once), bounded by a per-code budget cap and validity window. Money flows
-- through the existing credit_transactions ledger (type='bonus',
-- reference_type='promo'); these tables are the catalog + audit/uniqueness.

CREATE TABLE promo_codes (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    code                TEXT NOT NULL,                 -- stored as entered
    amount              NUMERIC(18,6) NOT NULL CHECK (amount > 0),
    description         TEXT,
    status              TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','disabled')),
    max_redemptions     INTEGER CHECK (max_redemptions IS NULL OR max_redemptions > 0),
    current_redemptions INTEGER NOT NULL DEFAULT 0 CHECK (current_redemptions >= 0),
    new_accounts_only   BOOLEAN NOT NULL DEFAULT false,
    valid_from          TIMESTAMPTZ NOT NULL DEFAULT now(),
    valid_to            TIMESTAMPTZ,
    created_by          TEXT REFERENCES users(id),
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT chk_promo_codes_within_cap
        CHECK (max_redemptions IS NULL OR current_redemptions <= max_redemptions)
);

-- Case-insensitive uniqueness: REDDIT5 == reddit5.
CREATE UNIQUE INDEX uq_promo_codes_code ON promo_codes (upper(code));
CREATE INDEX idx_promo_codes_status ON promo_codes (status) WHERE status = 'active';

CREATE TABLE promo_redemptions (
    id                    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    promo_code_id         UUID NOT NULL REFERENCES promo_codes(id) ON DELETE RESTRICT,
    user_id               TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    amount                NUMERIC(18,6) NOT NULL,
    credit_transaction_id UUID NOT NULL REFERENCES credit_transactions(id),
    source                TEXT NOT NULL CHECK (source IN ('manual','signup_link')),
    redeemed_at           TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- The hard guarantee: one redemption per user per code.
CREATE UNIQUE INDEX uq_promo_redemption_user_code
    ON promo_redemptions (user_id, promo_code_id);
CREATE INDEX idx_promo_redemptions_code ON promo_redemptions (promo_code_id);
CREATE INDEX idx_promo_redemptions_user ON promo_redemptions (user_id, redeemed_at DESC);

-- Widen the ledger reference_type enum so promo grants are legal.
ALTER TABLE credit_transactions DROP CONSTRAINT credit_transactions_reference_type_check;
ALTER TABLE credit_transactions ADD  CONSTRAINT credit_transactions_reference_type_check
    CHECK (reference_type IN ('job','payment','manual','system','billing_event','promo'));
```

- [ ] **Step 2: Write the down-migration**

`scripts/migrations/000042_promo_codes.down.sql` — drops the feature tables (child first) but deliberately leaves `'promo'` in the ledger CHECK, because reverting it would fail (or force destruction) of immutable `'promo'` ledger rows that already credited real balances:
```sql
-- Drop the child (FKs credit_transactions and promo_codes) before the parent.
DROP TABLE IF EXISTS promo_redemptions;
DROP TABLE IF EXISTS promo_codes;

-- Intentionally NOT reverting the reference_type CHECK: 'promo' ledger rows
-- are immutable financial records; re-narrowing the constraint would fail or
-- force their deletion (corrupting the audit chain and overstating
-- users.credit_balance). Leaving the extra enum value is a safe no-op.
```

- [ ] **Step 3: Apply the migration**

The server auto-applies migrations on `-web` startup. Build and run with a local DSN (see backend `CLAUDE.md`):
```bash
go build -o ./tmp/server . && DSN="postgres://scraper:strongpassword@localhost:5432/google_maps_scraper?sslmode=disable" ./tmp/server -web
```
Expected: startup log shows migration `000042` applied, no error. Stop the server (Ctrl-C).

- [ ] **Step 4: Verify schema in psql**

```bash
psql "postgres://scraper:strongpassword@localhost:5432/google_maps_scraper?sslmode=disable" -c "\d promo_codes" -c "\d promo_redemptions" -c "SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conname='credit_transactions_reference_type_check';"
```
Expected: both tables exist with the indexes above; the printed CHECK includes `'promo'`.

- [ ] **Step 5: Commit**

```bash
git add scripts/migrations/000042_promo_codes.up.sql scripts/migrations/000042_promo_codes.down.sql
git commit -m "feat(promo): add promo_codes + promo_redemptions schema (000042)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: Domain types & sentinel errors

**Files:**
- Create: `models/promo.go`

- [ ] **Step 1: Write the types and errors**

`models/promo.go`:
```go
package models

import (
	"errors"
	"time"
)

// PromoCode is a campaign code that grants free credits on redemption.
type PromoCode struct {
	ID                 string     `json:"id"`
	Code               string     `json:"code"`
	Amount             float64    `json:"amount"`
	Description        *string    `json:"description,omitempty"`
	Status             string     `json:"status"` // "active" | "disabled"
	MaxRedemptions     *int       `json:"max_redemptions,omitempty"`
	CurrentRedemptions int        `json:"current_redemptions"`
	NewAccountsOnly    bool       `json:"new_accounts_only"`
	ValidFrom          time.Time  `json:"valid_from"`
	ValidTo            *time.Time `json:"valid_to,omitempty"`
	CreatedBy          *string    `json:"created_by,omitempty"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
}

// RedeemResult is returned to the client after a successful redemption.
// Amounts are strings to avoid float drift on the wire (matches the credit API).
type RedeemResult struct {
	CreditsAdded string `json:"credits_added"`
	NewBalance   string `json:"new_balance"`
}

// CreatePromoCodeRequest is the admin create payload.
type CreatePromoCodeRequest struct {
	Code            string     `json:"code"`
	Amount          float64    `json:"amount"`
	Description     *string    `json:"description"`
	MaxRedemptions  *int       `json:"max_redemptions"`
	NewAccountsOnly bool       `json:"new_accounts_only"`
	ValidFrom       *time.Time `json:"valid_from"`
	ValidTo         *time.Time `json:"valid_to"`
}

// Promo redemption sentinel errors. The service maps these to HTTP statuses.
var (
	ErrPromoNotFound        = errors.New("promo code not found")
	ErrPromoDisabled        = errors.New("promo code disabled")
	ErrPromoNotYetActive    = errors.New("promo code not yet active")
	ErrPromoExpired         = errors.New("promo code expired")
	ErrPromoExhausted       = errors.New("promo code fully redeemed")
	ErrPromoAlreadyRedeemed = errors.New("promo code already redeemed by this user")
	ErrPromoNewAccountsOnly = errors.New("promo code is for new accounts only")
)
```

- [ ] **Step 2: Compile**

Run: `go build ./models/`
Expected: success, no output.

- [ ] **Step 3: Commit**

```bash
git add models/promo.go
git commit -m "feat(promo): domain types and sentinel errors

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Chunk 2: Repository layer (the safety-critical transaction)

### Task 3: `PromoRepository` — lookup, redeem transaction, admin CRUD

**Files:**
- Create: `postgres/promo.go`

- [ ] **Step 1: Write the repository skeleton + constructor**

`postgres/promo.go` (package `postgres`):
```go
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
```

- [ ] **Step 2: Write `GetByCode` (case-insensitive)**

Append:
```go
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
```

- [ ] **Step 3: Write `Redeem` — the transaction (clone of `grantSignupBonus`, FK-correct order)**

Append. **Insert order is load-bearing:** `credit_transactions` (step 7) is inserted before `promo_redemptions` (step 8) because the latter has a non-deferrable FK to the former.
```go
// Redeem grants a promo code's credits to a user inside one transaction.
// source is "manual" or "signup_link". Returns the new balance (as text) on
// success; returns a typed sentinel (models.ErrPromo*) on any rejection.
func (r *PromoRepository) Redeem(ctx context.Context, userID, code, source string) (amount float64, newBalance string, err error) {
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

	// 4. new_accounts_only window (default 7d; the service overrides via ctx value if set).
	if newAccountsOnly {
		windowDays := newAccountWindowDaysFromCtx(ctx) // see service; defaults to 7
		if createdAt.Before(time.Now().Add(-time.Duration(windowDays) * 24 * time.Hour)) {
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
		return 0, "", r.classifyClaimFailure(ctx, tx, codeID) // disabled/not-yet-active/expired/exhausted
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
	case isUniqueViolation(err): // see helper note below
		return 0, "", models.ErrPromoAlreadyRedeemed
	case err != nil:
		return 0, "", fmt.Errorf("promo: insert redemption: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return 0, "", fmt.Errorf("promo: commit: %w", err)
	}
	return grantAmount, fmt.Sprintf("%.6f", balanceAfter), nil
}
```

> **Note on `isUniqueViolation`** — Step 4 (Task 3) tests pin this. If the codebase already has a pgx unique-violation helper, reuse it (`grep -rn "23505\|UniqueViolation\|pgconn.PgError" postgres/`). Otherwise add to `postgres/promo.go`:
> ```go
> func isUniqueViolation(err error) bool {
> 	if err == nil {
> 		return false
> 	}
> 	var pgErr *pgconn.PgError
> 	return errors.As(err, &pgErr) && pgErr.Code == "23505"
> }
> ```
> with import `"github.com/jackc/pgx/v5/pgconn"` (already a dependency). Confirm the driver in use surfaces `*pgconn.PgError` through `database/sql` (it does with the pgx stdlib driver); if the repo uses lib/pq instead, switch to `*pq.Error` and code `"23505"`.

- [ ] **Step 4: Write `classifyClaimFailure` and the ctx-window helper**

Append:
```go
// classifyClaimFailure runs after the guarded claim UPDATE returned zero rows,
// to turn "ineligible" into a specific typed error.
func (r *PromoRepository) classifyClaimFailure(ctx context.Context, tx *sql.Tx, codeID string) error {
	var (
		status   string
		validF   time.Time
		validT   *time.Time
		maxR     *int
		curR     int
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

type newAccountWindowKey struct{}

// WithNewAccountWindow stores the new-account window (days) on ctx so the
// repository can read it without a config dependency. The service sets this.
func WithNewAccountWindow(ctx context.Context, days int) context.Context {
	return context.WithValue(ctx, newAccountWindowKey{}, days)
}

func newAccountWindowDaysFromCtx(ctx context.Context) int {
	if v, ok := ctx.Value(newAccountWindowKey{}).(int); ok && v > 0 {
		return v
	}
	return 7
}
```

- [ ] **Step 5: Write admin CRUD methods**

Append:
```go
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
		return models.PromoCode{}, fmt.Errorf("promo: code already exists")
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
```

- [ ] **Step 6: Compile**

Run: `go build ./postgres/`
Expected: success. (If `pgconn` import is unused because a shared helper already exists, remove it.)

- [ ] **Step 7: Commit**

```bash
git add postgres/promo.go
git commit -m "feat(promo): repository with FK-ordered redemption transaction

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: Repository concurrency & correctness tests

**Files:**
- Create: `postgres/promo_test.go`

> First mirror the existing integration-test bootstrap (see Conventions). The snippets below assume a helper `newTestDB(t) *sql.DB` that returns a connected, migrated DB and `t.Skip`s when no test DSN is set. Adapt names to whatever the repo actually uses.

- [ ] **Step 1: Write the "last slot is claimed exactly once" concurrency test (failing)**

`postgres/promo_test.go`:
```go
package postgres_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/gosom/google-maps-scraper/models"
	"github.com/gosom/google-maps-scraper/postgres"
)

func TestPromoRedeem_LastSlotClaimedOnce(t *testing.T) {
	db := newTestDB(t) // mirrors existing harness; skips if no DSN
	repo := postgres.NewPromoRepository(db, testLogger())
	ctx := context.Background()

	// One code, capacity 1; two distinct users race for it.
	code := "RACE-" + uuid.NewString()[:8]
	mustExec(t, db, `INSERT INTO promo_codes (code, amount, max_redemptions) VALUES ($1, 5.0, 1)`, code)
	u1, u2 := mkUser(t, db), mkUser(t, db)

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, u := range []string{u1, u2} {
		wg.Add(1)
		go func(i int, uid string) {
			defer wg.Done()
			_, _, errs[i] = repo.Redeem(ctx, uid, code, "manual")
		}(i, u)
	}
	wg.Wait()

	okCount, exhaustedCount := 0, 0
	for _, e := range errs {
		switch {
		case e == nil:
			okCount++
		case errors.Is(e, models.ErrPromoExhausted):
			exhaustedCount++
		default:
			t.Fatalf("unexpected error: %v", e)
		}
	}
	if okCount != 1 || exhaustedCount != 1 {
		t.Fatalf("want exactly 1 success + 1 exhausted, got ok=%d exhausted=%d", okCount, exhaustedCount)
	}

	// current_redemptions must equal the cap, not exceed it.
	var cur, max int
	mustQueryRow(t, db, `SELECT current_redemptions, max_redemptions FROM promo_codes WHERE upper(code)=upper($1)`, code).Scan(&cur, &max)
	if cur != 1 || max != 1 {
		t.Fatalf("counter leaked: current=%d max=%d", cur, max)
	}
}
```

- [ ] **Step 2: Run it to confirm the harness works and it passes**

Run: `go test ./postgres/ -run TestPromoRedeem_LastSlotClaimedOnce -v` (with the test DSN exported).
Expected: PASS (the implementation already exists). If it FAILs on counter leak or double-success, the transaction has a concurrency bug — fix `Redeem` before continuing.

- [ ] **Step 3: Add the "same user can't double-redeem concurrently" test**

Append:
```go
func TestPromoRedeem_SameUserConcurrentOnce(t *testing.T) {
	db := newTestDB(t)
	repo := postgres.NewPromoRepository(db, testLogger())
	ctx := context.Background()

	code := "DUP-" + uuid.NewString()[:8]
	mustExec(t, db, `INSERT INTO promo_codes (code, amount) VALUES ($1, 5.0)`, code) // unlimited cap
	uid := mkUser(t, db)

	const N = 8
	var wg sync.WaitGroup
	errs := make([]error, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) { defer wg.Done(); _, _, errs[i] = repo.Redeem(ctx, uid, code, "manual") }(i)
	}
	wg.Wait()

	ok := 0
	for _, e := range errs {
		if e == nil {
			ok++
		} else if !errors.Is(e, models.ErrPromoAlreadyRedeemed) {
			t.Fatalf("unexpected error: %v", e)
		}
	}
	if ok != 1 {
		t.Fatalf("want exactly 1 success, got %d", ok)
	}

	var rows, txns int
	mustQueryRow(t, db, `SELECT count(*) FROM promo_redemptions WHERE user_id=$1`, uid).Scan(&rows)
	mustQueryRow(t, db, `SELECT count(*) FROM credit_transactions WHERE user_id=$1 AND reference_type='promo'`, uid).Scan(&txns)
	if rows != 1 || txns != 1 {
		t.Fatalf("want 1 redemption + 1 ledger row, got redemptions=%d ledger=%d", rows, txns)
	}
}
```

- [ ] **Step 4: Add a table-driven validation test (expired / disabled / not-found / not-yet-active)**

Append a `TestPromoRedeem_Validation` that seeds codes with each bad state and asserts the matching sentinel via `errors.Is`. Cover: nonexistent code → `ErrPromoNotFound`; `status='disabled'` → `ErrPromoDisabled`; `valid_to` in the past → `ErrPromoExpired`; `valid_from` in the future → `ErrPromoNotYetActive`; `new_accounts_only=true` with an old user → `ErrPromoNewAccountsOnly`.

- [ ] **Step 5: Run all promo repo tests**

Run: `go test ./postgres/ -run TestPromo -v`
Expected: all PASS.

- [ ] **Step 6: Commit**

```bash
git add postgres/promo_test.go
git commit -m "test(promo): concurrency + validation tests for redemption tx

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Chunk 3: Service layer

### Task 5: `PromoService`

**Files:**
- Create: `web/services/promo.go`
- Create: `web/services/promo_test.go`

- [ ] **Step 1: Write the service**

`web/services/promo.go` (package `services`):
```go
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

	// Read the new-account window (per-request, cheap due to the 1-min config cache).
	windowDays := defaultNewAccountWindowDays
	if s.cfg != nil {
		if v, err := s.cfg.GetInt(ctx, "promo.new_account_window_days", defaultNewAccountWindowDays); err == nil {
			windowDays = v
		}
	}
	ctx = postgres.WithNewAccountWindow(ctx, windowDays)

	amount, newBalance, err := s.repo.Redeem(ctx, userID, code, source)
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
```

- [ ] **Step 2: Compile**

Run: `go build ./web/services/`
Expected: success.

- [ ] **Step 3: Write a service unit test for empty-code rejection**

`web/services/promo_test.go` — a focused test that `Redeem(ctx, "u", "  ", "manual")` returns `ErrPromoNotFound` without touching the DB (pass a repo built on a `nil` db is unsafe; instead assert the trim/empty guard by checking the error before any DB call — keep this test DB-free by only exercising the empty-string branch). For the DB-backed paths, the repository tests (Task 4) are authoritative; the service is a thin wrapper.

- [ ] **Step 4: Run + commit**

Run: `go test ./web/services/ -run TestPromo -v` → PASS.
```bash
git add web/services/promo.go web/services/promo_test.go
git commit -m "feat(promo): service layer with config-driven new-account window

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Chunk 4: HTTP handlers & routing

### Task 6: Redeem handler (session-only) + admin handlers

**Files:**
- Create: `web/handlers/promo.go`
- Modify: `web/handlers/handlers.go` (add `PromoSvc` to `Dependencies`)

- [ ] **Step 1: Add `PromoSvc` to `Dependencies`**

In `web/handlers/handlers.go`, add to the `Dependencies` struct (near `ConcurrentLimitSvc`):
```go
	PromoSvc *webservices.PromoService // nil-safe; promo routes 503 when nil
```
(The package alias `webservices` is already imported in this file — confirm with the existing `ConcurrentLimitSvc` field type.)

- [ ] **Step 2: Write the error→status mapping + the redeem handler**

`web/handlers/promo.go` (package `webhandlers`):
```go
package handlers // NOTE: match the actual package name in this dir (likely `handlers`)

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
	// Reject API keys — redemption is a human/session action only.
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
```

> Match the real package name (the extracted files show `package handlers` symbols used unqualified like `renderJSON`, `decodeStrict`, `internalError`, `BillingHandlers`, `AdminHandlers`, `requireAdminSession`). Use whatever `web/handlers/billing.go` declares.

- [ ] **Step 3: Write the admin handlers**

Append to `web/handlers/promo.go`:
```go
// CreatePromoCode handles POST /api/v1/admin/promo-codes (admin session only).
func (h *AdminHandlers) CreatePromoCode(w http.ResponseWriter, r *http.Request) {
	adminID, ok := requireAdminSession(w, r) // rejects API keys + non-admins
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
		renderJSON(w, http.StatusBadRequest, models.APIError{Code: http.StatusBadRequest, Message: err.Error()})
		return
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
	Status string `json:"status"` // "active" | "disabled"
}

// UpdatePromoCode handles PATCH /api/v1/admin/promo-codes/{id} (enable/disable).
func (h *AdminHandlers) UpdatePromoCode(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdminSession(w, r); !ok {
		return
	}
	if h.Deps.PromoSvc == nil {
		renderJSON(w, http.StatusServiceUnavailable, models.APIError{Code: http.StatusServiceUnavailable, Message: "promo service not available"})
		return
	}
	id := mux.Vars(r)["id"] // import gorilla/mux; mirror an existing {id} handler
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
	renderJSON(w, http.StatusNoContent, nil)
}
```
(`github.com/gorilla/mux` is already in this file's import block from Step 2.)

- [ ] **Step 4: Compile**

Run: `go build ./web/...`
Expected: success.

- [ ] **Step 5: Commit**

```bash
git add web/handlers/promo.go web/handlers/handlers.go
git commit -m "feat(promo): redeem (session-only) + admin handlers

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 7: Wire routes + handler tests

**Files:**
- Modify: `web/web.go`
- Create: `web/handlers/promo_test.go`

- [ ] **Step 1: Construct `PromoService` and inject into deps**

In `web/web.go`, where `deps` is built (~line 182-210), add — but only when the DB is present:
```go
	if ans.db != nil {
		promoRepo := postgres.NewPromoRepository(ans.db, ans.logger)
		promoCfg := config.New(ans.db) // env -> system_config -> default (config/config.go)
		deps.PromoSvc = webservices.NewPromoService(promoRepo, promoCfg, ans.logger)
	}
```
> Add the import `"github.com/gosom/google-maps-scraper/config"` if missing. A block-scoped `config.New(cfg.PgDB)` already exists at `web.go:144`; you may lift it to a shared variable and reuse it instead of constructing a second instance (both are cheap — they only wrap the `*sql.DB` and a 1-minute cache). Passing `nil` is also valid: `PromoService` then falls back to the 7-day new-account-window default.

- [ ] **Step 2: Register the redeem route with idempotency + per-user rate limit**

In `web/web.go`, right after the jobs routes (the `jobIdempotency` var is already in scope, ~line 325-338), add:
```go
	// Promo redemption: session-only (API keys rejected in-handler), rate-limited
	// to blunt code enumeration, and idempotent (reuses the jobs Idempotency mw,
	// which keys on (user_id, Idempotency-Key) and is opt-in per request).
	if ans.db != nil {
		redeemLimiter := webmiddleware.PerUserRateLimit(rate.Limit(promoRateLimitPerMin()/60.0), 5)
		apiRouter.Handle("/credits/redeem",
			jobIdempotency(redeemLimiter(http.HandlerFunc(hg.Billing.RedeemPromoCode))),
		).Methods(http.MethodPost)
	}
```
Add a small package-level helper (rate is read ONCE at startup; the static limiter cannot change at runtime, so no config-service tier here):
```go
// promoRateLimitPerMin reads the redeem rate limit (requests/min) once at
// startup. Env PROMO_RATE_LIMIT_PER_MIN overrides; default 5.
func promoRateLimitPerMin() float64 {
	if v := strings.TrimSpace(os.Getenv("PROMO_RATE_LIMIT_PER_MIN")); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
			return f
		}
	}
	return 5
}
```
> Place `/credits/redeem` registration OUTSIDE the `if ans.billingSvc != nil` block (it depends only on the DB). Add `os`, `strconv`, `strings` imports if missing.

- [ ] **Step 3: Register the admin promo routes**

In the `adminRouter` block (~line 386-388), add:
```go
	adminRouter.HandleFunc("/promo-codes", hg.Admin.CreatePromoCode).Methods(http.MethodPost)
	adminRouter.HandleFunc("/promo-codes", hg.Admin.ListPromoCodes).Methods(http.MethodGet)
	adminRouter.HandleFunc("/promo-codes/{id}", hg.Admin.UpdatePromoCode).Methods(http.MethodPatch)
```
> **Intentionally deferred to a fast-follow (NOT in this plan):** the spec §9.2 also lists `GET /api/v1/admin/promo-codes/{id}` (detail + recent redemptions) and a `PATCH` that edits `max_redemptions`/`valid_to`. They are omitted from v1 on purpose: create + list + enable/disable is the complete launch workflow, and editing a live cap downward (below `current_redemptions`) has concurrency subtleties better handled separately. The `List` response already exposes `current_redemptions` for campaign monitoring.

- [ ] **Step 4: Compile + run the whole suite**

Run: `go build ./... && go test ./web/... -run 'Promo|Admin' -v`
Expected: build success; existing admin tests still pass.

- [ ] **Step 5: Write handler tests (auth rejection + status mapping)**

`web/handlers/promo_test.go` — mirror `web/handlers/admin_test.go` (table-driven, `httptest`, context-injected identity). Cover at least:
- `RedeemPromoCode` with an API-key identity in context (`auth.APIKeyIDKey` set) → `403`.
- `RedeemPromoCode` unauthenticated → `401`.
- `RedeemPromoCode` malformed body → `422`; empty code → `400`.
- `CreatePromoCode` / `ListPromoCodes` / `UpdatePromoCode` reject non-admin → `403`, and reject admin-via-API-key → `403` (the `admin_test.go` cases already encode this pattern).
- `promoErrorStatus` unit table: each sentinel → expected status.

- [ ] **Step 6: Run + commit**

Run: `go test ./web/handlers/ -run Promo -v` → PASS.
```bash
git add web/web.go web/handlers/promo_test.go
git commit -m "feat(promo): wire redeem + admin routes, handler tests

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Chunk 5: Signup-link redemption

### Task 8: Redeem from the Clerk `user.created` webhook

**Files:**
- Modify: `web/handlers/clerk_webhook.go`
- Modify: `web/web.go` (inject `PromoSvc` into the Clerk webhook handler constructor)

> Design note: we redeem in the webhook handler **after** `Provision` returns (user row + bonus already exist), NOT inside `Provision`. `Provision` is coalesced by `singleflight` keyed on userID; threading the code through it would risk cross-call result sharing. The webhook is the only place the signup-link code exists (in `unsafe_metadata`), so this is the natural seam. Redemption is non-fatal and idempotent (unique index), so a Svix redelivery is safe.

- [ ] **Step 1: Parse `unsafe_metadata` in the payload struct**

In `web/handlers/clerk_webhook.go`, extend `clerkUserCreatedData`:
```go
	// Promo/signup-link code carried via Clerk unsafeMetadata at sign-up.
	UnsafeMetadata struct {
		PromoCode string `json:"promoCode"`
	} `json:"unsafe_metadata"`
```

- [ ] **Step 2: Hold a `PromoService` on the handler**

Add a `promoSvc *webservices.PromoService` field to `ClerkWebhookHandler` and a constructor param (nil-safe). Update `NewClerkWebhookHandler` and its call site in `web/web.go` to pass `deps.PromoSvc` (or the constructed `promoSvc`).

- [ ] **Step 3: Redeem after successful provisioning**

In `handleUserCreated`, after the successful `Provision` block (the `h.logger.Info("clerk_webhook_user_provisioned", …)` line), add:
```go
	if code := strings.TrimSpace(data.UnsafeMetadata.PromoCode); code != "" && h.promoSvc != nil {
		if _, err := h.promoSvc.Redeem(ctx, data.ID, code, "signup_link"); err != nil {
			// Non-fatal: a bad/expired/exhausted code must never fail signup.
			h.logger.Info("clerk_webhook_promo_redeem_skipped",
				slog.String("svix_id", msgID), slog.String("user_id", data.ID),
				slog.String("code", strings.ToUpper(code)), slog.String("reason", err.Error()))
		} else {
			h.logger.Info("clerk_webhook_promo_redeemed",
				slog.String("svix_id", msgID), slog.String("user_id", data.ID),
				slog.String("code", strings.ToUpper(code)))
		}
	}
```
(Add the `strings` import if missing.)

- [ ] **Step 4: Compile**

Run: `go build ./...`
Expected: success.

- [ ] **Step 5: Test — webhook redeems then is idempotent on redelivery**

Extend `web/handlers/clerk_webhook_test.go` (mirror existing webhook tests): post a `user.created` event whose `unsafe_metadata.promoCode` is a seeded code; assert the user gets the credit (one `promo_redemptions` row). Post the **same** event again; assert still exactly one redemption row (idempotent). If the existing webhook tests are not DB-backed, assert instead that `promoSvc.Redeem` is invoked once with `source="signup_link"` via a small fake/spy `PromoService` interface — introduce a minimal interface at the handler boundary if needed to keep this unit-testable.

- [ ] **Step 6: Commit**

```bash
git add web/handlers/clerk_webhook.go web/web.go web/handlers/clerk_webhook_test.go
git commit -m "feat(promo): auto-redeem signup-link code from Clerk webhook

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Chunk 6: Frontend

> Run frontend commands from `brezelscraper-frontend/`. Lint/test: `npm run lint`, `npm run test` (vitest). Follow existing patterns: `useAPI()` for mutations, `useCredits().refresh()` to revalidate balance, `sonner` for toasts, design tokens (`t-card-bg`, `t-card-border`, `t-label`).

### Task 9: Redeem card on the Credits page

**Files:**
- Create: `src/components/credits/PromoCodeRedemption.tsx`
- Modify: `src/app/dashboard/credits/CreditsClient.tsx`

- [ ] **Step 1: Build the component**

`src/components/credits/PromoCodeRedemption.tsx` — input + button, plain `useState`, `useAPI().post('/api/v1/credits/redeem', { code })`, then `useCredits().refresh()` + `toast.success`. On error, read `ApiError.data.message` and show it inline + `toast.error`. Use the structure from the frontend audit (border + `rounded-xl` + `t-card-bg`/`t-card-border`, uppercase code input). Disable the button while loading (this alone prevents double-submit); clear the field on success.

> **Idempotency-Key is optional here.** The server's `UNIQUE (user_id, promo_code_id)` index already makes a double-grant impossible, and the redeem route's idempotency middleware is opt-in (no header ⇒ pass-through). `useAPI().post(endpoint, data)` currently takes no headers arg (`use-api.ts`), so do NOT block on this: rely on the DB guarantee + the disabled-while-pending button. Only if you want belt-and-suspenders dedupe should you extend `apiClient`/`useAPI` to accept a custom header and send a per-attempt key.

- [ ] **Step 2: Mount it in `CreditsClient.tsx`**

Import and place `<PromoCodeRedemption />` immediately after `<BalanceCard … />` (the audit identified this slot). Keep spacing consistent with sibling sections (`mt-5`).

- [ ] **Step 3: Verify in the running app**

Use the @run skill (or `npm run dev`) to load `/dashboard/credits`. With a seeded code (created via the admin API), redeem it and confirm: the balance refreshes, a success toast appears, and re-redeeming shows the "already redeemed" message. Screenshot for the PR.

- [ ] **Step 4: Component test**

`src/components/credits/PromoCodeRedemption.test.tsx` (vitest + testing-library) — mock `useAPI`/`useCredits`: success path calls `refresh` + shows success text; error path renders the server message; button disabled while pending.

- [ ] **Step 5: Commit**

```bash
git add src/components/credits/PromoCodeRedemption.tsx src/app/dashboard/credits/CreditsClient.tsx src/components/credits/PromoCodeRedemption.test.tsx
git commit -m "feat(promo): redeem card on Credits page

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 10: Signup-link capture (`?promo=` → Clerk `unsafeMetadata`)

**Files:**
- Modify: the landing entry + the sign-up route (locate with `grep -rn "SignUp\|signUp\|clerk" src/app | head`).

- [ ] **Step 1: Capture `?promo=` on landing**

On the landing/marketing entry, read `?promo=`, normalize (`trim().toUpperCase()`), and persist to a short-lived cookie or `localStorage` key `promo_code` so it survives navigation to sign-up. Ignore absurd inputs (length cap, alnum/dash only).

- [ ] **Step 2: Attach to Clerk sign-up**

On the sign-up component/flow, pass `unsafeMetadata: { promoCode }` when present (Clerk `<SignUp>` supports `unsafeMetadata`, or set it via the sign-up resource before completion). This is the value the webhook (Task 8) reads. Clear the stored code after sign-up completes.

- [ ] **Step 3: Verify end-to-end (staging-like)**

With a seeded code, open `/?promo=REDDIT5`, complete a fresh sign-up, and confirm the webhook log shows `clerk_webhook_promo_redeemed` and the new account's balance = $2 signup bonus + promo amount. (Document this as a manual QA step in the PR; it requires a working Clerk + webhook tunnel.)

- [ ] **Step 4: Commit**

```bash
git add -A
git commit -m "feat(promo): capture signup-link promo code into Clerk metadata

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Final verification (before PR)

- [ ] `go build ./... && go test ./...` (backend) all green; `npm run lint && npm run test` (frontend) green.
- [ ] Create the launch code via the admin API:
  `POST /api/v1/admin/promo-codes` `{ "code":"REDDIT5","amount":5.0,"description":"Reddit launch","max_redemptions":1000,"valid_to":"<+90d ISO>" }`.
- [ ] Manual matrix: redeem valid (success, balance +5) · redeem again (409 already) · expired code (410) · disabled code (409) · API-key redeem (403) · rapid repeats trip the rate limit (429).
- [ ] Apply @verification-before-completion, then open the PR (base `main`) summarizing the feature, the schema change, and the manual QA for the signup-link path.

---

## Out of scope (do not build — see spec §14)

Admin UI · admin code **detail-view** endpoint and editing `max_redemptions`/`valid_to` on a live code (fast-follow; see Task 7) · percentage/discount codes · user-to-user referral payouts · multi-currency promo amounts · full Sybil/multi-account defense (the per-code budget cap is the v1 bound).
