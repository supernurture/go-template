package middleware

import (
	"net/http"
	"runtime/debug"

	"github.com/Yonathandj/go-template/pkg/logger"
	"github.com/gin-gonic/gin"
)

func Recovery(log logger.Logger) gin.HandlerFunc {
	return gin.CustomRecovery(func(c *gin.Context, recovered any) {

		log.Error("panic recovered",
			"error", recovered,
			"stack", string(debug.Stack()),
		)

		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
			"meta": gin.H{
				"success": false,
				"message": "internal server error",
			},
		})
	})
}
