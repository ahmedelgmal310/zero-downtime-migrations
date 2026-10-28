-- expect: changing-column-type
-- Changing the type rewrites the table and its indexes under ACCESS EXCLUSIVE.
-- +goose Up
SET lock_timeout = '1s';
SET statement_timeout = '10s';
ALTER TABLE users ALTER COLUMN name TYPE varchar(100);
