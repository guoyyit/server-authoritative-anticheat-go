// Package server ties transports to per-match engines and binds new matches
// to the currently active rule version.
package server

import (
	"sync"

	"github.com/guoyyit/server-authoritative-anticheat-go/internal/config"
	"github.com/guoyyit/server-authoritative-anticheat-go/internal/engine"
	"github.com/guoyyit/server-authoritative-anticheat-go/internal/model"
	"github.com/guoyyit/server-authoritative-anticheat-go/internal/rules"
)

// Server owns the match registry and rule registry.
type Server struct {
	cfg      *config.Config
	registry *rules.Registry

	mu      sync.Mutex
	matches map[string]*engine.MatchEngine
}

// New creates a server.
func New(cfg *config.Config, registry *rules.Registry) *Server {
	if cfg == nil {
		cfg = config.DefaultConfig()
	}
	return &Server{
		cfg:      cfg,
		registry: registry,
		matches:  make(map[string]*engine.MatchEngine),
	}
}

// StartMatch creates and starts an engine bound to the active rule version.
func (s *Server) StartMatch(matchID string) (*engine.MatchEngine, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.matches[matchID]; ok {
		return nil, ErrMatchExists
	}
	rs := s.registry.Current()
	if rs == nil {
		return nil, ErrNoActiveRules
	}
	e := engine.New(matchID, rs, s.cfg)
	e.Start()
	s.matches[matchID] = e
	return e, nil
}

// Match returns the engine for a match.
func (s *Server) Match(matchID string) (*engine.MatchEngine, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.matches[matchID]
	return e, ok
}

// Submit routes an operation to its match.
func (s *Server) Submit(op *model.Operation) (*model.Verdict, error) {
	e, ok := s.Match(op.MatchID)
	if !ok {
		return nil, ErrUnknownMatch
	}
	return e.Submit(op), nil
}

// Stop stops all match engines.
func (s *Server) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.matches {
		e.Stop()
	}
}
