package main

import (
	"strings"
)

// picker is a filtered list: the wizard's text input is the filter, the
// arrow keys move the cursor through the entries that match it. Used for the
// keyboard layout and timezone steps, whose lists are too long to scroll.
type picker struct {
	labels []string // what is shown and matched, one per entry
	match  []int    // indexes into labels that match the current filter
	cursor int      // position in match
}

// pickerRows is how many entries are shown at once. With the header, the
// filter line and the hints the step stays well inside a 25-row console.
const pickerRows = 12

func newPicker(labels []string) picker {
	p := picker{labels: labels}
	p.filter("")
	return p
}

// filter keeps the entries that contain every word of q, ignoring case, and
// keeps the cursor on the same entry when it still matches.
func (p *picker) filter(q string) {
	prev, hadPrev := p.selected()
	words := strings.Fields(strings.ToLower(q))
	// A fresh slice: bubbletea copies the model on every update, and a
	// reused backing array would be shared with older copies.
	p.match = make([]int, 0, len(p.labels))
	for i, l := range p.labels {
		ll := strings.ToLower(l)
		ok := true
		for _, w := range words {
			if !strings.Contains(ll, w) {
				ok = false
				break
			}
		}
		if ok {
			p.match = append(p.match, i)
		}
	}
	p.cursor = 0
	if hadPrev {
		p.selectIndex(prev)
	}
}

// selectIndex moves the cursor to entry i if it is in the filtered list.
func (p *picker) selectIndex(i int) {
	for pos, m := range p.match {
		if m == i {
			p.cursor = pos
			return
		}
	}
}

// selected returns the index of the entry under the cursor, if any.
func (p *picker) selected() (int, bool) {
	if p.cursor < 0 || p.cursor >= len(p.match) {
		return 0, false
	}
	return p.match[p.cursor], true
}

func (p *picker) move(delta int) {
	p.cursor += delta
	if p.cursor >= len(p.match) {
		p.cursor = len(p.match) - 1
	}
	if p.cursor < 0 {
		p.cursor = 0
	}
}

// window returns the slice of match positions to draw, keeping the cursor
// visible.
func (p *picker) window() (from, to int) {
	from = p.cursor - pickerRows/2
	if from > len(p.match)-pickerRows {
		from = len(p.match) - pickerRows
	}
	if from < 0 {
		from = 0
	}
	to = from + pickerRows
	if to > len(p.match) {
		to = len(p.match)
	}
	return from, to
}
