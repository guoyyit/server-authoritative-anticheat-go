package anticheat

import (
	"fmt"
	"sync"
)

// Engine 是顶层入口：管理多个对局，提供本地回环/内存管道式的提交通道。
// 整条链路只在本进程内运行，不依赖任何外部服务。
type Engine struct {
	cfg      Config
	registry *RuleRegistry
	store    *VerdictStore

	mu       sync.Mutex
	sessions map[string]*Session
}

func NewEngine(cfg Config, registry *RuleRegistry, store *VerdictStore) *Engine {
	if registry == nil {
		registry = NewRuleRegistry()
		registry.Register(DefaultRuleSet())
	}
	if store == nil {
		store = NewVerdictStore()
	}
	return &Engine{
		cfg:      cfg,
		registry: registry,
		store:    store,
		sessions: make(map[string]*Session),
	}
}

// StartMatch 以当前生效的规则版本开局，返回对局句柄。
// 对局一旦开始即绑定该版本，之后的规则升级不影响本局。
func (e *Engine) StartMatch(matchID string, playerIDs []string) (*Session, Receipt) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.sessions[matchID]; ok {
		return nil, Receipt{Accepted: false, Reason: RejectNone,
			Detail: "match " + matchID + " already exists"}
	}
	if len(e.sessions) >= e.cfg.MaxSessions {
		return nil, Receipt{Accepted: false, Reason: RejectQueueFull,
			Detail: fmt.Sprintf("session limit %d reached", e.cfg.MaxSessions)}
	}
	rules := e.registry.Current()
	if rules == nil {
		return nil, Receipt{Accepted: false, Reason: RejectNone, Detail: "no rule set registered"}
	}
	s := newSession(matchID, playerIDs, rules, e.cfg, e.store)
	e.sessions[matchID] = s
	return s, Receipt{Accepted: true}
}

// EndMatch 结束并移除对局。判定记录仍保留在 Store 中可按版本追溯。
func (e *Engine) EndMatch(matchID string) {
	e.mu.Lock()
	s, ok := e.sessions[matchID]
	if ok {
		delete(e.sessions, matchID)
	}
	e.mu.Unlock()
	if ok {
		s.Close()
	}
}

// Submit 将对局操作投递到对应对局（本地回环入口）。
func (e *Engine) Submit(op Operation) Receipt {
	e.mu.Lock()
	s, ok := e.sessions[op.MatchID]
	e.mu.Unlock()
	if !ok {
		return Receipt{Accepted: false, Reason: RejectUnknownMatch,
			Detail: "match " + op.MatchID + " not found"}
	}
	return s.Submit(op)
}

// Session 查询对局句柄。
func (e *Engine) Session(matchID string) (*Session, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	s, ok := e.sessions[matchID]
	return s, ok
}

// Store 返回判定存储。
func (e *Engine) Store() *VerdictStore {
	return e.store
}

// Registry 返回规则注册表。
func (e *Engine) Registry() *RuleRegistry {
	return e.registry
}
