package main

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"
)

type excluder struct {
	abs   []string
	rel   []string
	names []string
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
	for _, a := range x.abs {
		if within(foldKey(full), foldKey(a)) {
			return true
		}
	}
	name := path.Base(rel)
	for _, n := range x.names {
		if ok, _ := path.Match(n, name); ok {
			return true
		}
	}
	for _, r := range x.rel {
		if ok, _ := path.Match(r, rel); ok || strings.HasPrefix(rel, r+"/") {
			return true
		}
	}
	return false
}
