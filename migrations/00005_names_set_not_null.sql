-- CONTRACT, part 2.
--
-- VALIDATE CONSTRAINT scans the table under SHARE UPDATE EXCLUSIVE, which does
-- not block reads or writes. It fails (and the whole migration rolls back) if
-- the backfill left any NULL behind. Postgres 12+ then uses the validated CHECK
-- to prove SET NOT NULL without a second scan, so the ACCESS EXCLUSIVE lock it
-- needs is held only for a catalog update. The CHECK is redundant afterwards.

-- +goose Up
SET lock_timeout = '1s';
SET statement_timeout = '120s';
ALTER TABLE users VALIDATE CONSTRAINT users_names_not_null;
ALTER TABLE users ALTER COLUMN first_name SET NOT NULL;
ALTER TABLE users ALTER COLUMN last_name SET NOT NULL;
-- squawk-ignore ban-drop-constraint
ALTER TABLE users DROP CONSTRAINT IF EXISTS users_names_not_null;

-- +goose Down
SET lock_timeout = '1s';
SET statement_timeout = '10s';
-- squawk-ignore ban-drop-not-null
ALTER TABLE users ALTER COLUMN last_name DROP NOT NULL;
-- squawk-ignore ban-drop-not-null
ALTER TABLE users ALTER COLUMN first_name DROP NOT NULL;
