package main

import (
	"errors"
	"fmt"
	"sort"
)

func uniqueSorted(in []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

func (r *Runner) setPending(tops []string) {
	u := uniqueSorted(tops)
	r.mu.Lock()
	r.pending = u
	r.status.PendingCount = len(u)
	r.status.Pending = u[:min(len(u), pendingPreview)]
	r.mu.Unlock()
}

func (r *Runner) CancelUploads() ([]string, error) {
	r.mu.Lock()
	tops := append([]string(nil), r.pending...)
	stop := r.passStop
	r.mu.Unlock()
	if len(tops) == 0 {
		return nil, errors.New("nothing is uploading right now")
	}
	_, err := editConfig(r.paths, func(c *AppConfig) error {
		next, err := normalizeExcludes(append(append([]string{}, c.Exclude...), tops...))
		if err != nil {
			return err
		}
		c.Exclude = next
		return nil
	})
	if err != nil {
		return nil, err
	}
	if stop != nil {
		stop()
	}
	r.setPending(nil)
	r.addRecent(fmt.Sprintf("cancelled uploads; added %d item(s) to Don't back up", len(tops)))
	r.Trigger()
	return tops, nil
}
