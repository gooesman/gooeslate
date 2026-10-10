package main

import (
	"context"
	"testing"
)

func TestChangingServicesInvalidatesPendingWordRequests(t *testing.T) {
	lookup, lookupCancel := context.WithCancel(context.Background())
	vocabulary, vocabularyCancel := context.WithCancel(context.Background())
	translation, translationCancel := context.WithCancel(context.Background())
	defer translationCancel()
	a := &appState{lookupCancel: lookupCancel, vocabularyCancel: vocabularyCancel, cancel: translationCancel, lookupGeneration: 8, vocabularyGeneration: 12, generation: 4}
	a.cancelWordRequests()
	if lookup.Err() != context.Canceled || vocabulary.Err() != context.Canceled || a.lookupCancel != nil || a.vocabularyCancel != nil {
		t.Fatal("old lookup and collection requests must be canceled")
	}
	if a.lookupGeneration != 9 || a.vocabularyGeneration != 13 {
		t.Fatal("queued completions must become obsolete")
	}
	if translation.Err() != nil || a.generation != 4 {
		t.Fatal("an unrelated translation must not be invalidated")
	}
	a.cancelWordRequests()
	if a.lookupGeneration != 10 || a.vocabularyGeneration != 14 {
		t.Fatal("late completions must stay obsolete even without a live cancel handle")
	}
}
