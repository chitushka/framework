package cache

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"strings"
	"time"
)

type cancellableConn struct {
	net.Conn
	stop func() bool
}

func (c *cancellableConn) Close() error { c.stop(); return c.Conn.Close() }
func dialCache(ctx context.Context, address string, dialTimeout, ioTimeout time.Duration, tlsConfig *tls.Config) (net.Conn, error) {
	if ctx == nil {
		return nil, errors.New("cache: context is nil")
	}
	dialer := &net.Dialer{Timeout: dialTimeout}
	var connection net.Conn
	var err error
	if tlsConfig != nil {
		connection, err = (&tls.Dialer{NetDialer: dialer, Config: tlsConfig}).DialContext(ctx, "tcp", address)
	} else {
		connection, err = dialer.DialContext(ctx, "tcp", address)
	}
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(ioTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := connection.SetDeadline(deadline); err != nil {
		_ = connection.Close()
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { _ = connection.Close() })
	return &cancellableConn{connection, stop}, nil
}

func validWireKey(key string) bool {
	return key != "" && len(key) <= 250 && strings.IndexFunc(key, func(r rune) bool { return r <= 32 || r == 127 }) < 0
}
