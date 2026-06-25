-- Drop the child (FKs credit_transactions and promo_codes) before the parent.
DROP TABLE IF EXISTS promo_redemptions;
DROP TABLE IF EXISTS promo_codes;

-- Intentionally NOT reverting the reference_type CHECK: 'promo' ledger rows
-- are immutable financial records; re-narrowing the constraint would fail or
-- force their deletion (corrupting the audit chain and overstating
-- users.credit_balance). Leaving the extra enum value is a safe no-op.
