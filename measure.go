package main

import (
	"errors"
	"path/filepath"
	"strings"
)

type Measurement struct {
	FolderSize
	Free  int64 `json:"free"`
	Known bool  `json:"known"`
	Fits  bool  `json:"fits"`
}

func (r *Runner) Measure(raw string) (Measurement, error) {
	dir, err := expandHome(strings.TrimSpace(raw))
	if err != nil {
		return Measurement{}, err
	}
	if !filepath.IsAbs(dir) {
		return Measurement{}, errors.New("use a full path like ~/Documents")
	}
	cfg, _ := loadAppConfig(r.paths)
	if _, err := normalizeFolders(append(append([]string{}, cfg.Folders...), dir), r.paths.Root, cfg.Folders); err != nil {
		return Measurement{}, err
	}
	files, _, errs := scanTree(filepath.Clean(dir), newExcluder(cfg.Exclude))
	if files == nil {
		return Measurement{}, errors.Join(errs...)
	}
	m := Measurement{FolderSize: measureFiles(files), Fits: true}
	if q := r.Snapshot().Quota; q != nil && q.Limit > 0 {
		m.Known = true
		m.Free = max(q.Limit*int64(cfg.limit())/100-q.Used, 0)
		m.Fits = m.Bytes <= m.Free
	}
	return m, nil
}
