package rules

import "errors"

// ErrUnknownVersion is returned when a rule version is not registered.
var ErrUnknownVersion = errors.New("rules: unknown rule version")
