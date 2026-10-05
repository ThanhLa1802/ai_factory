package image

import (
	"github.com/ai-factory/go-server/internal/infrastructure/middleware"
	"github.com/gin-gonic/gin"
)

// RegisterRoutes mounts the image-generation routes. The OpenAI endpoint uses
// InferenceAuth (JWT or API key); the management/asset routes use JWT auth.
func (h *Handler) RegisterRoutes(e *gin.Engine) {
	e.POST("/v1/images/generations", middleware.InferenceAuth(h.auth), h.handleGenerate)

	g := e.Group("/api/v1/images", middleware.RequireAuth(h.auth))
	g.GET("/assets", h.handleListAssets)
	g.GET("/assets/:id", h.handleGetAsset)
	g.POST("/jobs", h.handleCreateJob)
	g.GET("/jobs", h.handleListJobs)
	g.GET("/jobs/:id", h.handleGetJob)
}
