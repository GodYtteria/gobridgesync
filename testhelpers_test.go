package main

import (
	"bytes"
	"context"
	"io"
	"time"
)

func bytesFetcher(f func(ctx context.Context, off, n int64) ([]byte, error)) rangeFetcher {
	return func(ctx context.Context, off, n int64) (io.ReadCloser, error) {
		b, err := f(ctx, off, n)
		if err != nil {
			return nil, err
		}
		return io.NopCloser(bytes.NewReader(b)), nil
	}
}

func (c *streamCache) setPinned(id, md5 string, on bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if on {
		c.pinned[id] = md5
	} else {
		delete(c.pinned, id)
	}
}

func (c *streamCache) waitIdle() {
	for {
		c.mu.Lock()
		busy := len(c.flight)
		c.mu.Unlock()
		if len(c.ahead) == 0 && busy == 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
}

func (c AppConfig) save(p Paths) error {
	b, err := c.encode()
	if err != nil {
		return err
	}
	return writeFileAtomic(p.appConfig(), b, 0o600)
}
