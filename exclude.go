package main

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"
	"sync"
)

var developerSkips = []string{
	"node_modules", ".venv", "venv", "__pycache__", ".pytest_cache", ".mypy_cache",
	".next", ".nuxt", ".svelte-kit", ".turbo", ".parcel-cache",
	"dist", "build", "target", ".gradle", "*.pyc", "*.o", "*.class",
}

const excludeMemoMax = 1 << 20

type excluder struct {
	abs   []string
	rel   []string
	names []string

	mu   sync.Mutex
	memo map[string]bool
}

func normalizeExcludes(in []string) ([]string, error) {
	out := []string{}
	seen := map[string]bool{}
	for _, raw := range in {
		p := strings.TrimSpace(raw)
		if p == "" {
			continue
		}
		if strings.HasPrefix(p, "~") || filepath.IsAbs(p) {
			dir, err := expandHome(p)
			if err != nil {
				return nil, err
			}
			p = filepath.Clean(dir)
		} else {
			p = strings.Trim(filepath.ToSlash(p), "/")
			if _, err := path.Match(p, ""); err != nil {
				return nil, fmt.Errorf("%q: invalid pattern", raw)
			}
			if p == "" || p == "." || strings.HasPrefix(p, "../") || p == ".." {
				return nil, fmt.Errorf("%q: invalid pattern", raw)
			}
		}
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out, nil
}

func newExcluder(patterns []string) *excluder {
	x := &excluder{}
	for _, p := range patterns {
		switch {
		case filepath.IsAbs(p):
			x.abs = append(x.abs, p)
		case strings.Contains(p, "/"):
			x.rel = append(x.rel, p)
		default:
			x.names = append(x.names, p)
		}
	}
	return x
}

func (x *excluder) skip(full, rel string) bool {
	if x == nil {
		return false
	}
	x.mu.Lock()
	hit, ok := x.memo[full]
	x.mu.Unlock()
	if ok {
		return hit
	}
	hit = x.match(full, rel)
	x.mu.Lock()
	if x.memo == nil || len(x.memo) >= excludeMemoMax {
		x.memo = map[string]bool{}
	}
	x.memo[full] = hit
	x.mu.Unlock()
	return hit
}

func (x *excluder) match(full, rel string) bool {
	for _, a := range x.abs {
		if within(foldKey(full), foldKey(a)) {
			return true
		}
	}
	for _, part := range strings.Split(rel, "/") {
		for _, n := range x.names {
			if ok, _ := path.Match(n, part); ok {
				return true
			}
		}
	}
	for _, r := range x.rel {
		if ok, _ := path.Match(r, rel); ok || strings.HasPrefix(rel, r+"/") {
			return true
		}
	}
	return false
}
