package image

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/ai-factory/go-server/internal/infrastructure/database"
	"github.com/google/uuid"
)

// TestJobClaimAndFinishIntegration exercises the atomic Claim / guarded Finish
// SQL against a real Postgres (gated on AI_FACTORY_DATABASE_URL). Unit fakes
// cannot validate FOR UPDATE SKIP LOCKED semantics.
func TestJobClaimAndFinishIntegration(t *testing.T) {
	dsn := os.Getenv("AI_FACTORY_DATABASE_URL")
	if dsn == "" {
		t.Skip("AI_FACTORY_DATABASE_URL not set; skipping integration test")
	}
	db, err := database.Open(dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := database.Migrate(db.Gorm()); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	tenantID := uuid.NewString()
	if err := db.Gorm().Exec(`INSERT INTO tenants (id, name) VALUES (?, ?)`,
		tenantID, "imgtest-"+tenantID).Error; err != nil {
		t.Fatalf("insert tenant: %v", err)
	}
	t.Cleanup(func() { db.Gorm().Exec(`DELETE FROM tenants WHERE id = ?`, tenantID) })

	repos := NewRepositories(db.Gorm())
	ctx := context.Background()
	newJob := func() *Job {
		j := &Job{
			ID: uuid.NewString(), TenantID: tenantID, Model: "sdxl-1.0", Status: JobQueued,
			Prompt: "x", Width: 1024, Height: 1024, Steps: 4, NumImages: 1,
		}
		if err := repos.Jobs.Create(ctx, j); err != nil {
			t.Fatalf("create job: %v", err)
		}
		return j
	}
	j1, j2, j3 := newJob(), newJob(), newJob()

	stale := time.Now().UTC().Add(-time.Hour)
	first, err := repos.Jobs.Claim(ctx, 2, stale)
	if err != nil {
		t.Fatalf("claim(2): %v", err)
	}
	if len(first) != 2 {
		t.Fatalf("claim(2) = %d jobs, want 2", len(first))
	}
	for _, j := range first {
		if j.Status != JobRunning {
			t.Errorf("claimed status = %q, want %q", j.Status, JobRunning)
		}
	}

	second, err := repos.Jobs.Claim(ctx, 10, stale)
	if err != nil {
		t.Fatalf("claim(10): %v", err)
	}
	if len(second) != 1 {
		t.Fatalf("claim(10) = %d jobs, want 1 (the remaining queued job)", len(second))
	}
	seen := map[string]bool{}
	for _, j := range append(first, second...) {
		if seen[j.ID] {
			t.Errorf("job %s claimed twice", j.ID)
		}
		seen[j.ID] = true
	}
	for _, id := range []string{j1.ID, j2.ID, j3.ID} {
		if !seen[id] {
			t.Errorf("job %s was never claimed", id)
		}
	}

	// Finish is guarded on running: the first transition wins, a later one is a
	// no-op (so a duplicate finish cannot flip a succeeded job to failed).
	if err := repos.Jobs.Finish(ctx, j1.ID, JobSucceeded, ""); err != nil {
		t.Fatalf("finish: %v", err)
	}
	if err := repos.Jobs.Finish(ctx, j1.ID, JobFailed, "late"); err != nil {
		t.Fatalf("second finish: %v", err)
	}
	got, err := repos.Jobs.Get(ctx, tenantID, j1.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != JobSucceeded {
		t.Errorf("status = %q, want %q (guarded)", got.Status, JobSucceeded)
	}

	// Progress is only written while running.
	if err := repos.Jobs.UpdateProgress(ctx, j2.ID, 3); err != nil {
		t.Fatalf("progress: %v", err)
	}
	got2, _ := repos.Jobs.Get(ctx, tenantID, j2.ID)
	if got2.ProgressStep != 3 {
		t.Errorf("progress_step = %d, want 3", got2.ProgressStep)
	}

	// A running job whose lease lapsed is reclaimed (crash recovery).
	if err := db.Gorm().Exec(`UPDATE image_jobs SET updated_at = ? WHERE id = ?`,
		time.Now().UTC().Add(-2*time.Hour), j2.ID).Error; err != nil {
		t.Fatalf("backdate: %v", err)
	}
	reclaimed, err := repos.Jobs.Claim(ctx, 10, stale)
	if err != nil {
		t.Fatalf("claim stale: %v", err)
	}
	found := false
	for _, j := range reclaimed {
		if j.ID == j2.ID {
			found = true
		}
	}
	if !found {
		t.Errorf("stale running job %s was not reclaimed", j2.ID)
	}
}
