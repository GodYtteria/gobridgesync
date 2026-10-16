package main

import (
	"fmt"
	"math"
	"path/filepath"
	"sync"
	"testing"
)

func TestPinStoreConcurrentEditsKeepEveryPin(t *testing.T) {
	s := pinStore{file: filepath.Join(t.TempDir(), "pins.json")}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := s.edit(fmt.Sprintf("Models/m%d.gguf", i), true); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	list, err := s.load()
	if err != nil || len(list) != 8 {
		t.Fatalf("every pin added at the same time must be kept, got %v %v", list, err)
	}
}

func TestPinnedFilesMatchesWholeFolderNamesOnly(t *testing.T) {
	r := &Remote{Files: map[string]RemoteFile{"a/b": {}, "a/b/c.bin": {}, "a/bc/d.bin": {}, "x.bin": {}}}
	got := pinnedFiles(r, []string{"a/b"})
	if len(got) != 2 {
		t.Fatalf("pin a/b must match a/b and a/b/c.bin only, got %v", got)
	}
}

func TestPinStoreAddRemoveAndResolve(t *testing.T) {
	s := pinStore{file: filepath.Join(t.TempDir(), "pins.json")}
	if err := s.edit("/Models/a.gguf/", true); err != nil {
		t.Fatal(err)
	}
	s.edit("Docs", true)
	s.edit("Models/a.gguf", true)
	list, _ := s.load()
	if len(list) != 2 || list[0] != "Docs" || list[1] != "Models/a.gguf" {
		t.Fatalf("pins %v", list)
	}
	r := &Remote{Files: map[string]RemoteFile{"Models/a.gguf": {ID: "1", Size: 10}, "Docs/x.md": {ID: "2", Size: 5}, "Docs/sub/y.md": {ID: "3", Size: 1}, "Other/z": {ID: "4", Size: 100}}}
	got := pinnedFiles(r, list)
	if len(got) != 3 || pinTotal(got) != 16 {
		t.Fatalf("resolved %v", got)
	}
	s.edit("Docs", false)
	if list, _ := s.load(); len(list) != 1 {
		t.Fatalf("unpin failed %v", list)
	}
	if err := s.edit("  ", true); err == nil {
		t.Fatal("an empty path must be rejected")
	}
	if _, err := (pinStore{file: filepath.Join(t.TempDir(), "none.json")}).load(); err != nil {
		t.Fatal("a missing pin file is an empty list")
	}
}

func TestPinRoomSaturatesInsteadOfOverflowing(t *testing.T) {
	cases := []struct{ limit, budget, used, want int64 }{
		{1 << 30, math.MaxInt64, 1 << 20, 1 << 30},
		{1 << 30, math.MaxInt64, 0, 1 << 30},
		{10, 3, 4, 7},
		{10, 8, 4, 10},
		{10, 0, 20, 10},
	}
	for _, c := range cases {
		if got := pinRoom(c.limit, c.budget, c.used); got != c.want {
			t.Fatalf("pinRoom(%d,%d,%d)=%d want %d", c.limit, c.budget, c.used, got, c.want)
		}
	}
}
