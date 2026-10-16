package main

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/api/drive/v3"
)

var (
	streamFetchTries   = 4
	streamStallTimeout = 30 * time.Second
	streamLoadTotal    = 15 * time.Minute
	errStreamStalled   = errors.New("Google Drive stopped sending data")
)

const (
	streamAhead         = 4
	streamRAMChunks     = 16
	streamRAMRecheck    = 10 * time.Second
	streamRAMIdle       = 2 * time.Minute
	streamLoaders       = 8
	streamPrefetchPause = 30 * time.Second
)

const (
	streamChunk = 8 << 20
	streamBlock = 256 << 10
)

var errStreamOffline = errors.New("this file is not on this computer yet and Google Drive cannot be reached")

type rangeFetcher func(ctx context.Context, off, n int64) (io.ReadCloser, error)

type chunkKey struct {
	id  string
	md5 string
	idx int64
}

type streamCache struct {
	mu     sync.Mutex
	dir    string
	limit  int64
	used   int64
	lru    *list.List
	items  map[chunkKey]*list.Element
	pinned map[string]string
	flight map[chunkKey]*partial
	last   map[string]lastRead
	ram    map[chunkKey][]byte
	ramLRU *list.List
	ramPos map[chunkKey]*list.Element
	ramMax atomic.Int64
	ramAt  atomic.Int64
	ramTmr *time.Timer
	ramUse map[chunkKey]time.Time
	failed map[string]time.Time
	ahead  chan struct{}
	loads  chan struct{}
	ctx    context.Context
	stop   context.CancelFunc
	closed bool
}

type partial struct {
	mu     sync.Mutex
	size   int64
	data   []byte
	filled []bool
	err    error
	done   chan struct{}
	notify chan struct{}
}

func newPartial(n int64) *partial {
	return &partial{size: n, filled: make([]bool, (n+streamBlock-1)/streamBlock), done: make(chan struct{}), notify: make(chan struct{})}
}

func (p *partial) alloc() {
	p.mu.Lock()
	p.data = make([]byte, p.size)
	p.mu.Unlock()
}

func (p *partial) advance(from, pos, n int64) {
	p.mu.Lock()
	for b := from / streamBlock; b*streamBlock < n; b++ {
		if min((b+1)*streamBlock, n) > pos {
			break
		}
		p.filled[b] = true
	}
	close(p.notify)
	p.notify = make(chan struct{})
	p.mu.Unlock()
}

func (p *partial) ready(from, to int64) bool {
	for b := from / streamBlock; b*streamBlock < to; b++ {
		if !p.filled[b] {
			return false
		}
	}
	return true
}

func (p *partial) wait(ctx context.Context, from, to int64) error {
	for {
		p.mu.Lock()
		if p.ready(from, to) {
			p.mu.Unlock()
			return nil
		}
		ch := p.notify
		p.mu.Unlock()
		select {
		case <-ch:
		case <-p.done:
			p.mu.Lock()
			ok, err := p.ready(from, to), p.err
			p.mu.Unlock()
			if ok {
				return nil
			}
			if err == nil {
				err = io.ErrUnexpectedEOF
			}
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

type lastRead struct {
	idx int64
	seq bool
}

type cacheItem struct {
	key  chunkKey
	size int64
}

func newStreamCache(dir string, limit int64) (*streamCache, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	ctx, stop := context.WithCancel(context.Background())
	c := &streamCache{ctx: ctx, stop: stop, dir: dir, limit: limit, lru: list.New(), items: map[chunkKey]*list.Element{}, pinned: map[string]string{}, flight: map[chunkKey]*partial{}, last: map[string]lastRead{}, ram: map[chunkKey][]byte{}, ramLRU: list.New(), ramPos: map[chunkKey]*list.Element{}, ramUse: map[chunkKey]time.Time{}, failed: map[string]time.Time{}, ahead: make(chan struct{}, streamAhead), loads: make(chan struct{}, streamLoaders)}
	entries, err := os.ReadDir(dir)
	if err != nil {
		stop()
		return nil, err
	}
	for _, e := range entries {
		sub, err := os.ReadDir(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		kept := 0
		for _, f := range sub {
			if strings.HasPrefix(f.Name(), tmpPrefix) {
				os.Remove(filepath.Join(dir, e.Name(), f.Name()))
				continue
			}
			kept++
			info, err := f.Info()
			if err != nil {
				continue
			}
			if k, ok := parseChunkPath(e.Name(), f.Name()); ok {
				c.items[k] = c.lru.PushFront(&cacheItem{key: k, size: info.Size()})
				c.used += info.Size()
			}
		}
		if kept == 0 {
			if err := os.Remove(filepath.Join(dir, e.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
				log.Printf("streaming: removing empty cache folder %s: %v", e.Name(), err)
			}
		}
	}
	return c, nil
}

func (c *streamCache) Close() {
	c.stop()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	if c.ramTmr != nil {
		c.ramTmr.Stop()
		c.ramTmr = nil
	}
	c.ram = map[chunkKey][]byte{}
	c.ramLRU = list.New()
	c.ramPos = map[chunkKey]*list.Element{}
	c.ramUse = map[chunkKey]time.Time{}
}

func chunkDirName(id, md5 string) string { return id + "_" + md5 }

func parseChunkPath(dirName, file string) (chunkKey, bool) {
	var id, md5 string
	for i := len(dirName) - 1; i >= 0; i-- {
		if dirName[i] == '_' {
			id, md5 = dirName[:i], dirName[i+1:]
			break
		}
	}
	idx, err := strconv.ParseInt(file, 10, 64)
	if id == "" || err != nil {
		return chunkKey{}, false
	}
	return chunkKey{id: id, md5: md5, idx: idx}, true
}

func (c *streamCache) path(k chunkKey) string {
	return filepath.Join(c.dir, chunkDirName(k.id, k.md5), strconv.FormatInt(k.idx, 10))
}

func (c *streamCache) isPinned(id, md5 string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	m, ok := c.pinned[id]
	return ok && m == md5
}

func (c *streamCache) setPinnedSet(ids map[string]string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pinned = ids
	c.evictLocked(chunkKey{})
}

func (c *streamCache) evictLocked(keep chunkKey) {
	for c.used > c.limit {
		var victim *list.Element
		for e := c.lru.Back(); e != nil; e = e.Prev() {
			it := e.Value.(*cacheItem)
			if m, pinned := c.pinned[it.key.id]; it.key != keep && !(pinned && m == it.key.md5) {
				victim = e
				break
			}
		}
		if victim == nil {
			return
		}
		it := victim.Value.(*cacheItem)
		c.lru.Remove(victim)
		delete(c.items, it.key)
		c.used -= it.size
		if err := os.Remove(c.path(it.key)); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Printf("streaming: evicting chunk %d of %s: %v", it.key.idx, it.key.id, err)
		}
	}
}

func (c *streamCache) start(k chunkKey, size, hint int64, fetch rangeFetcher) *partial {
	c.mu.Lock()
	if _, ok := c.items[k]; ok {
		c.mu.Unlock()
		return nil
	}
	if pt, busy := c.flight[k]; busy {
		c.mu.Unlock()
		return pt
	}
	n := min(streamChunk, size-k.idx*streamChunk)
	if n <= 0 {
		c.mu.Unlock()
		pt := newPartial(0)
		pt.err = fmt.Errorf("chunk %d is past the end of the file", k.idx)
		close(pt.done)
		return pt
	}
	pt := newPartial(n)
	c.flight[k] = pt
	c.mu.Unlock()
	go func() {
		var err error
		select {
		case c.loads <- struct{}{}:
			defer func() { <-c.loads }()
			pt.alloc()
			ctx, cancel := context.WithTimeout(c.ctx, streamLoadTotal)
			defer cancel()
			err = c.loadStream(ctx, k, n, hint, fetch, pt)
		case <-c.ctx.Done():
			err = c.ctx.Err()
		}
		c.mu.Lock()
		delete(c.flight, k)
		c.mu.Unlock()
		pt.mu.Lock()
		pt.err = err
		pt.mu.Unlock()
		close(pt.done)
	}()
	return pt
}

func (c *streamCache) ensure(ctx context.Context, id, md5 string, size, idx int64, fetch rangeFetcher) error {
	k := chunkKey{id, md5, idx}
	for {
		pt := c.start(k, size, 0, fetch)
		if pt == nil {
			c.mu.Lock()
			if el, ok := c.items[k]; ok {
				c.lru.MoveToFront(el)
			}
			c.mu.Unlock()
			return nil
		}
		select {
		case <-pt.done:
			pt.mu.Lock()
			err := pt.err
			pt.mu.Unlock()
			if err != nil {
				return err
			}
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (c *streamCache) ramLimit() int {
	if m := c.ramMax.Load(); m > 0 && time.Since(time.Unix(0, c.ramAt.Load())) < streamRAMRecheck {
		return int(m)
	}
	m := streamRAMChunksFor(memAvailable())
	c.ramMax.Store(int64(m))
	c.ramAt.Store(time.Now().UnixNano())
	return m
}

func streamRAMChunksFor(avail int64) int {
	if avail <= 0 {
		return streamRAMChunks
	}
	var budget int64
	if avail < 2<<30 {
		budget = max(avail/4, 64<<20)
	} else {
		budget = min(512<<20+(avail-2<<30)/12, 1<<30)
	}
	return int(budget / streamChunk)
}

func (c *streamCache) ramGet(k chunkKey) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	b, ok := c.ram[k]
	if ok {
		c.ramLRU.MoveToFront(c.ramPos[k])
		c.ramUse[k] = time.Now()
		if el, onDisk := c.items[k]; onDisk {
			c.lru.MoveToFront(el)
		}
	}
	return b, ok
}

func (c *streamCache) ramPut(k chunkKey, b []byte) {
	limit := c.ramLimit()
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.ram[k]; ok || c.closed {
		return
	}
	c.ram[k] = b
	c.ramPos[k] = c.ramLRU.PushFront(k)
	c.ramUse[k] = time.Now()
	c.ramTrimLocked(limit)
	c.armRAMTimerLocked()
}

func (c *streamCache) armRAMTimerLocked() {
	if c.ramTmr != nil || len(c.ram) == 0 || c.closed {
		return
	}
	c.ramTmr = time.AfterFunc(streamRAMIdle/2, func() {
		limit := c.ramLimit()
		c.mu.Lock()
		defer c.mu.Unlock()
		c.ramTmr = nil
		c.ramTrimLocked(limit)
		c.armRAMTimerLocked()
	})
}

func (c *streamCache) ramDropLocked(el *list.Element) {
	k := el.Value.(chunkKey)
	c.ramLRU.Remove(el)
	delete(c.ram, k)
	delete(c.ramPos, k)
	delete(c.ramUse, k)
}

func (c *streamCache) ramTrimLocked(limit int) {
	for c.ramLRU.Len() > limit {
		c.ramDropLocked(c.ramLRU.Back())
	}
	for el := c.ramLRU.Back(); el != nil; {
		prev := el.Prev()
		if time.Since(c.ramUse[el.Value.(chunkKey)]) > streamRAMIdle {
			c.ramDropLocked(el)
		}
		el = prev
	}
}

func (c *streamCache) forget(k chunkKey, seen *list.Element) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[k]; ok && el == seen {
		c.used -= el.Value.(*cacheItem).size
		c.lru.Remove(el)
		delete(c.items, k)
	}
}

func (c *streamCache) readChunk(ctx context.Context, id, md5 string, size, idx int64, p []byte, in int64, fetch rangeFetcher) (int, error) {
	k := chunkKey{id, md5, idx}
	if b, ok := c.ramGet(k); ok {
		if in >= int64(len(b)) {
			return 0, nil
		}
		return copy(p, b[in:]), nil
	}
	for attempt := 0; attempt < 3; attempt++ {
		pt := c.start(k, size, in, fetch)
		if pt != nil {
			if in >= pt.size {
				<-pt.done
				return 0, pt.err
			}
			end := min(in+int64(len(p)), pt.size)
			if err := pt.wait(ctx, in, end); err != nil {
				return 0, err
			}
			pt.mu.Lock()
			n := copy(p, pt.data[in:end])
			pt.mu.Unlock()
			return n, nil
		}
		if b, ok := c.ramGet(k); ok {
			if in >= int64(len(b)) {
				return 0, nil
			}
			return copy(p, b[in:]), nil
		}
		c.mu.Lock()
		el := c.items[k]
		if el != nil {
			c.lru.MoveToFront(el)
		}
		c.mu.Unlock()
		f, err := os.Open(c.path(k))
		if err != nil {
			c.forget(k, el)
			continue
		}
		n, err := f.ReadAt(p, in)
		f.Close()
		if err != nil && !errors.Is(err, io.EOF) {
			c.forget(k, el)
			continue
		}
		return n, nil
	}
	return 0, fmt.Errorf("cached chunk %d of %s keeps disappearing", idx, id)
}

func (c *streamCache) fill(ctx context.Context, open rangeFetcher, k chunkKey, n int64, pt *partial, from, to int64) error {
	pos := from
	off := k.idx * streamChunk
	var last error
	for attempt := 0; attempt < streamFetchTries && pos < to; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt) * time.Second):
			}
		}
		actx, acancel := context.WithCancel(ctx)
		var stalled atomic.Bool
		watchdog := time.AfterFunc(streamStallTimeout, func() {
			stalled.Store(true)
			acancel()
		})
		rc, err := open(actx, off+pos, to-pos)
		if err != nil {
			watchdog.Stop()
			acancel()
			last = err
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if stalled.Load() {
				last = errStreamStalled
			}
			continue
		}
		for pos < to {
			step := min(pos+streamBlock, to)
			nr, rerr := rc.Read(pt.data[pos:step])
			if nr > 0 {
				pos += int64(nr)
				watchdog.Reset(streamStallTimeout)
				pt.advance(from, pos, n)
			}
			if rerr != nil {
				if !errors.Is(rerr, io.EOF) {
					last = rerr
					if ctx.Err() == nil && stalled.Load() {
						last = errStreamStalled
					}
				}
				break
			}
		}
		watchdog.Stop()
		acancel()
		if err := rc.Close(); err != nil && ctx.Err() == nil && last == nil {
			log.Printf("streaming: closing the Google Drive download: %v", err)
		}
		if last == nil && pos < to {
			last = io.ErrUnexpectedEOF
		}
	}
	if pos < to {
		return fmt.Errorf("Google Drive returned %d of %d bytes: %w", pos-from, to-from, last)
	}
	return nil
}

func (c *streamCache) loadStream(ctx context.Context, k chunkKey, n, hint int64, fetch rangeFetcher, pt *partial) error {
	start := (hint / streamBlock) * streamBlock
	if start >= n || start < 0 {
		start = 0
	}
	if err := c.fill(ctx, fetch, k, n, pt, start, n); err != nil {
		return err
	}
	if start > 0 {
		if err := c.fill(ctx, fetch, k, n, pt, 0, start); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	p := c.path(k)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	if err := writeFileAtomic(p, pt.data, 0o600); err != nil {
		return err
	}
	c.mu.Lock()
	c.items[k] = c.lru.PushFront(&cacheItem{key: k, size: n})
	c.used += n
	c.evictLocked(k)
	c.mu.Unlock()
	c.ramPut(k, pt.data)
	return nil
}

func (c *streamCache) ReadAt(ctx context.Context, id, md5 string, size int64, p []byte, off int64, fetch rangeFetcher) (int, error) {
	if off >= size {
		return 0, io.EOF
	}
	total := 0
	for total < len(p) && off < size {
		idx := off / streamChunk
		in := off - idx*streamChunk
		want := min(int64(len(p)-total), min(streamChunk, size-idx*streamChunk)-in)
		c.readAhead(id, md5, size, idx, fetch)
		n, err := c.readChunk(ctx, id, md5, size, idx, p[total:total+int(want)], in, fetch)
		total += n
		off += int64(n)
		if err != nil {
			return total, err
		}
		if int64(n) < want {
			return total, io.ErrUnexpectedEOF
		}
	}
	if off >= size && total < len(p) {
		return total, io.EOF
	}
	return total, nil
}

func (c *streamCache) readAhead(id, md5 string, size, idx int64, fetch rangeFetcher) {
	c.mu.Lock()
	prev, seen := c.last[id]
	cur := lastRead{idx: idx}
	switch {
	case !seen:
	case idx == prev.idx+1:
		cur.seq = true
	case idx == prev.idx:
		cur.seq = prev.seq
	}
	c.last[id] = cur
	if len(c.last) > 4096 {
		c.last = map[string]lastRead{id: cur}
	}
	blocked := time.Now().Before(c.failed[id])
	c.mu.Unlock()
	if !cur.seq || blocked {
		return
	}
	for next := idx + 1; next <= idx+streamAhead && next*streamChunk < size; next++ {
		select {
		case c.ahead <- struct{}{}:
		default:
			return
		}
		go func(n int64) {
			defer func() { <-c.ahead }()
			ctx, cancel := context.WithTimeout(c.ctx, streamLoadTotal)
			defer cancel()
			if err := c.ensure(ctx, id, md5, size, n, fetch); err != nil && c.ctx.Err() == nil {
				c.mu.Lock()
				c.failed[id] = time.Now().Add(streamPrefetchPause)
				c.mu.Unlock()
			}
		}(next)
	}
}

func (c *streamCache) Hydrate(ctx context.Context, id, md5 string, size int64, fetch rangeFetcher) error {
	for idx := int64(0); idx*streamChunk < size; idx++ {
		if err := c.ensure(ctx, id, md5, size, idx, fetch); err != nil {
			return err
		}
	}
	return nil
}

func (c *streamCache) Used() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.used
}

func driveRangeFetcher(srv *drive.Service, id string) rangeFetcher {
	return func(ctx context.Context, off, n int64) (io.ReadCloser, error) {
		var body io.ReadCloser
		err := retryRead(ctx, func() error {
			call := srv.Files.Get(id).Context(ctx)
			call.Header().Set("Range", fmt.Sprintf("bytes=%d-%d", off, off+n-1))
			resp, err := call.Download()
			if err != nil {
				return err
			}
			if resp.StatusCode != http.StatusPartialContent && !(off == 0 && resp.StatusCode == http.StatusOK) {
				resp.Body.Close()
				return fmt.Errorf("Google Drive did not honor the range request (%s)", resp.Status)
			}
			body = resp.Body
			return nil
		})
		if err != nil {
			return nil, err
		}
		return struct {
			io.Reader
			io.Closer
		}{io.LimitReader(body, n), body}, nil
	}
}
