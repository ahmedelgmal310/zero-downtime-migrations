-- expect: constraint-missing-not-valid
-- A CHECK added without NOT VALID scans the whole table while blocking writes.
-- +goose Up
SET lock_timeout = '1s';
SET statement_timeout = '10s';
ALTER TABLE users ADD CONSTRAINT users_name_not_empty CHECK (name <> '');
