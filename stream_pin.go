package main

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"
	"time"
)

const pinLockWait = 5 * time.Second

type pinStore struct{ file string }

func (p Paths) pins() string { return p.StateDir + "/stream-pins.json" }

func cleanPin(rel string) (string, error) {
	rel = strings.Trim(path.Clean("/"+strings.TrimSpace(rel)), "/")
	if rel == "" {
		return "", errors.New("give a file or folder path inside your Drive, for example Models/llama.gguf")
	}
	return rel, nil
}

func (s pinStore) load() ([]string, error) {
	b, err := os.ReadFile(s.file)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s pinStore) save(list []string) error {
	sort.Strings(list)
	b, err := json.Marshal(list)
	if err != nil {
		return err
	}
	return writeFileAtomic(s.file, b, 0o600)
}

func (s pinStore) lockEdit() (*os.File, error) {
	deadline := time.Now().Add(pinLockWait)
	for {
		lf, err := lock(s.file + ".lock")
		if err == nil {
			return lf, nil
		}
		if time.Now().After(deadline) {
			return nil, errors.New("the pinned list is being changed by another command; try again")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (s pinStore) edit(rel string, add bool) error {
	rel, err := cleanPin(rel)
	if err != nil {
		return err
	}
	lf, err := s.lockEdit()
	if err != nil {
		return err
	}
	defer lf.Close()
	list, err := s.load()
	if err != nil {
		return err
	}
	out := list[:0:0]
	for _, p := range list {
		if p != rel {
			out = append(out, p)
		}
	}
	if add {
		out = append(out, rel)
	}
	return s.save(out)
}

func pinnedFiles(r *Remote, pins []string) map[string]RemoteFile {
	out := map[string]RemoteFile{}
	if len(pins) == 0 {
		return out
	}
	set := make(map[string]bool, len(pins))
	for _, pin := range pins {
		set[pin] = true
	}
	for p, f := range r.Files {
		for q := p; ; {
			if set[q] {
				out[p] = f
				break
			}
			i := strings.LastIndexByte(q, '/')
			if i < 0 {
				break
			}
			q = q[:i]
		}
	}
	return out
}

func pinTotal(files map[string]RemoteFile) int64 {
	var n int64
	for _, f := range files {
		n += f.Size
	}
	return n
}

func pinRoom(limit, budget, used int64) int64 {
	if budget >= limit-used {
		return limit
	}
	return budget + used
}
