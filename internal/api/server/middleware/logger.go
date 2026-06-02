package middleware

import (
	"time"

	"github.com/gin-gonic/gin"
)

type Logger interface {
	Info(msg string, args ...any)
	Error(msg string, args ...any)
}

func LoggerMiddleware(log Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()

		latency := time.Since(start)

		requestID, _ := c.Get("request_id")
		log.Info(
			"incoming request", "request_id", requestID.(string),
			"method", c.Request.Method, "path", c.Request.URL.Path,
			"status", c.Writer.Status(), "latency", latency,
			"ip", c.ClientIP(), "user_agent", c.Request.UserAgent(),
		)
	}
}
