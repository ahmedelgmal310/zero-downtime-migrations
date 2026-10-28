-- expect: adding-field-with-default
-- A volatile default (gen_random_uuid) forces a full table rewrite under an
-- ACCESS EXCLUSIVE lock: every read and write on users blocks until it ends.
-- +goose Up
SET lock_timeout = '1s';
SET statement_timeout = '10s';
ALTER TABLE users ADD COLUMN public_id uuid DEFAULT gen_random_uuid();
