package metrics

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"sync/atomic"
	"time"
)

// Network counts bytes actually read/written by this service's TCP sockets.
// HTTP headers, compressed bodies and TLS records are included. DNS/UDP,
// subprocesses (ffmpeg/node/plugins) and other applications are not included.
// These counters are independent of Download/Upload's media payload counters.
type Network struct {
	recv atomic.Uint64
	sent atomic.Uint64
}

var processNetwork Network
var networkInstance = fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano())

type NetworkSnapshot struct {
	Scope     string `json:"scope"`
	Instance  string `json:"instance"`
	RecvTotal uint64 `json:"recv_total"`
	SentTotal uint64 `json:"sent_total"`
	UnixMs    int64  `json:"unix_ms"`
}

func ReadNetwork() NetworkSnapshot {
	return NetworkSnapshot{"process", networkInstance, processNetwork.recv.Load(), processNetwork.sent.Load(), time.Now().UnixMilli()}
}

type networkConn struct {
	net.Conn
	stats *Network
}

func (c *networkConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 {
		c.stats.recv.Add(uint64(n))
	}
	return n, err
}

func (c *networkConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	if n > 0 {
		c.stats.sent.Add(uint64(n))
	}
	return n, err
}

// InstrumentTransport clones a transport, preserving pooling/proxy/timeout/TLS
// settings. Wrapping at the dial boundary counts HTTP/2, retries and TLS once.
// Call before the transport is used; do not mutate an in-use transport.
func InstrumentTransport(base *http.Transport) *http.Transport {
	return processNetwork.instrumentTransport(base)
}

func (stats *Network) instrumentTransport(base *http.Transport) *http.Transport {
	t := base.Clone()
	dial := t.DialContext
	if dial == nil {
		if t.Dial != nil {
			legacy := t.Dial
			dial = func(_ context.Context, network, address string) (net.Conn, error) { return legacy(network, address) }
		} else {
			dial = (&net.Dialer{}).DialContext
		}
	}
	t.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		conn, err := dial(ctx, network, address)
		if err != nil {
			return nil, err
		}
		return &networkConn{Conn: conn, stats: stats}, nil
	}
	return t
}

type networkListener struct {
	net.Listener
	stats *Network
}

func (l *networkListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &networkConn{Conn: conn, stats: l.stats}, nil
}

// ListenAndServe retains net/http lifecycle, including Close/Shutdown, while
// measuring all accepted connections (static/API/media on every service port).
func ListenAndServe(srv *http.Server) error {
	addr := srv.Addr
	if addr == "" {
		addr = ":http"
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	return srv.Serve(&networkListener{Listener: listener, stats: &processNetwork})
}
