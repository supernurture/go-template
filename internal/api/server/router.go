package server

import (
	"time"

	"github.com/gin-gonic/gin"
	"github.com/spf13/viper"
	"github.com/yonathandj/go-template/internal/api/server/middleware"
	"github.com/yonathandj/go-template/pkg/logger"
)

func RouterInit(log logger.Logger) *gin.Engine {
	gin.SetMode(viper.GetString("server.mode"))

	router := gin.New()
	router.Use(
		middleware.RequestID(),
		middleware.Timeout(viper.GetDuration("server.timeout")*time.Second),
		middleware.Recovery(log), // always put recovery middleware at the end of the middleware chain
	)

	return router
}
