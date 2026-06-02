package server

type Service struct {
}

// NewService creates a new instance of the Service.
// This function initializes the Service struct and returns a pointer to it.
// It can be used to set up any necessary configurations or dependencies for the Service before it is used.
func NewService() *Service {
	return &Service{}
}
