package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

const localFullScanEvery = 10 * time.Minute

type localCache struct {
	mu      sync.Mutex
	root    string
	exclude []string
	files   map[string]LocalFile

	unreadable []string
	dirty      map[string]bool
	watching   bool
	full       bool
	fullAt     time.Time
}

func newLocalCache() *localCache { return &localCache{full: true} }

func (c *localCache) setWatching(on bool) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.watching = on
	c.full = true
}

func (c *localCache) changed(abs string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dirty == nil {
		c.dirty = map[string]bool{}
	}
	c.dirty[abs] = true
}

func (c *localCache) scan(root string, ex *excluder, exclude []string, now time.Time) (map[string]LocalFile, []string, error) {
	if c == nil {
		return scanLocalSkipping(root, ex)
	}
	c.mu.Lock()
	dirty := c.dirty
	c.dirty = nil
	full := c.full || !c.watching || c.files == nil || c.root != root || !slices.Equal(c.exclude, exclude) || now.Sub(c.fullAt) >= localFullScanEvery
	c.mu.Unlock()
	if full {
		files, skipped, err := scanLocalSkipping(root, ex)
		if err != nil {
			c.mu.Lock()
			c.full = true
			c.mu.Unlock()
			return nil, nil, err
		}
		c.mu.Lock()
		c.files, c.root, c.exclude, c.fullAt, c.full = files, root, slices.Clone(exclude), now, false
		c.unreadable = slices.Clone(skipped)
		c.mu.Unlock()
		return files, skipped, nil
	}
	for abs := range dirty {
		if err := c.refresh(root, ex, abs); err != nil {
			c.mu.Lock()
			c.full = true
			c.mu.Unlock()
			return scanLocalSkipping(root, ex)
		}
	}
	return c.files, slices.Clone(c.unreadable), nil
}

func (c *localCache) refresh(root string, ex *excluder, abs string) error {
	rel, err := filepath.Rel(root, abs)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return nil
	}
	rel = filepath.ToSlash(rel)
	c.dropUnreadable(rel)
	if _, wasFile := c.files[rel]; wasFile {
		delete(c.files, rel)
	} else {
		c.dropTree(rel)
	}
	for dir := path0(rel); dir != ""; dir = path0(dir) {
		if ex.skip(filepath.Join(root, filepath.FromSlash(dir)), dir) {
			return nil
		}
	}
	if _, err := os.Lstat(abs); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	err = walkLocal(root, abs, ex, c.files, &c.unreadable)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

func (c *localCache) dropUnreadable(rel string) {
	c.unreadable = slices.DeleteFunc(c.unreadable, func(u string) bool { return u == rel || strings.HasPrefix(u, rel+"/") })
}

func (c *localCache) dropTree(rel string) {
	for k := range c.files {
		if strings.HasPrefix(k, rel+"/") {
			delete(c.files, k)
		}
	}
}

func path0(rel string) string {
	i := strings.LastIndex(rel, "/")
	if i < 0 {
		return ""
	}
	return rel[:i]
}
