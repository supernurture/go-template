package server

import (
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/supernurture/go-template/internal/api/server/modules/example"
	"github.com/supernurture/go-template/internal/api/server/modules/health"
	examplecontract "github.com/supernurture/go-template/internal/api/server/oapicodegen/example"
	healthcontract "github.com/supernurture/go-template/internal/api/server/oapicodegen/health"
	"github.com/supernurture/go-template/internal/config"
	"github.com/supernurture/go-template/internal/container"
	"github.com/supernurture/go-template/internal/middleware"
)

// NewRouter builds the gin engine: mode, trusted proxies, the middleware chain, and every module's generated routes.
func NewRouter(cfg *config.Config, deps *container.Container) (*gin.Engine, error) {
	gin.SetMode(cfg.Server.Mode)

	router := gin.New()
	if err := router.SetTrustedProxies(cfg.Server.TrustedProxies); err != nil {
		return nil, fmt.Errorf("set trusted proxies: %w", err)
	}

	// Without this, a handler's *gin.Context carries no deadline and the timeout never reaches downstream calls.
	router.ContextWithFallback = true
	router.Use(middleware.Default(cfg, deps.Logger)...)

	register(router, deps)
	return router, nil
}

func register(router gin.IRouter, deps *container.Container) {
	healthcontract.RegisterHandlersWithOptions(router,
		healthcontract.NewStrictHandlerWithOptions(health.NewHandler(deps.Pings(), deps.Logger), nil, healthOptions),
		healthcontract.GinServerOptions{ErrorHandler: invalidParam})

	if client, db := deps.Redis["example"], deps.Postgres["example"]; client != nil && db != nil {
		service := example.NewService(client, example.NewRepository(db))
		handler := example.NewHandler(service, deps.Logger)
		examplecontract.RegisterHandlersWithOptions(router,
			examplecontract.NewStrictHandlerWithOptions(handler, nil, exampleOptions),
			examplecontract.GinServerOptions{ErrorHandler: invalidParam})
	} else {
		deps.Logger.Warn("module not mounted: a dependency it needs is not configured", map[string]any{
			"module": "example",
			"needs":  []string{"redis.example", "databases.postgres.example"},
		})
	}
}

// The generated defaults write err.Error() into the body, leaking internals such as database errors,
// and answer {"msg": ...} where the spec's Error, Recovery and Timeout all use "message".
var (
	healthOptions = healthcontract.StrictGinServerOptions{
		RequestErrorHandlerFunc: badRequest, HandlerErrorFunc: internalError, ResponseErrorHandlerFunc: internalError,
	}
	exampleOptions = examplecontract.StrictGinServerOptions{
		RequestErrorHandlerFunc: badRequest, HandlerErrorFunc: internalError, ResponseErrorHandlerFunc: internalError,
	}
)

// invalidParam answers a path or query parameter that does not parse; the message names the parameter.
func invalidParam(c *gin.Context, err error, status int) {
	_ = c.Error(err)
	c.JSON(status, gin.H{"message": err.Error()})
}

// badRequest answers a body the server could not decode; the decoder's message is about the caller's input.
func badRequest(c *gin.Context, err error) {
	_ = c.Error(err)
	switch tooLarge := (*http.MaxBytesError)(nil); {
	case errors.As(err, &tooLarge):
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"message": "request body too large"})
	case errors.Is(err, io.EOF):
		c.JSON(http.StatusBadRequest, gin.H{"message": "a JSON body is required"})
	default:
		c.JSON(http.StatusBadRequest, gin.H{"message": err.Error()})
	}
}

// internalError keeps the cause in c.Errors for AccessLog and out of the response.
func internalError(c *gin.Context, err error) {
	_ = c.Error(err)
	// Left unwritten past the deadline, so Timeout can answer 504; and a response already
	// on the wire (a failed write) must not get a second body appended.
	if c.Writer.Written() || c.Request.Context().Err() != nil {
		c.Status(http.StatusInternalServerError)
		return
	}
	c.JSON(http.StatusInternalServerError, gin.H{"message": "internal server error"})
}
