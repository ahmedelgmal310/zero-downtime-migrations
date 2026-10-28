-- expect: require-lock-timeout, require-statement-timeout
-- Without lock_timeout this ALTER can wait forever behind a long query while
-- every new query on users queues behind it: a full outage from a "fast" DDL.
-- +goose Up
ALTER TABLE users ADD COLUMN nickname text;
