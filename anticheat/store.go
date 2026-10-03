package anticheat

import "sync"

// VerdictStore 保存判定与复核记录。判定一旦写入不可变；
// 复核以追加记录的方式存在，不改写原判定。
type VerdictStore struct {
	mu       sync.RWMutex
	nextID   uint64
	verdicts []Verdict
	reviews  map[uint64][]Review
}

func NewVerdictStore() *VerdictStore {
	return &VerdictStore{nextID: 1, reviews: make(map[uint64][]Review)}
}

// Append 追加一条判定，返回分配的判定 ID。
func (s *VerdictStore) Append(v Verdict) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	v.ID = s.nextID
	s.nextID++
	s.verdicts = append(s.verdicts, v)
	return v.ID
}

// AddReview 追加一条复核记录。原判定保持不变。
func (s *VerdictStore) AddReview(r Review) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reviews[r.VerdictID] = append(s.reviews[r.VerdictID], r)
}

// Verdicts 返回指定对局的全部判定（按写入顺序）。
func (s *VerdictStore) Verdicts(matchID string) []Verdict {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []Verdict
	for _, v := range s.verdicts {
		if v.MatchID == matchID {
			out = append(out, v)
		}
	}
	return out
}

// VerdictsByRuleVersion 按规则版本查询历史判定。
func (s *VerdictStore) VerdictsByRuleVersion(version string) []Verdict {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []Verdict
	for _, v := range s.verdicts {
		if v.RuleVersion == version {
			out = append(out, v)
		}
	}
	return out
}

// Reviews 返回某条判定的全部复核记录。
func (s *VerdictStore) Reviews(verdictID uint64) []Review {
	s.mu.RLock()
	defer s.mu.RUnlock()
	src := s.reviews[verdictID]
	out := make([]Review, len(src))
	copy(out, src)
	return out
}

// Verdict 按 ID 查询判定。
func (s *VerdictStore) Verdict(id uint64) (Verdict, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, v := range s.verdicts {
		if v.ID == id {
			return v, true
		}
	}
	return Verdict{}, false
}

// EffectiveSeverity 返回考虑复核后的有效严重等级（原记录不变）。
// 若最近一次复核推翻了判定，返回 SeverityNone。
func (s *VerdictStore) EffectiveSeverity(verdictID uint64) (Severity, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var v *Verdict
	for i := range s.verdicts {
		if s.verdicts[i].ID == verdictID {
			v = &s.verdicts[i]
			break
		}
	}
	if v == nil {
		return SeverityNone, false
	}
	sev := v.Severity
	for _, r := range s.reviews[verdictID] {
		switch r.Outcome {
		case ReviewOverturned:
			sev = SeverityNone
		case ReviewAdjusted:
			sev = r.NewSeverity
		case ReviewUpheld:
			// 维持
		}
	}
	return sev, true
}
