package main

import (
	"strings"
	"testing"
)

func TestGetResumeHash(t *testing.T) {
	h1, err := getResumeHash("test.pdf", 12345, 67890, "Extract text", "model-a", 200, "http://api", "glm", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	h2, err := getResumeHash("test.pdf", 12345, 67890, "Extract text", "model-a", 200, "http://api", "glm", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h1 != h2 {
		t.Errorf("expected hashes to be identical, got %s and %s", h1, h2)
	}

	h3, _ := getResumeHash("test.pdf", 123456, 67890, "Extract text", "model-a", 200, "http://api", "glm", "")
	if h1 == h3 {
		t.Errorf("expected hashes to differ for different modtimes, but they matched: %s", h1)
	}
}

func TestGetResumeHash_EngineIsolation(t *testing.T) {
	h1, _ := getResumeHash("test.pdf", 12345, 67890, "Extract text", "model-a", 200, "http://api", "glm", "")
	h2, _ := getResumeHash("test.pdf", 12345, 67890, "Extract text", "model-a", 200, "http://api", "baidu", "window_size=1024")
	if h1 == h2 {
		t.Errorf("expected hashes to differ for different engines, but they matched")
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

func TestParseBaiduContent_SinglePage(t *testing.T) {
	raw := "<|det|>title [14, 0, 999, 999]<|/det|>Bai du 百度"
	pages := parseBaiduContent(raw)
	if len(pages) != 1 {
		t.Fatalf("expected 1 page, got %d", len(pages))
	}
	blocks := pages[0]
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(blocks))
	}
	b := blocks[0]
	if b.Label != "title" {
		t.Errorf("expected label 'title', got %q", b.Label)
	}
	if b.Content != "Bai du 百度" {
		t.Errorf("expected content 'Bai du 百度', got %q", b.Content)
	}
	bbox, ok := getBBox(b.BBox2D)
	if !ok || len(bbox) != 4 || bbox[0] != 14 || bbox[1] != 0 || bbox[2] != 999 || bbox[3] != 999 {
		t.Errorf("incorrect bbox: %v", b.BBox2D)
	}
}

func TestParseBaiduContent_MultiPage(t *testing.T) {
	raw := `<PAGE><|det|>title [33, 58, 372, 117]<|/det|>Invoice Number 42
<|det|>title [33, 158, 323, 222]<|/det|>Total: $1,234.56
<|det|>title [33, 258, 349, 320]<|/det|>Date: 2026-06-30
<PAGE><|det|>title [33, 82, 202, 143]<|/det|>Page Two
<|det|>title [31, 163, 425, 228]<|/det|>Customer: Acme Corp
<|det|>title [33, 256, 384, 323]<|/det|>Balance Due: $0.00`

	pages := parseBaiduContent(raw)
	if len(pages) != 2 {
		t.Fatalf("expected 2 pages, got %d", len(pages))
	}
	if len(pages[0]) != 3 {
		t.Fatalf("expected 3 blocks on page 1, got %d", len(pages[0]))
	}
	if len(pages[1]) != 3 {
		t.Fatalf("expected 3 blocks on page 2, got %d", len(pages[1]))
	}

	b := pages[0][0]
	if b.Label != "title" || b.Content != "Invoice Number 42" {
		t.Errorf("unexpected block 0 on page 1: label=%q, content=%q", b.Label, b.Content)
	}
	bbox, _ := getBBox(b.BBox2D)
	if bbox[0] != 33 || bbox[1] != 58 || bbox[2] != 372 || bbox[3] != 117 {
		t.Errorf("unexpected bbox for page 1 block 0: %v", bbox)
	}
}

func TestParseBaiduContent_RefUnwrap(t *testing.T) {
	pages := parseBaiduContent("<|det|>text [0,0,10,10]<|/det|>hello <|ref|>world<|/ref>")
	if pages[0][0].Content != "hello world" {
		t.Errorf("expected 'hello world', got %q", pages[0][0].Content)
	}
}

func TestParseBaiduContent_Malformed(t *testing.T) {
	pages := parseBaiduContent("<|det|>malformed text hello")
	if len(pages) != 1 || len(pages[0]) != 1 {
		t.Fatalf("expected 1 page and 1 block, got pages=%d, blocks=%d", len(pages), len(pages[0]))
	}
	if pages[0][0].Label != "text" {
		t.Errorf("expected label 'text', got %q", pages[0][0].Label)
	}
}

func TestRenderHTML(t *testing.T) {
	pages := [][]OCRBlock{
		{
			{Index: 0, Label: "title", Content: "Invoice Number 42", BBox2D: []int{33, 58, 372, 117}},
			{Index: 1, Label: "text", Content: "Total: $1,234.56", BBox2D: []int{33, 158, 323, 222}},
		},
		{
			{Index: 0, Label: "title", Content: "Page Two", BBox2D: []int{33, 82, 202, 143}},
		},
	}
	dims := []PageDim{
		{Width: 800, Height: 400, DPI: 200, Rotation: 0},
		{Width: 800, Height: 400, DPI: 200, Rotation: 0},
	}

	htmlOut, err := renderHTML(pages, "test.pdf", "some-model", dims)
	if err != nil {
		t.Fatalf("renderHTML error: %v", err)
	}

	if !strings.Contains(htmlOut, "class=\"ocr-page\"") {
		t.Error("expected output to contain ocr-page class")
	}
	if !strings.Contains(htmlOut, "Page Two") {
		t.Error("expected output to contain 'Page Two'")
	}
	if !strings.Contains(htmlOut, "left:26.4px") && !strings.Contains(htmlOut, "left:26.") {
		t.Errorf("expected left positioning around 26px, html:\n%s", htmlOut)
	}
}

func TestRenderHTML_NoBBox(t *testing.T) {
	pages := [][]OCRBlock{
		{
			{Index: 0, Label: "title", Content: "Heading without box"},
		},
	}
	dims := []PageDim{
		{Width: 800, Height: 400, DPI: 200, Rotation: 0},
	}
	htmlOut, err := renderHTML(pages, "test.pdf", "some-model", dims)
	if err != nil {
		t.Fatalf("renderHTML error: %v", err)
	}
	if !strings.Contains(htmlOut, "ocr-linear-page") {
		t.Error("expected output to fallback to ocr-linear-page")
	}
}

func TestHTMLTableToMarkdown(t *testing.T) {
	htmlTable := "<table><tr><th>Header 1</th><th>Header 2</th></tr><tr><td>Cell 1</td><td>Cell 2</td></tr></table>"
	expected := "| Header 1 | Header 2 |\n| --- | --- |\n| Cell 1 | Cell 2 |"
	
	result := htmlTableToMarkdown(htmlTable)
	if result != expected {
		t.Errorf("expected:\n%s\ngot:\n%s", expected, result)
	}
}
