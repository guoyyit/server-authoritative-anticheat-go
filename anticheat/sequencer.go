package anticheat

import "fmt"

// sequencer 负责单玩家操作的排序、去重与迟到识别，
// 保证应用结果与"按序号顺序单次处理"完全一致：
//   - 乱序到达的操作进入有界缓冲，按序号连续后依次放出；
//   - 重复提交（同序号同内容）被拒绝且不会被应用两次；
//   - 序号回退（同序号不同内容，或水位线以下的未知序号）被拒绝，
//     不会改写已发布的权威状态；
//   - 序号跳跃超过上限被拒绝。
type sequencer struct {
	maxGap    uint64
	maxBuffer int

	nextSeq uint64               // 下一个期望应用的序号（从 1 开始）
	pending map[uint64]Operation // 乱序缓冲：已到达但序号未连续的操作
	applied map[uint64]uint64    // 已应用序号 -> 校验值，用于精确识别重复提交
}

func newSequencer(maxGap uint64, maxBuffer int) *sequencer {
	return &sequencer{
		maxGap:    maxGap,
		maxBuffer: maxBuffer,
		nextSeq:   1,
		pending:   make(map[uint64]Operation),
		applied:   make(map[uint64]uint64),
	}
}

// push 接收一条操作，返回可立即按序应用的操作列表与回执。
// 任何被拒绝的输入都带可区分的原因，不会被静默丢弃。
func (s *sequencer) push(op Operation) ([]Operation, Receipt) {
	switch {
	case op.Seq < s.nextSeq:
		// 水位线以下：一定已经处理过该序号。
		if sum, ok := s.applied[op.Seq]; ok && sum == op.Checksum {
			return nil, Receipt{Accepted: false, Reason: RejectDuplicate,
				Detail: fmt.Sprintf("seq %d already applied", op.Seq)}
		}
		// 同序号不同内容：序号回退/冲突，疑似伪造，不得改写已发布状态。
		return nil, Receipt{Accepted: false, Reason: RejectSeqRollback,
			Detail: fmt.Sprintf("seq %d below watermark %d with conflicting content", op.Seq, s.nextSeq)}

	case op.Seq == s.nextSeq:
		out := []Operation{op}
		s.applied[op.Seq] = op.Checksum
		s.nextSeq++
		// 顺延放出缓冲中连续的操作。
		for {
			next, ok := s.pending[s.nextSeq]
			if !ok {
				break
			}
			delete(s.pending, s.nextSeq)
			s.applied[next.Seq] = next.Checksum
			s.nextSeq++
			out = append(out, next)
		}
		return out, Receipt{Accepted: true}

	default: // op.Seq > s.nextSeq：乱序，进入缓冲
		if _, ok := s.pending[op.Seq]; ok {
			return nil, Receipt{Accepted: false, Reason: RejectDuplicate,
				Detail: fmt.Sprintf("seq %d already buffered", op.Seq)}
		}
		if op.Seq-s.nextSeq+1 > s.maxGap {
			return nil, Receipt{Accepted: false, Reason: RejectSeqGap,
				Detail: fmt.Sprintf("seq %d jumps beyond gap limit from %d", op.Seq, s.nextSeq)}
		}
		if len(s.pending) >= s.maxBuffer {
			return nil, Receipt{Accepted: false, Reason: RejectBufferFull,
				Detail: fmt.Sprintf("reorder buffer full (%d)", s.maxBuffer)}
		}
		s.pending[op.Seq] = op
		return nil, Receipt{Accepted: true}
	}
}
