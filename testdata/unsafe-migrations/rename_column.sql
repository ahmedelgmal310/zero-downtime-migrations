-- expect: renaming-column
-- Renaming in place breaks every running pod that still uses the old name.
-- +goose Up
SET lock_timeout = '1s';
SET statement_timeout = '10s';
ALTER TABLE users RENAME COLUMN name TO full_name;
