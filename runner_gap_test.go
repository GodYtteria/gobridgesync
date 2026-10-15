package main

import (
	"testing"
	"time"
)

func TestPassGap(t *testing.T) {
	cases := map[time.Duration]time.Duration{
		0:                      passGapMin,
		100 * time.Millisecond: passGapMin,
		time.Second:            20 * time.Second,
		time.Minute:            passGapMax,
	}
	for took, want := range cases {
		if got := passGap(took); got != want {
			t.Errorf("passGap(%v) = %v, want %v", took, got, want)
		}
	}
}
