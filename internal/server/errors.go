package server

import "errors"

var (
	ErrMatchExists   = errors.New("server: match already exists")
	ErrUnknownMatch  = errors.New("server: unknown match")
	ErrNoActiveRules = errors.New("server: no active rule version")
)
