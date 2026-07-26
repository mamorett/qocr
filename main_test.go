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
	raw := "<|det|>title [14, 0, 999, 999]<|/det|>Baidu"
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
	if b.Content != "Baidu" {
		t.Errorf("expected content 'Baidu', got %q", b.Content)
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

func TestStripDetTags(t *testing.T) {
	input := "<|det|>title [45, 64, 138, 125]<|/det|>Dividends"
	expected := "Dividends"
	got := stripDetTags(input)
	if got != expected {
		t.Errorf("expected %q, got %q", expected, got)
	}
}

func TestRenderLatex(t *testing.T) {
	pages := [][]OCRBlock{
		{
			{Index: 0, Label: "title", Content: "Invoice Number 42", BBox2D: []int{33, 58, 372, 117}},
			{Index: 1, Label: "text", Content: "Total: $1,234.56", BBox2D: []int{33, 158, 323, 222}},
		},
		{
			{Index: 0, Label: "title", Content: "Page Two", BBox2D: []int{33, 82, 202, 143}},
		},
	}

	latexOut, err := renderLatex(pages, "test.pdf", "some-model")
	if err != nil {
		t.Fatalf("renderLatex error: %v", err)
	}

	if !strings.Contains(latexOut, "\\documentclass{article}") {
		t.Error("expected output to contain article class")
	}
	if !strings.Contains(latexOut, "\\usepackage[T1]{fontenc}") {
		t.Error("expected output to contain T1 fontenc package")
	}
	if !strings.Contains(latexOut, "\\usepackage[margin=0.75in]{geometry}") {
		t.Error("expected output to contain geometry package with 0.75in margin")
	}
	if !strings.Contains(latexOut, "\\newsavebox{\\tblbox}") {
		t.Error("expected output to contain global tblbox savebox declaration")
	}
	if !strings.Contains(latexOut, "Page Two") {
		t.Error("expected output to contain 'Page Two'")
	}
	if !strings.Contains(latexOut, "Invoice Number 42") {
		t.Error("expected output to contain 'Invoice Number 42'")
	}
}

func TestHTMLTableToLatex(t *testing.T) {
	htmlTable := "<table><tr><th>Header 1</th><th>Header 2</th></tr><tr><td>Cell 1</td><td>Cell 2</td></tr></table>"
	expected := "\\begin{table}[h]\n\\centering\n\\sbox{\\tblbox}{%\n\\small\n\\begin{tabular}{l l}\n\\hline\nHeader 1 & Header 2 \\\\ \\relax\n\\hline\nCell 1 & Cell 2 \\\\ \\relax\n\\hline\n\\end{tabular}%\n}\n\\ifdim\\wd\\tblbox>\\linewidth\n  \\resizebox{\\linewidth}{!}{\\usebox{\\tblbox}}%\n\\else\n  \\usebox{\\tblbox}%\n\\fi\n\\end{table}\n"

	result := htmlTableToLatex(htmlTable)
	if result != expected {
		t.Errorf("expected:\n%s\ngot:\n%s", expected, result)
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

func TestHTMLTableToMarkdown_NormalizesAndEscapesCells(t *testing.T) {
	htmlTable := `<TABLE class="data"><TR><TH>Plan | tier</TH><TH>Cost</TH></TR><TR><TD>Pro<br>annual</TD><TD>$10 &amp; tax</TD></TR><TR><TD>Free</TD></TR></TABLE>`
	expected := "| Plan \\| tier | Cost |\n| --- | --- |\n| Pro annual | $10 & tax |\n| Free |  |"
	if result := htmlTableToMarkdown(htmlTable); result != expected {
		t.Errorf("expected:\n%s\ngot:\n%s", expected, result)
	}
}

func TestRenderMarkdown_NativeTableDoesNotLeakHTML(t *testing.T) {
	pages := [][]OCRBlock{{
		{Index: 0, Label: "title", Content: "Synthetic report"},
		{Index: 1, Label: "table", Content: "<table><tr><th>Name</th><th>Score</th></tr><tr><td>Ada</td><td>10</td></tr></table>"},
	}}
	result := renderMarkdown(pages, false)
	if strings.Contains(strings.ToLower(result), "<table") {
		t.Fatalf("native Markdown must not contain raw table HTML: %q", result)
	}
	expected := "## Synthetic report\n\n| Name | Score |\n| --- | --- |\n| Ada | 10 |"
	if result != expected {
		t.Errorf("expected:\n%s\ngot:\n%s", expected, result)
	}
}
func TestRenderMarkdown_ShowBBox(t *testing.T) {
	pages := [][]OCRBlock{{
		{Index: 0, Label: "text", Content: "Sample paragraph", BBox2D: []int{10, 20, 30, 40}},
	}}
	result := renderMarkdown(pages, true)
	if !strings.Contains(result, "<!-- bbox: [10 20 30 40] -->") {
		t.Errorf("expected output to contain bounding box comment, got:\n%s", result)
	}
}

func TestNormalizeNativeText(t *testing.T) {
	input := "\r\nA\u00a0B\r\nC\r\x00"
	if got, want := normalizeNativeText(input), "A B\nC"; got != want {
		t.Errorf("normalizeNativeText() = %q, want %q", got, want)
	}
}

func TestHybridRegionsKeepReadingOrderAndFilter(t *testing.T) {
	pages := [][]OCRBlock{{
		{Label: "text", BBox2D: []int{0, 400, 900, 600}},
		{Label: "table", BBox2D: []int{50, 100, 950, 350}},
		{Label: "noise", BBox2D: []int{0, 0, 1, 1}},
	}}
	regions := hybridRegions(pages)
	// Model reading order is preserved; junk labels are dropped.
	if len(regions) != 2 || regions[0].kind != "text" || regions[1].kind != "table" {
		t.Fatalf("unexpected regions: %#v", regions)
	}
}

func TestHybridRegionsFilterJunk(t *testing.T) {
	pages := [][]OCRBlock{{
		{Label: "text", BBox2D: []int{10, 10, 15, 12}},       // tiny dot -> junk
		{Label: "text", BBox2D: []int{100, 100, 400, 500}},   // real region
		{Label: "text", BBox2D: []int{5, 990, 18, 999}},      // small corner speck -> junk
		{Label: "figure", BBox2D: []int{500, 100, 900, 600}}, // real figure
	}}
	regions := hybridRegions(pages)
	if len(regions) != 2 {
		t.Fatalf("expected 2 regions after junk filter, got %d: %#v", len(regions), regions)
	}
	if regions[0].kind != "text" || regions[1].kind != "figure" {
		t.Fatalf("unexpected regions kept: %#v", regions)
	}
}

func TestHybridRegionsCarryModelText(t *testing.T) {
	// The layout model transcribes each region's text; hybridRegions must carry
	// it through so extractHybridPage can use it as the primary content source.
	pages := [][]OCRBlock{{
		{Label: "title", Content: "Soyuz Mishap Sounds Alarms", BBox2D: []int{383, 61, 873, 98}},
		{Label: "text", Content: "An unsettling event during a Soyuz spacecraft's descent.", BBox2D: []int{376, 136, 635, 342}},
		{Label: "image", BBox2D: []int{379, 352, 737, 508}}, // normalized to figure, no text
	}}
	regions := hybridRegions(pages)
	if len(regions) != 3 {
		t.Fatalf("expected 3 regions, got %d: %#v", len(regions), regions)
	}
	if regions[0].text != "Soyuz Mishap Sounds Alarms" {
		t.Fatalf("title text lost: %#v", regions[0])
	}
	if regions[2].kind != "figure" {
		t.Fatalf("image should normalize to figure, got %q", regions[2].kind)
	}
}

func TestNativeParagraphsJoinHyphenation(t *testing.T) {
	lines := []hybridLine{
		{text: "Linux's rapid develop-", top: 100, bot: 112, left: 60, right: 460, size: 10},
		{text: "ment makes it a moving target", top: 114, bot: 126, left: 60, right: 455, size: 10},
	}
	para := joinHybridLines(lines)
	if para.text != "Linux's rapid development makes it a moving target" {
		t.Fatalf("unexpected join: %q", para.text)
	}
}

func TestRenderMarkdown_HybridKeepsBaiduTableMarkdown(t *testing.T) {
	pages := [][]OCRBlock{{
		{Index: 0, Label: "hybrid-text", Content: "Native text above."},
		{Index: 1, Label: "hybrid-table", Content: "| Item | Count |\n| --- | ---: |\n| Valve | 4 |"},
		{Index: 2, Label: "hybrid-text", Content: "Native text below."},
	}}
	want := "Native text above.\n\n| Item | Count |\n| --- | ---: |\n| Valve | 4 |\n\nNative text below."
	if got := renderMarkdown(pages, false); got != want {
		t.Errorf("renderMarkdown() = %q, want %q", got, want)
	}
}

func TestRenderHTML(t *testing.T) {
	pages := [][]OCRBlock{
		{
			{Index: 0, Label: "title", Content: "Main Title", BBox2D: []int{0, 0, 100, 100}}, // h = 100 - 0 = 100 (>60) -> h1
			{Index: 1, Label: "title", Content: "Sub Title", BBox2D: []int{0, 0, 40, 40}},   // h = 40 - 0 = 40 (<=60) -> h2
			{Index: 2, Label: "text", Content: "Hello <World>", BBox2D: []int{0, 0, 10, 10}},
			{Index: 3, Label: "image", Content: "", BBox2D: []int{0, 0, 10, 10}},
			{Index: 4, Label: "table", Content: "Table Content", BBox2D: []int{0, 0, 10, 10}},
			{Index: 5, Label: "page_number", Content: "1", BBox2D: []int{0, 0, 10, 10}},
		},
	}

	htmlOut := renderHTML(pages)

	expectedSnippet1 := `<div class="ocr-page" data-page="1">`
	expectedSnippet2 := `<h1 class="ocr-heading" contenteditable="true" data-detection-index="0">Main Title</h1>`
	expectedSnippet3 := `<h2 class="ocr-heading" contenteditable="true" data-detection-index="1">Sub Title</h2>`
	expectedSnippet4 := `<p class="ocr-text" contenteditable="true" data-detection-index="2">Hello &lt;World&gt;</p>`
	expectedSnippet5 := `<div class="ocr-image" data-detection-index="3"><span class="image-placeholder">🖼 Image Area</span></div>`
	expectedSnippet6 := `<div class="ocr-table" contenteditable="true" data-detection-index="4">Table Content</div>`
	expectedSnippet7 := `<span class="ocr-page-number" data-detection-index="5">1</span>`

	for _, snippet := range []string{expectedSnippet1, expectedSnippet2, expectedSnippet3, expectedSnippet4, expectedSnippet5, expectedSnippet6, expectedSnippet7} {
		if !strings.Contains(htmlOut, snippet) {
			t.Errorf("expected HTML output to contain %q, got:\n%s", snippet, htmlOut)
		}
	}
}

func TestRenderHTML_EmptyPages(t *testing.T) {
	out := renderHTML([][]OCRBlock{})
	if out != `<div class="ocr-page empty"><p class="muted">No content detected</p></div>` {
		t.Errorf("unexpected empty output: %q", out)
	}

	outSingle := renderHTML([][]OCRBlock{{}})
	if outSingle != `<div class="ocr-page empty" data-page="1"><p class="muted">No content detected</p></div>` {
		t.Errorf("unexpected single empty page output: %q", outSingle)
	}
}

func TestReconstructStructure(t *testing.T) {
	blocks := []OCRBlock{
		{Index: 0, Label: "title", Content: "Big Title", BBox2D: []int{0, 0, 100, 100}},
		{Index: 1, Label: "text", Content: "First paragraph line 1"},
		{Index: 2, Label: "text", Content: "First paragraph line 2"},
		{Index: 3, Label: "image", Content: "Diagram"},
		{Index: 4, Label: "text", Content: "Second paragraph"},
	}

	structured := reconstructStructure(blocks)
	if len(structured) != 4 {
		t.Fatalf("expected 4 structured blocks, got %d", len(structured))
	}

	if structured[0].BlockType != "heading" || structured[0].Level != 1 || structured[0].Text != "Big Title" {
		t.Errorf("unexpected block 0: %#v", structured[0])
	}
	if structured[1].BlockType != "paragraph" || structured[1].Text != "First paragraph line 1\nFirst paragraph line 2" {
		t.Errorf("unexpected block 1: %#v", structured[1])
	}
	if structured[2].BlockType != "image" || structured[2].Text != "Diagram" {
		t.Errorf("unexpected block 2: %#v", structured[2])
	}
	if structured[3].BlockType != "paragraph" || structured[3].Text != "Second paragraph" {
		t.Errorf("unexpected block 3: %#v", structured[3])
	}
}

