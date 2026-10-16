package main

import (
	"log"
	"os"
	"path/filepath"
	"time"
)

const (
	trashStamp  = "20060102-150405"
	trashMaxAge = 30 * 24 * time.Hour
)

func pruneTrash(dir string, now time.Time) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("prune trash: %v", err)
		}
		return
	}
	for _, e := range entries {
		when, perr := time.ParseInLocation(trashStamp, e.Name(), time.Local)
		if perr != nil {
			info, ierr := e.Info()
			if ierr != nil {
				log.Printf("prune trash %s: %v", e.Name(), ierr)
				continue
			}
			when = info.ModTime()
		}
		if now.Sub(when) <= trashMaxAge {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			log.Printf("prune trash %s: %v", e.Name(), err)
			continue
		}
		log.Printf("trash: removed %s (older than 30 days)", e.Name())
	}
}
