// Package transport delivers client operations to the server through an
// in-process memory pipe or a TCP loopback connection. Nothing here depends
// on external services.
package transport

import (
	"math/rand"
	"sync"
	"time"

	"github.com/guoyyit/server-authoritative-anticheat-go/internal/model"
)

// Pipe is the minimal transport interface: send an operation, receive an
// operation. Implementations must be safe for concurrent senders.
type Pipe interface {
	Send(op *model.Operation) error
	Recv() (*model.Operation, error)
	Close() error
}

// MemoryPipe is a bounded in-process channel transport.
type MemoryPipe struct {
	ch chan *model.Operation
}

// NewMemoryPipe creates a memory pipe with the given buffer size.
func NewMemoryPipe(buffer int) *MemoryPipe {
	return &MemoryPipe{ch: make(chan *model.Operation, buffer)}
}

func (p *MemoryPipe) Send(op *model.Operation) error {
	select {
	case p.ch <- op:
		return nil
	default:
		return ErrPipeFull
	}
}

func (p *MemoryPipe) Recv() (*model.Operation, error) {
	op, ok := <-p.ch
	if !ok {
		return nil, ErrPipeClosed
	}
	return op, nil
}

func (p *MemoryPipe) Close() error {
	close(p.ch)
	return nil
}

// JitterPipe wraps a Pipe and delays each send by a deterministic amount of
// simulated network latency/jitter. Used to prove that normal jitter does not
// cause false positives.
type JitterPipe struct {
	inner   Pipe
	ch      chan *model.Operation
	latency time.Duration
	jitter  time.Duration

	mu   sync.Mutex
	rand *rand.Rand
}

// NewJitterPipe wraps inner with base latency plus deterministic jitter.
func NewJitterPipe(inner Pipe, latency, jitter time.Duration, seed int64) *JitterPipe {
	p := &JitterPipe{
		inner:   inner,
		ch:      make(chan *model.Operation, 64),
		latency: latency,
		jitter:  jitter,
		rand:    rand.New(rand.NewSource(seed)),
	}
	go p.forward()
	return p
}

func (p *JitterPipe) forward() {
	for op := range p.ch {
		delay := p.nextDelay()
		time.Sleep(delay)
		_ = p.inner.Send(op)
	}
}

func (p *JitterPipe) nextDelay() time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	j := time.Duration(0)
	if p.jitter > 0 {
		j = time.Duration(p.rand.Int63n(int64(p.jitter)))
	}
	return p.latency + j
}

func (p *JitterPipe) Send(op *model.Operation) error {
	select {
	case p.ch <- op:
		return nil
	default:
		return ErrPipeFull
	}
}

func (p *JitterPipe) Recv() (*model.Operation, error) {
	return p.inner.Recv()
}

func (p *JitterPipe) Close() error {
	close(p.ch)
	return p.inner.Close()
}
