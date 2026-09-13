package serving

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/ai-factory/go-server/internal/infrastructure/database"
	"github.com/google/uuid"
)

func TestModelRoundTrip(t *testing.T) {
	m := Model{ID: "m1", Name: "deepseek-v3", Task: "text-generation", Framework: "vllm"}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out Model
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Name != "deepseek-v3" || out.Framework != "vllm" {
		t.Errorf("out = %+v", out)
	}
}

// TestListVersionsIntegration runs against a live Postgres (set
// AI_FACTORY_DATABASE_URL). It lists every version of a model and a template,
// newest first.
func TestListVersionsIntegration(t *testing.T) {
	dsn := os.Getenv("AI_FACTORY_DATABASE_URL")
	if dsn == "" {
		t.Skip("AI_FACTORY_DATABASE_URL not set; skipping integration test")
	}
	ctx := context.Background()
	d, err := database.Open(dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	if err := database.Migrate(d.Gorm()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	s := NewServiceFromGorm(d.Gorm())

	model, err := s.CreateModel(ctx, Model{Name: "lv-model-" + uuid.NewString()[:8], Task: "chat", Framework: "transformers"})
	if err != nil {
		t.Fatalf("create model: %v", err)
	}
	for _, v := range []string{"v1", "v2"} {
		if _, err := s.CreateModelVersion(ctx, ModelVersion{ModelID: model.ID, Version: v, ArtifactURI: "local://m"}); err != nil {
			t.Fatalf("create model version %s: %v", v, err)
		}
	}
	tpl, err := s.CreateTemplate(ctx, ServingTemplate{Name: "lv-template-" + uuid.NewString()[:8], Runtime: "transformers"})
	if err != nil {
		t.Fatalf("create template: %v", err)
	}
	for _, v := range []string{"v1", "v2"} {
		if _, err := s.CreateTemplateVersion(ctx, TemplateVersion{TemplateID: tpl.ID, Version: v, Image: "img:latest"}); err != nil {
			t.Fatalf("create template version %s: %v", v, err)
		}
	}

	mvs, err := s.ListModelVersions(ctx, model.ID)
	if err != nil {
		t.Fatalf("list model versions: %v", err)
	}
	if len(mvs) != 2 {
		t.Fatalf("model versions = %d, want 2", len(mvs))
	}
	tvs, err := s.ListTemplateVersions(ctx, tpl.ID)
	if err != nil {
		t.Fatalf("list template versions: %v", err)
	}
	if len(tvs) != 2 {
		t.Fatalf("template versions = %d, want 2", len(tvs))
	}

	t.Cleanup(func() {
		_ = d.Gorm().Exec(`DELETE FROM models WHERE id = $1`, model.ID)
		_ = d.Gorm().Exec(`DELETE FROM serving_templates WHERE id = $1`, tpl.ID)
	})
}
