package main

import (
	"image"
	"testing"
)

func TestSelectionButtonBoundsFitsMonitorWorkArea(t *testing.T) {
	for _, tc := range []struct {
		name   string
		anchor image.Point
		work   image.Rectangle
		size   int
	}{
		{"primary bottom right", image.Pt(1918, 1038), image.Rect(0, 0, 1920, 1040), 32},
		{"negative monitor", image.Pt(-1919, -199), image.Rect(-1920, -200, 0, 880), 48},
		{"negative bottom right", image.Pt(-2, 879), image.Rect(-1920, -200, 0, 880), 48},
		{"undersized work area", image.Pt(10, 10), image.Rect(0, 0, 24, 20), 32},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := selectionButtonBounds(tc.anchor, tc.work, tc.size, 10)
			if got.Empty() || got.Intersect(tc.work) != got {
				t.Fatalf("button escaped work area: %v in %v", got, tc.work)
			}
			if got.Dx() != min(tc.size, tc.work.Dx()) || got.Dy() != min(tc.size, tc.work.Dy()) {
				t.Fatalf("unexpected button size: %v", got)
			}
		})
	}
	if got := selectionButtonBounds(image.Point{}, image.Rectangle{}, 32, 10); !got.Empty() {
		t.Fatalf("empty monitor must not produce a button: %v", got)
	}
}

func TestSelectionGestureRequiresDragOrDoubleClickInSameWindow(t *testing.T) {
	g := selectionGesture{}
	tolerance := image.Pt(4, 4)
	g.begin(image.Pt(100, 100), 7)
	if g.end(image.Pt(101, 100), 7, 1000, 500, tolerance, tolerance) {
		t.Fatal("single click should not show a stale selection")
	}
	g.begin(image.Pt(100, 100), 7)
	if !g.end(image.Pt(101, 100), 7, 1150, 500, tolerance, tolerance) {
		t.Fatal("double click must probe a word selection")
	}
	g.begin(image.Pt(100, 100), 7)
	if !g.end(image.Pt(200, 100), 7, 3000, 500, tolerance, tolerance) {
		t.Fatal("drag must probe selected text")
	}
	g.begin(image.Pt(100, 100), 7)
	if g.end(image.Pt(200, 100), 8, 3100, 500, tolerance, tolerance) {
		t.Fatal("focus change during drag must not read another window")
	}
	if g.end(image.Pt(200, 100), 8, 3150, 500, tolerance, tolerance) {
		t.Fatal("mouseup without matching mousedown must not probe")
	}
}

func TestSelectionResultsDiscardStaleQueriesAndDisabledController(t *testing.T) {
	if !selectionResultCurrent(true, false, 4, 4, 12, 12) {
		t.Fatal("current selection should be accepted")
	}
	for _, test := range []struct {
		enabled, closed     bool
		generation, request uint64
		target, foreground  uintptr
	}{
		{false, false, 4, 4, 12, 12}, {true, true, 4, 4, 12, 12},
		{true, false, 5, 4, 12, 12}, {true, false, 4, 4, 12, 13},
		{true, false, 4, 4, 0, 0},
	} {
		if selectionResultCurrent(test.enabled, test.closed, test.generation, test.request, test.target, test.foreground) {
			t.Fatalf("stale query accepted: %+v", test)
		}
	}
}
