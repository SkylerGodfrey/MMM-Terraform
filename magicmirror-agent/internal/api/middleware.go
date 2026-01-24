package api

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// authMiddleware validates the API key
func (s *Server) authMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if s.config.Auth.APIKey == "" {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": "API key not configured on server",
			})
			c.Abort()
			return
		}

		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.JSON(http.StatusUnauthorized, gin.H{
				"error": "Authorization header required",
			})
			c.Abort()
			return
		}

		// Support both "Bearer <key>" and "ApiKey <key>" formats
		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 {
			c.JSON(http.StatusUnauthorized, gin.H{
				"error": "Invalid authorization header format",
			})
			c.Abort()
			return
		}

		scheme := strings.ToLower(parts[0])
		if scheme != "bearer" && scheme != "apikey" {
			c.JSON(http.StatusUnauthorized, gin.H{
				"error": "Authorization scheme must be Bearer or ApiKey",
			})
			c.Abort()
			return
		}

		if parts[1] != s.config.Auth.APIKey {
			c.JSON(http.StatusUnauthorized, gin.H{
				"error": "Invalid API key",
			})
			c.Abort()
			return
		}

		c.Next()
	}
}
