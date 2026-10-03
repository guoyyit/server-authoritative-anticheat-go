// Package model defines the core data types for server-authoritative
// match validation: client operations, authoritative match state, and
// verdict records.
package model

// Vec3 is a 3D position/velocity vector in authoritative world units.
type Vec3 struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	Z float64 `json:"z"`
}

// OpKind identifies the kind of a client-reported operation.
type OpKind string

const (
	// OpMove reports a position change.
	OpMove OpKind = "move"
	// OpAttack reports an attack against another player.
	OpAttack OpKind = "attack"
)

// Operation is a client-reported action. Sequence numbers are allocated per
// player and are expected (but not guaranteed) to arrive in order.
type Operation struct {
	MatchID    string  `json:"match_id"`
	PlayerID   string  `json:"player_id"`
	Seq        uint64  `json:"seq"`
	ClientTime int64   `json:"client_time"` // client timestamp, unix milliseconds
	Kind       OpKind  `json:"kind"`
	Position   Vec3    `json:"position"`    // target position for move
	TargetID   string  `json:"target_id"`   // target player for attack
	WeaponID   string  `json:"weapon_id"`
	Damage     float64 `json:"damage"` // reported damage for attack
	Nonce      uint64  `json:"nonce"`
	// MAC is a hex-encoded HMAC-SHA256 over the canonical operation fields.
	// The server rejects operations whose MAC does not match as tampered.
	MAC string `json:"mac"`
}

// PlayerState is the server-authoritative state of a single player in a match.
type PlayerState struct {
	PlayerID string `json:"player_id"`
	Position Vec3   `json:"position"`

	// sequencing
	AppliedSeq  uint64              `json:"applied_seq"`
	AppliedSeqs map[uint64]struct{} `json:"-"` // bounded dedup window

	// movement timing (client timestamps)
	LastMoveTime   int64 `json:"last_move_time"`
	LastOpTime     int64 `json:"last_op_time"`
	LastServerTime int64 `json:"last_server_time"`

	// combat
	Health       float64   `json:"health"`
	AttackTimes  []int64   `json:"-"` // client times of recent attacks (window)
	AttackTokens float64   `json:"-"` // token-bucket tokens for attack rate
	Resource     float64   `json:"resource"`
	LastRegenTime int64    `json:"last_regen_time"`

	// violation bookkeeping
	SpeedViolations  int `json:"speed_violations"`
	AttackViolations int `json:"attack_violations"`
	LastAttackTime   int64 `json:"last_attack_time"`

	// stats
	AppliedCount  uint64 `json:"applied_count"`
	RejectedCount uint64 `json:"rejected_count"`
}

// MatchState is the server-authoritative state of a match.
type MatchState struct {
	MatchID     string                   `json:"match_id"`
	RuleVersion string                   `json:"rule_version"`
	Players     map[string]*PlayerState  `json:"players"`
	Version     uint64                   `json:"version"` // increments per applied operation
	StartedAt   int64                    `json:"started_at"`
	Finished    bool                     `json:"finished"`
}

// VerdictKind classifies the outcome of validating one operation.
type VerdictKind string

const (
	// VerdictAccepted means the operation passed validation and was applied
	// to the authoritative state. Findings may still be attached (warnings).
	VerdictAccepted VerdictKind = "accepted"
	// VerdictRejected means the operation was not applied; Reasons and
	// Findings explain why.
	VerdictRejected VerdictKind = "rejected"
)

// ReasonCode gives a machine-distinguishable rejection reason.
type ReasonCode string

const (
	// ReasonDuplicate: same (player, seq) was already seen.
	ReasonDuplicate ReasonCode = "duplicate"
	// ReasonRollback: seq regressed below the applied frontier (late arrival
	// or forged rollback); the operation is never applied.
	ReasonRollback ReasonCode = "rollback"
	// ReasonSeqJump: seq skips more than the configured gap.
	ReasonSeqJump ReasonCode = "seq_jump"
	// ReasonForgedTime: client timestamp is skewed, in the future, or not
	// monotonic.
	ReasonForgedTime ReasonCode = "forged_time"
	// ReasonTampered: MAC verification failed (operation altered in transit
	// or forged).
	ReasonTampered ReasonCode = "tampered"
	// ReasonQueueFull: the per-match wait queue is at its configured bound.
	ReasonQueueFull ReasonCode = "queue_full"
	// ReasonCheat: a game-play rule (speed, damage, resource, ...) was violated.
	ReasonCheat ReasonCode = "cheat"
	// ReasonInvalid: malformed operation (missing fields, bad kind, ...).
	ReasonInvalid ReasonCode = "invalid"
)

// Severity grades the explainable severity of a finding.
type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityWarning  Severity = "warning"
	SeverityMinor    Severity = "minor"
	SeverityMajor    Severity = "major"
	SeverityCritical Severity = "critical"
)

// SeverityRank maps severities to an ordered numeric rank.
func SeverityRank(s Severity) int {
	switch s {
	case SeverityInfo:
		return 0
	case SeverityWarning:
		return 1
	case SeverityMinor:
		return 2
	case SeverityMajor:
		return 3
	case SeverityCritical:
		return 4
	}
	return -1
}

// Finding is a single rule evaluation result with observed value, allowed
// range, severity and a human-readable basis.
type Finding struct {
	RuleID   string   `json:"rule_id"`
	Title    string   `json:"title"`
	Observed string   `json:"observed"`
	Allowed  string   `json:"allowed"`
	Severity Severity `json:"severity"`
	Basis    string   `json:"basis"`
}

// MaxSeverity returns the highest severity among findings.
func MaxSeverity(findings []Finding) Severity {
	max := SeverityInfo
	for _, f := range findings {
		if SeverityRank(f.Severity) > SeverityRank(max) {
			max = f.Severity
		}
	}
	return max
}

// Verdict is the traceable conclusion for one operation.
type Verdict struct {
	VerdictID     string      `json:"verdict_id"`
	MatchID       string      `json:"match_id"`
	PlayerID      string      `json:"player_id"`
	Seq           uint64      `json:"seq"`
	Kind          VerdictKind `json:"kind"`
	Reasons       []ReasonCode `json:"reasons,omitempty"`
	Findings      []Finding   `json:"findings,omitempty"`
	RuleVersion   string      `json:"rule_version"`
	StateVersion  uint64      `json:"state_version"` // committed state version after this verdict
	DecidedAt     int64       `json:"decided_at"`     // server time
}
