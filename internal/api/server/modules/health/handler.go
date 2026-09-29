package health

import (
	"context"

	healthcontract "github.com/supernurture/go-template/internal/api/server/oapicodegen/health"
	"github.com/supernurture/go-template/internal/middleware"
	"github.com/supernurture/go-template/pkg/logger"
)

type Handler struct {
	checks map[string]func(context.Context) error
	log    *logger.Logger
}

// NewHandler takes the readiness checks keyed by dependency name, as container.Pings returns them.
func NewHandler(checks map[string]func(context.Context) error, log *logger.Logger) *Handler {
	return &Handler{checks: checks, log: log}
}

var _ healthcontract.StrictServerInterface = (*Handler)(nil)

func (h *Handler) GetHealth(
	_ context.Context, _ healthcontract.GetHealthRequestObject) (healthcontract.GetHealthResponseObject, error) {
	return healthcontract.GetHealth200JSONResponse{Condition: "Healthy"}, nil
}

// GetReady runs every check, not just up to the first failure, so the log names every dependency that is down.
func (h *Handler) GetReady(
	ctx context.Context, _ healthcontract.GetReadyRequestObject) (healthcontract.GetReadyResponseObject, error) {
	ctx = middleware.RequestContext(ctx)

	ready := true
	for name, check := range h.checks {
		if err := check(ctx); err != nil {
			ready = false
			h.log.Warn("readiness check failed", map[string]any{
				"request_id": middleware.RequestIDFrom(ctx),
				"dependency": name,
				"error":      err.Error(),
			})
		}
	}

	if !ready {
		return healthcontract.GetReady503JSONResponse{Condition: "NotReady"}, nil
	}
	return healthcontract.GetReady200JSONResponse{Condition: "Ready"}, nil
}
