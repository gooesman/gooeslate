package main

import "image"

// Button bounds use desktop coordinates, including negative monitor origins.
func selectionButtonBounds(anchor image.Point, work image.Rectangle, size, gap int) image.Rectangle {
	if work.Empty() || size <= 0 {
		return image.Rectangle{}
	}
	width, height := min(size, work.Dx()), min(size, work.Dy())
	x, y := anchor.X+gap, anchor.Y+gap
	if x+width > work.Max.X {
		x = anchor.X - gap - width
	}
	if y+height > work.Max.Y {
		y = anchor.Y - gap - height
	}
	x = max(work.Min.X, min(x, work.Max.X-width))
	y = max(work.Min.Y, min(y, work.Max.Y-height))
	return image.Rect(x, y, x+width, y+height)
}

type selectionGesture struct {
	down         bool
	start        image.Point
	target       uintptr
	hasLastUp    bool
	lastUp       uint32
	lastUpPoint  image.Point
	lastUpTarget uintptr
}

func (g *selectionGesture) begin(p image.Point, target uintptr) {
	g.down, g.start, g.target = true, p, target
}

// A plain single click cannot resurrect a prior selection. Double clicks and
// drags are probed passively; applications without text selection stay silent.
func (g *selectionGesture) end(p image.Point, target uintptr, at, doubleClickMS uint32, dragTolerance, doubleClickTolerance image.Point) bool {
	if !g.down {
		return false
	}
	g.down = false
	if target == 0 || target != g.target {
		g.hasLastUp = false
		return false
	}
	delta := p.Sub(g.start)
	drag := absSelection(delta.X) > dragTolerance.X || absSelection(delta.Y) > dragTolerance.Y
	between := p.Sub(g.lastUpPoint)
	double := g.hasLastUp && target == g.lastUpTarget && at-g.lastUp <= doubleClickMS && absSelection(between.X) <= doubleClickTolerance.X && absSelection(between.Y) <= doubleClickTolerance.Y
	g.hasLastUp, g.lastUp, g.lastUpPoint, g.lastUpTarget = true, at, p, target
	return drag || double
}

func absSelection(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

func selectionResultCurrent(enabled, closed bool, generation, requestGeneration uint64, target, foreground uintptr) bool {
	return enabled && !closed && generation == requestGeneration && target != 0 && target == foreground
}
