package main

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
)

type Entry struct {
	ID    string `json:"id"`
	MD5   string `json:"md5"`
	Size  int64  `json:"size"`
	MTime int64  `json:"mtime"`
}

type State struct {
	Files map[string]Entry  `json:"files"`
	Links map[string]string `json:"links"`
}

func loadState(path string) (*State, error) {
	st := &State{}
	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return nil, err
	default:
		if err := json.Unmarshal(b, st); err != nil {
			return nil, err
		}
	}
	if st.Files == nil {
		st.Files = map[string]Entry{}
	}
	if st.Links == nil {
		st.Links = map[string]string{}
	}
	return st, nil
}

func (s *State) save(path string) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return writeFileAtomic(path, b, 0o600)
}
