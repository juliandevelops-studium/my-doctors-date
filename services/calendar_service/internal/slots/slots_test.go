package slots

import (
	"testing"
	"time"
)

var base = time.Date(2030, 1, 2, 7, 0, 0, 0, time.UTC) // 08:00 Berlin

func at(h, m int) time.Time          { return time.Date(2030, 1, 2, h, m, 0, 0, time.UTC) }
func iv(h1, m1, h2, m2 int) Interval { return Interval{at(h1, m1), at(h2, m2)} }

func TestNormalise(t *testing.T) {
	got := Normalise([]Interval{iv(9, 0, 10, 0), iv(8, 0, 9, 0), iv(9, 30, 9, 45), iv(12, 0, 12, 0), iv(13, 0, 14, 0)})
	want := []Interval{iv(8, 0, 10, 0), iv(13, 0, 14, 0)}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %v", got)
	}
}

func TestSubtract(t *testing.T) {
	working := []Interval{iv(8, 0, 12, 0), iv(13, 0, 17, 0)}
	cases := []struct {
		name string
		busy []Interval
		want []Interval
	}{
		{"nothing busy", nil, working},
		{"middle", []Interval{iv(9, 0, 10, 0)}, []Interval{iv(8, 0, 9, 0), iv(10, 0, 12, 0), iv(13, 0, 17, 0)}},
		{"start", []Interval{iv(8, 0, 9, 0)}, []Interval{iv(9, 0, 12, 0), iv(13, 0, 17, 0)}},
		{"end", []Interval{iv(11, 0, 12, 0)}, []Interval{iv(8, 0, 11, 0), iv(13, 0, 17, 0)}},
		{"whole window", []Interval{iv(8, 0, 12, 0)}, []Interval{iv(13, 0, 17, 0)}},
		{"outside working time", []Interval{iv(12, 0, 13, 0), iv(6, 0, 7, 0)}, working},
		{"crossing the lunch break", []Interval{iv(11, 30, 13, 30)}, []Interval{iv(8, 0, 11, 30), iv(13, 30, 17, 0)}},
		{"two in one window", []Interval{iv(9, 0, 9, 30), iv(10, 0, 10, 30)}, []Interval{iv(8, 0, 9, 0), iv(9, 30, 10, 0), iv(10, 30, 12, 0), iv(13, 0, 17, 0)}},
		{"overlapping busy", []Interval{iv(9, 0, 10, 0), iv(9, 30, 10, 30)}, []Interval{iv(8, 0, 9, 0), iv(10, 30, 12, 0), iv(13, 0, 17, 0)}},
	}
	for _, c := range cases {
		got := Subtract(working, c.busy)
		if len(got) != len(c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s: got %v, want %v", c.name, got, c.want)
			}
		}
	}
}

func TestContainsAndFree(t *testing.T) {
	working := []Interval{iv(8, 0, 12, 0)}
	if !Contains(working, iv(8, 0, 8, 30)) || !Contains(working, iv(11, 30, 12, 0)) {
		t.Error("slots at the edges are inside")
	}
	for _, s := range []Interval{iv(7, 45, 8, 15), iv(11, 45, 12, 15), iv(12, 0, 12, 30)} {
		if Contains(working, s) {
			t.Errorf("%v is not inside", s)
		}
	}
	busy := []Interval{iv(9, 0, 9, 30)}
	if Free(busy, iv(9, 15, 9, 45)) || Free(busy, iv(8, 45, 9, 15)) || Free(busy, iv(9, 0, 9, 30)) {
		t.Error("overlap must be detected")
	}
	if !Free(busy, iv(8, 30, 9, 0)) || !Free(busy, iv(9, 30, 10, 0)) {
		t.Error("adjacent slots are free")
	}
}

func TestBookable(t *testing.T) {
	working := []Interval{iv(8, 0, 10, 0)}
	busy := []Interval{iv(8, 30, 9, 0)}
	got := Bookable(working, busy, 30*time.Minute, 15*time.Minute, at(0, 0))
	want := []Interval{iv(8, 0, 8, 30), iv(9, 0, 9, 30), iv(9, 15, 9, 45), iv(9, 30, 10, 0)}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("slot %d: %v, want %v", i, got[i], want[i])
		}
	}
	// A 60-minute service does not fit between 8:00 and 8:30.
	if got := Bookable(working, busy, 60*time.Minute, 15*time.Minute, at(0, 0)); len(got) != 1 || got[0] != iv(9, 0, 10, 0) {
		t.Errorf("60 minutes: %v", got)
	}
	// Nothing in the past: only start times after notBefore.
	got = Bookable(working, nil, 30*time.Minute, 15*time.Minute, at(9, 0))
	if len(got) == 0 || !got[0].Start.After(at(9, 0)) {
		t.Errorf("notBefore: %v", got)
	}
	// Too long for any window.
	if got := Bookable(working, nil, 3*time.Hour, 15*time.Minute, at(0, 0)); len(got) != 0 {
		t.Errorf("3 hours: %v", got)
	}
	if got := Bookable(nil, nil, time.Hour, time.Minute, at(0, 0)); len(got) != 0 {
		t.Errorf("no working time: %v", got)
	}
}

func TestBlocks(t *testing.T) {
	blocks := Blocks([]Interval{iv(8, 0, 12, 0)}, []Interval{iv(9, 0, 10, 0), iv(12, 30, 13, 0)})
	want := []Block{{iv(8, 0, 9, 0), StateFree}, {iv(9, 0, 10, 0), StateBusy}, {iv(10, 0, 12, 0), StateFree}, {iv(12, 30, 13, 0), StateBusy}}
	if len(blocks) != len(want) {
		t.Fatalf("got %v", blocks)
	}
	for i := range want {
		if blocks[i] != want[i] {
			t.Errorf("block %d: %v, want %v", i, blocks[i], want[i])
		}
	}
}

func TestBusyWithin(t *testing.T) {
	busy := []Interval{iv(8, 0, 9, 0), iv(11, 30, 12, 30), iv(15, 0, 16, 0)}
	if got := BusyWithin(busy, iv(8, 30, 12, 0)); got != 60*time.Minute {
		t.Errorf("got %v, want 60m", got)
	}
	if got := BusyWithin(nil, iv(8, 0, 12, 0)); got != 0 {
		t.Errorf("got %v", got)
	}
}
