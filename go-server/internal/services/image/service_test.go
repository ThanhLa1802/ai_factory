package image

import (
	"context"
	"errors"
	"testing"

	infraimage "github.com/ai-factory/go-server/internal/infrastructure/image"
)

func TestGenerateAppliesDefaultsAndPersists(t *testing.T) {
	gen := &fakeGenerator{result: &infraimage.GenerateResult{
		PNG: [][]byte{{9, 8, 7}}, Width: 1024, Height: 1024, Seed: 42, Steps: 25, Scheduler: "default",
	}}
	svc, _, assets, _ := newTestService(gen, nil, 1)

	got, err := svc.Generate(context.Background(), "t1", GenerateParams{
		Model: "sdxl-1.0", Prompt: "a cat", Seed: -1, GuidanceScale: -1,
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(got.Assets) != 1 {
		t.Fatalf("assets = %d, want 1", len(got.Assets))
	}
	// Defaults applied on the outgoing worker request.
	if gen.lastReq.Width != 1024 || gen.lastReq.Height != 1024 {
		t.Errorf("size = %dx%d, want 1024x1024", gen.lastReq.Width, gen.lastReq.Height)
	}
	if gen.lastReq.Steps != 25 {
		t.Errorf("steps = %d, want 25", gen.lastReq.Steps)
	}
	if gen.lastReq.GuidanceScale != 7 {
		t.Errorf("guidance = %v, want 7", gen.lastReq.GuidanceScale)
	}
	if gen.lastReq.Seed != -1 {
		t.Errorf("seed = %d, want -1 (random)", gen.lastReq.Seed)
	}
	if gen.lastReq.NumImages != 1 {
		t.Errorf("num_images = %d, want 1", gen.lastReq.NumImages)
	}
	// Persisted asset carries the worker's real metadata.
	a := got.Assets[0]
	if a.Seed != 42 || a.Width != 1024 || a.Path == "" {
		t.Errorf("asset = %+v", a)
	}
	if len(assets.items) != 1 {
		t.Errorf("stored assets = %d, want 1", len(assets.items))
	}
	if got.Seed != 42 || got.RevisedPrompt != "a cat" {
		t.Errorf("generation = %+v", got)
	}
}

func TestGeneratePassesExplicitGuidanceAndSeed(t *testing.T) {
	fake := &fakeGenerator{result: &infraimage.GenerateResult{
		PNG: [][]byte{{1}}, Width: 1024, Height: 1024, Seed: 123, Steps: 4, Scheduler: "default",
	}}
	svc, _, _, _ := newTestService(fake, nil, 1)
	if _, err := svc.Generate(context.Background(), "t1", GenerateParams{
		Model: "sdxl-1.0", Prompt: "x", GuidanceScale: 0, Seed: 123, Steps: 4,
	}); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	// Guidance 0 must pass through (turbo), not fall back to the default 7.
	if fake.lastReq.GuidanceScale != 0 {
		t.Errorf("guidance = %v, want 0", fake.lastReq.GuidanceScale)
	}
	if fake.lastReq.Seed != 123 {
		t.Errorf("seed = %d, want 123", fake.lastReq.Seed)
	}
}

func TestGenerateRejectsInvalidRequests(t *testing.T) {
	cases := []struct {
		name string
		p    GenerateParams
	}{
		{"missing prompt", GenerateParams{Model: "sdxl-1.0"}},
		{"missing model", GenerateParams{Prompt: "x"}},
		{"width not multiple of 8", GenerateParams{Model: "m", Prompt: "x", Width: 1001, Height: 1024}},
		{"too small", GenerateParams{Model: "m", Prompt: "x", Width: 128, Height: 1024}},
		{"too large", GenerateParams{Model: "m", Prompt: "x", Width: 2048, Height: 1024}},
	}
	svc, _, _, _ := newTestService(nil, nil, 2)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := svc.Generate(context.Background(), "t1", tc.p); !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("err = %v, want ErrInvalidRequest", err)
			}
		})
	}
}

func TestGenerateRejectsTooManyImages(t *testing.T) {
	svc, _, _, _ := newTestService(nil, nil, 2)
	_, err := svc.Generate(context.Background(), "t1", GenerateParams{
		Model: "m", Prompt: "x", NumImages: 5,
	})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("err = %v, want ErrInvalidRequest", err)
	}
}

func TestGenerateModelNotFound(t *testing.T) {
	svc, _, _, _ := newTestService(nil, fakeResolver{err: ErrNotFound}, 1)
	_, err := svc.Generate(context.Background(), "t1", GenerateParams{Model: "nope", Prompt: "x"})
	if !errors.Is(err, ErrModelNotFound) {
		t.Fatalf("err = %v, want ErrModelNotFound", err)
	}
}

func TestGetAssetReturnsBytesTenantScoped(t *testing.T) {
	svc, _, _, _ := newTestService(nil, nil, 1)
	got, err := svc.Generate(context.Background(), "t1", GenerateParams{Model: "m", Prompt: "x", Seed: -1})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	_, raw, err := svc.GetAsset(context.Background(), "t1", got.Assets[0].ID)
	if err != nil {
		t.Fatalf("GetAsset: %v", err)
	}
	if len(raw) == 0 {
		t.Fatal("empty asset bytes")
	}
	if _, _, err := svc.GetAsset(context.Background(), "other", got.Assets[0].ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-tenant err = %v, want ErrNotFound", err)
	}
}

func TestCreateJobQueues(t *testing.T) {
	svc, _, _, _ := newTestService(nil, nil, 1)
	job, err := svc.CreateJob(context.Background(), "t1", GenerateParams{Model: "sdxl-1.0", Prompt: "a dog"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if job.Status != JobQueued {
		t.Errorf("status = %q, want %q", job.Status, JobQueued)
	}
	list, err := svc.ListJobs(context.Background(), "t1", 10)
	if err != nil || len(list) != 1 {
		t.Fatalf("ListJobs = %d, %v", len(list), err)
	}
	if _, err := svc.GetJob(context.Background(), "t1", job.ID); err != nil {
		t.Fatalf("GetJob: %v", err)
	}
}
