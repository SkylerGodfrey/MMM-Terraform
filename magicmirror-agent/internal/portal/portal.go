// Package portal serves the family-facing web UI. Unlike /api/v1 (the
// Terraform control plane, Bearer-key authenticated), the portal is
// intentionally unauthenticated: it is data-plane only, the agent binds to
// the LAN, and family members can't manage API keys (HOM-60).
package portal

import (
	_ "embed"
	"net/http"

	"github.com/gin-gonic/gin"
)

//go:embed web/index.html
var indexHTML []byte

// Register mounts the portal page and returns the /portal/api route group
// that feature handlers (chores, photos) attach to.
func Register(router *gin.Engine) *gin.RouterGroup {
	page := func(c *gin.Context) {
		c.Data(http.StatusOK, "text/html; charset=utf-8", indexHTML)
	}
	router.GET("/portal", page)
	router.GET("/portal/", page)

	return router.Group("/portal/api")
}
