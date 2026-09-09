-- +goose Up
-- Per-turn token usage, tenant-scoped, written best-effort after each completion.
CREATE TABLE usage_events (
    id                BIGSERIAL PRIMARY KEY,
    tenant_id         UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    model             TEXT NOT NULL DEFAULT '',
    prompt_tokens     INT  NOT NULL DEFAULT 0,
    completion_tokens INT  NOT NULL DEFAULT 0,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX usage_events_tenant_created_idx ON usage_events (tenant_id, created_at DESC);

-- +goose Down
DROP TABLE IF EXISTS usage_events CASCADE;
