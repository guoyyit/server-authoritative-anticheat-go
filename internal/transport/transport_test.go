package transport_test

import (
"testing"
"time"

"github.com/guoyyit/server-authoritative-anticheat-go/internal/model"
"github.com/guoyyit/server-authoritative-anticheat-go/internal/transport"
)

func testOp(seq uint64) *model.Operation {
return &model.Operation{MatchID: "m1", PlayerID: "p1", Seq: seq, ClientTime: int64(seq) * 200, Kind: model.OpMove}
}

// Memory pipe delivers operations in order.
func TestMemoryPipeDelivery(t *testing.T) {
p := transport.NewMemoryPipe(4)
defer p.Close()
for i := uint64(1); i <= 3; i++ {
if err := p.Send(testOp(i)); err != nil {
t.Fatal(err)
}
}
for i := uint64(1); i <= 3; i++ {
op, err := p.Recv()
if err != nil {
t.Fatal(err)
}
if op.Seq != i {
t.Fatalf("got seq %d, want %d", op.Seq, i)
}
}
}

// Full pipe returns ErrPipeFull instead of blocking or dropping.
func TestMemoryPipeFull(t *testing.T) {
p := transport.NewMemoryPipe(1)
defer p.Close()
if err := p.Send(testOp(1)); err != nil {
t.Fatal(err)
}
if err := p.Send(testOp(2)); err != transport.ErrPipeFull {
t.Fatalf("got %v, want ErrPipeFull", err)
}
}

// Jitter pipe delays delivery but preserves order and content.
func TestJitterPipeDelays(t *testing.T) {
inner := transport.NewMemoryPipe(4)
defer inner.Close()
jp := transport.NewJitterPipe(inner, 50*time.Millisecond, 10*time.Millisecond, 42)
if err := jp.Send(testOp(1)); err != nil {
t.Fatal(err)
}
start := time.Now()
op, err := jp.Recv()
elapsed := time.Since(start)
if err != nil {
t.Fatal(err)
}
if op.Seq != 1 {
t.Fatalf("seq = %d, want 1", op.Seq)
}
if elapsed < 40*time.Millisecond {
t.Fatalf("delivery too fast: %v, jitter delay not applied", elapsed)
}
}

// Loopback server/client round-trip over TCP.
func TestLoopbackRoundTrip(t *testing.T) {
srv, err := transport.NewLoopbackServer()
if err != nil {
t.Fatal(err)
}
defer srv.Close()
received := make(chan *model.Operation, 1)
go func() {
_ = srv.Serve(func(op *model.Operation) {
received <- op
})
}()
client, err := transport.DialLoopback(srv.Addr())
if err != nil {
t.Fatal(err)
}
defer client.Close()
if err := client.Send(testOp(7)); err != nil {
t.Fatal(err)
}
select {
case op := <-received:
if op.Seq != 7 {
t.Fatalf("got seq %d, want 7", op.Seq)
}
case <-time.After(2 * time.Second):
t.Fatal("loopback delivery timed out")
}
}
