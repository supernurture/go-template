package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/yonathandj/go-template/pkg/logger"
)

func Recovery(log logger.Logger) gin.HandlerFunc {
	return gin.CustomRecovery(func(c *gin.Context, err any) {

		log.Error(
			"panic recovered",
			"error", err,
		)

		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
			"error": "internal server error",
		})
	})
}
