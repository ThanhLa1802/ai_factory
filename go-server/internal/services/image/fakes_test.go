package image

import (
	"context"

	infraimage "github.com/ai-factory/go-server/internal/infrastructure/image"
	"github.com/ai-factory/go-server/internal/infrastructure/middleware"
)

// --- fakes ---

type fakeGenerator struct {
	lastReq infraimage.GenerateRequest
	result  *infraimage.GenerateResult
	err     error
	calls   int
}

func (f *fakeGenerator) Generate(_ context.Context, req infraimage.GenerateRequest) (*infraimage.GenerateResult, error) {
	f.calls++
	f.lastReq = req
	if f.err != nil {
		return nil, f.err
	}
	return f.result, nil
}

func (f *fakeGenerator) Stream(_ context.Context, req infraimage.GenerateRequest) (<-chan infraimage.ProgressEvent, error) {
	f.lastReq = req
	ch := make(chan infraimage.ProgressEvent)
	close(ch)
	return ch, nil
}

type fakeStorage struct{ files map[string][]byte }

func (s *fakeStorage) Save(id string, data []byte) (string, error) {
	if s.files == nil {
		s.files = map[string][]byte{}
	}
	path := id + ".png"
	s.files[path] = data
	return path, nil
}

func (s *fakeStorage) Load(path string) ([]byte, error) { return s.files[path], nil }
func (s *fakeStorage) Delete(path string) error         { delete(s.files, path); return nil }

type fakeResolver struct{ err error }

func (r fakeResolver) ResolveDeployment(_ context.Context, tenantID, _ string) (*ResolvedDeployment, error) {
	if r.err != nil {
		return nil, r.err
	}
	return &ResolvedDeployment{ID: "d1", TenantID: tenantID, Region: "local"}, nil
}

type memAssetRepo struct{ items []*Asset }

func (r *memAssetRepo) Create(_ context.Context, a *Asset) error {
	r.items = append(r.items, a)
	return nil
}

func (r *memAssetRepo) Get(_ context.Context, tenantID, id string) (*Asset, error) {
	for _, a := range r.items {
		if a.ID == id && a.TenantID == tenantID {
			return a, nil
		}
	}
	return nil, ErrNotFound
}

func (r *memAssetRepo) List(_ context.Context, tenantID string, limit int) ([]Asset, error) {
	var out []Asset
	for _, a := range r.items {
		if a.TenantID == tenantID {
			out = append(out, *a)
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

type memJobRepo struct{ items []*Job }

func (r *memJobRepo) Create(_ context.Context, j *Job) error {
	r.items = append(r.items, j)
	return nil
}

func (r *memJobRepo) Get(_ context.Context, tenantID, id string) (*Job, error) {
	for _, j := range r.items {
		if j.ID == id && j.TenantID == tenantID {
			return j, nil
		}
	}
	return nil, ErrNotFound
}

func (r *memJobRepo) List(_ context.Context, tenantID string, limit int) ([]Job, error) {
	var out []Job
	for _, j := range r.items {
		if j.TenantID == tenantID {
			out = append(out, *j)
		}
	}
	return out, nil
}

func (r *memJobRepo) UpdateStatus(_ context.Context, id, status, errMsg string) error {
	for _, j := range r.items {
		if j.ID == id {
			j.Status = status
			j.Error = errMsg
		}
	}
	return nil
}

func (r *memJobRepo) UpdateProgress(_ context.Context, id string, step int) error {
	for _, j := range r.items {
		if j.ID == id {
			j.ProgressStep = step
		}
	}
	return nil
}

type fakeAuth struct{}

func (fakeAuth) ParseToken(string) (middleware.Principal, error) {
	return middleware.Principal{TenantID: "t1", UserID: "u1", Role: "platform_admin"}, nil
}
func (fakeAuth) AuthenticateAPIKey(context.Context, string) (middleware.Principal, error) {
	return middleware.Principal{TenantID: "t1", Role: "platform_admin"}, nil
}
func (fakeAuth) Allows(string, string) bool { return true }

// newTestService builds a Service over in-memory fakes with SDXL-base defaults.
func newTestService(gen *fakeGenerator, res DeploymentResolver, maxImages int) (*Service, *fakeStorage, *memAssetRepo, *memJobRepo) {
	if gen == nil {
		gen = &fakeGenerator{result: &infraimage.GenerateResult{
			PNG: [][]byte{{1, 2, 3}}, Width: 1024, Height: 1024, Seed: 42, Steps: 25, Scheduler: "default",
		}}
	}
	if res == nil {
		res = fakeResolver{}
	}
	storage := &fakeStorage{}
	assets := &memAssetRepo{}
	jobs := &memJobRepo{}
	svc := NewService(
		Repositories{Assets: assets, Jobs: jobs},
		gen, storage, res,
		Config{DefaultWidth: 1024, DefaultHeight: 1024, DefaultSteps: 25, DefaultGuidance: 7, MaxImages: maxImages},
	)
	return svc, storage, assets, jobs
}
