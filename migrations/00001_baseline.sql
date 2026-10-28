-- Baseline schema used by app v1: one "name" column holds the full name.
-- app_instances is the fleet registry: every API pod heartbeats its version
-- here, and the contract migration refuses to run while an old version is alive.

-- +goose Up
SET lock_timeout = '1s';
SET statement_timeout = '10s';

CREATE TABLE users (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE app_instances (
    instance_id text PRIMARY KEY,
    version text NOT NULL,
    started_at timestamptz NOT NULL DEFAULT now(),
    last_seen timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
SET lock_timeout = '1s';
SET statement_timeout = '10s';
-- squawk-ignore ban-drop-table
DROP TABLE IF EXISTS app_instances;
-- squawk-ignore ban-drop-table
DROP TABLE IF EXISTS users;
