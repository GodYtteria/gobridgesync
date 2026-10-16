package main

import (
	"io/fs"
	"log"
	"path/filepath"
	"strings"
)

const tmpPrefix = ".gdsync-"

type LocalFile struct {
	Size  int64
	MTime int64
}

func scanLocal(root string, ex *excluder) (map[string]LocalFile, error) {
	out, _, err := scanLocalSkipping(root, ex)
	return out, err
}

func scanLocalSkipping(root string, ex *excluder) (map[string]LocalFile, []string, error) {
	out := map[string]LocalFile{}
	var skipped []string
	err := walkLocal(root, root, ex, out, &skipped)
	return out, skipped, err
}

func walkLocal(root, start string, ex *excluder, out map[string]LocalFile, skipped *[]string) error {
	return filepath.WalkDir(start, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == root || d == nil || !d.IsDir() {
				return err
			}
			rel, rerr := filepath.Rel(root, p)
			if rerr != nil {
				return err
			}
			log.Printf("scan %s: cannot read folder, skipped this time: %v", rel, err)
			*skipped = append(*skipped, filepath.ToSlash(rel))
			return filepath.SkipDir
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
}
