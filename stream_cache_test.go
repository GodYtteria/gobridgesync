package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func fakeFile(size int64) ([]byte, rangeFetcher, *atomic.Int64) {
	data := make([]byte, size)
	for i := range data {
		data[i] = byte(i * 7)
	}
	var calls atomic.Int64
	return data, bytesFetcher(func(ctx context.Context, off, n int64) ([]byte, error) {
		calls.Add(1)
		return append([]byte(nil), data[off:off+n]...), nil
	}), &calls
}

func TestStreamCacheReadsAcrossChunksAndCaches(t *testing.T) {
	size := int64(streamChunk*2 + 1000)
	data, fetch, calls := fakeFile(size)
	c, err := newStreamCache(t.TempDir(), 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 3000)
	off := int64(streamChunk - 1500)
	n, err := c.ReadAt(context.Background(), "f1", "m", size, buf, off, fetch)
	if err != nil || n != 3000 || !bytes.Equal(buf, data[off:off+3000]) {
		t.Fatalf("n=%d err=%v", n, err)
	}
	c.waitIdle()
	before := calls.Load()
	if before < 2 || before > 6 {
		t.Fatalf("a read spanning two chunks should need only a few range fetches, got %d", before)
	}
	c.ReadAt(context.Background(), "f1", "m", size, buf, off, fetch)
	c.waitIdle()
	if calls.Load() != before {
		t.Fatal("second read must come from the cache")
	}
	tail := make([]byte, 5000)
	n, err = c.ReadAt(context.Background(), "f1", "m", size, tail, size-1000, fetch)
	if n != 1000 || err != io.EOF || !bytes.Equal(tail[:n], data[size-1000:]) {
		t.Fatalf("tail read n=%d err=%v", n, err)
	}
}

func TestStreamCacheEvictsLeastRecentlyUsedAndKeepsPinned(t *testing.T) {
	size := int64(streamChunk)
	_, fetch, _ := fakeFile(size)
	c, _ := newStreamCache(t.TempDir(), 2*streamChunk)
	c.setPinned("pinned", "m", true)
	buf := make([]byte, 10)
	for _, id := range []string{"pinned", "a", "b", "c"} {
		if _, err := c.ReadAt(context.Background(), id, "m", size, buf, 0, fetch); err != nil {
			t.Fatal(err)
		}
		c.waitIdle()
	}
	if c.Used() > 2*streamChunk {
		t.Fatalf("cache over its limit: %d", c.Used())
	}
	c.mu.Lock()
	_, hasPinned := c.items[chunkKey{"pinned", "m", 0}]
	_, hasA := c.items[chunkKey{"a", "m", 0}]
	_, hasC := c.items[chunkKey{"c", "m", 0}]
	c.mu.Unlock()
	if !hasPinned || hasA || !hasC {
		t.Fatalf("pinned=%v a=%v c=%v", hasPinned, hasA, hasC)
	}
}

func TestStreamCacheSharesOneFetchAndSurvivesRestart(t *testing.T) {
	size := int64(streamChunk)
	_, fetch, calls := fakeFile(size)
	dir := t.TempDir()
	c, _ := newStreamCache(dir, 1<<30)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			buf := make([]byte, 100)
			c.ReadAt(context.Background(), "f", "m", size, buf, 0, fetch)
		}()
	}
	wg.Wait()
	c.waitIdle()
	if calls.Load() != 1 {
		t.Fatalf("concurrent readers fetched the same chunk %d times", calls.Load())
	}
	again, _ := newStreamCache(dir, 1<<30)
	if again.Used() != size {
		t.Fatalf("cache not rebuilt after restart: %d", again.Used())
	}
	buf := make([]byte, 100)
	again.ReadAt(context.Background(), "f", "m", size, buf, 0, fetch)
	if calls.Load() != 1 {
		t.Fatal("restart lost the cached chunk")
	}
}

func TestStreamCacheOfflineReturnsErrorNotPartialData(t *testing.T) {
	c, _ := newStreamCache(t.TempDir(), 1<<30)
	fail := bytesFetcher(func(ctx context.Context, off, n int64) ([]byte, error) { return nil, errStreamOffline })
	buf := make([]byte, 100)
	n, err := c.ReadAt(context.Background(), "f", "m", streamChunk, buf, 0, fail)
	if !errors.Is(err, errStreamOffline) || n != 0 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	short := bytesFetcher(func(ctx context.Context, off, n int64) ([]byte, error) { return make([]byte, n-1), nil })
	if _, err := c.ReadAt(context.Background(), "g", "m", streamChunk, buf[:40], streamChunk-50, short); err == nil {
		t.Fatal("a short range response must be an error, never cached")
	}
}

func TestStreamCacheLoaderCancelDoesNotFailOtherReaders(t *testing.T) {
	size := int64(streamChunk)
	data, _, _ := fakeFile(size)
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	fetch := bytesFetcher(func(ctx context.Context, off, n int64) ([]byte, error) {
		select {
		case started <- struct{}{}:
		default:
		}
		select {
		case <-release:
			return append([]byte(nil), data[off:off+n]...), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	c, _ := newStreamCache(t.TempDir(), 1<<30)
	first, cancel := context.WithCancel(context.Background())
	done := make(chan error, 2)
	go func() {
		_, err := c.ReadAt(first, "f", "m", size, make([]byte, 10), 0, fetch)
		done <- err
	}()
	<-started
	go func() {
		buf := make([]byte, 10)
		_, err := c.ReadAt(context.Background(), "f", "m", size, buf, 0, fetch)
		if err == nil && !bytes.Equal(buf, data[:10]) {
			err = errors.New("wrong bytes")
		}
		done <- err
	}()
	cancel()
	close(release)
	var good int
	for i := 0; i < 2; i++ {
		if err := <-done; err == nil {
			good++
		}
	}
	if good < 1 {
		t.Fatal("a reader that was not canceled must still succeed")
	}
}

func TestStreamCacheRebuildRemovesLeftoverTempFiles(t *testing.T) {
	dir := t.TempDir()
	sub := dir + "/f_m"
	os.MkdirAll(sub, 0o700)
	os.WriteFile(sub+"/"+tmpPrefix+"x", []byte("junk"), 0o600)
	c, _ := newStreamCache(dir, 1<<30)
	if _, err := os.Stat(sub + "/" + tmpPrefix + "x"); !os.IsNotExist(err) || c.Used() != 0 {
		t.Fatalf("leftover temp file kept (used=%d)", c.Used())
	}
}

func TestStreamCacheReadsOnlyWhatIsAsked(t *testing.T) {
	size := int64(streamChunk)
	data, fetch, _ := fakeFile(size)
	c, _ := newStreamCache(t.TempDir(), 1<<30)
	buf := make([]byte, 128<<10)
	for off := int64(0); off < 1<<20; off += int64(len(buf)) {
		n, err := c.ReadAt(context.Background(), "f", "m", size, buf, off, fetch)
		if err != nil || n != len(buf) || !bytes.Equal(buf, data[off:off+int64(n)]) {
			t.Fatalf("off=%d n=%d err=%v", off, n, err)
		}
	}
}

func TestStreamCacheReadsAheadWhenSequentialAndHydrates(t *testing.T) {
	size := int64(streamChunk*6 + 10)
	_, fetch, calls := fakeFile(size)
	c, _ := newStreamCache(t.TempDir(), 1<<30)
	buf := make([]byte, 100)
	c.ReadAt(context.Background(), "f", "m", size, buf, 0, fetch)
	c.ReadAt(context.Background(), "f", "m", size, buf, streamChunk, fetch)
	c.waitIdle()
	deadline := time.Now().Add(3 * time.Second)
	for calls.Load() < 5 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if calls.Load() < 5 {
		t.Fatalf("sequential reads should prefetch the next chunks, fetches=%d", calls.Load())
	}
	big := int64(streamChunk * 12)
	_, f2, calls2 := fakeFile(big)
	c2, _ := newStreamCache(t.TempDir(), 1<<30)
	c2.ReadAt(context.Background(), "g", "m", big, buf, 0, f2)
	c2.waitIdle()
	base := calls2.Load()
	if base != 1 {
		t.Fatalf("the first read of a file must fetch only its own chunk, fetches=%d", base)
	}
	c2.ReadAt(context.Background(), "g", "m", big, buf, 9*streamChunk, f2)
	c2.waitIdle()
	if n := calls2.Load(); n != base+1 {
		t.Fatalf("a random jump must fetch only its own chunk: before=%d after=%d", base, n)
	}
	if err := c2.Hydrate(context.Background(), "g", "m", big, f2); err != nil {
		t.Fatal(err)
	}
	if c2.Used() != big {
		t.Fatalf("hydrate should cache the whole file: %d of %d", c2.Used(), big)
	}
}

func TestStreamCachePinnedStaleRevisionIsEvictable(t *testing.T) {
	size := int64(streamChunk)
	_, fetch, _ := fakeFile(size)
	c, _ := newStreamCache(t.TempDir(), 2*streamChunk)
	c.setPinned("p", "new", true)
	buf := make([]byte, 10)
	for _, id := range [][2]string{{"p", "old"}, {"p", "new"}, {"x", "m"}, {"y", "m"}} {
		c.ReadAt(context.Background(), id[0], id[1], size, buf, 0, fetch)
		c.waitIdle()
	}
	c.mu.Lock()
	_, old := c.items[chunkKey{"p", "old", 0}]
	_, cur := c.items[chunkKey{"p", "new", 0}]
	c.mu.Unlock()
	if old || !cur || c.Used() > 2*streamChunk {
		t.Fatalf("old revision kept=%v current kept=%v used=%d", old, cur, c.Used())
	}
}

func TestStreamCacheFailedPrefetchPausesReadAhead(t *testing.T) {
	size := int64(streamChunk * 8)
	data, _, _ := fakeFile(size)
	var calls atomic.Int64
	fetch := bytesFetcher(func(ctx context.Context, off, n int64) ([]byte, error) {
		calls.Add(1)
		if off >= streamChunk {
			return nil, errStreamOffline
		}
		return append([]byte(nil), data[off:off+n]...), nil
	})
	c, _ := newStreamCache(t.TempDir(), 1<<30)
	buf := make([]byte, 10)
	c.ReadAt(context.Background(), "f", "m", size, buf, 0, fetch)
	c.ReadAt(context.Background(), "f", "m", size, buf, 10, fetch)
	c.ReadAt(context.Background(), "f", "m", size, buf, streamChunk-100, fetch)
	c.waitIdle()
	first := calls.Load()
	for i := 0; i < 20; i++ {
		c.ReadAt(context.Background(), "f", "m", size, buf, int64(i), fetch)
	}
	c.waitIdle()
	if calls.Load() != first {
		t.Fatalf("a failing prefetch must not be retried on every read: %d -> %d", first, calls.Load())
	}
}

func TestStreamCacheServesRecentChunksFromMemory(t *testing.T) {
	size := int64(streamChunk * 3)
	data, fetch, calls := fakeFile(size)
	dir := t.TempDir()
	c, _ := newStreamCache(dir, 1<<30)
	buf := make([]byte, 100)
	c.ReadAt(context.Background(), "f", "m", size, buf, 0, fetch)
	c.waitIdle()
	before := calls.Load()
	os.RemoveAll(dir + "/" + chunkDirName("f", "m"))
	n, err := c.ReadAt(context.Background(), "f", "m", size, buf, 50, fetch)
	if err != nil || n != 100 || !bytes.Equal(buf, data[50:150]) {
		t.Fatalf("a chunk held in memory must not need the disk: n=%d err=%v", n, err)
	}
	if calls.Load() != before {
		t.Fatal("memory hit must not fetch from Drive")
	}
	limit := c.ramLimit()
	for i := 0; i < limit+5; i++ {
		c.ramPut(chunkKey{"x", "m", int64(i)}, []byte{1})
	}
	c.mu.Lock()
	held := len(c.ram)
	c.mu.Unlock()
	if held > limit {
		t.Fatalf("memory cache grew past its limit: %d", held)
	}
}

func TestStreamRAMBudgetFollowsFreeMemory(t *testing.T) {
	if got := streamRAMChunksFor(0); got != streamRAMChunks {
		t.Fatalf("unknown memory must use the default, got %d", got)
	}
	if got := streamRAMChunksFor(2 << 30); got != (512<<20)/streamChunk {
		t.Fatalf("2 GB free must give 512 MB, got %d chunks", got)
	}
	if got := streamRAMChunksFor(1 << 30); got != (256<<20)/streamChunk {
		t.Fatalf("1 GB free must give a quarter, got %d chunks", got)
	}
	if got := streamRAMChunksFor(5 << 30); got <= (512<<20)/streamChunk || got >= (1<<30)/streamChunk {
		t.Fatalf("5 GB free must sit between 512 MB and 1 GB, got %d", got)
	}
	if got := streamRAMChunksFor(100 << 20); got != (64<<20)/streamChunk {
		t.Fatalf("low memory must keep the 64 MB floor, got %d", got)
	}
	if got := streamRAMChunksFor(64 << 30); got != (1<<30)/streamChunk {
		t.Fatalf("plenty of memory must cap at 1 GB, got %d", got)
	}
}

func TestStreamRAMDropsIdleChunks(t *testing.T) {
	c, _ := newStreamCache(t.TempDir(), 1<<30)
	c.ramPut(chunkKey{"a", "m", 0}, []byte{1})
	c.ramPut(chunkKey{"b", "m", 0}, []byte{1})
	c.mu.Lock()
	c.ramUse[chunkKey{"a", "m", 0}] = time.Now().Add(-streamRAMIdle - time.Second)
	c.mu.Unlock()
	c.ramPut(chunkKey{"c", "m", 0}, []byte{1})
	c.mu.Lock()
	_, a := c.ram[chunkKey{"a", "m", 0}]
	_, cc := c.ram[chunkKey{"c", "m", 0}]
	c.mu.Unlock()
	if a || !cc {
		t.Fatalf("a chunk idle for minutes must be released, a=%v c=%v", a, cc)
	}
}

func TestStreamCacheBoundsConcurrentLoaders(t *testing.T) {
	size := int64(streamChunk * 40)
	var cur, peak atomic.Int64
	fetch := bytesFetcher(func(ctx context.Context, off, n int64) ([]byte, error) {
		v := cur.Add(1)
		for {
			p := peak.Load()
			if v <= p || peak.CompareAndSwap(p, v) {
				break
			}
		}
		time.Sleep(40 * time.Millisecond)
		cur.Add(-1)
		return make([]byte, n), nil
	})
	c, _ := newStreamCache(t.TempDir(), 1<<30)
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			buf := make([]byte, 100)
			c.ReadAt(context.Background(), "f", "m", size, buf, int64(i)*streamChunk, fetch)
		}(i)
	}
	wg.Wait()
	c.waitIdle()
	if p := peak.Load(); p > streamLoaders {
		t.Fatalf("scrubbing started %d parallel downloads, limit %d", p, streamLoaders)
	}
}

func TestStreamRAMTrimReleasesEverythingIdle(t *testing.T) {
	c, _ := newStreamCache(t.TempDir(), 1<<30)
	for i := 0; i < 3; i++ {
		c.ramPut(chunkKey{"a", "m", int64(i)}, []byte{1})
	}
	c.mu.Lock()
	for k := range c.ramUse {
		c.ramUse[k] = time.Now().Add(-streamRAMIdle - time.Second)
	}
	c.ramTrimLocked(c.ramLimit())
	left := len(c.ram)
	c.mu.Unlock()
	if left != 0 {
		t.Fatalf("idle chunks must all be released without a new load, %d left", left)
	}
}

func TestStreamQueuedLoaderHoldsNoBuffer(t *testing.T) {
	size := int64(streamChunk * 4)
	_, fetch, _ := fakeFile(size)
	c, _ := newStreamCache(t.TempDir(), 1<<30)
	for i := 0; i < streamLoaders; i++ {
		c.loads <- struct{}{}
	}
	pt := c.start(chunkKey{"f", "m", 1}, size, 0, fetch)
	time.Sleep(50 * time.Millisecond)
	pt.mu.Lock()
	held := pt.data != nil
	pt.mu.Unlock()
	if held {
		t.Fatal("a download waiting for a free slot must not hold an 8 MiB buffer")
	}
	for i := 0; i < streamLoaders; i++ {
		<-c.loads
	}
	<-pt.done
	c.waitIdle()
}

func dropRAM(c *streamCache) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for el := c.ramLRU.Back(); el != nil; el = c.ramLRU.Back() {
		c.ramDropLocked(el)
	}
}

func hasChunk(c *streamCache, id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.items[chunkKey{id, "m", 0}]
	return ok
}

func TestStreamCacheDiskHitsRefreshRecency(t *testing.T) {
	size := int64(streamChunk)
	_, fetch, _ := fakeFile(size)
	c, _ := newStreamCache(t.TempDir(), 2*streamChunk)
	buf := make([]byte, 10)
	for _, id := range []string{"a", "b"} {
		if _, err := c.ReadAt(context.Background(), id, "m", size, buf, 0, fetch); err != nil {
			t.Fatal(err)
		}
	}
	c.waitIdle()
	dropRAM(c)
	for i := 0; i < 3; i++ {
		if _, err := c.ReadAt(context.Background(), "a", "m", size, buf, 0, fetch); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := c.ReadAt(context.Background(), "c", "m", size, buf, 0, fetch); err != nil {
		t.Fatal(err)
	}
	c.waitIdle()
	if !hasChunk(c, "a") || hasChunk(c, "b") {
		t.Fatalf("the chunk read most recently must outlive the idle one: a=%v b=%v", hasChunk(c, "a"), hasChunk(c, "b"))
	}
}

func TestStreamCacheMemoryHitsRefreshDiskRecency(t *testing.T) {
	size := int64(streamChunk)
	_, fetch, _ := fakeFile(size)
	c, _ := newStreamCache(t.TempDir(), 2*streamChunk)
	buf := make([]byte, 10)
	for _, id := range []string{"a", "b"} {
		c.ReadAt(context.Background(), id, "m", size, buf, 0, fetch)
	}
	c.waitIdle()
	c.ReadAt(context.Background(), "a", "m", size, buf, 0, fetch)
	c.ReadAt(context.Background(), "c", "m", size, buf, 0, fetch)
	c.waitIdle()
	if !hasChunk(c, "a") || hasChunk(c, "b") {
		t.Fatalf("a=%v b=%v", hasChunk(c, "a"), hasChunk(c, "b"))
	}
}

func TestStreamCacheEvictionKeepsFolderAndRestartSweepsEmptyOnes(t *testing.T) {
	size := int64(streamChunk)
	_, fetch, _ := fakeFile(size)
	dir := t.TempDir()
	c, _ := newStreamCache(dir, streamChunk)
	buf := make([]byte, 10)
	c.ReadAt(context.Background(), "a", "m", size, buf, 0, fetch)
	c.ReadAt(context.Background(), "b", "m", size, buf, 0, fetch)
	c.waitIdle()
	if hasChunk(c, "a") {
		t.Fatal("setup: a should be evicted")
	}
	if _, err := os.Stat(dir + "/" + chunkDirName("a", "m")); err != nil {
		t.Fatalf("evicting must not remove the folder a sibling chunk load may be writing into: %v", err)
	}
	if _, err := newStreamCache(dir, streamChunk); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir + "/" + chunkDirName("a", "m")); !os.IsNotExist(err) {
		t.Fatalf("a restart must sweep the empty folder: %v", err)
	}
	if _, err := os.Stat(dir + "/" + chunkDirName("b", "m")); err != nil {
		t.Fatalf("a folder with chunks must stay: %v", err)
	}
}

type slowBody struct {
	ctx   context.Context
	data  []byte
	step  int
	gap   time.Duration
	hang  bool
	given int
}

func (b *slowBody) Read(p []byte) (int, error) {
	if len(b.data) == 0 {
		return 0, io.EOF
	}
	if b.hang && b.given > 0 {
		<-b.ctx.Done()
		return 0, b.ctx.Err()
	}
	select {
	case <-time.After(b.gap):
	case <-b.ctx.Done():
		return 0, b.ctx.Err()
	}
	n := min(b.step, len(b.data), len(p))
	copy(p, b.data[:n])
	b.data = b.data[n:]
	b.given += n
	return n, nil
}

func (b *slowBody) Close() error { return nil }

func setStreamTimeouts(t *testing.T, stall, total time.Duration, tries int) {
	ps, pt, pf := streamStallTimeout, streamLoadTotal, streamFetchTries
	streamStallTimeout, streamLoadTotal, streamFetchTries = stall, total, tries
	t.Cleanup(func() { streamStallTimeout, streamLoadTotal, streamFetchTries = ps, pt, pf })
}

func TestStreamCacheSlowButSteadyDownloadCompletes(t *testing.T) {
	setStreamTimeouts(t, 300*time.Millisecond, 20*time.Second, 1)
	size := int64(1 << 20)
	data, _, _ := fakeFile(size)
	fetch := func(ctx context.Context, off, n int64) (io.ReadCloser, error) {
		return &slowBody{ctx: ctx, data: data[off : off+n], step: 64 << 10, gap: 100 * time.Millisecond}, nil
	}
	c, _ := newStreamCache(t.TempDir(), 1<<30)
	buf := make([]byte, size)
	start := time.Now()
	n, err := c.ReadAt(context.Background(), "f", "m", size, buf, 0, fetch)
	if err != nil || int64(n) != size || !bytes.Equal(buf, data) {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if time.Since(start) < 600*time.Millisecond {
		t.Fatal("setup: the download should take longer than the stall timeout")
	}
	c.waitIdle()
	if c.Used() != size {
		t.Fatalf("chunk not cached: %d", c.Used())
	}
}

func TestStreamCacheStalledDownloadFailsAfterStallTimeout(t *testing.T) {
	setStreamTimeouts(t, 150*time.Millisecond, 30*time.Second, 1)
	size := int64(1 << 20)
	data, _, _ := fakeFile(size)
	fetch := func(ctx context.Context, off, n int64) (io.ReadCloser, error) {
		return &slowBody{ctx: ctx, data: data[off : off+n], step: 64 << 10, gap: time.Millisecond, hang: true}, nil
	}
	c, _ := newStreamCache(t.TempDir(), 1<<30)
	start := time.Now()
	_, err := c.ReadAt(context.Background(), "f", "m", size, make([]byte, size), 0, fetch)
	if !errors.Is(err, errStreamStalled) {
		t.Fatalf("err=%v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("a stalled download must fail after the stall timeout, took %v", time.Since(start))
	}
	c.waitIdle()
	if c.Used() != 0 {
		t.Fatal("a failed download must not be cached")
	}
}

func TestStreamCacheCloseStopsLoadersAndReleasesMemory(t *testing.T) {
	dir := t.TempDir()
	size := int64(streamChunk)
	started := make(chan struct{}, 1)
	fetch := bytesFetcher(func(ctx context.Context, off, n int64) ([]byte, error) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-ctx.Done()
		return nil, ctx.Err()
	})
	c, _ := newStreamCache(dir, 1<<30)
	done := make(chan error, 1)
	go func() {
		_, err := c.ReadAt(context.Background(), "f", "m", size, make([]byte, 10), 0, fetch)
		done <- err
	}()
	<-started
	c.Close()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a load running when the cache closes must fail")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not stop the loader")
	}
	c.waitIdle()
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("a closed cache must not write chunks: %v", entries)
	}

	c2, _ := newStreamCache(t.TempDir(), 1<<30)
	c2.ramPut(chunkKey{"a", "m", 0}, []byte{1})
	c2.Close()
	c2.ramPut(chunkKey{"a", "m", 1}, []byte{1})
	c2.mu.Lock()
	held, tmr := len(c2.ram), c2.ramTmr
	c2.mu.Unlock()
	if held != 0 || tmr != nil {
		t.Fatalf("Close must clear memory and stop the timer: held=%d timer=%v", held, tmr)
	}
}

func TestStreamCacheEvictsWhenOpenedOverLimitAndWhenPinsChange(t *testing.T) {
	dir := t.TempDir()
	for _, id := range []string{"a", "b", "c"} {
		os.MkdirAll(dir+"/"+chunkDirName(id, "m"), 0o700)
		os.WriteFile(dir+"/"+chunkDirName(id, "m")+"/0", make([]byte, 10), 0o600)
	}
	c, err := newStreamCache(dir, 10)
	if err != nil {
		t.Fatal(err)
	}
	if c.Used() != 30 {
		t.Fatalf("opening must not evict before pins are known: %d", c.Used())
	}
	c.setPinnedSet(map[string]string{"b": "m"})
	if c.Used() > 10 || !hasChunk(c, "b") {
		t.Fatalf("a lowered limit must be enforced once pins are set, keeping pinned b: used=%d b=%v", c.Used(), hasChunk(c, "b"))
	}
	c2, _ := newStreamCache(t.TempDir(), 100)
	for _, id := range []string{"a", "b", "c"} {
		c2.items[chunkKey{id, "m", 0}] = c2.lru.PushFront(&cacheItem{key: chunkKey{id, "m", 0}, size: 10})
		c2.used += 10
	}
	c2.limit = 10
	c2.setPinnedSet(map[string]string{"a": "m"})
	if c2.Used() != 10 || !hasChunk(c2, "a") {
		t.Fatalf("changing pins must evict the unpinned overflow: used=%d a=%v", c2.Used(), hasChunk(c2, "a"))
	}
}
