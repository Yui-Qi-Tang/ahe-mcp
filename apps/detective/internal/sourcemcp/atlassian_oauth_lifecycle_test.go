package sourcemcp

import (
	"context"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

// The pipe blocks response writes until read or closed, without relying on
// socket buffer sizes or sleeping to arrange the shutdown race.
type oauthResponseConn struct {
	net.Conn
	writing chan struct{}
	once    sync.Once
}

func (c *oauthResponseConn) Write(p []byte) (int, error) {
	c.once.Do(func() { close(c.writing) })
	return c.Conn.Write(p)
}

type oauthPipeListener struct {
	conn      net.Conn
	accepted  bool
	closed    chan struct{}
	closeOnce sync.Once
}

func (l *oauthPipeListener) Accept() (net.Conn, error) {
	if !l.accepted {
		l.accepted = true
		return l.conn, nil
	}
	<-l.closed
	return nil, net.ErrClosed
}

func (l *oauthPipeListener) Close() error {
	l.closeOnce.Do(func() { close(l.closed) })
	return nil
}

func (*oauthPipeListener) Addr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1}
}

func TestAtlassianCallbackStalledResponseStops(t *testing.T) {
	for _, mode := range []string{"drain-timeout", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			serverConn, browserConn := net.Pipe()
			defer serverConn.Close()
			defer browserConn.Close()
			if err := browserConn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
				t.Fatal(err)
			}
			conn := &oauthResponseConn{Conn: serverConn, writing: make(chan struct{})}
			listener := &oauthPipeListener{conn: conn, closed: make(chan struct{})}
			defer listener.Close()
			login := &AtlassianAuthorization{listener: listener, state: "synthetic-state"}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			completed := make(chan error, 1)
			go func() { _, err := login.Wait(ctx); completed <- err }()
			request := "GET /oauth/callback?state=synthetic-state&error=access_denied HTTP/1.1\r\nHost: 127.0.0.1:1\r\n\r\n"
			if _, err := io.WriteString(browserConn, request); err != nil {
				t.Fatal(err)
			}
			select {
			case <-conn.writing:
			case <-time.After(3 * time.Second):
				t.Fatal("callback did not attempt a response")
			}
			if mode == "cancel" {
				cancel()
			}
			// Do not read: the response remains blocked until bounded cleanup.
			select {
			case err := <-completed:
				if err == nil {
					t.Fatal("declined callback created a session")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("stalled response prevented cleanup")
			}
			if _, err := browserConn.Read(make([]byte, 1)); err != io.EOF {
				t.Fatalf("callback connection not closed: %v", err)
			}
		})
	}
}
