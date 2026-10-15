package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"time"
)

func passGap(took time.Duration) time.Duration {
	return min(max(took*passGapFactor, passGapMin), passGapMax)
}

const (
	passGapFactor = 20
	passGapMin    = 5 * time.Second
	passGapMax    = 2 * time.Minute
)

const (
	instantRemoteCheck = time.Minute
	watchEvery         = 10 * time.Second
	watchEveryLarge    = 30 * time.Second
	largeTree          = 10000
)

func passInterval(every int) (time.Duration, bool) {
	switch {
	case every == manualSync:
		return 0, false
	case every == instantSync:
		return instantRemoteCheck, true
	case every > 0:
		return time.Duration(every) * time.Minute, true
	}
	return defaultEveryMinutes * time.Minute, true
}

func localFingerprint(cfg AppConfig, root string) (string, int) {
	ex := newExcluder(cfg.Exclude)
	h := sha256.New()
	count := 0
	add := func(prefix string, files map[string]LocalFile) {
		count += len(files)
		keys := make([]string, 0, len(files))
		for k := range files {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(h, "%s/%s\x00%d\x00%d\n", prefix, k, files[k].Size, files[k].MTime)
		}
	}
	for _, dir := range cfg.Folders {
		files, _, _ := scanTree(dir, ex)
		add(dir, files)
	}
	if cfg.TwoWay {
		if files, err := scanLocal(root, ex); err == nil {
			add(root, files)
		}
	}
	return hex.EncodeToString(h.Sum(nil)), count
}

func (r *Runner) pollChanges(ctx context.Context, cfg AppConfig, last *string, every *time.Duration) {
	start := time.Now()
	fp, files := localFingerprint(cfg, r.paths.Root)
	*every = watchEvery
	if files > largeTree {
		*every = watchEveryLarge
	}
	*every = max(*every, min(time.Since(start)*passGapFactor, passGapMax))
	if *last != "" && fp != *last {
		r.Changed()
	}
	*last = fp
}
