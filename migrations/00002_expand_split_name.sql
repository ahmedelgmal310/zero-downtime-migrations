-- EXPAND (ships with app v2, runs before any v2 pod starts).
--
-- Adds the new columns as nullable with no default, which is a metadata-only
-- change: no table rewrite, and the ACCESS EXCLUSIVE lock is held for
-- milliseconds. lock_timeout bounds how long the ALTER may queue behind
-- running queries (and therefore how long it can block the queries queued
-- behind it); the migrator retries on lock timeout.
--
-- The trigger keeps both representations in sync for every writer:
--   * v1 (old code, also after a rollback) writes only "name": the trigger
--     derives first_name/last_name.
--   * v3 writes only first_name/last_name: the trigger derives "name", so a
--     rollback from v3 to v2 or v1 stays possible until the contract step.
--   * v2 writes all three explicitly (dual write), the trigger leaves them alone.
-- The split rule is the same as internal/names.Split.

-- +goose Up
SET lock_timeout = '1s';
SET statement_timeout = '10s';

ALTER TABLE users ADD COLUMN IF NOT EXISTS first_name text;
ALTER TABLE users ADD COLUMN IF NOT EXISTS last_name text;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION users_sync_name() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        IF NEW.first_name IS NULL AND NEW.name IS NOT NULL THEN
            NEW.first_name := split_part(NEW.name, ' ', 1);
            NEW.last_name := CASE WHEN strpos(NEW.name, ' ') > 0
                                  THEN substr(NEW.name, strpos(NEW.name, ' ') + 1)
                                  ELSE '' END;
        ELSIF NEW.name IS NULL AND NEW.first_name IS NOT NULL THEN
            NEW.name := CASE WHEN coalesce(NEW.last_name, '') = ''
                             THEN NEW.first_name
                             ELSE NEW.first_name || ' ' || NEW.last_name END;
        END IF;
    ELSIF NEW.name IS DISTINCT FROM OLD.name
          AND NEW.first_name IS NOT DISTINCT FROM OLD.first_name
          AND NEW.last_name IS NOT DISTINCT FROM OLD.last_name THEN
        -- old code changed "name": re-derive the split columns
        NEW.first_name := split_part(NEW.name, ' ', 1);
        NEW.last_name := CASE WHEN strpos(NEW.name, ' ') > 0
                              THEN substr(NEW.name, strpos(NEW.name, ' ') + 1)
                              ELSE '' END;
    ELSIF NEW.name IS NOT DISTINCT FROM OLD.name
          AND OLD.first_name IS NOT NULL
          AND (NEW.first_name IS DISTINCT FROM OLD.first_name
               OR NEW.last_name IS DISTINCT FROM OLD.last_name) THEN
        -- new code changed the split columns: re-derive "name"
        NEW.name := CASE WHEN coalesce(NEW.last_name, '') = ''
                         THEN NEW.first_name
                         ELSE NEW.first_name || ' ' || NEW.last_name END;
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE OR REPLACE TRIGGER users_sync_name
    BEFORE INSERT OR UPDATE ON users
    FOR EACH ROW EXECUTE FUNCTION users_sync_name();

-- +goose Down
SET lock_timeout = '1s';
SET statement_timeout = '10s';
-- squawk-ignore ban-drop-trigger
DROP TRIGGER IF EXISTS users_sync_name ON users;
-- squawk-ignore ban-drop-function
DROP FUNCTION IF EXISTS users_sync_name();
-- squawk-ignore ban-drop-column
ALTER TABLE users DROP COLUMN IF EXISTS last_name;
-- squawk-ignore ban-drop-column
ALTER TABLE users DROP COLUMN IF EXISTS first_name;
