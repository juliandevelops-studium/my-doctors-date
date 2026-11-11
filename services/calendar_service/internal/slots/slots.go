// Package slots computes free appointment times from working windows and busy intervals. It is pure (no I/O) so the
// booking rules can be tested exhaustively. All times are instants (UTC); working windows were already expanded in
// the practice time zone by database_middleware.
package slots

import (
	"sort"
	"time"
)

// Interval is the half-open range [Start, End).
type Interval struct{ Start, End time.Time }

// Duration of the interval.
func (i Interval) Duration() time.Duration { return i.End.Sub(i.Start) }

// Overlaps reports whether two intervals share time. Adjacent intervals do not overlap.
func (i Interval) Overlaps(o Interval) bool { return i.Start.Before(o.End) && o.Start.Before(i.End) }

// Normalise sorts and merges overlapping or adjacent intervals and drops empty ones.
func Normalise(in []Interval) []Interval {
	out := make([]Interval, 0, len(in))
	for _, i := range in {
		if i.End.After(i.Start) {
			out = append(out, i)
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Start.Before(out[b].Start) })
	var merged []Interval
	for _, i := range out {
		if n := len(merged); n > 0 && !i.Start.After(merged[n-1].End) {
			if i.End.After(merged[n-1].End) {
				merged[n-1].End = i.End
			}
			continue
		}
		merged = append(merged, i)
	}
	return merged
}

// Subtract removes busy time from working time.
func Subtract(working, busy []Interval) []Interval {
	busy = Normalise(busy)
	var out []Interval
	for _, w := range Normalise(working) {
		cur := w.Start
		for _, b := range busy {
			if !b.End.After(cur) {
				continue
			}
			if !b.Start.Before(w.End) {
				break
			}
			if b.Start.After(cur) {
				out = append(out, Interval{cur, b.Start})
			}
			if b.End.After(cur) {
				cur = b.End
			}
		}
		if cur.Before(w.End) {
			out = append(out, Interval{cur, w.End})
		}
	}
	return out
}

// Contains reports whether the slot lies completely inside one working window.
func Contains(working []Interval, slot Interval) bool {
	for _, w := range working {
		if !slot.Start.Before(w.Start) && !slot.End.After(w.End) {
			return true
		}
	}
	return false
}

// Free reports whether the slot overlaps no busy interval.
func Free(busy []Interval, slot Interval) bool {
	for _, b := range busy {
		if slot.Overlaps(b) {
			return false
		}
	}
	return true
}

// Bookable lists the start times at which a service of the given duration fits: inside a working window, not
// overlapping a busy interval, starting after notBefore. Start times lie on a grid of `step` counted from the start
// of the free block, so a block that begins at 08:00 offers 08:00, 08:15, 08:30, ...
func Bookable(working, busy []Interval, duration, step time.Duration, notBefore time.Time) []Interval {
	var out []Interval
	for _, f := range Subtract(working, busy) {
		for s := f.Start; !s.Add(duration).After(f.End); s = s.Add(step) {
			if s.After(notBefore) {
				out = append(out, Interval{s, s.Add(duration)})
			}
		}
	}
	return out
}

// State of a calendar block.
type State string

// Block states.
const (
	StateFree State = "free"
	StateBusy State = "busy"
)

// Block is a free or busy range of one doctor's calendar. It carries no booking details.
type Block struct {
	Interval
	State State
}

// Blocks returns the free time inside the working windows and the busy time, sorted by start.
func Blocks(working, busy []Interval) []Block {
	var out []Block
	for _, f := range Subtract(working, busy) {
		out = append(out, Block{f, StateFree})
	}
	for _, b := range Normalise(busy) {
		out = append(out, Block{b, StateBusy})
	}
	sort.SliceStable(out, func(a, b int) bool { return out[a].Start.Before(out[b].Start) })
	return out
}

// BusyWithin sums the busy time that overlaps the range (used to spread bookings over doctors).
func BusyWithin(busy []Interval, r Interval) time.Duration {
	var sum time.Duration
	for _, b := range Normalise(busy) {
		if !b.Overlaps(r) {
			continue
		}
		s, e := b.Start, b.End
		if s.Before(r.Start) {
			s = r.Start
		}
		if e.After(r.End) {
			e = r.End
		}
		sum += e.Sub(s)
	}
	return sum
}
