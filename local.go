package main

import (
	"io/fs"
	"path/filepath"
	"strings"
)

const tmpPrefix = ".gdsync-"

type LocalFile struct {
	Size  int64
	MTime int64
}

func scanLocal(root string, ex *excluder) (map[string]LocalFile, error) {
	out := map[string]LocalFile{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == root {
			return nil
		}
		if !d.IsDir() && strings.HasPrefix(d.Name(), tmpPrefix+"part-") {
			removeStalePart(p, d)
			return nil
		}
		if rel, err := filepath.Rel(root, p); err == nil && ex.skip(p, filepath.ToSlash(rel)) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		if strings.HasPrefix(d.Name(), tmpPrefix) || isLinkFile(d.Name()) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		out[filepath.ToSlash(rel)] = LocalFile{Size: info.Size(), MTime: info.ModTime().UnixNano()}
		return nil
	})
	return out, err
}
