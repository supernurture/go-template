package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

func Recovery(log Logger) gin.HandlerFunc {
	return gin.CustomRecovery(func(c *gin.Context, err any) {
		log.Error("panic recovered", "error", err)

		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
			"error": "internal server error",
		})
	})
}
