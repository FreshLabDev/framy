// SPDX-License-Identifier: Apache-2.0
package chat

import (
	"testing"
	"time"
)

// heldLocks is how many runs have a mutex now.
func (f *Flow) heldLocks() int {
	f.locksMu.Lock()
	defer f.locksMu.Unlock()
	return len(f.locks)
}

// len is how many marks are kept.
func (m *marks) len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.at)
}

// Presses on a result leave no mutex behind.
func TestAPressOnAResultLeavesNoMutex(t *testing.T) {
	h := newHarness(t)
	id := h.generating(t)
	h.delivered(t, id, picture)
	for i := range 3 {
		h.now = h.now.Add(10 * time.Second)
		if err := h.onResult(id, 7, actAgain, "cq-"+string(rune('a'+i))); err != nil {
			t.Fatal(err)
		}
	}
	if n := h.flow.heldLocks(); n != 0 {
		t.Fatalf("%d mutexes left", n)
	}
}

// A mark is kept as long as it matters and then forgotten, so a day of
// presses does not stay in memory.
func TestMarksAreForgottenOnceTheyNoLongerMatter(t *testing.T) {
	var m marks
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	if !m.put("1:again", now, tapAgain) || m.put("1:again", now.Add(time.Second), tapAgain) {
		t.Fatal("a double tap marked twice")
	}
	if !m.within("1:again", now.Add(4*time.Second), tapAgain) || m.within("1:again", now.Add(tapAgain), tapAgain) {
		t.Fatal("kept for the wrong time")
	}
	for i := range 1000 {
		now = now.Add(time.Second)
		m.put(string(rune(i)), now, tapAgain)
	}
	if n := m.len(); n > 2*int(tapAgain/time.Second) {
		t.Fatalf("%d marks kept", n)
	}
}

// Original sends the result's file once, and the mark of it goes later.
func TestOriginalGoesOnceAndIsForgotten(t *testing.T) {
	h := newHarness(t)
	id := h.generating(t)
	h.delivered(t, id, picture)
	for range 2 {
		if err := h.onResult(id, 7, actOriginal, "cq"); err != nil {
			t.Fatal(err)
		}
	}
	if docs := h.tg.methods("sendDocument"); len(docs) != 1 {
		t.Fatalf("documents %+v", docs)
	}
	h.now = h.now.Add(2 * originalOnce)
	h.flow.originals.put("0", h.now, originalOnce)
	if n := h.flow.originals.len(); n != 1 {
		t.Fatalf("%d marks kept", n)
	}
}
