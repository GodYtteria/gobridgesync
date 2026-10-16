package main

import (
	"path"
	"sort"
	"sync"
)

type treeEntry struct {
	Name string
	Dir  bool
	File RemoteFile
}

type streamTree struct {
	mu   sync.Mutex
	from *Remote
	kids map[string]map[string]treeEntry
}

func (t *streamTree) index(r *Remote) map[string]map[string]treeEntry {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.from == r && t.kids != nil {
		return t.kids
	}
	kids := map[string]map[string]treeEntry{"": {}}
	add := func(p string, e treeEntry) {
		dir := path.Dir(p)
		if dir == "." {
			dir = ""
		}
		if kids[dir] == nil {
			kids[dir] = map[string]treeEntry{}
		}
		kids[dir][e.Name] = e
	}
	for p := range r.Folders {
		if p == "" {
			continue
		}
		if kids[p] == nil {
			kids[p] = map[string]treeEntry{}
		}
		add(p, treeEntry{Name: path.Base(p), Dir: true})
	}
	for p, f := range r.Files {
		add(p, treeEntry{Name: path.Base(p), File: f})
	}
	t.from, t.kids = r, kids
	return kids
}

func (t *streamTree) List(r *Remote, dir string) ([]treeEntry, bool) {
	kids, ok := t.index(r)[dir]
	if !ok {
		return nil, false
	}
	out := make([]treeEntry, 0, len(kids))
	for _, e := range kids {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, true
}

func (t *streamTree) Lookup(r *Remote, dir, name string) (treeEntry, bool) {
	e, ok := t.index(r)[dir][name]
	return e, ok
}
