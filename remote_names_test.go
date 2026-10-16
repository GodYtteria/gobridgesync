package main

import (
	"testing"

	"google.golang.org/api/drive/v3"
)

func TestBuildRemoteBlocksSubtreeOfInvalidName(t *testing.T) {
	all := map[string]*drive.File{
		"bad":   {Id: "bad", Name: "a/b", MimeType: folderMime, Parents: []string{"root"}},
		"child": {Id: "child", Name: "c.txt", Parents: []string{"bad"}, Md5Checksum: "m", ModifiedTime: "2026-01-01T00:00:00Z"},
		"ok":    {Id: "ok", Name: "fine.txt", Parents: []string{"root"}, Md5Checksum: "m", ModifiedTime: "2026-01-01T00:00:00Z"},
	}
	r, err := buildRemote(all, "root")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := r.Files["a/b/c.txt"]; ok {
		t.Fatal("child of an unusable folder name must not be listed")
	}
	if !r.blocked("a/b/c.txt") || !r.blocked("a/b") {
		t.Fatal("subtree must be blocked so nothing under it is deleted or re-uploaded")
	}
	if r.blocked("fine.txt") {
		t.Fatal("unrelated file blocked")
	}
	if _, ok := r.Files["fine.txt"]; !ok {
		t.Fatal("valid file lost")
	}
	errs := r.invalidNameErrors()
	if len(errs) != 1 || errs[0] == nil {
		t.Fatalf("rename request not surfaced: %v", errs)
	}
}

func TestBuildRemoteSkipsFileWithBadModifiedTime(t *testing.T) {
	all := map[string]*drive.File{
		"bad": {Id: "bad", Name: "x.txt", Parents: []string{"root"}, Md5Checksum: "m", ModifiedTime: "garbage"},
		"ok":  {Id: "ok", Name: "y.txt", Parents: []string{"root"}, Md5Checksum: "m", ModifiedTime: "2026-01-01T00:00:00Z"},
	}
	r, err := buildRemote(all, "root")
	if err != nil {
		t.Fatalf("one bad timestamp aborted the whole listing: %v", err)
	}
	if _, ok := r.Files["x.txt"]; ok {
		t.Fatal("file with unreadable time listed")
	}
	if !r.blocked("x.txt") {
		t.Fatal("skipped file must be blocked so its local copy is not deleted")
	}
	if _, ok := r.Files["y.txt"]; !ok {
		t.Fatal("good file lost")
	}
}
