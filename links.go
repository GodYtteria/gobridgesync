package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
)

func (s *Syncer) syncLinks() error {
	var errs []error
	for p, d := range s.remote.Docs {
		b, err := json.Marshal(map[string]string{"url": d.URL, "doc_id": d.ID})
		if err != nil {
			return err
		}
		full := s.abs(p)
		if cur, err := os.ReadFile(full); err != nil || string(cur) != string(b) {
			if err := writeFileAtomic(full, b, 0o644); err != nil {
				errs = append(errs, fmt.Errorf("link %s: %w", p, err))
				continue
			}
		}
		if s.st.Links[p] != d.ID {
			s.st.Links[p] = d.ID
			s.dirty = true
		}
	}
	for p := range s.st.Links {
		if _, ok := s.remote.Docs[p]; ok || s.remote.blocked(p) {
			continue
		}
		if err := os.Remove(s.abs(p)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, fmt.Errorf("link %s: %w", p, err))
			continue
		}
		delete(s.st.Links, p)
		s.dirty = true
	}
	return errors.Join(errs...)
}
