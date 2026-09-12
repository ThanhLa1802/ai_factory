package api

import (
	"github.com/ai-factory/go-server/pkg/response"
	"github.com/gin-gonic/gin"
)

// JSON response helpers shared by the inference and control-plane handlers.
// The envelopes live in internal/infrastructure/response.

func writeJSON(c *gin.Context, status int, v any) {
	response.WriteJSON(c, status, v)
}

func writeAPIError(c *gin.Context, status int, code, msg string) {
	response.WriteAPIError(c, status, code, msg)
}

func writeOpenAIError(c *gin.Context, status int, typ, msg string) {
	response.WriteOpenAIError(c, status, typ, msg)
}
