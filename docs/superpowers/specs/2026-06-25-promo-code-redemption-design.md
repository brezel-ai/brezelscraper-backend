# Promo Code Redemption — Design Spec

- **Date:** 2026-06-25
- **Status:** Draft (pending review)
- **Author:** Engineering
- **Scope:** `brezelscraper-backend` (Go REST API) + `brezelscraper-frontend` (Next.js)

## 1. Summary

Add a production-ready **promo code** system that grants free credits. Two redemption
entry points, one redemption engine:

1. **Manual** — a "Redeem code" card on the Credits page where any signed-in user types a
   code (e.g. `REDDIT5`) and receives credits.
2. **Signup link** — a referral-style URL (`https://brezel.ai/?promo=REDDIT5`) that carries
   the code through Clerk signup and auto-grants on account creation.

Codes are **shared campaign codes**: one code is redeemable by many users, each user once,
bounded by a per-code budget cap and validity window. Promo credits **stack on top of** the
existing $2 automatic signup bonus.

Admins create and manage codes via authenticated REST endpoints (no admin UI in v1).

## 2. Goals / Non-goals

**Goals**
- Issue free credit via shared, expiring, budget-capped codes.
- Redeem safely under concurrency, retries, and adversarial input.
- Reuse the existing credit ledger as the single source of truth for balances.
- Fit the existing architecture (handler → service → repository, raw SQL/pgx, golang-migrate).

**Non-goals (v1)** — see §14.
- Admin dashboard UI, percentage/discount-on-purchase codes, user-to-user referral
  attribution/payouts, multi-currency promo amounts, full Sybil defense.

## 3. Context: existing infrastructure (audit)

The credit system is mature; there is **no** pre-existing promo/referral/coupon code. Key
facts the design builds on:

- **Balance & ledger.** `users.credit_balance NUMERIC(18,6)` is the spendable balance.
  `credit_transactions` is an immutable append-only ledger; every balance change writes a row
  with the DB-enforced invariant `balance_after = balance_before + amount`
  (`scripts/migrations/000012_add_credit_system.up.sql`).
- **The signup bonus is the proven template.** `web/services/user_provisioning.go`
  (`const SignupBonusAmount = 2.0`, `grantSignupBonus`) grants inside a transaction using
  `SELECT … FOR UPDATE` on the user row + an idempotency guard, and is backed by the partial
  unique index `idx_unique_signup_bonus` (migration `000022`). The redemption transaction
  clones this recipe.
- **Migrations.** golang-migrate, numbered SQL in `scripts/migrations/`, latest `000041`,
  auto-applied on web startup. New migration will be `000042`.
- **No ORM.** Raw SQL via `pgx/v5`; models in `models/`, repositories in `postgres/`.

### 3.1 What's new / easy to miss (drove the security model)

- **Dual auth on every `/api/v1/*` route.** `web/auth/auth.go` accepts a Clerk JWT, an
  `X-API-Key`/`bscraper_` API key, **or** the `__session` cookie. Existing `/credits/*`
  routes (`web.go:364-370`) inherit this, so a naively-mounted redeem endpoint would be
  callable headlessly with an API key → scriptable code-farming. **The redeem endpoint must
  reject API keys** (session-only).
- **MCP server** (`cmd/mcp-server/`, `mcpauth/`) exposes tools via API-key + OAuth and
  **consumes credits** (`mcp_tool_call_audit.credits_consumed`, migration `000041`). Promo
  credits are spendable via MCP — acceptable (one balance) — but reinforces that redemption
  itself must be a human/session action, never an MCP tool.
- **Idempotency middleware exists.** `webmiddleware.Idempotency` + `idempotency_keys` table
  (migration `000034`), Stripe-style two-phase, `Idempotency-Key` header. Wired only to
  `POST /api/v1/jobs` today (`web.go:325-327`). **Reuse it on redeem.**
- **Rate-limit middleware exists.** `PerUserRateLimit`, `PerIPRateLimit`, `PerAPIKeyRateLimit`
  (`web.go:271-374`). **Wrap redeem** to stop code enumeration.
- **Roles/admin infra exists.** `users.role IN ('user','admin')` (migration `000028`),
  `auth.IsAdmin`/`GetUserRole` (`auth/role.go`), `RequireRole` middleware
  (`middleware/middleware.go:54-70`), and an isolated `adminRouter` whose handlers also reject
  API keys as defense-in-depth (`web.go:379-388`). Admin promo endpoints slot directly in.
- **`reference_type` CHECK rejects `'promo'`.** `credit_transactions.reference_type` is
  constrained to `('job','payment','manual','system','billing_event')` (`000012:42`). The new
  migration **must add `'promo'`** or every ledger insert fails.

## 4. Decisions

| # | Decision | Choice |
|---|---|---|
| D1 | Entry points (v1) | Both: manual redeem card **and** auto-apply signup link |
| D2 | Code model | Shared campaign codes (unique code = `max_redemptions = 1`) |
| D3 | Interaction with $2 signup bonus | **Stack** — two independent ledger entries |
| D4 | Code management (v1) | Admin REST API only (no seed migration, no admin UI) |
| D5 | Redeem auth | **Session-only**; API keys rejected; not an MCP tool |
| D6 | Per-user uniqueness | One redemption per (user, code), DB-enforced |
| D7 | Expiry | Per-code `valid_to` (default 90 days), enforced atomically |

## 5. Architecture

```
              ┌─────────────── Manual path ───────────────┐
 Credits page ─▶ POST /api/v1/credits/redeem {code}        │
   (Clerk session, API key rejected, rate-limited,         │
    Idempotency-Key)                                       │
                                                           ▼
 Signup link ─▶ ?promo=CODE ─▶ Clerk unsafeMetadata ─▶ user.created webhook
              ─▶ UserProvisioning.Provision()                │
                 ─▶ grantSignupBonus()  (existing, $2)       │
                 ─▶ PromoService.Redeem(userID, code, "signup_link")  (non-fatal)
                                                           ▼
            PromoService.Redeem(ctx, userID, code, source)
                                                           ▼
      ┌──────────────── one DB transaction (READ COMMITTED) ───────────────┐
      │ 1. look up code by upper(code)                                     │
      │ 2. SELECT … FOR UPDATE on users row                                │
      │ 3. EXISTS: already redeemed? → early reject                        │
      │ 4. new_accounts_only check (users.created_at) if set               │
      │ 5. claim slot on promo_codes (guarded atomic UPDATE, cap-checked)  │
      │ 6. UPDATE users.credit_balance += amount                           │
      │ 7. INSERT credit_transactions (type='bonus', reference_type='promo')│
      │ 8. INSERT promo_redemptions  (FK→ledger; UNIQUE (user_id,code))    │
      └────────────────────────────────────────────────────────────────────┘
```

One service method serves both entry points; the only difference is `source`
(`manual` | `signup_link`) and the caller.

New code:
- `models/promo.go` — `PromoCode`, `PromoRedemption`, typed sentinel errors.
- `postgres/promo.go` — repository (lookup, redemption tx, admin CRUD).
- `web/services/promo.go` — `PromoService.Redeem(...)` + admin operations.
- `web/handlers/promo.go` — user redeem handler + admin handlers.
- `scripts/migrations/000042_promo_codes.{up,down}.sql`.
- Frontend: `src/components/credits/PromoCodeRedemption.tsx` + signup-link capture.

## 6. Data model (migration `000042_promo_codes`)

### 6.1 `promo_codes` — campaign catalog

```sql
CREATE TABLE promo_codes (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    code                TEXT NOT NULL,                 -- stored as entered
    amount              NUMERIC(18,6) NOT NULL CHECK (amount > 0),
    description         TEXT,
    status              TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','disabled')),
    max_redemptions     INTEGER CHECK (max_redemptions IS NULL OR max_redemptions > 0), -- NULL = unlimited
    current_redemptions INTEGER NOT NULL DEFAULT 0 CHECK (current_redemptions >= 0),
    new_accounts_only   BOOLEAN NOT NULL DEFAULT false,
    valid_from          TIMESTAMPTZ NOT NULL DEFAULT now(),
    valid_to            TIMESTAMPTZ,                   -- NULL = no expiry
    created_by          TEXT REFERENCES users(id),
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT chk_promo_codes_within_cap
        CHECK (max_redemptions IS NULL OR current_redemptions <= max_redemptions)
);

-- Case-insensitive uniqueness: REDDIT5 == reddit5.
CREATE UNIQUE INDEX uq_promo_codes_code ON promo_codes (upper(code));
CREATE INDEX idx_promo_codes_status ON promo_codes (status) WHERE status = 'active';
```

### 6.2 `promo_redemptions` — audit trail + per-user uniqueness

```sql
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
```

### 6.3 Ledger constraint change (required)

```sql
ALTER TABLE credit_transactions DROP CONSTRAINT credit_transactions_reference_type_check;
ALTER TABLE credit_transactions ADD  CONSTRAINT credit_transactions_reference_type_check
    CHECK (reference_type IN ('job','payment','manual','system','billing_event','promo'));
```

Promo grants write `type='bonus'` (already allowed), `reference_type='promo'`,
`reference_id=<upper(code)>`, `metadata = {promo_code_id, source}`.

**Down-migration policy:** the down migration drops `promo_redemptions` then `promo_codes`, but
does **not** revert this CHECK. Re-narrowing it would hard-fail — or force deletion of — any
`'promo'` ledger rows that have already credited real balances, corrupting the immutable audit
chain and leaving `users.credit_balance` overstated. Adding an enum value to a financial-ledger
CHECK is treated as one-way; `'promo'` stays permanently. See §17.

### 6.4 Launch code creation (no DDL/DML mix)

The schema migration does **not** seed promo codes — that would bake a marketing decision into
version control and mix DDL with DML. The launch code is created after deploy via the admin API
(§9.2), e.g. `POST /api/v1/admin/promo-codes` with `{code: "REDDIT5", amount: 5.0,
description: "Reddit launch", max_redemptions: 1000, valid_to: "<+90d>"}`. This also exercises
the admin path end-to-end before any public link is published.

## 7. Redemption transaction (safety-critical core)

Implemented in `postgres/promo.go`, called by `PromoService.Redeem`. Single transaction,
`READ COMMITTED` (matching `grantSignupBonus`; correctness comes from row locks + the guarded
counter + the unique index, not from isolation level).

**FK ordering (critical):** `promo_redemptions.credit_transaction_id` is `NOT NULL REFERENCES
credit_transactions(id)` and the FK is **not** deferrable, so Postgres checks it per-statement.
The `credit_transactions` row must therefore be inserted **before** the `promo_redemptions`
row. The ledger row id is generated app-side so both statements agree.

1. **Look up the code** — `SELECT id, amount, status, valid_from, valid_to, max_redemptions,
   current_redemptions, new_accounts_only FROM promo_codes WHERE upper(code) = upper($1)`.
   No row → `ErrPromoNotFound`.
2. **Lock the user row** — `SELECT credit_balance, created_at FROM users WHERE id = $1 FOR UPDATE`.
3. **Already-redeemed short-circuit** — `SELECT EXISTS(SELECT 1 FROM promo_redemptions
   WHERE user_id = $1 AND promo_code_id = $2)`; true → `ErrPromoAlreadyRedeemed`. Mirrors the
   `grantSignupBonus` EXISTS guard and avoids increment-then-rollback churn on repeat attempts.
4. **`new_accounts_only`** — if set, verify `created_at >= now() - interval '<window>'`
   (window from config, default 7 days). Fail → `ErrPromoNewAccountsOnly`.
5. **Claim a slot** — atomic guarded update that validates *and* reserves in one statement:
   ```sql
   UPDATE promo_codes
      SET current_redemptions = current_redemptions + 1, updated_at = now()
    WHERE id = $1
      AND status = 'active'
      AND now() >= valid_from
      AND (valid_to IS NULL OR now() < valid_to)
      AND (max_redemptions IS NULL OR current_redemptions < max_redemptions)
   RETURNING amount;
   ```
   Zero rows → re-`SELECT` the row to classify the typed error: `disabled` → `ErrPromoDisabled`,
   before `valid_from` → `ErrPromoNotYetActive`, past `valid_to` → `ErrPromoExpired`,
   at cap → `ErrPromoExhausted`.
6. **Credit the balance** — `UPDATE users SET credit_balance = credit_balance + $amount,
   updated_at = now() WHERE id = $1 RETURNING credit_balance` (→ `balance_after`).
7. **Write the ledger row** — `INSERT INTO credit_transactions (id, user_id, type, amount,
   balance_before, balance_after, description, reference_id, reference_type, metadata) VALUES
   ($txID, $user, 'bonus', $amount, $before, $after, 'Promo code <CODE>', upper($code),
   'promo', $meta)`, with `$txID` generated app-side.
8. **Insert the redemption** — `INSERT INTO promo_redemptions (id, promo_code_id, user_id,
   amount, credit_transaction_id, source) VALUES (..., $txID, $source)`. The unique index on
   `(user_id, promo_code_id)` is the hard backstop: if two concurrent transactions for the same
   user both passed step 3, the loser fails here → `ErrPromoAlreadyRedeemed` and the whole tx
   rolls back, undoing the step-5 increment and the step-6 credit (**no leak**).
9. **Commit.** Return `{credits_added, new_balance}`.

Races handled: *last slot claimed twice* (guarded counter, step 5), *same user redeems twice*
(`FOR UPDATE` + EXISTS + unique index, steps 2/3/8). No `SERIALIZABLE` needed.

**Per-code contention:** all concurrent redemptions of one code serialize on its `promo_codes`
row at step 5. Correct, and acceptable at expected campaign volumes — noted as the throughput
ceiling rather than a correctness concern.

## 8. Security model / defense in depth

| Threat | Defense |
|---|---|
| Scripted/bot farming via API key or MCP | Redeem is **session-only**; handler rejects API-key identities (mirror admin precedent); not exposed as an MCP tool |
| Double-click / network retry double-grant | `Idempotency` middleware **and** DB unique `(user_id, promo_code_id)` — two independent layers |
| Same user redeems twice (concurrent) | `SELECT … FOR UPDATE` on user + unique index |
| Last-slot race / over budget | Guarded atomic counter `WHERE current_redemptions < max_redemptions` + CHECK constraint |
| Code enumeration / brute force | `PerUserRateLimit` on the route + global `PerIPRateLimit`; campaign codes for high-value offers should use higher entropy |
| Expired / disabled code | Enforced inside the atomic claim, not in app code |
| Tampered Clerk `unsafeMetadata` code | Treated as untrusted; routed through the identical validation as manual; codes are public → no privilege gain |
| Negative/overflow amounts | `amount > 0` CHECK; `credit_balance >= 0` and `balance_after = balance_before + amount` invariants already enforced |

## 9. API surface

### 9.1 User endpoint (session-only)

`POST /api/v1/credits/redeem`
- **Auth:** Clerk session/JWT only. Handler rejects API-key identities → `403`.
- **Mounting:** registered when `db != nil`, **not** gated on `billingSvc != nil`. The rest of
  `/credits/*` is mounted only when Stripe is configured (`web.go:365`), but redemption needs
  only the DB — it must register independently or a no-Stripe deployment loses it.
- **Middleware:** `PerUserRateLimit` (5/min, burst 5) + existing `Idempotency`.
- **Request:** `{ "code": "REDDIT5" }`
- **Success `200`:** `{ "credits_added": "5.000000", "new_balance": "7.000000" }`
- **Errors:** `404` not found · `410` expired · `409` already redeemed / exhausted / disabled /
  not-yet-active · `422` new-accounts-only · `400` malformed · `429` rate-limited.

### 9.2 Admin endpoints (on existing `adminRouter`: `RequireRole(RoleAdmin)` + API-key rejection)

- `POST   /api/v1/admin/promo-codes` — create `{code, amount, description?, max_redemptions?, new_accounts_only?, valid_from?, valid_to?}`.
- `GET    /api/v1/admin/promo-codes` — list with redemption stats (`current_redemptions`, remaining).
- `GET    /api/v1/admin/promo-codes/{id}` — detail + recent redemptions.
- `PATCH  /api/v1/admin/promo-codes/{id}` — enable/disable, edit `max_redemptions` / `valid_to`.
  (Code string and amount are immutable once created; disable + create a new one instead.)

## 10. Signup-link flow

1. Landing page reads `?promo=CODE`, normalizes (trim/upper), persists briefly
   (cookie/localStorage) to survive the signup navigation.
2. On sign-up, the code is attached as Clerk `unsafeMetadata.promoCode` so it rides to the
   `user.created` webhook payload.
3. `UserProvisioning.Provision` (server-side), **after** the user row exists and the $2 bonus
   is granted, reads `unsafe_metadata.promoCode` and calls
   `PromoService.Redeem(ctx, userID, code, "signup_link")`.
4. **Non-fatal:** a missing/invalid/exhausted code is logged and ignored — it must never block
   account creation (same policy as the signup bonus). Idempotent across Clerk webhook
   redeliveries via the unique index.

Chosen server-side (via metadata) over a client call to `/redeem` after signup because
provisioning is asynchronous; a client call could race ahead of the user row existing.
`unsafeMetadata` is client-writable and therefore treated as untrusted input — validated
through the identical path as manual redemption.

## 11. Frontend

- New `src/components/credits/PromoCodeRedemption.tsx`: `Input` + `Button`, plain `useState`,
  `useAPI().post('/api/v1/credits/redeem', { code })`, on success `useCredits().refresh()` +
  `toast.success`; on error inline message + `toast.error`. Sends an `Idempotency-Key`.
- Placed in `CreditsClient.tsx` after `BalanceCard`. Uses design tokens (`t-card-bg`,
  `t-card-border`, `t-label`); dark mode automatic.
- Signup-link capture wired on the landing/sign-up route (`?promo=` → persist → Clerk
  `unsafeMetadata`).

## 12. Configuration

Per-code amounts make a global amount config unnecessary. Two tunables:
- `promo.rate_limit_per_min` (default 5) — read **once at startup** from env var (or default).
  It parameterizes `PerUserRateLimit`, which takes a static `rate.Limit` at construction and is
  not runtime-mutable, so the `system_config` tier does **not** apply to this knob.
- `promo.new_account_window_days` (default 7) — read per-request, so it may use the full 3-tier
  config (env → `system_config` → default).

## 13. Observability

- Structured `slog` on every attempt: `promo_redeemed` / `promo_redeem_rejected` with
  `user_id`, `code`, `source`, `reason`, `amount`. Never log full PII beyond the user id.
- Admin list surfaces `current_redemptions` / remaining for campaign monitoring.
- (Optional) a counter metric `promo_redemptions_total{source,result}`.

## 14. Out of scope (YAGNI, v1)

Admin dashboard UI · percentage / discount-on-purchase codes (these are **grant** codes only)
· user-to-user referral attribution & payouts · multi-currency promo amounts · scheduled
campaign automation · full Sybil/multi-account defense (see §15).

## 15. Residual risks

- **Sybil / multi-account farming.** "One per user" does not stop one person creating N
  throwaway accounts to claim a code N times. **v1 mitigation:** the per-code
  `max_redemptions` budget cap bounds total dollar exposure regardless of account count.
  Deeper defenses (IP/device/payment-method dedup, email-domain rules) are deferred and
  explicitly flagged, not silently assumed.
- **Low-entropy human codes** (e.g. `REDDIT5`) are enumerable in principle. Mitigated by
  auth-required redemption + per-user rate limiting + budget cap. High-value offers should use
  higher-entropy codes.

## 16. Testing strategy

**Backend**
- Table-driven service tests: valid, not-found, disabled, expired, exhausted,
  already-redeemed, new-accounts-only (eligible/ineligible).
- Concurrency integration tests against a real Postgres:
  - N goroutines redeem the **last** remaining slot → exactly one succeeds, counter ends at cap.
  - Same user redeems concurrently → exactly one `promo_redemptions` row; balance credited once.
- Ledger correctness: a successful redemption writes exactly one `credit_transactions` row with
  `type='bonus'`, `reference_type='promo'`, and `balance_after = balance_before + amount`.
- Auth: redeem with an API-key identity → `403`; admin endpoints reject non-admin and API keys
  (extend `admin_test.go` patterns).

**Frontend**
- Component tests for the redeem card: success (balance refresh + toast), error states, disabled
  while loading.

## 17. Migration & rollout

1. Ship `000042` (two tables + widen the `reference_type` CHECK to include `'promo'`).
   Auto-applies on startup. No promo codes are seeded by the migration.
2. Deploy backend. The redeem endpoint is live but harmless until a code exists; admin creates
   the launch code via the admin API (§6.4).
3. Ship frontend redeem card + signup-link capture.
4. Verify the launch code end-to-end before publishing any public link.

**Down migration (`000042_promo_codes.down.sql`).** Drops `promo_redemptions` first (it FKs
`credit_transactions`), then `promo_codes`. It deliberately **does not** revert the
`reference_type` CHECK: `'promo'` ledger rows are immutable financial records, and re-narrowing
the constraint would either fail or force their destruction — breaking the audit chain and
leaving `users.credit_balance` overstated. Leaving `'promo'` in the enum is a safe no-op, so the
down migration is reversible for the feature tables while preserving the ledger.

## 18. Resolved defaults (locked for v1)

These ship with working defaults, locked unless changed before implementation:
- Redeem rate limit: **5/min, burst 5**, per user, read at startup.
- `new_account_window_days`: **7** (applies only to `new_accounts_only` codes).
- Per-IP velocity check on `signup_link` redemptions: **deferred** with the rest of Sybil
  defense (§15); the per-code budget cap is the v1 bound.
