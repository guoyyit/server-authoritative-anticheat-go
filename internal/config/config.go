// Package config holds all tunable bounds and tolerances. Every bound has a
// configurable upper limit; every tolerance is meant to absorb normal network
// jitter while still distinguishing real cheating.
package config

import (
	"encoding/json"
	"os"

	"github.com/guoyyit/server-authoritative-anticheat-go/internal/model"
)

// Config groups all engine/rule knobs.
type Config struct {
	// --- bounds (upper limits) ---
	InboxCapacity        int `json:"inbox_capacity"`         // per-match wait queue
	MaxPendingPerPlayer  int `json:"max_pending_per_player"` // in-flight out-of-order ops
	MaxSeqGap            uint64 `json:"max_seq_gap"`          // max tolerated seq jump
	DedupWindowSize      int `json:"dedup_window_size"`      // recent applied seqs remembered
	VerdictStoreCapacity int `json:"verdict_store_capacity"` // per-match decision records
	ReplayLogCapacity    int `json:"replay_log_capacity"`    // per-match replay entries

	// --- movement rule ---
	MaxSpeed           float64 `json:"max_speed"`            // m/s, authoritative cap
	SpeedTolerance     float64 `json:"speed_tolerance"`      // fraction inside which no finding (jitter band)
	SpeedWarnRatio     float64 `json:"speed_warn_ratio"`     // ratio above which a warning is raised
	SpeedMinorRatio    float64 `json:"speed_minor_ratio"`
	SpeedMajorRatio    float64 `json:"speed_major_ratio"`
	MaxTeleportDistance float64 `json:"max_teleport_distance"` // m, instant displacement cap

	// --- combat rule ---
	MaxAttackRate  float64 `json:"max_attack_rate"`  // attacks per second
	AttackBurst    int     `json:"attack_burst"`     // token-bucket burst (jitter absorption)
	MaxDamage      float64 `json:"max_damage"`       // per-hit damage cap
	ResourceMax    float64 `json:"resource_max"`
	ResourceRegen  float64 `json:"resource_regen"`  // per second
	AttackCost     float64 `json:"attack_cost"`     // resource per attack
	PlayerMaxHealth float64 `json:"player_max_health"`

	// --- time rule ---
	TimeSkewAllowanceMS  int64 `json:"time_skew_allowance_ms"`  // |client - server| tolerated
	FutureAllowanceMS    int64 `json:"future_allowance_ms"`     // client ahead of server tolerated
	TimeDivergenceFactor float64 `json:"time_divergence_factor"` // client dt vs server dt divergence
	DivergenceMinServerMS int64  `json:"divergence_min_server_ms"`   // min server interval to evaluate divergence

	// --- policy ---
	RejectThreshold model.Severity `json:"reject_threshold"` // findings >= this reject the op

	// --- security ---
	MACKey string `json:"mac_key"`
}

// DefaultConfig returns the production defaults.
func DefaultConfig() *Config {
	return &Config{
		InboxCapacity:        256,
		MaxPendingPerPlayer:  64,
		MaxSeqGap:            128,
		DedupWindowSize:      256,
		VerdictStoreCapacity: 4096,
		ReplayLogCapacity:    8192,

		MaxSpeed:            10.0,
		SpeedTolerance:      0.20,
		SpeedWarnRatio:      1.3,
		SpeedMinorRatio:     1.6,
		SpeedMajorRatio:     2.0,
		MaxTeleportDistance: 100.0,

		MaxAttackRate:   4.0,
		AttackBurst:     6,
		MaxDamage:       50.0,
		ResourceMax:     100.0,
		ResourceRegen:   5.0,
		AttackCost:      10.0,
		PlayerMaxHealth: 100.0,

		TimeSkewAllowanceMS:   5000,
		FutureAllowanceMS:     1000,
		TimeDivergenceFactor:  2.0,
		DivergenceMinServerMS: 1000,

		RejectThreshold: model.SeverityMinor,
		MACKey:          "dev-only-in-process-mac-key",
	}
}

// Load reads a JSON config file, falling back to defaults for missing fields.
func Load(path string) (*Config, error) {
	cfg := DefaultConfig()
	if path == "" {
		return cfg, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}
