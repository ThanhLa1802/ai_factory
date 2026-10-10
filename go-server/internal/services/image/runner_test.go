package image

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	infraimage "github.com/ai-factory/go-server/internal/infrastructure/image"
)

func TestRunnerProcessesQueuedJob(t *testing.T) {
	gen := &fakeGenerator{
		result: &infraimage.GenerateResult{PNG: [][]byte{{1}, {2}}, Width: 1024, Height: 1024, Seed: 9, Steps: 3, Scheduler: "default"},
		steps:  3,
	}
	svc, storage, assets, jobs := newTestService(gen, nil, 2)
	job, err := svc.CreateJob(context.Background(), "t1", GenerateParams{Model: "sdxl-1.0", Prompt: "a cat"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if _, err := NewRunner(svc, nil).RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	if jobs.items[0].Status != JobSucceeded {
		t.Fatalf("status = %q, want %q", jobs.items[0].Status, JobSucceeded)
	}
	if jobs.items[0].ProgressStep != 3 {
		t.Errorf("progress = %d, want 3", jobs.items[0].ProgressStep)
	}
	if len(assets.items) != 2 {
		t.Fatalf("assets = %d, want 2", len(assets.items))
	}
	for _, a := range assets.items {
		if a.JobID != job.ID {
			t.Errorf("asset job_id = %q, want %q", a.JobID, job.ID)
		}
	}
	if len(storage.files) != 2 {
		t.Fatalf("stored files = %d, want 2", len(storage.files))
	}
	if gen.lastReq.RequestID != job.ID {
		t.Errorf("request_id = %q, want %q", gen.lastReq.RequestID, job.ID)
	}
}

func TestRunnerDoesNotReprocessFinishedJob(t *testing.T) {
	gen := &fakeGenerator{result: &infraimage.GenerateResult{PNG: [][]byte{{1}}, Width: 1024, Height: 1024, Seed: 1, Steps: 1}}
	svc, _, _, _ := newTestService(gen, nil, 1)
	if _, err := svc.CreateJob(context.Background(), "t1", GenerateParams{Model: "sdxl-1.0", Prompt: "x"}); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	r := NewRunner(svc, nil)
	if n, _ := r.RunOnce(context.Background()); n != 1 {
		t.Fatalf("first RunOnce n = %d, want 1", n)
	}
	if n, _ := r.RunOnce(context.Background()); n != 0 {
		t.Fatalf("second RunOnce n = %d, want 0", n)
	}
	if gen.calls != 1 {
		t.Fatalf("generator calls = %d, want 1", gen.calls)
	}
}

func TestRunnerResumesStaleRunningJob(t *testing.T) {
	gen := &fakeGenerator{result: &infraimage.GenerateResult{PNG: [][]byte{{1}}, Width: 1024, Height: 1024, Seed: 1, Steps: 1}}
	svc, _, _, jobs := newTestService(gen, nil, 1)
	if _, err := svc.CreateJob(context.Background(), "t1", GenerateParams{Model: "sdxl-1.0", Prompt: "x"}); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	// Simulate a job stuck in running from a crashed node: its lease lapsed.
	jobs.items[0].Status = JobRunning
	jobs.items[0].UpdatedAt = time.Now().UTC().Add(-time.Hour)

	if n, err := NewRunner(svc, nil).RunOnce(context.Background()); err != nil || n != 1 {
		t.Fatalf("RunOnce = %d, %v", n, err)
	}
	if jobs.items[0].Status != JobSucceeded {
		t.Fatalf("status = %q, want %q", jobs.items[0].Status, JobSucceeded)
	}
	if gen.calls != 1 {
		t.Fatalf("generator calls = %d, want 1", gen.calls)
	}
}

func TestRunnerMarksFailedJob(t *testing.T) {
	gen := &fakeGenerator{err: errors.New("boom")}
	svc, _, _, jobs := newTestService(gen, nil, 1)
	if _, err := svc.CreateJob(context.Background(), "t1", GenerateParams{Model: "sdxl-1.0", Prompt: "x"}); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if _, err := NewRunner(svc, nil).RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if jobs.items[0].Status != JobFailed {
		t.Fatalf("status = %q, want %q", jobs.items[0].Status, JobFailed)
	}
	if !strings.Contains(jobs.items[0].Error, "boom") {
		t.Errorf("error = %q, want it to contain boom", jobs.items[0].Error)
	}
}
