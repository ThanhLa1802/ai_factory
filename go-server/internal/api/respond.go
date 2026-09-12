package api

import "github.com/gin-gonic/gin"

// JSON response helpers shared by the inference and control-plane handlers.

func writeJSON(c *gin.Context, status int, v any) {
	c.JSON(status, v)
}

func writeAPIError(c *gin.Context, status int, code, msg string) {
	c.JSON(status, gin.H{"error": gin.H{"code": code, "message": msg}})
}

func writeOpenAIError(c *gin.Context, status int, typ, msg string) {
	c.JSON(status, gin.H{"error": gin.H{"type": typ, "message": msg, "code": status}})
}
