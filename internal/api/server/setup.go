package transport

import (
	"os"
	"time"

	"github.com/Yonathandj/go-template/internal/api/server/middleware"
	"github.com/Yonathandj/go-template/pkg/logger"
	"github.com/gin-gonic/gin"
	"github.com/spf13/viper"
)

func Setup(log logger.Logger) *gin.Engine {
	gin.SetMode(os.Getenv("GIN_MODE"))
	r := gin.New()
	r.SetTrustedProxies(nil)
	r.Use(
		middleware.RequestID(),
		middleware.Logger(log),
		middleware.Recovery(log),
		middleware.Timeout(viper.GetDuration("server.timeout")*time.Second),
	)
	return r
}
