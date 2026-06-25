-- Promo code redemption. Shared campaign codes (one code, many users, each
-- once), bounded by a per-code budget cap and validity window. Money flows
-- through the existing credit_transactions ledger (type='bonus',
-- reference_type='promo'); these tables are the catalog + audit/uniqueness.

CREATE TABLE promo_codes (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    code                TEXT NOT NULL,
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

CREATE UNIQUE INDEX uq_promo_redemption_user_code
    ON promo_redemptions (user_id, promo_code_id);
CREATE INDEX idx_promo_redemptions_code ON promo_redemptions (promo_code_id);
CREATE INDEX idx_promo_redemptions_user ON promo_redemptions (user_id, redeemed_at DESC);

-- Widen the ledger reference_type enum so promo grants are legal.
ALTER TABLE credit_transactions DROP CONSTRAINT credit_transactions_reference_type_check;
ALTER TABLE credit_transactions ADD  CONSTRAINT credit_transactions_reference_type_check
    CHECK (reference_type IN ('job','payment','manual','system','billing_event','promo'));
