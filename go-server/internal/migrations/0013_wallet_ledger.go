package migrations

import (
	"github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// M0013WalletLedger creates the prepaid billing tables: the tenant wallet
// (balance + reserved hold), the append-only signed ledger, active holds
// (authorize → capture → release), and top-up transactions (idempotent).
func M0013WalletLedger() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "0013_wallet_ledger",
		Migrate: func(tx *gorm.DB) error {
			stmts := []string{
				`CREATE TABLE wallets (
					tenant_id  UUID PRIMARY KEY REFERENCES tenants(id) ON DELETE CASCADE,
					currency   TEXT NOT NULL DEFAULT 'USD',
					balance    BIGINT NOT NULL DEFAULT 0,
					reserved   BIGINT NOT NULL DEFAULT 0,
					updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
				)`,
				`CREATE TABLE ledger_entries (
					id              BIGSERIAL PRIMARY KEY,
					tenant_id       UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
					kind            TEXT NOT NULL,
					amount          BIGINT NOT NULL,
					balance_after   BIGINT NOT NULL,
					currency        TEXT NOT NULL DEFAULT 'USD',
					ref_type        TEXT NOT NULL DEFAULT '',
					ref_id          TEXT NOT NULL DEFAULT '',
					idempotency_key TEXT UNIQUE,
					metadata        JSONB,
					created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
				)`,
				`CREATE INDEX ledger_entries_tenant_id_idx ON ledger_entries (tenant_id, id DESC)`,
				`CREATE TABLE wallet_reservations (
					id           UUID PRIMARY KEY,
					tenant_id    UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
					amount       BIGINT NOT NULL,
					input_price  BIGINT NOT NULL DEFAULT 0,
					output_price BIGINT NOT NULL DEFAULT 0,
					currency     TEXT NOT NULL DEFAULT 'USD',
					status       TEXT NOT NULL DEFAULT 'held',
					expires_at   TIMESTAMPTZ NOT NULL,
					created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
					settled_at   TIMESTAMPTZ
				)`,
				`CREATE INDEX wallet_reservations_tenant_status_idx ON wallet_reservations (tenant_id, status)`,
				`CREATE INDEX wallet_reservations_expires_idx ON wallet_reservations (expires_at)`,
				`CREATE TABLE topup_transactions (
					id              UUID PRIMARY KEY,
					tenant_id       UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
					amount          BIGINT NOT NULL,
					currency        TEXT NOT NULL DEFAULT 'USD',
					provider        TEXT NOT NULL DEFAULT 'mock',
					provider_ref    TEXT NOT NULL DEFAULT '',
					status          TEXT NOT NULL DEFAULT 'succeeded',
					idempotency_key TEXT NOT NULL UNIQUE,
					created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
					updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
				)`,
			}
			for _, s := range stmts {
				if err := tx.Exec(s).Error; err != nil {
					return err
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Exec(`DROP TABLE IF EXISTS topup_transactions, wallet_reservations, ledger_entries, wallets CASCADE`).Error
		},
	}
}
