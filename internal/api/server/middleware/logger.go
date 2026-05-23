package middleware

import (
	"time"

	"github.com/Yonathandj/go-template/pkg/logger"
	"github.com/gin-gonic/gin"
)

func Logger(log logger.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()

		c.Next()

		latency := time.Since(start)

		requestID, _ := c.Get(RequestIDKey)

		log.Info("incoming request",
			"request_id", requestID.(string),
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
			"status", c.Writer.Status(),
			"latency", latency,
			"ip", c.ClientIP(),
			"user_agent", c.Request.UserAgent(),
		)
	}
}
