BEGIN;

-- Reverse 000038: restore the two-column shape. The original user_reviews
-- column was always empty, so on rollback we restore it as a NULL column and
-- move the data back under user_reviews_extended.

ALTER TABLE results RENAME COLUMN user_reviews TO user_reviews_extended;
ALTER TABLE results ADD COLUMN IF NOT EXISTS user_reviews JSONB;

COMMIT;
