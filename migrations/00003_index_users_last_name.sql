-- EXPAND (ships with app v2). Index for v3's lookup by last name.
--
-- CREATE INDEX CONCURRENTLY builds the index without blocking writes, but it
-- cannot run inside a transaction. If a previous attempt failed (for example
-- on lock_timeout) it leaves an INVALID index behind that IF NOT EXISTS would
-- silently accept, so the index is dropped (concurrently) first. goose only
-- records this version after both statements succeed, so a retry re-runs both.

-- +goose NO TRANSACTION
-- squawk-disable-assume-in-transaction

-- +goose Up
SET lock_timeout = '1s';
SET statement_timeout = '120s';
DROP INDEX CONCURRENTLY IF EXISTS users_last_name_idx;
CREATE INDEX CONCURRENTLY IF NOT EXISTS users_last_name_idx ON users (last_name);

-- +goose Down
SET lock_timeout = '1s';
SET statement_timeout = '120s';
DROP INDEX CONCURRENTLY IF EXISTS users_last_name_idx;
