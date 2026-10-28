-- expect: adding-not-nullable-field
-- SET NOT NULL without a validated CHECK scans the table under ACCESS EXCLUSIVE.
-- +goose Up
SET lock_timeout = '1s';
SET statement_timeout = '10s';
ALTER TABLE users ALTER COLUMN first_name SET NOT NULL;
