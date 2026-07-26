package main

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	imgcolor "image/color"
	"image/draw"
	"image/png"
	"math"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/klippa-app/go-pdfium/requests"
	"github.com/klippa-app/go-pdfium/responses"
)

const hybridLayoutPrompt = `<image>Analyze this document page for layout only. Return each region in reading order using grounding tokens with a 0-999 coordinate grid. Allowed labels: text, table, figure, caption. Do not transcribe any text. Emit one <|det|>label [x1,y1,x2,y2]<|/det|> token per region.`
const hybridTablePrompt = `<image>Recognize this table exactly. Return only one valid GitHub-flavored Markdown table. Preserve rows, columns, headers, and empty cells. Do not add explanation.`

type hybridRegion struct {
	kind string
	bbox []int
	text string // the model's own transcription of this region (from the layout pass)
}

// hybridTextKinds are region labels whose content is pulled from the native
// text layer.
var hybridTextKinds = map[string]bool{
	"text": true, "caption": true, "paragraph": true, "page_number": true,
	"title": true, "header": true, "heading": true,
}

func isHeadingKind(kind string) bool {
	return kind == "title" || kind == "header" || kind == "heading"
}

func extractHybridPage(path string, index int, apiURL, model string, maxTokens int) ([]OCRBlock, PageDim, error) {
	lowURI, dim, err := renderPDFPageToDataURI(path, index, 96)
	if err != nil {
		return nil, PageDim{}, err
	}
	layoutResp, err := callAPIBaidu(apiURL, model, hybridLayoutPrompt, []string{lowURI}, false, 2048)
	if err != nil || layoutResp == nil || len(layoutResp.Choices) == 0 {
		return extractNativePage(path, index)
	}
	regions := hybridRegions(parseBaiduContent(layoutResp.Choices[0].Message.Content))
	if len(regions) == 0 {
		return extractNativePage(path, index)
	}

	// The layout model already transcribes each region's text with high
	// fidelity (it is a vision-language model reading the page directly). The
	// native text layer is only a fallback for regions the model left empty,
	// because the model's 0-999 grid does not map reliably onto PDF points.
	var chars []*responses.GetPageTextStructuredChar
	var pageWidth, pageHeight float64
	needNative := false
	for _, r := range regions {
		if hybridTextKinds[r.kind] && strings.TrimSpace(r.text) == "" {
			needNative = true
			break
		}
	}
	if needNative {
		if c, w, h, nerr := nativePageChars(path, index); nerr == nil {
			chars, pageWidth, pageHeight = c, w, h
		}
	}

	var blocks []OCRBlock
	for _, region := range regions {
		switch {
		case hybridTextKinds[region.kind]:
			label := "hybrid-text"
			if isHeadingKind(region.kind) {
				label = "hybrid-title"
			}
			text := strings.TrimSpace(stripDetTags(region.text))
			if text == "" && chars != nil {
				text = strings.TrimSpace(nativeTextInRegion(chars, region.bbox, pageWidth, pageHeight))
			}
			if text != "" {
				blocks = append(blocks, OCRBlock{Index: len(blocks), Label: label, Content: text, BBox2D: region.bbox})
			}
		case region.kind == "table":
			// Re-OCR the table crop at high DPI for structure; fall back to the
			// model's region text so content is never lost.
			tableMD := ""
			if highURI, _, renderErr := renderPDFPageToDataURI(path, index, 250); renderErr == nil {
				if crop, cropErr := cropDataURI(highURI, region.bbox); cropErr == nil {
					if tableResp, tableErr := callAPIBaidu(apiURL, model, hybridTablePrompt, []string{crop}, false, maxTokens); tableErr == nil && tableResp != nil && len(tableResp.Choices) > 0 {
						tableMD = stripDetTags(tableResp.Choices[0].Message.Content)
					}
				}
			}
			if looksLikeMarkdownTable(tableMD) {
				blocks = append(blocks, OCRBlock{Index: len(blocks), Label: "hybrid-table", Content: tableMD, BBox2D: region.bbox})
			} else if text := strings.TrimSpace(stripDetTags(region.text)); text != "" {
				blocks = append(blocks, OCRBlock{Index: len(blocks), Label: "hybrid-text", Content: text, BBox2D: region.bbox})
			}
		case region.kind == "figure":
			blocks = append(blocks, OCRBlock{Index: len(blocks), Label: "hybrid-figure", Content: "[Figure]", BBox2D: region.bbox})
		}
	}
	if len(blocks) == 0 {
		return extractNativePage(path, index)
	}
	// Trust the model's reading order; it groups regions by article/column
	// correctly. Re-sorting by geometry would break that grouping.
	for i := range blocks {
		blocks[i].Index = i
	}
	return blocks, dim, nil
}

// looksLikeMarkdownTable reports whether the table OCR response contains an
// actual GitHub-flavored markdown table rather than prose or an error message.
func looksLikeMarkdownTable(s string) bool {
	return strings.Count(s, "\n") >= 1 && strings.Contains(s, "|") && strings.Contains(s, "---")
}

func hybridRegions(pages [][]OCRBlock) []hybridRegion {
	if len(pages) == 0 {
		return nil
	}
	var regions []hybridRegion
	for _, block := range pages[0] {
		kind := strings.ToLower(strings.TrimSpace(block.Label))
		// Normalize a few labels the model emits.
		switch kind {
		case "image", "picture", "photo":
			kind = "figure"
		case "aside_text", "footnote":
			kind = "text"
		}
		if !hybridTextKinds[kind] && kind != "table" && kind != "figure" {
			continue
		}
		bbox, ok := getBBox(block.BBox2D)
		if !ok || bbox[2] <= bbox[0] || bbox[3] <= bbox[1] {
			continue
		}
		text := strings.TrimSpace(blockContentString(block))
		// Skip the prompt-echo block (no bbox) and page furniture with no text.
		regions = append(regions, hybridRegion{kind: kind, bbox: bbox, text: text})
	}
	// Drop junk regions: zero-area dots and edge slivers the model emits for
	// noise. Anything covering < 0.15% of the page that is also thin carries no
	// recoverable text — unless it has real transcribed content.
	var filtered []hybridRegion
	for _, r := range regions {
		w := r.bbox[2] - r.bbox[0]
		h := r.bbox[3] - r.bbox[1]
		if w*h < 1500 && (w < 20 || h < 20) && len(strings.Fields(r.text)) < 3 {
			continue
		}
		filtered = append(filtered, r)
	}
	if len(filtered) > 0 {
		regions = filtered
	}
	// Keep the model's reading order; it sequences regions correctly.
	return regions
}

func nativePageChars(path string, index int) ([]*responses.GetPageTextStructuredChar, float64, float64, error) {
	if err := initPDFium(); err != nil {
		return nil, 0, 0, err
	}
	instance, err := pdfiumPool.GetInstance(2 * time.Minute)
	if err != nil {
		return nil, 0, 0, err
	}
	defer instance.Close()
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, 0, err
	}
	doc, err := instance.OpenDocument(&requests.OpenDocument{File: &data})
	if err != nil {
		return nil, 0, 0, err
	}
	defer instance.FPDF_CloseDocument(&requests.FPDF_CloseDocument{Document: doc.Document}) //nolint:errcheck
	size, err := instance.FPDF_GetPageSizeByIndex(&requests.FPDF_GetPageSizeByIndex{Document: doc.Document, Index: index})
	if err != nil {
		return nil, 0, 0, err
	}
	resp, err := instance.GetPageTextStructured(&requests.GetPageTextStructured{Page: requests.Page{ByIndex: &requests.PageByIndex{Document: doc.Document, Index: index}}, Mode: requests.GetPageTextStructuredModeChars})
	if err != nil {
		return nil, 0, 0, err
	}
	return resp.Chars, size.Width, size.Height, nil
}

// hybridLine is one visual text line with its font metadata and bounding box.
type hybridLine struct {
	text  string
	top   float64 // normalized 0-999
	left  float64
	right float64
	bot   float64
	size  float64 // median font size (pt)
}

// nativeParagraph is a paragraph assembled from consecutive lines.
type nativeParagraph struct {
	text string
	bbox []int
}

func nativeTextInRegion(chars []*responses.GetPageTextStructuredChar, bbox []int, width, height float64) string {
	paras := nativeParagraphsInRegion(chars, bbox, width, height)
	var parts []string
	for _, p := range paras {
		parts = append(parts, p.text)
	}
	return strings.Join(parts, "\n\n")
}

// nativeParagraphsInRegion selects characters intersecting the region and
// assembles them into paragraphs using font size and line gaps.
func nativeParagraphsInRegion(chars []*responses.GetPageTextStructuredChar, bbox []int, width, height float64) []nativeParagraph {
	lines := hybridLinesInRegion(chars, bbox, width, height)
	if len(lines) == 0 {
		return nil
	}

	// Median line height for paragraph-gap detection.
	heights := make([]float64, 0, len(lines))
	for _, l := range lines {
		if h := l.bot - l.top; h > 0 {
			heights = append(heights, h)
		}
	}
	medH := medianFloat(heights)
	if medH <= 0 {
		medH = 12
	}

	var paras []nativeParagraph
	var cur []hybridLine
	flush := func() {
		if len(cur) == 0 {
			return
		}
		paras = append(paras, joinHybridLines(cur))
		cur = nil
	}
	for i, l := range lines {
		if i == 0 {
			cur = append(cur, l)
			continue
		}
		prev := cur[len(cur)-1]
		gap := l.top - prev.bot
		sizeJump := prev.size > 0 && l.size > 0 && (l.size > prev.size*1.25 || l.size < prev.size*0.8)
		bigGap := gap > medH*0.6
		indentChange := math.Abs(l.left-prev.left) > 40 && gap > medH*0.2
		if sizeJump || bigGap || indentChange {
			flush()
		}
		cur = append(cur, l)
	}
	flush()
	return paras
}

// hybridLinesInRegion selects chars intersecting the (slightly expanded)
// region and groups them into visual lines with font metadata.
func hybridLinesInRegion(chars []*responses.GetPageTextStructuredChar, bbox []int, width, height float64) []hybridLine {
	if width <= 0 || height <= 0 {
		return nil
	}
	// Expand the box by ~0.4% of the page so characters straddling the model's
	// coarse grid boundary are not clipped.
	const pad = 4.0
	x1 := float64(bbox[0]) - pad
	y1 := float64(bbox[1]) - pad
	x2 := float64(bbox[2]) + pad
	y2 := float64(bbox[3]) + pad

	var selected []*responses.GetPageTextStructuredChar
	for _, char := range chars {
		if char == nil {
			continue
		}
		cx1 := char.PointPosition.Left * 1000 / width
		cx2 := char.PointPosition.Right * 1000 / width
		cy1 := char.PointPosition.Top * 1000 / height
		cy2 := char.PointPosition.Bottom * 1000 / height
		// Overlap (intersection) test, not center-point containment.
		if cx2 >= x1 && cx1 <= x2 && cy2 >= y1 && cy1 <= y2 {
			selected = append(selected, char)
		}
	}
	// Split the region into sub-columns at vertical whitespace gutters, so a
	// full-width region spanning two text columns does not fuse left and right
	// chars into one line. Then build lines within each sub-column and emit
	// them column-by-column (reading order).
	subCols := splitCharsIntoColumns(selected)
	var out []hybridLine
	for _, colChars := range subCols {
		for _, lineChars := range groupCharsIntoLines(colChars) {
			text := strings.TrimSpace(assembleLineText(lineChars))
			if text == "" {
				continue
			}
			hl := hybridLine{text: text, top: math.Inf(1), left: math.Inf(1)}
			var sizes []float64
			for _, c := range lineChars {
				if c == nil {
					continue
				}
				cx1 := c.PointPosition.Left * 1000 / width
				cx2 := c.PointPosition.Right * 1000 / width
				cy1 := c.PointPosition.Top * 1000 / height
				cy2 := c.PointPosition.Bottom * 1000 / height
				if cy1 < hl.top {
					hl.top = cy1
				}
				if cy2 > hl.bot {
					hl.bot = cy2
				}
				if cx1 < hl.left {
					hl.left = cx1
				}
				if cx2 > hl.right {
					hl.right = cx2
				}
				if c.FontInformation != nil {
					sz := c.FontInformation.RenderedSize
					if sz <= 0 {
						sz = c.FontInformation.Size
					}
					if sz > 0 {
						sizes = append(sizes, sz)
					}
				}
			}
			hl.size = medianFloat(sizes)
			out = append(out, hl)
		}
	}
	return out
}

// joinHybridLines joins consecutive lines into one paragraph, preserving
// hyphenation and the paragraph bounding box.
func joinHybridLines(lines []hybridLine) nativeParagraph {
	var sb strings.Builder
	bbox := []int{int(lines[0].left), int(lines[0].top), int(lines[0].right), int(lines[0].bot)}
	for i, l := range lines {
		if i > 0 {
			// Hyphenated line break: drop the trailing hyphen and join directly.
			if strings.HasSuffix(sb.String(), "-") {
				joined := sb.String()[:sb.Len()-1] + l.text
				sb.Reset()
				sb.WriteString(joined)
			} else {
				sb.WriteString(" ")
				sb.WriteString(l.text)
			}
		} else {
			sb.WriteString(l.text)
		}
		if int(l.left) < bbox[0] {
			bbox[0] = int(l.left)
		}
		if int(l.top) < bbox[1] {
			bbox[1] = int(l.top)
		}
		if int(l.right) > bbox[2] {
			bbox[2] = int(l.right)
		}
		if int(l.bot) > bbox[3] {
			bbox[3] = int(l.bot)
		}
	}
	return nativeParagraph{text: sb.String(), bbox: bbox}
}

// splitCharsIntoColumns clusters characters into sub-columns by their x
// position, so that a region spanning multiple text columns does not merge
// left and right chars into one line. Returns columns left-to-right.
// splitCharsIntoColumns splits characters into sub-columns at vertical
// whitespace gutters (x positions with no characters). This lets a region that
// spans two text columns be decomposed so left/right chars never share a line.
// Works in raw PDF points. Returns columns left-to-right, or a single column
// when no gutter is found.
func splitCharsIntoColumns(chars []*responses.GetPageTextStructuredChar) [][]*responses.GetPageTextStructuredChar {
	if len(chars) == 0 {
		return nil
	}
	// Build an x-coverage histogram at ~1pt resolution over the char spans.
	minL, maxR := math.Inf(1), math.Inf(-1)
	for _, c := range chars {
		if c == nil {
			continue
		}
		if c.PointPosition.Left < minL {
			minL = c.PointPosition.Left
		}
		if c.PointPosition.Right > maxR {
			maxR = c.PointPosition.Right
		}
	}
	span := maxR - minL
	if span <= 0 {
		return [][]*responses.GetPageTextStructuredChar{chars}
	}
	const buckets = 200
	covered := make([]bool, buckets)
	for _, c := range chars {
		if c == nil {
			continue
		}
		a := int((c.PointPosition.Left - minL) / span * buckets)
		b := int((c.PointPosition.Right - minL) / span * buckets)
		if a < 0 {
			a = 0
		}
		if b >= buckets {
			b = buckets - 1
		}
		for i := a; i <= b; i++ {
			covered[i] = true
		}
	}
	// Find interior gutters: runs of uncovered buckets not touching the edges,
	// wide enough to be a real column gap (>= ~1.5% of the span).
	minGutter := int(0.015 * buckets)
	if minGutter < 2 {
		minGutter = 2
	}
	var gutterCenters []float64 // normalized 0..1 positions to split at
	i := 0
	for i < buckets {
		if !covered[i] {
			j := i
			for j < buckets && !covered[j] {
				j++
			}
			runStart, runEnd := i, j // [runStart, runEnd)
			interior := runStart > 0 && runEnd < buckets
			if interior && runEnd-runStart >= minGutter {
				gutterCenters = append(gutterCenters, float64(runStart+runEnd)/2/buckets)
			}
			i = j
		} else {
			i++
		}
	}
	if len(gutterCenters) == 0 {
		return [][]*responses.GetPageTextStructuredChar{chars}
	}
	// Assign each char to the column whose gutter range contains its center.
	colOf := func(c *responses.GetPageTextStructuredChar) int {
		cx := (c.PointPosition.Left + c.PointPosition.Right) / 2
		pos := (cx - minL) / span
		col := 0
		for gi, g := range gutterCenters {
			if pos > g {
				col = gi + 1
			}
		}
		return col
	}
	cols := make([][]*responses.GetPageTextStructuredChar, len(gutterCenters)+1)
	for _, c := range chars {
		if c == nil {
			continue
		}
		ci := colOf(c)
		cols[ci] = append(cols[ci], c)
	}
	// Drop empty columns and keep order left-to-right.
	var out [][]*responses.GetPageTextStructuredChar
	for _, c := range cols {
		if len(c) > 0 {
			out = append(out, c)
		}
	}
	if len(out) == 0 {
		return [][]*responses.GetPageTextStructuredChar{chars}
	}
	return out
}

func medianFloat(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	sorted := make([]float64, len(vals))
	copy(sorted, vals)
	sort.Float64s(sorted)
	mid := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[mid]
	}
	return (sorted[mid-1] + sorted[mid]) / 2
}

func cropDataURI(uri string, bbox []int) (string, error) {
	const prefix = "data:image/png;base64,"
	if !strings.HasPrefix(uri, prefix) {
		return "", fmt.Errorf("unsupported image URI")
	}
	data, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(uri, prefix))
	if err != nil {
		return "", err
	}
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	bounds := src.Bounds()
	x1, y1 := int(math.Floor(float64(bounds.Dx())*float64(bbox[0])/1000)), int(math.Floor(float64(bounds.Dy())*float64(bbox[1])/1000))
	x2, y2 := int(math.Ceil(float64(bounds.Dx())*float64(bbox[2])/1000)), int(math.Ceil(float64(bounds.Dy())*float64(bbox[3])/1000))
	if x1 < 0 {
		x1 = 0
	}
	if y1 < 0 {
		y1 = 0
	}
	if x2 > bounds.Dx() {
		x2 = bounds.Dx()
	}
	if y2 > bounds.Dy() {
		y2 = bounds.Dy()
	}
	if x2 <= x1 || y2 <= y1 {
		return "", fmt.Errorf("empty crop")
	}
	cropBounds := image.Rect(x1, y1, x2, y2)
	dst := image.NewRGBA(image.Rect(0, 0, cropBounds.Dx(), cropBounds.Dy()))
	draw.Draw(dst, dst.Bounds(), &image.Uniform{imgcolor.White}, image.Point{}, draw.Src)
	draw.Draw(dst, dst.Bounds(), src, cropBounds.Min, draw.Src)
	var out bytes.Buffer
	if err := png.Encode(&out, dst); err != nil {
		return "", err
	}
	return prefix + base64.StdEncoding.EncodeToString(out.Bytes()), nil
}
