package session

import (
	"context"
	"os"
	"testing"

	"github.com/ai-factory/go-server/internal/db"
)

// TestPGStoreRoundTrip exercises UpsertSession/AppendMessage/LoadSession against
// a real Postgres, catching pgx type-mapping regressions (uuid/int/jsonb casts).
// Skips unless AI_FACTORY_DATABASE_URL is set (mirrors db_test.go).
func TestPGStoreRoundTrip(t *testing.T) {
	dsn := os.Getenv("AI_FACTORY_DATABASE_URL")
	if dsn == "" {
		t.Skip("AI_FACTORY_DATABASE_URL not set; skipping integration test")
	}
	ctx := context.Background()
	d, err := db.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { d.Pool().Close() })
	if err := d.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	store := NewPGStore(d.Pool())

	// FK target tenant (fixed id, idempotent).
	const tenantID = "00000000-0000-0000-0000-000000000001"
	if _, err := d.Pool().Exec(ctx,
		`INSERT INTO tenants (id, name) VALUES ($1, $2) ON CONFLICT (id) DO NOTHING`,
		tenantID, "session-store-test"); err != nil {
		t.Fatalf("insert tenant: %v", err)
	}

	// Clean slate so re-runs don't hit the (session_id, seq) unique constraint.
	const sessionID = "test-store-session"
	if _, err := d.Pool().Exec(ctx, `DELETE FROM sessions WHERE id = $1`, sessionID); err != nil {
		t.Fatalf("cleanup: %v", err)
	}

	s := NewSession(sessionID, DefaultMaxTokens)
	s.TenantID = tenantID
	s.Model = "qwen-3b"
	s.SystemPrompt = "you are helpful"

	if err := store.UpsertSession(ctx, s); err != nil {
		t.Fatalf("upsert session: %v", err)
	}
	if err := store.AppendMessage(ctx, s.ID, 1, Message{Role: RoleUser, Content: "hi"}); err != nil {
		t.Fatalf("append user msg: %v", err)
	}
	withTools := Message{
		Role:    RoleAssistant,
		Content: "hello",
		ToolCalls: []ToolCall{
			{ID: "t1", Name: "read_file", Arguments: `{"path":"x"}`},
		},
	}
	if err := store.AppendMessage(ctx, s.ID, 2, withTools); err != nil {
		t.Fatalf("append assistant msg with tool_calls: %v", err)
	}

	loaded, err := store.LoadSession(ctx, s.ID, tenantID)
	if err != nil {
		t.Fatalf("load session: %v", err)
	}
	if got := len(loaded.Messages); got != 2 {
		t.Fatalf("loaded %d messages, want 2", got)
	}
	if loaded.Messages[0].Role != RoleUser || loaded.Messages[0].Content != "hi" {
		t.Errorf("msg[0] = %+v, want user/hi", loaded.Messages[0])
	}
	if len(loaded.Messages[1].ToolCalls) != 1 || loaded.Messages[1].ToolCalls[0].Name != "read_file" {
		t.Errorf("msg[1].tool_calls = %+v, want 1 read_file call", loaded.Messages[1].ToolCalls)
	}

	// Scoping: a different tenant must not see the session.
	if _, err := store.LoadSession(ctx, s.ID, "00000000-0000-0000-0000-000000000002"); err != ErrSessionNotFound {
		t.Errorf("cross-tenant load err = %v, want ErrSessionNotFound", err)
	}
}
