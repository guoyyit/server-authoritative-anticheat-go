package anticheat

// Replay 用指定规则版本重放一份操作日志，返回重放产生的判定。
// 判定只依赖操作字段与逻辑时钟，因此相同日志 + 相同规则版本
// 必然得到与在线判定逐条一致的结果（除判定 ID 外，ID 由存储分配）。
func Replay(matchID string, journal []Operation, rules *RuleSet, cfg Config) []Verdict {
	store := NewVerdictStore()
	// 从日志中恢复玩家集合。
	seen := map[string]bool{}
	var players []string
	for _, op := range journal {
		if !seen[op.PlayerID] {
			seen[op.PlayerID] = true
			players = append(players, op.PlayerID)
		}
	}
	s := newSession(matchID, players, rules, cfg, store)
	for _, op := range journal {
		s.Submit(op)
	}
	return store.Verdicts(matchID)
}
