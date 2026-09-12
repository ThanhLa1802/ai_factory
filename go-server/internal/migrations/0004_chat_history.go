package migrations

import (
	"github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// M0004ChatHistory (converted from goose 0004).
func M0004ChatHistory() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "0004_chat_history",
		Migrate: func(tx *gorm.DB) error {
			if err := tx.Exec(`CREATE TABLE sessions (
				id            TEXT PRIMARY KEY,
				tenant_id     UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
				user_id       UUID REFERENCES users(id) ON DELETE SET NULL,
				model         TEXT NOT NULL DEFAULT '',
				system_prompt TEXT NOT NULL DEFAULT '',
				max_tokens    INT  NOT NULL DEFAULT 8192,
				created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
				updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
			)`).Error; err != nil {
				return err
			}
			if err := tx.Exec(`CREATE INDEX sessions_tenant_updated_idx ON sessions (tenant_id, updated_at DESC)`).Error; err != nil {
				return err
			}
			return tx.Exec(`CREATE TABLE messages (
				id           BIGSERIAL PRIMARY KEY,
				session_id   TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
				seq          INT  NOT NULL,
				role         TEXT NOT NULL,
				content      TEXT NOT NULL DEFAULT '',
				tool_calls   JSONB NOT NULL DEFAULT '[]',
				tool_call_id TEXT NOT NULL DEFAULT '',
				tool_result  TEXT NOT NULL DEFAULT '',
				is_error     BOOL NOT NULL DEFAULT false,
				created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
				UNIQUE (session_id, seq)
			)`).Error
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Exec(`DROP TABLE IF EXISTS messages, sessions CASCADE`).Error
		},
	}
}
