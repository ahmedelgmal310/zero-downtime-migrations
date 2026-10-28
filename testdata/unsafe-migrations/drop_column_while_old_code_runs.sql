-- expect: ban-drop-column
-- Dropping "name" in the same release as the code change: pods still running
-- the previous release read and write it and start failing immediately.
-- +goose Up
SET lock_timeout = '1s';
SET statement_timeout = '10s';
ALTER TABLE users DROP COLUMN name;
