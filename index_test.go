package main

import (
	"testing"

	"google.golang.org/api/drive/v3"
)

func folder(id, name, parent string) *drive.File {
	return &drive.File{Id: id, Name: name, MimeType: folderMime, Parents: []string{parent}}
}

func file(id, name, parent, sum string) *drive.File {
	return &drive.File{Id: id, Name: name, MimeType: "text/plain", Parents: []string{parent}, Md5Checksum: sum, ModifiedTime: "2026-01-01T00:00:00Z"}
}

func TestBuildRemoteWithRoot(t *testing.T) {
	all := map[string]*drive.File{
		"f1": folder("f1", "Docs", "ROOT"),
		"a":  file("a", "a.txt", "f1", "m1"),
		"d1": file("d1", "dup.txt", "f1", "x"),
		"d2": file("d2", "dup.txt", "f1", "y"),
		"g":  {Id: "g", Name: "Notes", MimeType: gappsPrefix + "document", Parents: []string{"f1"}, WebViewLink: "u"},
		"o":  file("o", "orphan.txt", "missing", "z"),
	}
	r, err := buildRemote(all, "ROOT")
	if err != nil {
		t.Fatal(err)
	}
	if r.Files["Docs/a.txt"].ID != "a" || r.Folders["Docs"] != "f1" || r.Docs["Docs/Notes.gdoc"].ID != "g" {
		t.Fatalf("remote %+v", r)
	}
	if _, ok := r.Files["Docs/dup.txt"]; ok || !r.blocked("Docs/dup.txt") {
		t.Fatal("duplicates must be blocked")
	}
	if len(r.Files) != 1 {
		t.Fatalf("orphan included: %+v", r.Files)
	}
}

func TestBuildRemoteAppOnlyAccess(t *testing.T) {
	all := map[string]*drive.File{
		"b":  folder("b", backupRootName, "HIDDEN"),
		"s":  folder("s", syncRootName, "HIDDEN"),
		"x":  folder("x", "Other", "HIDDEN"),
		"f":  file("f", "a.txt", "s", "m"),
		"f2": file("f2", "b.txt", "x", "m"),
	}
	r, err := buildRemote(all, "")
	if err != nil {
		t.Fatal(err)
	}
	if r.Files[syncRootName+"/a.txt"].ID != "f" || r.Folders[backupRootName] != "b" || len(r.Files) != 1 {
		t.Fatalf("remote %+v", r)
	}
}

func TestApplyChanges(t *testing.T) {
	x := &driveIndex{Files: map[string]*drive.File{"a": file("a", "a.txt", "p", "1"), "b": file("b", "b.txt", "p", "1"), "c": file("c", "c.txt", "p", "1")}}
	files := x.Files
	x.apply([]*drive.Change{
		{FileId: "a", Removed: true},
		{FileId: "b", File: &drive.File{Id: "b", Trashed: true}},
		{FileId: "c", File: file("c", "c2.txt", "p", "2")},
		{FileId: "d", File: file("d", "d.txt", "p", "3")},
		{FileId: ""},
	})
	if len(files) != 2 || files["c"].Name != "c2.txt" || files["d"] == nil {
		t.Fatalf("files %+v", files)
	}
}

func TestIndexNote(t *testing.T) {
	x := &driveIndex{Files: map[string]*drive.File{"a": file("a", "a.txt", "p", "old")}}
	x.note(&drive.File{Id: "a", Md5Checksum: "new", ModifiedTime: "2026-01-02T00:00:00Z"}, "a.txt", "")
	if x.Files["a"].Md5Checksum != "new" || x.Files["a"].Parents[0] != "p" {
		t.Fatalf("update: %+v", x.Files["a"])
	}
	x.note(&drive.File{Id: "b", Md5Checksum: "m", ModifiedTime: "2026-01-02T00:00:00Z"}, "b.txt", "")
	if _, ok := x.Files["b"]; ok {
		t.Fatal("new file without a parent must not be recorded")
	}
	x.note(&drive.File{Id: "c", Md5Checksum: "m", ModifiedTime: "2026-01-02T00:00:00Z"}, "c.txt", "p")
	r, _ := buildRemote(map[string]*drive.File{"p": folder("p", "P", "ROOT"), "c": x.Files["c"]}, "ROOT")
	if r.Files["P/c.txt"].MD5 != "m" {
		t.Fatalf("new file not placed: %+v", r.Files)
	}
	var nilIndex *driveIndex
	nilIndex.note(&drive.File{Id: "z"}, "z", "p")
}

func TestIndexForget(t *testing.T) {
	x := &driveIndex{Files: map[string]*drive.File{"p": folder("p", "P", "ROOT"), "a": file("a", "a.txt", "p", "m")}}
	x.forget("a")
	r, _ := buildRemote(x.Files, "ROOT")
	if _, ok := r.Files["P/a.txt"]; ok {
		t.Fatal("trashed file still listed")
	}
	var nilIndex *driveIndex
	nilIndex.forget("a")
}

func TestIndexForgetTree(t *testing.T) {
	x := &driveIndex{Files: map[string]*drive.File{
		"a": folder("a", "A", "ROOT"), "b": folder("b", "B", "a"), "c": file("c", "c.txt", "b", "m"),
		"k": folder("k", "K", "ROOT"), "d": file("d", "d.txt", "k", "m"),
	}}
	x.forgetTree("a")
	if len(x.Files) != 2 || x.Files["k"] == nil || x.Files["d"] == nil {
		t.Fatalf("left %v", x.Files)
	}
	var nilIndex *driveIndex
	nilIndex.forgetTree("a")
}

func TestIndexCachesBuiltRemote(t *testing.T) {
	x := &driveIndex{RootID: "ROOT", Files: map[string]*drive.File{"p": folder("p", "P", "ROOT"), "a": file("a", "a.txt", "p", "m")}}
	first, err := x.snapshot()
	if err != nil {
		t.Fatal(err)
	}
	built := x.built
	if _, err := x.snapshot(); err != nil || x.built != built {
		t.Fatal("unchanged index rebuilt its listing")
	}
	c := first.clone()
	c.Files["P/x.txt"] = RemoteFile{}
	if _, ok := first.Files["P/x.txt"]; ok {
		t.Fatal("clone shares maps with the cached listing")
	}
	x.note(&drive.File{Id: "b", Md5Checksum: "n", ModifiedTime: "2026-01-02T00:00:00Z"}, "b.txt", "p")
	after, _ := x.snapshot()
	if x.built == built || after.Files["P/b.txt"].MD5 != "n" {
		t.Fatal("changed index did not rebuild its listing")
	}
	built = x.built
	x.note(&drive.File{Id: "a", Md5Checksum: "edited", ModifiedTime: "2026-01-03T00:00:00Z", Size: 9}, "a.txt", "")
	if x.built != built {
		t.Fatal("content-only change rebuilt the listing")
	}
	if got, _ := x.snapshot(); got.Files["P/a.txt"].MD5 != "edited" || got.Files["P/a.txt"].Size != 9 {
		t.Fatalf("content-only change not applied: %+v", got.Files["P/a.txt"])
	}
	x.apply([]*drive.Change{{FileId: "a", File: file("a", "renamed.txt", "p", "edited")}})
	if got, _ := x.snapshot(); x.built == built || got.Files["P/renamed.txt"].ID != "a" {
		t.Fatal("rename did not rebuild the listing")
	}
}
