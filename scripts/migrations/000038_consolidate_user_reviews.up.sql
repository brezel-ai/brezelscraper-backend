BEGIN;

-- Consolidate two review columns into one.
--
-- Background: results.user_reviews has been written as an empty JSON array on
-- every row since the column was introduced (gmaps/entry.go EntryFromJSON only
-- allocated the slice and never called parseReviews on the initial-page payload).
-- All real review data was written to results.user_reviews_extended via
-- AddExtraReviews. The dual columns produced an always-empty CSV column and a
-- misleading "_extended" name on the one column that actually carried data.
--
-- This migration drops the empty column and renames the populated one to the
-- canonical user_reviews name. Safe because user_reviews carries no data.

ALTER TABLE results DROP COLUMN IF EXISTS user_reviews;
ALTER TABLE results RENAME COLUMN user_reviews_extended TO user_reviews;

COMMIT;
