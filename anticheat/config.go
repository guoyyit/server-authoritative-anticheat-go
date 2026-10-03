package anticheat

// Config 是引擎的容量与并发配置，所有上界均可配置。
type Config struct {
	InboxCapacity         int // 每玩家在途操作队列容量
	MaxReorderBuffer      int // 每玩家乱序缓冲容量
	MaxVerdictsPerSession int // 每对局判定记录上界
	MaxSessions           int // 同时进行的对局数上界
}

// DefaultConfig 返回默认配置。
func DefaultConfig() Config {
	return Config{
		InboxCapacity:         256,
		MaxReorderBuffer:      64,
		MaxVerdictsPerSession: 4096,
		MaxSessions:           64,
	}
}
