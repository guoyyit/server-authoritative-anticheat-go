package transport

import "errors"

var (
	// ErrPipeFull is returned when a non-blocking send cannot enqueue.
	ErrPipeFull = errors.New("transport: pipe full")
	// ErrPipeClosed is returned when receiving from a closed pipe.
	ErrPipeClosed = errors.New("transport: pipe closed")
)
