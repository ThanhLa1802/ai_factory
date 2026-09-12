package inference

import (
	"github.com/ai-factory/go-server/internal/infrastructure/middleware"
	"github.com/gin-gonic/gin"
)

// RegisterRoutes registers all inference HTTP routes: chat completions, health,
// session history, and the static test UI.
func (h *Handler) RegisterRoutes(e *gin.Engine) {
	e.POST("/v1/chat/completions", middleware.InferenceAuth(h.auth), h.handleOpenAIChatCompletions)
	e.GET("/health", h.handleHealth)

	sessions := e.Group("/api/v1/sessions", middleware.RequireAuth(h.auth))
	sessions.GET("", h.handleListSessions)
	sessions.GET("/:id", h.handleGetSession)
	sessions.PATCH("/:id", h.handleRenameSession)
	sessions.DELETE("/:id", h.handleDeleteSession)

	// Static UI for testing
	e.GET("/", h.handleUI)
	e.GET("/chat", h.handleUI)
	e.GET("/keys", h.handleUI)
	e.GET("/concepts", h.handleConcepts)
}
