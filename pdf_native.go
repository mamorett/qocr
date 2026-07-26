package main

import (
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/klippa-app/go-pdfium/references"
	"github.com/klippa-app/go-pdfium/requests"
	"github.com/klippa-app/go-pdfium/responses"
)

// ─────────────────────────────────────────────────────────────────────────────
// Top-level per-page entry point
// ─────────────────────────────────────────────────────────────────────────────

// extractNativePage extracts text and tables from a single PDF page without
// any OCR or AI calls.
//
// Strategy:
//  1. (Tier 1) If the PDF has a logical structure tree (tagged PDF / PDF-UA),
//     walk it to get exact Table/TR/TD elements and semantic heading levels.
//  2. (Tier 2) Fallback: preserve PDFium's native text-stream order. It does
//     not reconstruct lines from visual coordinates or guess table boundaries:
//     both operations can interleave columns and duplicate page content.
func extractNativePage(path string, index int) ([]OCRBlock, PageDim, error) {
	if err := initPDFium(); err != nil {
		return nil, PageDim{}, err
	}

	instance, err := pdfiumPool.GetInstance(time.Minute * 2)
	if err != nil {
		return nil, PageDim{}, fmt.Errorf("getting PDFium instance: %w", err)
	}
	defer instance.Close()

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, PageDim{}, fmt.Errorf("reading PDF: %w", err)
	}

	doc, err := instance.OpenDocument(&requests.OpenDocument{File: &data})
	if err != nil {
		return nil, PageDim{}, fmt.Errorf("opening PDF document: %w", err)
	}
	defer instance.FPDF_CloseDocument(&requests.FPDF_CloseDocument{Document: doc.Document}) //nolint:errcheck

	// ── Page dimensions ──────────────────────────────────────────────────────
	dim := PageDim{}
	if sizeResp, err2 := instance.FPDF_GetPageSizeByIndex(&requests.FPDF_GetPageSizeByIndex{
		Document: doc.Document,
		Index:    index,
	}); err2 == nil && sizeResp != nil {
		dim = PageDim{Width: int(sizeResp.Width), Height: int(sizeResp.Height)}
	}

	// ── Per-character font metadata for heading classification ───────────────
	var chars []*responses.GetPageTextStructuredChar
	if charResp, err2 := instance.GetPageTextStructured(&requests.GetPageTextStructured{
		Page: requests.Page{
			ByIndex: &requests.PageByIndex{Document: doc.Document, Index: index},
		},
		Mode:                   requests.GetPageTextStructuredModeChars,
		CollectFontInformation: true,
	}); err2 == nil && charResp != nil {
		chars = charResp.Chars
	}

	// ── Tier 1: PDF structure tree (tagged PDFs / PDF-UA) ───────────────────
	structTree, err2 := instance.FPDF_StructTree_GetForPage(&requests.FPDF_StructTree_GetForPage{
		Page: requests.Page{
			ByIndex: &requests.PageByIndex{Document: doc.Document, Index: index},
		},
	})
	if err2 == nil && structTree != nil {
		childCount, err3 := instance.FPDF_StructTree_CountChildren(&requests.FPDF_StructTree_CountChildren{
			StructTree: structTree.StructTree,
		})
		if err3 == nil && childCount != nil && childCount.Count > 0 {
			bodySize := modalFontSize(chars)
			blockIdx := 0
			var blocks []OCRBlock
			for ci := 0; ci < childCount.Count; ci++ {
				child, err4 := instance.FPDF_StructTree_GetChildAtIndex(&requests.FPDF_StructTree_GetChildAtIndex{
					StructTree: structTree.StructTree,
					Index:      ci,
				})
				if err4 != nil || child == nil {
					continue
				}
				extracted := extractStructElem(instance, child.StructElement, bodySize, &blockIdx)
				blocks = append(blocks, extracted...)
			}
			instance.FPDF_StructTree_Close(&requests.FPDF_StructTree_Close{StructTree: structTree.StructTree}) //nolint:errcheck
			if len(blocks) > 0 {
				return blocks, dim, nil
			}
		} else {
			instance.FPDF_StructTree_Close(&requests.FPDF_StructTree_Close{StructTree: structTree.StructTree}) //nolint:errcheck
		}
	}

	// ── Tier 2: preserve PDFium's text-stream order ─────────────────────────
	textResp, err3 := instance.GetPageText(&requests.GetPageText{
		Page: requests.Page{
			ByIndex: &requests.PageByIndex{Document: doc.Document, Index: index},
		},
	})
	if err3 == nil && textResp != nil {
		if text := normalizeNativeText(textResp.Text); text != "" {
			return []OCRBlock{{Index: 0, Label: "text", Content: text}}, dim, nil
		}
	}
	return charSliceToBlocks(chars), dim, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Tier 1: pdfium struct-instance mini interface
// ─────────────────────────────────────────────────────────────────────────────

type pdfiumStructInst interface {
	FPDF_StructElement_GetType(*requests.FPDF_StructElement_GetType) (*responses.FPDF_StructElement_GetType, error)
	FPDF_StructElement_CountChildren(*requests.FPDF_StructElement_CountChildren) (*responses.FPDF_StructElement_CountChildren, error)
	FPDF_StructElement_GetChildAtIndex(*requests.FPDF_StructElement_GetChildAtIndex) (*responses.FPDF_StructElement_GetChildAtIndex, error)
	FPDF_StructElement_GetActualText(*requests.FPDF_StructElement_GetActualText) (*responses.FPDF_StructElement_GetActualText, error)
	FPDF_StructElement_GetAltText(*requests.FPDF_StructElement_GetAltText) (*responses.FPDF_StructElement_GetAltText, error)
}

// extractStructElem recursively maps a PDF struct element to OCRBlocks.
func extractStructElem(inst pdfiumStructInst, elem references.FPDF_STRUCTELEMENT, bodySize float64, blockIdx *int) []OCRBlock {
	typeResp, _ := inst.FPDF_StructElement_GetType(&requests.FPDF_StructElement_GetType{StructElement: elem})
	tag := ""
	if typeResp != nil {
		tag = strings.ToLower(strings.TrimSpace(typeResp.Type))
	}

	if tag == "table" {
		return extractStructTable(inst, elem, blockIdx)
	}

	childCountResp, err := inst.FPDF_StructElement_CountChildren(&requests.FPDF_StructElement_CountChildren{StructElement: elem})
	if err != nil || childCountResp == nil || childCountResp.Count == 0 {
		text := getStructElemText(inst, elem)
		if strings.TrimSpace(text) == "" {
			return nil
		}
		b := OCRBlock{Index: *blockIdx, Label: structTagToLabel(tag), Content: strings.TrimSpace(text)}
		*blockIdx++
		return []OCRBlock{b}
	}

	var result []OCRBlock
	for ci := 0; ci < childCountResp.Count; ci++ {
		childResp, err2 := inst.FPDF_StructElement_GetChildAtIndex(&requests.FPDF_StructElement_GetChildAtIndex{
			StructElement: elem, Index: ci,
		})
		if err2 != nil || childResp == nil {
			continue
		}
		result = append(result, extractStructElem(inst, childResp.StructElement, bodySize, blockIdx)...)
	}
	return result
}

// extractStructTable walks Table → TR → TD/TH and returns an HTML table OCRBlock.
// This is the zero-ambiguity path: pure PDF structural metadata.
func extractStructTable(inst pdfiumStructInst, tableElem references.FPDF_STRUCTELEMENT, blockIdx *int) []OCRBlock {
	rowCountResp, err := inst.FPDF_StructElement_CountChildren(&requests.FPDF_StructElement_CountChildren{StructElement: tableElem})
	if err != nil || rowCountResp == nil || rowCountResp.Count == 0 {
		return nil
	}

	var grid [][]string
	for ri := 0; ri < rowCountResp.Count; ri++ {
		rowResp, err2 := inst.FPDF_StructElement_GetChildAtIndex(&requests.FPDF_StructElement_GetChildAtIndex{
			StructElement: tableElem, Index: ri,
		})
		if err2 != nil || rowResp == nil {
			continue
		}
		cellCountResp, err3 := inst.FPDF_StructElement_CountChildren(&requests.FPDF_StructElement_CountChildren{StructElement: rowResp.StructElement})
		if err3 != nil || cellCountResp == nil {
			continue
		}
		var row []string
		for ci := 0; ci < cellCountResp.Count; ci++ {
			cellResp, err4 := inst.FPDF_StructElement_GetChildAtIndex(&requests.FPDF_StructElement_GetChildAtIndex{
				StructElement: rowResp.StructElement, Index: ci,
			})
			if err4 != nil || cellResp == nil {
				row = append(row, "")
				continue
			}
			row = append(row, strings.TrimSpace(collectStructText(inst, cellResp.StructElement)))
		}
		if len(row) > 0 {
			grid = append(grid, row)
		}
	}

	if len(grid) == 0 {
		return nil
	}
	b := OCRBlock{Index: *blockIdx, Label: "table", Content: buildHTMLTable(grid)}
	*blockIdx++
	return []OCRBlock{b}
}

// collectStructText recursively collects all text from a struct element subtree.
func collectStructText(inst pdfiumStructInst, elem references.FPDF_STRUCTELEMENT) string {
	if text := getStructElemText(inst, elem); text != "" {
		return text
	}
	childCountResp, err := inst.FPDF_StructElement_CountChildren(&requests.FPDF_StructElement_CountChildren{StructElement: elem})
	if err != nil || childCountResp == nil {
		return ""
	}
	var parts []string
	for ci := 0; ci < childCountResp.Count; ci++ {
		childResp, err2 := inst.FPDF_StructElement_GetChildAtIndex(&requests.FPDF_StructElement_GetChildAtIndex{
			StructElement: elem, Index: ci,
		})
		if err2 != nil || childResp == nil {
			continue
		}
		if t := collectStructText(inst, childResp.StructElement); t != "" {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, " ")
}

// getStructElemText reads actualText / altText from a leaf struct element.
func getStructElemText(inst pdfiumStructInst, elem references.FPDF_STRUCTELEMENT) string {
	if r, err := inst.FPDF_StructElement_GetActualText(&requests.FPDF_StructElement_GetActualText{StructElement: elem}); err == nil && r != nil && strings.TrimSpace(r.Actualtext) != "" {
		return r.Actualtext
	}
	if r, err := inst.FPDF_StructElement_GetAltText(&requests.FPDF_StructElement_GetAltText{StructElement: elem}); err == nil && r != nil && strings.TrimSpace(r.AltText) != "" {
		return r.AltText
	}
	return ""
}

// structTagToLabel maps a PDF structure tag string to an OCRBlock label.
func structTagToLabel(tag string) string {
	switch tag {
	case "h1", "h2":
		return "title"
	case "h3", "h4", "h5", "h6", "h":
		return "header"
	case "figure":
		return "figure"
	case "caption":
		return "caption"
	default:
		return "text"
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Native text fallback helpers
// ─────────────────────────────────────────────────────────────────────────────

func normalizeNativeText(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	text = strings.ReplaceAll(text, "\u00a0", " ")
	text = strings.ReplaceAll(text, "\x00", "")
	return strings.TrimSpace(text)
}

// ─────────────────────────────────────────────────────────────────────────────
// Font-size modal classifier (reads actual PDF font table values)
// ─────────────────────────────────────────────────────────────────────────────

// modalFontSize returns the most-frequent font size across page characters
// (rounded to nearest 0.5pt for stability). This is the body-text baseline.
func modalFontSize(chars []*responses.GetPageTextStructuredChar) float64 {
	freq := make(map[float64]int)
	for _, c := range chars {
		if c == nil || c.FontInformation == nil || c.FontInformation.Size <= 0 {
			continue
		}
		rounded := math.Round(c.FontInformation.Size*2) / 2
		freq[rounded]++
	}
	if len(freq) == 0 {
		return 12.0
	}
	var best float64
	bestCount := 0
	for sz, count := range freq {
		if count > bestCount || (count == bestCount && sz < best) {
			bestCount = count
			best = sz
		}
	}
	return best
}

// classifyLine maps a line to an OCRBlock label using actual PDF font metadata.
func classifyLine(line []*responses.GetPageTextStructuredChar, bodySize float64) string {
	if len(line) == 0 || bodySize <= 0 {
		return "text"
	}
	var sumSz, sumWeight, count float64
	for _, c := range line {
		if c.FontInformation == nil {
			continue
		}
		sz := c.FontInformation.RenderedSize
		if sz <= 0 {
			sz = c.FontInformation.Size
		}
		if sz > 0 {
			sumSz += sz
			count++
		}
		if c.FontInformation.Weight > 0 {
			sumWeight += float64(c.FontInformation.Weight)
		}
	}
	if count == 0 {
		return "text"
	}
	ratio := (sumSz / count) / bodySize
	avgWeight := sumWeight / count
	switch {
	case ratio >= 1.6:
		return "title"
	case ratio >= 1.25:
		return "header"
	case avgWeight > 500 && len(line) < 80:
		return "header"
	default:
		return "text"
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Character grouping helpers
// ─────────────────────────────────────────────────────────────────────────────

// groupCharsIntoLines groups characters into visual lines by Y-coordinate
// clustering (~3pt tolerance). Returns top-to-bottom, left-to-right order.
func groupCharsIntoLines(chars []*responses.GetPageTextStructuredChar) [][]*responses.GetPageTextStructuredChar {
	if len(chars) == 0 {
		return nil
	}
	type lineGroup struct {
		avgTop float64
		chars  []*responses.GetPageTextStructuredChar
	}
	var groups []lineGroup
	for _, c := range chars {
		if c == nil {
			continue
		}
		top := c.PointPosition.Top
		placed := false
		for i := range groups {
			if math.Abs(top-groups[i].avgTop) < 3.0 {
				groups[i].chars = append(groups[i].chars, c)
				n := float64(len(groups[i].chars))
				groups[i].avgTop = (groups[i].avgTop*(n-1) + top) / n
				placed = true
				break
			}
		}
		if !placed {
			groups = append(groups, lineGroup{avgTop: top, chars: []*responses.GetPageTextStructuredChar{c}})
		}
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].avgTop < groups[j].avgTop })
	lines := make([][]*responses.GetPageTextStructuredChar, len(groups))
	for i, g := range groups {
		line := make([]*responses.GetPageTextStructuredChar, len(g.chars))
		copy(line, g.chars)
		sort.Slice(line, func(a, b int) bool { return line[a].PointPosition.Left < line[b].PointPosition.Left })
		lines[i] = line
	}
	return lines
}

// assembleLineText concatenates character text, inserting spaces for visible gaps.
// The gap threshold scales with font size so tight tracking in small fonts does
// not fuse words together.
func assembleLineText(line []*responses.GetPageTextStructuredChar) string {
	var sb strings.Builder
	var prev *responses.GetPageTextStructuredChar
	for _, c := range line {
		if c == nil {
			continue
		}
		if prev != nil {
			gap := c.PointPosition.Left - prev.PointPosition.Right
			// Word-space threshold: a fraction of the font size, clamped. A real
			// inter-word gap is typically ≥ ~25% of the font size.
			threshold := 1.5
			if prev.FontInformation != nil {
				sz := prev.FontInformation.RenderedSize
				if sz <= 0 {
					sz = prev.FontInformation.Size
				}
				if sz > 0 {
					threshold = sz * 0.22
					if threshold < 1.0 {
						threshold = 1.0
					}
				}
			}
			if gap > threshold && sb.Len() > 0 {
				s := sb.String()
				if s[len(s)-1] != ' ' {
					sb.WriteRune(' ')
				}
			}
		}
		sb.WriteString(c.Text)
		prev = c
	}
	return sb.String()
}

// charSliceToBlocks is the last-resort fallback: assembles all chars as text.
func charSliceToBlocks(chars []*responses.GetPageTextStructuredChar) []OCRBlock {
	lines := groupCharsIntoLines(chars)
	var sb strings.Builder
	for _, line := range lines {
		sb.WriteString(assembleLineText(line))
		sb.WriteRune('\n')
	}
	text := strings.TrimSpace(sb.String())
	if text == "" {
		return nil
	}
	return []OCRBlock{{Index: 0, Label: "text", Content: text}}
}

// ─────────────────────────────────────────────────────────────────────────────
// HTML table builder
// ─────────────────────────────────────────────────────────────────────────────

// buildHTMLTable converts a 2-D string grid into an HTML table string.
// The existing renderer.go htmlTableToMarkdown() and htmlTableToLatex()
// consume this format — zero changes to the rendering pipeline required.
func buildHTMLTable(rows [][]string) string {
	if len(rows) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("<table>")
	for rowIdx, row := range rows {
		sb.WriteString("<tr>")
		for _, cell := range row {
			cell = strings.ReplaceAll(strings.TrimSpace(cell), "\n", " ")
			if rowIdx == 0 {
				sb.WriteString("<th>")
				sb.WriteString(htmlEscapeCell(cell))
				sb.WriteString("</th>")
			} else {
				sb.WriteString("<td>")
				sb.WriteString(htmlEscapeCell(cell))
				sb.WriteString("</td>")
			}
		}
		sb.WriteString("</tr>")
	}
	sb.WriteString("</table>")
	return sb.String()
}

// htmlEscapeCell escapes the minimal HTML special characters for cell content.
func htmlEscapeCell(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}
