package transport

import (
	"bufio"
	"encoding/json"
	"net"

	"github.com/guoyyit/server-authoritative-anticheat-go/internal/model"
)

// LoopbackServer listens on a local TCP loopback address and serves JSON-line
// framed operations to a handler.
type LoopbackServer struct {
	ln net.Listener
}

// NewLoopbackServer starts listening on 127.0.0.1:0 and returns the server.
func NewLoopbackServer() (*LoopbackServer, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	return &LoopbackServer{ln: ln}, nil
}

// Addr returns the loopback address the server listens on.
func (s *LoopbackServer) Addr() string {
	return s.ln.Addr().String()
}

// Serve accepts one connection and forwards every operation to handler until
// the connection closes.
func (s *LoopbackServer) Serve(handler func(*model.Operation)) error {
	conn, err := s.ln.Accept()
	if err != nil {
		return err
	}
	defer conn.Close()
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		var op model.Operation
		if err := json.Unmarshal(scanner.Bytes(), &op); err != nil {
			continue // malformed frames are skipped; engine validates semantics
		}
		handler(&op)
	}
	return scanner.Err()
}

// Close stops listening.
func (s *LoopbackServer) Close() error {
	return s.ln.Close()
}

// LoopbackClient dials a loopback server and sends JSON-line operations.
type LoopbackClient struct {
	conn net.Conn
	w    *bufio.Writer
}

// DialLoopback connects to a loopback address.
func DialLoopback(addr string) (*LoopbackClient, error) {
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		return nil, err
	}
	return &LoopbackClient{conn: conn, w: bufio.NewWriter(conn)}, nil
}

// Send writes one operation as a JSON line.
func (c *LoopbackClient) Send(op *model.Operation) error {
	data, err := json.Marshal(op)
	if err != nil {
		return err
	}
	if _, err := c.w.Write(append(data, '\n')); err != nil {
		return err
	}
	return c.w.Flush()
}

// Close closes the connection.
func (c *LoopbackClient) Close() error {
	return c.conn.Close()
}
