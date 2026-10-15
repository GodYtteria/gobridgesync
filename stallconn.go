package main

import (
	"context"
	"net"
	"time"
)

const (
	connWriteStall = 2 * time.Minute
	connReadStall  = 5 * time.Minute
)

type stallConn struct {
	net.Conn
	read, write time.Duration
}

func (c *stallConn) Read(b []byte) (int, error) {
	if err := c.Conn.SetReadDeadline(time.Now().Add(c.read)); err != nil {
		return 0, err
	}
	return c.Conn.Read(b)
}

func (c *stallConn) Write(b []byte) (int, error) {
	if err := c.Conn.SetWriteDeadline(time.Now().Add(c.write)); err != nil {
		return 0, err
	}
	return c.Conn.Write(b)
}

func stallDial(dial func(context.Context, string, string) (net.Conn, error), read, write time.Duration) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		c, err := dial(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		return &stallConn{Conn: c, read: read, write: write}, nil
	}
}
