-- expect: require-concurrent-index-creation
-- A plain CREATE INDEX takes a SHARE lock: writes block for the whole build.
-- +goose Up
SET lock_timeout = '1s';
SET statement_timeout = '10s';
CREATE INDEX users_created_at_idx ON users (created_at);
