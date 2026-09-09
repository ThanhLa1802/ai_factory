-- +goose Up
ALTER TABLE deployments ADD COLUMN workload_ref TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE deployments DROP COLUMN workload_ref;
