//go:build unix && !darwin

package main

import "testing"

func TestDesktopQuote(t *testing.T) {
	got := desktopQuote(`/home/a b/$x"y\z`)
	want := `"/home/a b/\\$x\\"y\\\\z"`
	if got != want {
		t.Fatalf("got %s want %s", got, want)
	}
}
