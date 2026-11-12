package metrics

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

type shortConn struct{ net.Conn }

func (shortConn) Read(p []byte) (int, error) { copy(p, "abc"); return 3, io.EOF }
func (shortConn) Write([]byte) (int, error)  { return 2, io.ErrShortWrite }

func TestNetworkCountsActualBytesWithError(t *testing.T) {
	stats := &Network{}
	c := &networkConn{Conn: shortConn{}, stats: stats}
	if n, err := c.Read(make([]byte, 8)); n != 3 || !errors.Is(err, io.EOF) {
		t.Fatal(n, err)
	}
	if n, err := c.Write([]byte("abcdefgh")); n != 2 || !errors.Is(err, io.ErrShortWrite) {
		t.Fatal(n, err)
	}
	if stats.recv.Load() != 3 || stats.sent.Load() != 2 {
		t.Fatal("counted requested rather than transferred bytes")
	}
}

func TestNetworkHTTPPoolingAndBodies(t *testing.T) {
	stats := &Network{}
	var connections atomic.Int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_, _ = w.Write(append([]byte("reply:"), body...))
	}))
	srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			connections.Add(1)
		}
	}
	srv.Start()
	defer srv.Close()
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.ResponseHeaderTimeout = 3 * time.Second
	transport := stats.instrumentTransport(base)
	defer transport.CloseIdleConnections()
	if transport.ResponseHeaderTimeout != base.ResponseHeaderTimeout || !transport.ForceAttemptHTTP2 {
		t.Fatal("transport settings changed")
	}
	client := &http.Client{Transport: transport}
	for i := 0; i < 2; i++ {
		resp, err := client.Post(srv.URL, "text/plain", bytes.NewBufferString("request"))
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil || string(b) != "reply:request" {
			t.Fatal(string(b), err)
		}
	}
	if connections.Load() != 1 {
		t.Fatal("keepalive pool lost", connections.Load())
	}
	if stats.recv.Load() <= 26 || stats.sent.Load() <= 14 {
		t.Fatal("HTTP headers or request body not counted")
	}
	if base.DialContext == nil {
		t.Fatal("base transport mutated")
	}
}

func TestNetworkCountsTLSCompressedWireBytes(t *testing.T) {
	body := bytes.Repeat([]byte("repeatable payload"), 10000)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		g := gzip.NewWriter(w)
		_, _ = g.Write(body)
		_ = g.Close()
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()
	stats := &Network{}
	transport := stats.instrumentTransport(srv.Client().Transport.(*http.Transport))
	defer transport.CloseIdleConnections()
	resp, err := (&http.Client{Transport: transport}).Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil || !bytes.Equal(b, body) {
		t.Fatal("decoded response changed", err)
	}
	if resp.ProtoMajor != 2 {
		t.Fatal("HTTP/2 disabled", resp.Proto)
	}
	if stats.recv.Load() == 0 || stats.recv.Load() >= uint64(len(body)) || stats.sent.Load() == 0 {
		t.Fatal("must count encrypted/compressed wire bytes, not decoded body", stats.recv.Load(), stats.sent.Load())
	}
}

func TestNetworkAcceptedConnectionAndShutdown(t *testing.T) {
	stats := &Network{}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	idle := make(chan struct{}, 1)
	srv := &http.Server{ConnState: func(_ net.Conn, state http.ConnState) {
		if state == http.StateIdle {
			select {
			case idle <- struct{}{}:
			default:
			}
		}
	}, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_, _ = w.Write(b)
	})}
	done := make(chan error, 1)
	go func() { done <- srv.Serve(&networkListener{Listener: listener, stats: stats}) }()
	defer srv.Close()
	client := &http.Client{Transport: http.DefaultTransport.(*http.Transport).Clone(), Timeout: 3 * time.Second}
	defer client.CloseIdleConnections()
	resp, err := client.Post("http://"+listener.Addr().String(), "text/plain", bytes.NewBufferString("upload"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil || string(b) != "upload" {
		t.Fatal(string(b), err)
	}
	// The client can receive bytes before the server Write returns and records them.
	select {
	case <-idle:
	case <-time.After(3 * time.Second):
		t.Fatal("server did not finish writing response")
	}
	if stats.recv.Load() <= 6 || stats.sent.Load() <= 6 {
		t.Fatal("accepted socket not measured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, http.ErrServerClosed) {
		t.Fatal(err)
	}
}
