package server

import (
	"time"

	"github.com/Yonathandj/go-template/internal/api/server/middleware"
	"github.com/gin-gonic/gin"
	"github.com/spf13/viper"
)

func RouterInit(log middleware.Logger) *gin.Engine {
	gin.SetMode(viper.GetString("server.mode"))

	router := gin.New()
	router.Use(
		middleware.RequestID(),
		middleware.Timeout(viper.GetDuration("server.timeout")*time.Second), // set timeout for all requests
		middleware.LoggerMiddleware(log),
		middleware.Recovery(log), // always put recovery middleware at the end of the middleware chain
	)

	return router
}
