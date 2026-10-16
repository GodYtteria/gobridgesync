package main

import (
	"testing"
	"time"

	"google.golang.org/api/drive/v3"
)

func TestDuplicatesAreRenamedOnlyAfterTheySettle(t *testing.T) {
	var d dupTracker
	now := time.Now()
	dupes := map[string][]*drive.File{"repo/.git/index": {{Id: "a"}, {Id: "b"}}}
	if got := d.settled(dupes, now); len(got) != 0 {
		t.Fatal("a duplicate seen for the first time may be a rename in progress; it must not be renamed yet")
	}
	if got := d.settled(dupes, now.Add(time.Minute)); len(got) != 0 {
		t.Fatal("a duplicate younger than the settle time must wait")
	}
	if got := d.settled(dupes, now.Add(dupSettle+time.Second)); len(got) != 1 {
		t.Fatal("a duplicate that stays must be renamed")
	}
	d.settled(map[string][]*drive.File{}, now.Add(3*dupSettle))
	if got := d.settled(dupes, now.Add(4*dupSettle)); len(got) != 0 {
		t.Fatal("a duplicate that went away and came back starts its wait again")
	}
}
