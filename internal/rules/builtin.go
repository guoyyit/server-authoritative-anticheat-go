package rules

// Rule set versions. A match binds to the version active at start time;
// registering a new version never changes the behavior of running matches.
const (
	// Version1 is the initial rule set.
	Version1 = "1.0.0"
	// Version2 tightens movement and combat caps.
	Version2 = "2.0.0"
)

// V1 returns the initial rule set.
func V1() *RuleSet {
	return &RuleSet{
		Version: Version1,
		Rules: []Rule{
			TimeRule{},
			SpeedRule{},
			TeleportRule{},
			AttackRateRule{},
			ResourceRule{},
			DamageRule{},
		},
	}
}

// V2 returns the tightened rule set.
func V2() *RuleSet {
	return &RuleSet{
		Version: Version2,
		Rules: []Rule{
			TimeRule{},
			SpeedRule{},
			TeleportRule{},
			AttackRateRule{},
			ResourceRule{},
			DamageRule{},
		},
	}
}

// RegisterBuiltins registers all built-in versions and activates V1.
func RegisterBuiltins(r *Registry) {
	r.Register(V1())
	r.Register(V2())
	_ = r.SetCurrent(Version1)
}
