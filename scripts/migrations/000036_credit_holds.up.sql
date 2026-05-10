-- 000036: Credit holds (estimate-as-quote reservation)
--
-- Why: previously the only protection against a user submitting two jobs
-- whose individual estimates each fit but combined exceed their balance
-- was a SELECT … FOR UPDATE on users in ConcurrentLimitService. That
-- correctly serialised the BALANCE READS but did NOT reserve credits —
-- both jobs would pass the check before either had charged anything,
-- then the second's end-of-job ChargeAllJobEvents would race the first's
-- and one would be silently underbilled or fail mid-charge while
-- results were already in the DB (free-results adversarial vector,
-- see PR thread on the architectural review 2026-05-10).
--
-- The fix introduces a hold (reservation) column on users:
--
--   available = credit_balance - credit_held_precise
--
-- Submitting a job atomically increments credit_held_precise by the
-- estimate. The hold is released at end-of-job (success OR failure)
-- regardless of the actual charged amount; the actual charge still
-- decrements credit_balance via the existing ChargeJobStart /
-- ChargeAllJobEvents path.
--
-- Held credits are not yet refundable to the user's balance — the
-- accompanying handler logic uses the persisted estimated_cost_precise
-- column (already in 000017) to remember how much to release.
--
-- Backfill: existing rows get DEFAULT 0. Jobs in flight at migration
-- time were not held (they pre-date this contract); they will release
-- 0 at end and are unaffected.

BEGIN;

ALTER TABLE users
    ADD COLUMN IF NOT EXISTS credit_held_precise NUMERIC(18,6)
        NOT NULL DEFAULT 0;

ALTER TABLE users
    ADD CONSTRAINT credit_held_precise_non_negative
        CHECK (credit_held_precise >= 0);

ALTER TABLE users
    ADD CONSTRAINT credit_held_not_exceed_balance
        CHECK (credit_held_precise <= credit_balance);

COMMENT ON COLUMN users.credit_held_precise IS
    'Reserved credits for in-flight jobs. Available balance = credit_balance - credit_held_precise. Incremented by estimated_cost_precise at job creation, decremented by the same amount at job end (success or failure). The actual charge happens against credit_balance via billing_events.';

COMMIT;
