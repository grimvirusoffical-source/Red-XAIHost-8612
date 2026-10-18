package engine

import (
	"math/bits"

	"redx/box"
)

// Hierarchical timing wheel: 7 levels x 64 slots, 10 ms ticks (horizon ~
// 2^42 ticks, far beyond any TTL). Insert is O(1); advancing cascades items
// from coarse levels into finer ones. Items are never cancelled; the owner
// checks liveness when an item fires (an entry that was replaced, deleted or
// had its deadline pushed out is dropped or re-inserted lazily).
const (
	wheelBits   = 6
	wheelSlots  = 1 << wheelBits
	wheelLevels = 7
	wheelTickMs = 10
)

type wItem struct {
	at   int64 // unix ms deadline this item was scheduled for
	segs []box.Seg
	e    *box.Entry
}

type wheel struct {
	cur   int64 // current tick (absolute, in ticks)
	slots [wheelLevels][wheelSlots][]*wItem
	occ0  uint64 // bit i set <=> slots[0][i] is non-empty (lets advance skip idle ticks)
	n     int
	init  bool
}

func tickOf(ms int64) int64 { return ms / wheelTickMs }

func (w *wheel) len() int { return w.n }

// add schedules it. Items already due go to the next tick to fire.
func (w *wheel) add(it *wItem) {
	if !w.init {
		return
	}
	w.n++
	w.insert(it)
}

func (w *wheel) insert(it *wItem) {
	t := tickOf(it.at)
	if t <= w.cur {
		t = w.cur + 1
	}
	delta := t - w.cur
	level := 0
	for level < wheelLevels-1 && delta >= int64(1)<<(wheelBits*(level+1)) {
		level++
	}
	idx := (t >> (wheelBits * level)) & (wheelSlots - 1)
	w.slots[level][idx] = append(w.slots[level][idx], it)
	if level == 0 {
		w.occ0 |= 1 << uint(idx)
	}
}

// start sets the wheel's current time (must be called once before use).
func (w *wheel) start(nowMs int64) {
	w.cur = tickOf(nowMs)
	w.init = true
}

// advance moves time to nowMs and appends every item whose deadline has
// passed to out, which it returns. Cost is O(ticks elapsed + items fired).
func (w *wheel) advance(nowMs int64, out []*wItem) []*wItem {
	target := tickOf(nowMs)
	for w.cur < target {
		if w.n == 0 {
			w.cur = target
			break
		}
		// skip idle ticks: jump to the next occupied level-0 slot in this
		// rotation, or to the rotation boundary where coarser levels cascade
		idx0 := w.cur & (wheelSlots - 1)
		var nt int64
		if rem := w.occ0 &^ (uint64(1)<<uint(idx0+1) - 1); idx0 < wheelSlots-1 && rem != 0 {
			nt = w.cur - idx0 + int64(bits.TrailingZeros64(rem))
		} else {
			nt = w.cur - idx0 + wheelSlots
		}
		if nt > target {
			w.cur = target
			break
		}
		w.cur = nt
		// cascade finer levels when lower digits wrap
		for lvl := 1; lvl < wheelLevels; lvl++ {
			if w.cur&((int64(1)<<(wheelBits*lvl))-1) != 0 {
				break
			}
			idx := (w.cur >> (wheelBits * lvl)) & (wheelSlots - 1)
			items := w.slots[lvl][idx]
			w.slots[lvl][idx] = nil
			for _, it := range items {
				w.insert(it)
			}
		}
		idx := w.cur & (wheelSlots - 1)
		if items := w.slots[0][idx]; len(items) > 0 {
			w.slots[0][idx] = nil
			w.occ0 &^= 1 << uint(idx)
			for _, it := range items {
				w.n--
				out = append(out, it)
			}
		}
	}
	return out
}
