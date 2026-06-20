package main

import (
	"testing"
)

func TestGetResumeHash(t *testing.T) {
	h1, err := getResumeHash("test.pdf", 12345, 67890, "Extract text", "model-a", 200, "http://api")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	h2, err := getResumeHash("test.pdf", 12345, 67890, "Extract text", "model-a", 200, "http://api")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h1 != h2 {
		t.Errorf("expected hashes to be identical, got %s and %s", h1, h2)
	}

	h3, _ := getResumeHash("test.pdf", 123456, 67890, "Extract text", "model-a", 200, "http://api")
	if h1 == h3 {
		t.Errorf("expected hashes to differ for different modtimes, but they matched: %s", h1)
	}
}

func TestFindCachedPage(t *testing.T) {
	var state *ResumeState
	content, found := findCachedPage(state, 0)
	if found {
		t.Error("expected not found for nil state")
	}

	state = &ResumeState{
		Pages: []PageState{
			{PageIndex: 0, Content: "page 0 content"},
			{PageIndex: 2, Content: "page 2 content"},
		},
	}

	content, found = findCachedPage(state, 0)
	if !found || content != "page 0 content" {
		t.Errorf("expected page 0 content, got %s (found: %v)", content, found)
	}

	_, found = findCachedPage(state, 1)
	if found {
		t.Error("expected page 1 to not be found")
	}

	content, found = findCachedPage(state, 2)
	if !found || content != "page 2 content" {
		t.Errorf("expected page 2 content, got %s (found: %v)", content, found)
	}
}
