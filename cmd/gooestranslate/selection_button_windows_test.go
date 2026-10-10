package main

import (
	"errors"
	"testing"
)

func TestSelectionButtonReadsPassivelyOnlyForCurrentForeground(t *testing.T) {
	foreground := uintptr(9)
	readCount := 0
	c := &SelectionController{
		foreground: func() uintptr { return foreground },
		readSelection: func(target uintptr, copyFallback bool) (string, error) {
			readCount++
			if target != 9 || copyFallback {
				t.Fatal("passive button detection must not copy text or read another window")
			}
			return "hello", nil
		},
	}
	c.generation.Store(4)
	request := selectionQuery{target: 9, generation: 4}
	if selected, ok := c.readQuery(request); !ok || selected != "hello" || readCount != 1 {
		t.Fatalf("current passive read failed: %q, %v, calls=%d", selected, ok, readCount)
	}
	c.generation.Store(5)
	if _, ok := c.readQuery(request); ok || readCount != 1 {
		t.Fatal("an outside click or new selection must invalidate old accessibility work")
	}
	c.generation.Store(4)
	foreground = 10
	if _, ok := c.readQuery(request); ok || readCount != 1 {
		t.Fatal("focus change must prevent the accessibility read")
	}
}

func TestSelectionButtonDoesNotOfferEmptyOrUnsupportedSelection(t *testing.T) {
	c := &SelectionController{foreground: func() uintptr { return 9 }}
	c.generation.Store(4)
	for _, test := range []struct {
		text string
		err  error
	}{
		{"", nil}, {" \r\n\t", nil}, {"ignored", errors.New("unsupported selection")},
	} {
		c.readSelection = func(uintptr, bool) (string, error) { return test.text, test.err }
		if _, ok := c.readQuery(selectionQuery{target: 9, generation: 4}); ok {
			t.Fatalf("unsupported selection offered a translate button: %+v", test)
		}
	}
}

func TestSelectionButtonClickReadsFreshTextAndPendingQueueIsBounded(t *testing.T) {
	c := &SelectionController{
		selectedText: "previous mouse selection",
		foreground:   func() uintptr { return 9 },
		readSelection: func(_ uintptr, copyFallback bool) (string, error) {
			if copyFallback {
				t.Fatal("button click must still use passive selection reading")
			}
			return "new keyboard selection", nil
		},
		queries: make(chan selectionQuery, 1),
	}
	c.generation.Store(4)
	click := selectionQuery{target: 9, generation: 4, translate: true}
	if selected, ok := c.readQuery(click); !ok || selected != "new keyboard selection" {
		t.Fatalf("click translated cached text instead of fresh selection: %q %v", selected, ok)
	}
	c.enqueueQuery(selectionQuery{target: 9, generation: 2})
	c.enqueueQuery(selectionQuery{target: 9, generation: 3})
	c.enqueueQuery(click)
	if len(c.queries) != 1 {
		t.Fatal("slow providers must not accumulate unbounded selection reads")
	}
	if got := <-c.queries; got != click {
		t.Fatalf("latest selection click must replace stale queued reads: %+v", got)
	}
}

func TestSelectionMouseQueueOverflowInvalidatesInsteadOfBlockingHook(t *testing.T) {
	notifications := 0
	c := &SelectionController{
		mouseEvents: make(chan selectionMouseEvent, 1),
		wakeMouse:   func() bool { notifications++; return true },
	}
	c.generation.Store(8)
	c.queueMouse(selectionMouseEvent{message: 0x0201})
	c.queueMouse(selectionMouseEvent{message: 0x0202})
	if len(c.mouseEvents) != 1 || notifications != 2 || !c.mouseOverflow.Load() || c.generation.Load() != 9 {
		t.Fatal("a full hook queue must return immediately and invalidate pending selection work")
	}
	c.processMouseQueue()
	if len(c.mouseEvents) != 0 || c.mouseOverflow.Load() || c.generation.Load() != 10 {
		t.Fatal("overflow must discard the incomplete mouse gesture")
	}
	c.wakeMouse = func() bool { return false }
	c.queueMouse(selectionMouseEvent{message: 0x0201})
	if !c.mouseOverflow.Load() || c.generation.Load() != 11 {
		t.Fatal("a failed window notification must also invalidate the queued gesture")
	}
}
