package main

import "testing"

func TestStreamTreeListsAndLooksUp(t *testing.T) {
	r := &Remote{
		Folders: map[string]string{"": "root", "models": "m", "models/llama": "l", "empty": "e"},
		Files:   map[string]RemoteFile{"models/llama/a.gguf": {ID: "1", Size: 5}, "models/b.txt": {ID: "2"}, "top.txt": {ID: "3"}},
	}
	var tr streamTree
	root, ok := tr.List(r, "")
	if !ok || len(root) != 3 || root[0].Name != "empty" || !root[0].Dir || root[2].Name != "top.txt" {
		t.Fatalf("root %+v", root)
	}
	if kids, ok := tr.List(r, "empty"); !ok || len(kids) != 0 {
		t.Fatalf("empty folder must list as empty: %+v %v", kids, ok)
	}
	if e, ok := tr.Lookup(r, "models/llama", "a.gguf"); !ok || e.File.Size != 5 || e.Dir {
		t.Fatalf("lookup %+v %v", e, ok)
	}
	if _, ok := tr.List(r, "nope"); ok {
		t.Fatal("unknown directory must not exist")
	}
	r2 := &Remote{Folders: map[string]string{"": "root"}, Files: map[string]RemoteFile{"new.txt": {}}}
	if got, _ := tr.List(r2, ""); len(got) != 1 || got[0].Name != "new.txt" {
		t.Fatalf("index must follow the snapshot: %+v", got)
	}
}
