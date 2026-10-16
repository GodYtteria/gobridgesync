package main

import (
	"bufio"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

func (p Paths) syncIDs() string   { return filepath.Join(p.StateDir, "sync-ids") }
func (p Paths) backupIDs() string { return filepath.Join(p.StateDir, "backup-ids") }

type idJournal struct {
	mu   sync.Mutex
	path string
	ids  map[string]string
	f    *os.File
}

func loadIDJournal(path string) *idJournal {
	j := &idJournal{path: path, ids: map[string]string{}}
	f, err := os.Open(path)
	if err != nil {
		return j
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		if key, id, ok := strings.Cut(sc.Text(), "\t"); ok && id != "" {
			j.ids[key] = id
		}
	}
	return j
}

func (j *idJournal) get(key string) (string, bool) {
	if j == nil {
		return "", false
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	id, ok := j.ids[key]
	return id, ok
}

func (j *idJournal) put(key, id string) error {
	if j == nil || strings.ContainsAny(key, "\t\n") {
		return nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.f == nil {
		f, err := os.OpenFile(j.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return err
		}
		j.f = f
	}
	if _, err := j.f.WriteString(key + "\t" + id + "\n"); err != nil {
		return err
	}
	if err := j.f.Sync(); err != nil {
		return err
	}
	j.ids[key] = id
	return nil
}

func (j *idJournal) drop(key string) error {
	return j.prune(func(k string) bool { return k == key })
}

func (j *idJournal) prune(resolved func(key string) bool) error {
	if j == nil {
		return nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	changed := false
	for k := range j.ids {
		if resolved(k) {
			delete(j.ids, k)
			changed = true
		}
	}
	if !changed {
		return nil
	}
	if j.f != nil {
		j.f.Close()
		j.f = nil
	}
	if len(j.ids) == 0 {
		if err := os.Remove(j.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	keys := make([]string, 0, len(j.ids))
	for k := range j.ids {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k + "\t" + j.ids[k] + "\n")
	}
	return writeFileAtomic(j.path, []byte(b.String()), 0o600)
}
