-- CONTRACT, part 3: remove the old representation.
--
-- Dropping a column is only safe once no running code reads or writes it.
-- The migrator enforces that before this file runs: it refuses while any
-- instance other than v3 has heartbeated into app_instances recently (see
-- internal/migrate guards). The squawk-ignore below is the reviewed exception
-- for exactly that reason; an unguarded DROP COLUMN fails the lint gate.

-- +goose Up
SET lock_timeout = '1s';
SET statement_timeout = '10s';
-- squawk-ignore ban-drop-trigger
DROP TRIGGER IF EXISTS users_sync_name ON users;
-- squawk-ignore ban-drop-function
DROP FUNCTION IF EXISTS users_sync_name();
-- squawk-ignore ban-drop-column
ALTER TABLE users DROP COLUMN IF EXISTS name;

-- +goose Down
-- Restoring "name" needs the expand trigger and a backfill, which is a new
-- forward migration rather than a rollback. The down step is intentionally
-- a no-op so it cannot be mistaken for a working rollback.
SELECT 1;
