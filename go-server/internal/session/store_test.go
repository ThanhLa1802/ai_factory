package session

import (
	"context"
	"fmt"
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

// TestSessionForbiddenCrossTenantGetOrCreate reproduces the cross-tenant write
// bug: tenant B presents tenant A's session id on a cold cache. GetOrCreate must
// return ErrSessionForbidden (not mint a shadow session) and must not insert any
// message into A's persisted conversation.
func TestSessionForbiddenCrossTenantGetOrCreate(t *testing.T) {
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

	const (
		tenantA   = "00000000-0000-0000-0000-000000000011"
		tenantB   = "00000000-0000-0000-0000-000000000012"
		sessionID = "test-forbidden-session"
	)
	for i, id := range []string{tenantA, tenantB} {
		if _, err := d.Pool().Exec(ctx,
			`INSERT INTO tenants (id, name) VALUES ($1, $2) ON CONFLICT (id) DO NOTHING`,
			id, fmt.Sprintf("session-forbidden-test-%d", i)); err != nil {
			t.Fatalf("insert tenant %s: %v", id, err)
		}
	}
	// Clean slate so re-runs don't hit the (session_id, seq) unique constraint.
	if _, err := d.Pool().Exec(ctx, `DELETE FROM sessions WHERE id = $1`, sessionID); err != nil {
		t.Fatalf("cleanup: %v", err)
	}

	// Tenant A persists a session with one private message.
	mgrA := NewManagerWithStore(store)
	sA, err := mgrA.GetOrCreate(ctx, sessionID, tenantA, "")
	if err != nil {
		t.Fatalf("tenant A get or create: %v", err)
	}
	sA.AddMessage(ctx, Message{Role: RoleUser, Content: "private"})

	// Cold cache: a fresh manager has no in-memory copy of A's session.
	mgrB := NewManagerWithStore(store)
	if _, err := mgrB.GetOrCreate(ctx, sessionID, tenantB, ""); err != ErrSessionForbidden {
		t.Fatalf("tenant B get or create err = %v, want ErrSessionForbidden", err)
	}

	// B's attempt must not have written any messages into A's session.
	var n int
	if err := d.Pool().QueryRow(ctx,
		`SELECT COUNT(*) FROM messages WHERE session_id = $1`, sessionID).Scan(&n); err != nil {
		t.Fatalf("count messages: %v", err)
	}
	if n != 1 {
		t.Fatalf("messages under %s = %d, want 1 (only A's)", sessionID, n)
	}
}

// TestSessionForbiddenUpsertZeroRow covers the defense-in-depth layer: even if
// a colliding session reaches UpsertSession, the ON CONFLICT ... WHERE tenant_id
// guard no-ops and must surface as ErrSessionForbidden instead of a silent success.
func TestSessionForbiddenUpsertZeroRow(t *testing.T) {
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

	const (
		tenantA   = "00000000-0000-0000-0000-000000000013"
		tenantB   = "00000000-0000-0000-0000-000000000014"
		sessionID = "test-forbidden-upsert"
	)
	for i, id := range []string{tenantA, tenantB} {
		if _, err := d.Pool().Exec(ctx,
			`INSERT INTO tenants (id, name) VALUES ($1, $2) ON CONFLICT (id) DO NOTHING`,
			id, fmt.Sprintf("session-forbidden-upsert-%d", i)); err != nil {
			t.Fatalf("insert tenant %s: %v", id, err)
		}
	}
	if _, err := d.Pool().Exec(ctx, `DELETE FROM sessions WHERE id = $1`, sessionID); err != nil {
		t.Fatalf("cleanup: %v", err)
	}

	// Tenant A persists a session with one message.
	mgrA := NewManagerWithStore(store)
	sA, err := mgrA.GetOrCreate(ctx, sessionID, tenantA, "")
	if err != nil {
		t.Fatalf("tenant A get or create: %v", err)
	}
	sA.AddMessage(ctx, Message{Role: RoleUser, Content: "private"})

	// Tenant B upserts a session object with the colliding id directly.
	sB := NewSession(sessionID, DefaultMaxTokens)
	sB.TenantID = tenantB
	sB.Model = "qwen-3b"
	if err := store.UpsertSession(ctx, sB); err != ErrSessionForbidden {
		t.Fatalf("upsert err = %v, want ErrSessionForbidden", err)
	}

	var n int
	if err := d.Pool().QueryRow(ctx,
		`SELECT COUNT(*) FROM messages WHERE session_id = $1`, sessionID).Scan(&n); err != nil {
		t.Fatalf("count messages: %v", err)
	}
	if n != 1 {
		t.Fatalf("messages under %s = %d, want 1 (only A's)", sessionID, n)
	}
}

// TestStoreListRenameDeleteTitle exercises ListSessions/RenameSession/DeleteSession
// and title persistence against a real Postgres.
func TestStoreListRenameDeleteTitle(t *testing.T) {
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

	const tenantID = "00000000-0000-0000-0000-000000000001"
	if _, err := d.Pool().Exec(ctx,
		`INSERT INTO tenants (id, name) VALUES ($1, $2) ON CONFLICT (id) DO NOTHING`,
		tenantID, "session-store-test"); err != nil {
		t.Fatalf("insert tenant: %v", err)
	}
	const sessionID = "test-store-list"

	// Clean slate.
	if _, err := d.Pool().Exec(ctx, `DELETE FROM sessions WHERE id = $1`, sessionID); err != nil {
		t.Fatalf("cleanup: %v", err)
	}

	// Build a session + one user message through the Manager so auto-title runs.
	mgr := NewManagerWithStore(store)
	s, err := mgr.GetOrCreate(ctx, sessionID, tenantID, "")
	if err != nil {
		t.Fatalf("get or create: %v", err)
	}
	s.Model = "qwen-3b"
	s.AddMessage(ctx, Message{Role: RoleUser, Content: "first message that is quite long and will be truncated"})

	// Auto-title: truncate to 40 runes + ellipsis.
	loaded, err := store.LoadSession(ctx, sessionID, tenantID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.Title == "" {
		t.Fatalf("title empty, want auto-generated")
	}
	if runes := []rune(loaded.Title); len(runes) > 41 {
		t.Fatalf("title = %q, want ≤ 41 runes", loaded.Title)
	}

	// List returns the session with a title + 1 message.
	list, err := store.ListSessions(ctx, tenantID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) == 0 {
		t.Fatalf("list empty, want ≥ 1")
	}
	var found *SessionSummary
	for i := range list {
		if list[i].ID == sessionID {
			found = &list[i]
		}
	}
	if found == nil {
		t.Fatalf("list = %+v, want contain %s", list, sessionID)
	}
	if found.MessageCount != 1 || found.Title == "" {
		t.Fatalf("found = %+v, want message_count=1 + non-empty title", *found)
	}

	// Rename.
	if err := store.RenameSession(ctx, sessionID, tenantID, "Renamed chat"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	loaded2, err := store.LoadSession(ctx, sessionID, tenantID)
	if err != nil {
		t.Fatalf("load after rename: %v", err)
	}
	if loaded2.Title != "Renamed chat" {
		t.Fatalf("title = %q, want Renamed chat", loaded2.Title)
	}

	// Cross-tenant rename must NOT find the session.
	if err := store.RenameSession(ctx, sessionID, "00000000-0000-0000-0000-000000000002", "x"); err != ErrSessionNotFound {
		t.Fatalf("cross-tenant rename err = %v, want ErrSessionNotFound", err)
	}

	// Delete.
	if err := store.DeleteSession(ctx, sessionID, tenantID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := store.LoadSession(ctx, sessionID, tenantID); err != ErrSessionNotFound {
		t.Fatalf("load after delete err = %v, want ErrSessionNotFound", err)
	}
}
