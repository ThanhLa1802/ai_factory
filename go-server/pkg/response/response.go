// Package response provides the shared JSON envelope helpers used by every
// HTTP handler (design §4.1).
package response

import "github.com/gin-gonic/gin"

// WriteJSON writes an arbitrary value with the given status code.
func WriteJSON(c *gin.Context, status int, v any) {
	c.JSON(status, v)
}

// WriteAPIError writes the control-plane error envelope
// {"error":{"code","message"}}.
func WriteAPIError(c *gin.Context, status int, code, msg string) {
	c.JSON(status, gin.H{"error": gin.H{"code": code, "message": msg}})
}

// WriteOpenAIError writes the OpenAI error envelope
// {"error":{"type","message","code"}}.
func WriteOpenAIError(c *gin.Context, status int, typ, msg string) {
	c.JSON(status, gin.H{"error": gin.H{"type": typ, "message": msg, "code": status}})
}
