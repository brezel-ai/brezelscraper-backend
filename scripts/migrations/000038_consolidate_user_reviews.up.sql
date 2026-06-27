BEGIN;

-- Collapse the two review columns into one. user_reviews has always been
-- written as an empty JSON array in production, so dropping it loses no data;
-- the populated user_reviews_extended takes over the canonical name.

ALTER TABLE results DROP COLUMN IF EXISTS user_reviews;
ALTER TABLE results RENAME COLUMN user_reviews_extended TO user_reviews;

COMMIT;
