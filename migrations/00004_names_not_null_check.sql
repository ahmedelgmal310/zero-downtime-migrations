-- CONTRACT, part 1 (ships with the contract release, after v3 is fully rolled
-- out and the backfill has finished).
--
-- Step 1 of the safe SET NOT NULL pattern: add the CHECK as NOT VALID. Postgres
-- does not scan the table, so the ACCESS EXCLUSIVE lock is held for
-- milliseconds. New and updated rows are checked from now on.

-- +goose Up
SET lock_timeout = '1s';
SET statement_timeout = '10s';
ALTER TABLE users DROP CONSTRAINT IF EXISTS users_names_not_null;
ALTER TABLE users
    ADD CONSTRAINT users_names_not_null
    CHECK (first_name IS NOT NULL AND last_name IS NOT NULL) NOT VALID;

-- +goose Down
SET lock_timeout = '1s';
SET statement_timeout = '10s';
ALTER TABLE users DROP CONSTRAINT IF EXISTS users_names_not_null;
