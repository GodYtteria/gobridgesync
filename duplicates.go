package main

import (
	"context"
	"fmt"
	"maps"
	"path"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"google.golang.org/api/drive/v3"
)

func duplicateRenames(dupes map[string][]*drive.File, ours func(id string) bool, now time.Time) map[string]string {
	out := map[string]string{}
	keys := make([]string, 0, len(dupes))
	for k := range dupes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		files := exactNameGroup(dupes[k])
		if len(files) < 2 || slices.ContainsFunc(files, func(f *drive.File) bool {
			return len(f.Parents) == 0 || len(files[0].Parents) == 0 || f.Parents[0] != files[0].Parents[0]
		}) {
			continue
		}
		sort.SliceStable(files, func(i, j int) bool {
			oi, oj := ours(files[i].Id), ours(files[j].Id)
			if oi != oj {
				return oi
			}
			if files[i].ModifiedTime != files[j].ModifiedTime {
				return files[i].ModifiedTime < files[j].ModifiedTime
			}
			return files[i].Id < files[j].Id
		})
		for n, f := range files[1:] {
			ext := path.Ext(f.Name)
			if f.MimeType == folderMime || strings.HasPrefix(f.MimeType, gappsPrefix) {
				ext = ""
			}
			suffix := ""
			if n > 0 {
				suffix = fmt.Sprintf(" %d", n+1)
			}
			out[f.Id] = strings.TrimSuffix(f.Name, ext) + " (conflict " + now.Format("20060102-150405") + suffix + ")" + ext
		}
	}
	return out
}

func exactNameGroup(all []*drive.File) []*drive.File {
	byName := map[string][]*drive.File{}
	for _, f := range all {
		if !f.OwnedByMe || (f.Capabilities != nil && !f.Capabilities.CanRename) {
			continue
		}
		byName[f.Name] = append(byName[f.Name], f)
	}
	var best []*drive.File
	for _, g := range byName {
		if len(g) > len(best) {
			best = g
		}
	}
	return slices.Clone(best)
}

const dupSettle = 2 * time.Minute

type dupTracker struct {
	mu    sync.Mutex
	first map[string]time.Time
}

func (d *dupTracker) settled(dupes map[string][]*drive.File, now time.Time) map[string][]*drive.File {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.first == nil {
		d.first = map[string]time.Time{}
	}
	out := map[string][]*drive.File{}
	for k, fs := range dupes {
		seen, ok := d.first[k]
		if !ok {
			d.first[k] = now
			continue
		}
		if now.Sub(seen) >= dupSettle {
			out[k] = fs
		}
	}
	for k := range d.first {
		if _, ok := dupes[k]; !ok {
			delete(d.first, k)
		}
	}
	return out
}

func (s *Syncer) renameDuplicates(ctx context.Context, remote *Remote) []error {
	dupes := s.dups.settled(remote.Dupes, time.Now())
	if len(dupes) == 0 {
		return nil
	}
	s.mu.Lock()
	known := map[string]bool{}
	for _, e := range s.st.Files {
		known[e.ID] = true
	}
	for _, id := range remote.Folders {
		known[id] = true
	}
	for _, id := range slices.Collect(maps.Keys(known)) {
		s.index.ancestors(id, known)
	}
	s.mu.Unlock()
	parent := map[string]string{}
	for _, fs := range dupes {
		for _, f := range fs {
			if len(f.Parents) > 0 {
				parent[f.Id] = f.Parents[0]
			}
		}
	}
	var errs []error
	for id, name := range duplicateRenames(dupes, func(id string) bool { return known[id] }, time.Now()) {
		err := retry(ctx, func() error {
			_, err := s.srv.Files.Update(id, &drive.File{Name: name}).Fields("id").Context(ctx).Do()
			return err
		})
		if err != nil {
			errs = append(errs, fmt.Errorf("rename duplicate to %s: %w", name, err))
			continue
		}
		s.index.moved(id, name, parent[id])
		s.report("renamed duplicate on Drive to " + name)
	}
	return errs
}
