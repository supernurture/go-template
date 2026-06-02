package server

import "context"

type Service struct {
}

var (
	shutdowns            []func() error
	shutdownsWithContext []func(context.Context) error
)

// NewService creates a new instance of the Service.
// This function initializes the Service struct and returns a pointer to it.
// It can be used to set up any necessary configurations or dependencies for the Service before it is used.
func NewService() *Service {
	return &Service{}
}

func (s *Service) Shutdown() error {
	for _, fn := range shutdowns {
		if err := fn(); err != nil {
			return err
		}
	}

	for _, fn := range shutdownsWithContext {
		if err := fn(context.Background()); err != nil {
			return err
		}
	}

	return nil
}
