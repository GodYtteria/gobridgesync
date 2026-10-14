package main

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
)

type history struct {
	Recent  []string  `json:"recent"`
	Issues  []Issue   `json:"issues"`
	LastRun *RunStats `json:"lastRun"`
}

func (p Paths) history() string { return filepath.Join(p.StateDir, "history.json") }

func loadHistory(p Paths) history {
	var h history
	if b, err := os.ReadFile(p.history()); err == nil {
		if err := json.Unmarshal(b, &h); err != nil {
			log.Printf("history unreadable, starting fresh: %v", err)
			h = history{}
		}
	}
	if len(h.Recent) > recentLimit {
		h.Recent = h.Recent[:recentLimit]
	}
	if len(h.Issues) > issueLimit {
		h.Issues = h.Issues[:issueLimit]
	}
	if h.Recent == nil {
		h.Recent = []string{}
	}
	return h
}

func (r *Runner) saveHistory() {
	r.mu.Lock()
	h := history{
		Recent:  append([]string(nil), r.status.Recent...),
		Issues:  append([]Issue(nil), r.status.Issues...),
		LastRun: r.status.LastRun,
	}
	r.mu.Unlock()
	b, err := json.Marshal(h)
	if err != nil {
		log.Printf("save history: %v", err)
		return
	}
	if err := writeFileAtomic(r.paths.history(), b, 0o600); err != nil {
		log.Printf("save history: %v", err)
	}
}
