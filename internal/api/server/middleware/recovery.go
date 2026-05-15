package middleware

import (
	"net/http"
	"runtime/debug"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

func Recovery(log *zap.Logger) gin.HandlerFunc {
	return gin.CustomRecovery(func(c *gin.Context, recovered any) {

		log.Error("panic recovered",
			zap.Any("error", recovered),
			zap.ByteString("stack", debug.Stack()),
		)

		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
			"meta": gin.H{
				"success": false,
				"message": "internal server error",
			},
		})
	})
}
