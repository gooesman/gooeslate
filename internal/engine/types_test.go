package engine

import (
	"math"
	"testing"
)

func block(text string, x, y, w, h float64) Block {
	return Block{text, [4][2]float64{{x, y}, {x + w, y}, {x + w, y + h}, {x, y + h}}}
}
func TestGroupingAndSelection(t *testing.T) {
	blocks := []Block{block("world", 55, 10, 45, 20), block("Hello", 0, 10, 50, 20), block("中文", 0, 50, 40, 20), block("第二栏", 300, 10, 80, 20)}
	lines := GroupLines(blocks)
	if len(lines) != 3 || lines[0].Text != "Hello world" || lines[1].Text != "第二栏" || lines[2].Text != "中文" {
		t.Fatalf("%#v", lines)
	}
	tilted := Block{"tilted", [4][2]float64{{10, 0}, {20, 10}, {10, 20}, {0, 10}}}
	if !tilted.Hit(Point{10, 10}) || tilted.Hit(Point{1, 1}) {
		t.Error("polygon hit testing is wrong")
	}
	v := Fit(640, 200, Rect{20, 30, 320, 200})
	p := v.ToImage(Point{v.X + 120*v.Scale, v.Y + 80*v.Scale})
	if math.Abs(p.X-120) > .0001 || math.Abs(p.Y-80) > .0001 {
		t.Error("scaled selection does not map back")
	}
}
