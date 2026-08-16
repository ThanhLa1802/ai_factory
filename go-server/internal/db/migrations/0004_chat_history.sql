-- +goose Up
-- Chat history: durable sessions + messages (tenant-scoped).
CREATE TABLE sessions (
    id            TEXT PRIMARY KEY,              -- client-supplied x-session-id (or server UUID)
    tenant_id     UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    user_id       UUID REFERENCES users(id) ON DELETE SET NULL,
    model         TEXT NOT NULL DEFAULT '',
    system_prompt TEXT NOT NULL DEFAULT '',
    max_tokens    INT  NOT NULL DEFAULT 8192,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX sessions_tenant_updated_idx ON sessions (tenant_id, updated_at DESC);

CREATE TABLE messages (
    id           BIGSERIAL PRIMARY KEY,
    session_id   TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    seq          INT  NOT NULL,                  -- ordering within a session
    role         TEXT NOT NULL,                  -- user | assistant | system | tool
    content      TEXT NOT NULL DEFAULT '',
    tool_calls   JSONB NOT NULL DEFAULT '[]',    -- []session.ToolCall
    tool_call_id TEXT NOT NULL DEFAULT '',
    tool_result  TEXT NOT NULL DEFAULT '',
    is_error     BOOL NOT NULL DEFAULT false,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (session_id, seq)
);

-- +goose Down
DROP TABLE IF EXISTS messages, sessions CASCADE;
