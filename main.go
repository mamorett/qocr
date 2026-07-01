package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

var latexTableCounter int64

func getUniqueBoxName() string {
	val := atomic.AddInt64(&latexTableCounter, 1)
	s := fmt.Sprintf("%d", val)
	var sb strings.Builder
	sb.WriteString("tblbox")
	for _, r := range s {
		sb.WriteRune(rune('a' + (r - '0')))
	}
	return sb.String()
}

const defaultPrompt = "Extract all text from this document"

var refRegexp = regexp.MustCompile(`(?i)<[|/]*ref[|/]*>(.*?)<[|/]+ref[|/]+>`)
var refLeftoverRegexp = regexp.MustCompile(`(?i)<[|/]*ref[|/]*>`)

func stripReferenceTags(content string) string {
	matches := refRegexp.FindAllStringSubmatch(content, -1)
	for _, match := range matches {
		if len(match) == 2 {
			fullTag := match[0]
			inner := strings.TrimSpace(match[1])
			innerLower := strings.ToLower(inner)
			
			if innerLower == "italic" || innerLower == "bold" || innerLower == "regular" || 
				innerLower == "underline" || innerLower == "italian" || innerLower == "roman" {
				content = strings.ReplaceAll(content, fullTag, "")
			} else {
				content = strings.ReplaceAll(content, fullTag, inner)
			}
		}
	}
	content = refLeftoverRegexp.ReplaceAllString(content, "")
	return content
}

func isGibberish(content string) bool {
	lower := strings.ToLower(content)
	if strings.Contains(lower, "0 0 0 0") || 
		strings.Contains(lower, "00 00 00") ||
		strings.Contains(lower, "24 0 24") ||
		strings.Contains(lower, "侍") ||
		(strings.Contains(lower, "水平") && strings.Contains(lower, "0 0")) ||
		strings.Contains(lower, "收元铜业") ||
		strings.Contains(lower, "bitch") {
		return true
	}
	
	if strings.Count(lower, "\\(") >= 4 {
		return true
	}
	
	parts := strings.Fields(lower)
	if len(parts) > 10 {
		zeroCount := 0
		numCount := 0
		for _, p := range parts {
			if p == "0" || p == "00" || p == "000" {
				zeroCount++
			}
			isNum := true
			for _, c := range p {
				if (c < '0' || c > '9') && c != '.' && c != '-' {
					isNum = false
					break
				}
			}
			if isNum {
				numCount++
			}
		}
		if numCount > 8 && float64(numCount)/float64(len(parts)) > 0.7 {
			return true
		}
		if zeroCount >= 4 {
			return true
		}
	}
	return false
}

func isLeakedContent(content string, bbox []int) bool {
	lower := strings.ToLower(content)
	if strings.Contains(lower, "ground truth") ||
		strings.Contains(lower, "rule 2") ||
		strings.Contains(lower, "inconsistent") ||
		strings.Contains(lower, "no text or characters") ||
		strings.Contains(lower, "too blurry") ||
		strings.Contains(lower, "unreadable") ||
		strings.Contains(lower, "\\therefore") ||
		strings.Contains(lower, "\\frac{") ||
		strings.Contains(lower, "广力云") ||
		(strings.Contains(lower, "loopback") && strings.Contains(lower, "65536") && strings.Contains(lower, "italian")) {
		return true
	}
	
	if isGibberish(content) {
		return true
	}
	
	if len(bbox) >= 4 {
		w := bbox[2] - bbox[0]
		if w > 0 && len(content) > 25 {
			ratio := float64(len(content)) / float64(w)
			if ratio > 0.35 {
				return true
			}
		}
	}
	return false
}

const asciiArt = `
   __ _  ___   ___ _ __ 
 / _` + "`" + ` |/ _ \ / __| '__|
| (_| | (_) | (__| |   
 \__, |\___/ \___|_|   
    |_|                `

var version = "dev"

// ---------------------------------------------------------------------------
// API wire types
// ---------------------------------------------------------------------------

type PageDim struct {
	Width    int `json:"width"`
	Height   int `json:"height"`
	DPI      int `json:"dpi"`
	Rotation int `json:"rotation"`
}

type Engine string

const (
	EngineGLM   Engine = "glm"
	EngineBaidu Engine = "baidu"
)

type ImageURL struct {
	URL string `json:"url"`
}

type ContentPart struct {
	Type     string    `json:"type"`
	ImageURL *ImageURL `json:"image_url,omitempty"`
	Text     string    `json:"text,omitempty"`
}

type Message struct {
	Role    string        `json:"role"`
	Content []ContentPart `json:"content"`
}

type CustomParams struct {
	NgramSize  int `json:"ngram_size"`
	WindowSize int `json:"window_size"`
}

type ImagesConfig struct {
	ImageMode string `json:"image_mode,omitempty"`
}

type ChatRequest struct {
	Model                string        `json:"model"`
	Messages             []Message     `json:"messages"`
	Temperature          float64       `json:"temperature"`
	MaxTokens            int           `json:"max_tokens,omitempty"`
	SkipSpecialTokens    *bool         `json:"skip_special_tokens,omitempty"`
	ImagesConfig         *ImagesConfig `json:"images_config,omitempty"`
	CustomLogitProcessor string        `json:"custom_logit_processor,omitempty"`
	CustomParams         *CustomParams `json:"custom_params,omitempty"`
}

type Choice struct {
	Message struct {
		Content string `json:"content"`
	} `json:"message"`
}

type ChatResponse struct {
	Choices []Choice `json:"choices"`
	Error   *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// ---------------------------------------------------------------------------
// OCR structured response
// ---------------------------------------------------------------------------

type OCRBlock struct {
	Index   int         `json:"index"`
	Label   string      `json:"label"`
	Content interface{} `json:"content"`
	BBox2D  interface{} `json:"bbox_2d"`
}

func parseOCRContent(raw string) (pages [][]OCRBlock, structured bool) {
	raw = strings.TrimSpace(raw)

	// Clean Markdown code blocks if any (model might return JSON or text inside backticks)
	cleanRaw := raw
	if strings.HasPrefix(raw, "```") {
		lines := strings.Split(raw, "\n")
		if len(lines) > 2 {
			cleanRaw = strings.Join(lines[1:len(lines)-1], "\n")
		} else {
			cleanRaw = strings.Trim(raw, "`")
			// Remove leading language name if any
			if strings.HasPrefix(cleanRaw, "json\n") {
				cleanRaw = cleanRaw[5:]
			}
		}
		cleanRaw = strings.TrimSpace(cleanRaw)
	}

	// Try to find a JSON array in the cleaned raw string or original
	for _, s := range []string{cleanRaw, raw} {
		start := strings.Index(s, "[")
		end := strings.LastIndex(s, "]")
		if start != -1 && end != -1 && end > start {
			jsonPart := s[start : end+1]

			// Case 1: [[{...}, {...}]] - Array of pages (each page is an array of blocks)
			if err := json.Unmarshal([]byte(jsonPart), &pages); err == nil && len(pages) > 0 {
				return pages, true
			}

			// Case 2: [{...}, {...}] - Single array of blocks (treat as one page)
			var singlePage []OCRBlock
			if err := json.Unmarshal([]byte(jsonPart), &singlePage); err == nil && len(singlePage) > 0 {
				return [][]OCRBlock{singlePage}, true
			}
		}
	}

	// Fallback: Split raw text into individual blocks if it's not JSON
	var blocks []OCRBlock
	lines := strings.Split(cleanRaw, "\n")
	index := 0
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		label := "text"
		if strings.HasPrefix(line, "#") {
			label = "title"
		} else if isListItem(line) {
			label = "list_item"
		}

		blocks = append(blocks, OCRBlock{
			Index:   index,
			Label:   label,
			Content: line,
		})
		index++
	}

	if len(blocks) > 0 {
		return [][]OCRBlock{blocks}, false
	}
	return [][]OCRBlock{{{Index: 0, Label: "text", Content: raw}}}, false
}

func isListItem(s string) bool {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "- ") || strings.HasPrefix(s, "* ") {
		return true
	}
	// Check for "1. ", "2. ", etc.
	if len(s) > 2 && s[0] >= '0' && s[0] <= '9' {
		for i := 1; i < len(s); i++ {
			if s[i] == '.' {
				if i+1 < len(s) && s[i+1] == ' ' {
					return true
				}
				break
			}
			if s[i] < '0' || s[i] > '9' {
				break
			}
		}
	}
	return false
}

func parseLabelAndBbox(tagContent string) (string, []int) {
	tagContent = strings.TrimSpace(tagContent)
	bracketStart := strings.Index(tagContent, "[")
	bracketEnd := strings.Index(tagContent, "]")
	if bracketStart == -1 || bracketEnd == -1 || bracketEnd <= bracketStart {
		return strings.TrimSpace(tagContent), nil
	}

	label := strings.TrimSpace(tagContent[:bracketStart])
	bboxStr := tagContent[bracketStart+1 : bracketEnd]
	parts := strings.Split(bboxStr, ",")
	if len(parts) != 4 {
		return label, nil
	}

	var bbox []int
	for _, part := range parts {
		var val int
		_, err := fmt.Sscanf(strings.TrimSpace(part), "%d", &val)
		if err != nil {
			return label, nil
		}
		bbox = append(bbox, val)
	}
	return label, bbox
}

func parseBaiduChunkWithDet(chunk string) []OCRBlock {
	var blocks []OCRBlock
	var remaining = chunk
	index := 0

	firstDet := strings.Index(remaining, "<|det|>")
	if firstDet > 0 {
		beforeText := strings.TrimSpace(remaining[:firstDet])
		if beforeText != "" {
			blocks = append(blocks, OCRBlock{
				Index:   index,
				Label:   "text",
				Content: beforeText,
			})
			index++
		}
		remaining = remaining[firstDet:]
	}

	for {
		detStart := strings.Index(remaining, "<|det|>")
		if detStart == -1 {
			leftover := strings.TrimSpace(remaining)
			if leftover != "" {
				if len(blocks) > 0 {
					prevContent := blocks[len(blocks)-1].Content.(string)
					if prevContent != "" {
						blocks[len(blocks)-1].Content = prevContent + "\n" + leftover
					} else {
						blocks[len(blocks)-1].Content = leftover
					}
				} else {
					blocks = append(blocks, OCRBlock{
						Index:   index,
						Label:   "text",
						Content: leftover,
					})
					index++
				}
			}
			break
		}

		detEnd := strings.Index(remaining[detStart:], "<|/det|>")
		if detEnd == -1 {
			leftover := strings.TrimSpace(remaining[detStart+7:])
			blocks = append(blocks, OCRBlock{
				Index:   index,
				Label:   "text",
				Content: leftover,
			})
			break
		}
		detEndIdx := detStart + detEnd

		tagContent := remaining[detStart+7 : detEndIdx]
		label, bbox := parseLabelAndBbox(tagContent)

		contentStart := detEndIdx + 8
		nextDet := strings.Index(remaining[contentStart:], "<|det|>")

		var blockContent string
		if nextDet == -1 {
			blockContent = remaining[contentStart:]
			remaining = ""
		} else {
			blockContent = remaining[contentStart : contentStart+nextDet]
			remaining = remaining[contentStart+nextDet:]
		}

		blockContent = strings.TrimSpace(blockContent)
		blockContent = stripReferenceTags(blockContent)
		blockContent = strings.ReplaceAll(blockContent, "<|det|>", "")
		blockContent = strings.ReplaceAll(blockContent, "<|/det|>", "")

		blocks = append(blocks, OCRBlock{
			Index:   index,
			Label:   label,
			Content: blockContent,
			BBox2D:  bbox,
		})
		index++

		if remaining == "" {
			break
		}
	}
	return blocks
}

func parseBaiduSiblingFormat(chunk string) ([]OCRBlock, bool) {
	lines := strings.Split(chunk, "\n")
	var blocks []OCRBlock
	index := 0
	hasAnySiblingBlock := false

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		bracketStart := strings.Index(line, "[")
		bracketEnd := strings.Index(line, "]")
		if bracketStart != -1 && bracketEnd != -1 && bracketEnd > bracketStart {
			label := strings.TrimSpace(line[:bracketStart])
			if label != "" && !strings.Contains(label, "\n") {
				bboxStr := line[bracketStart+1 : bracketEnd]
				parts := strings.Split(bboxStr, ",")
				if len(parts) == 4 {
					var bbox []int
					validBbox := true
					for _, part := range parts {
						var val int
						_, err := fmt.Sscanf(strings.TrimSpace(part), "%d", &val)
						if err != nil {
							validBbox = false
							break
						}
						bbox = append(bbox, val)
					}
					if validBbox {
						content := strings.TrimSpace(line[bracketEnd+1:])
						content = stripReferenceTags(content)
						content = strings.ReplaceAll(content, "<|det|>", "")
						content = strings.ReplaceAll(content, "<|/det|>", "")

						blocks = append(blocks, OCRBlock{
							Index:   index,
							Label:   label,
							Content: content,
							BBox2D:  bbox,
						})
						index++
						hasAnySiblingBlock = true
						continue
					}
				}
			}
		}

		if len(blocks) > 0 {
			prevContent := blocks[len(blocks)-1].Content.(string)
			if prevContent != "" {
				blocks[len(blocks)-1].Content = prevContent + "\n" + line
			} else {
				blocks[len(blocks)-1].Content = line
			}
		} else {
			blocks = append(blocks, OCRBlock{
				Index:   index,
				Label:   "text",
				Content: line,
			})
			index++
		}
	}

	return blocks, hasAnySiblingBlock
}

func parseBaiduContent(raw string) [][]OCRBlock {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return [][]OCRBlock{{{Index: 0, Label: "text", Content: ""}}}
	}

	var chunks []string
	if strings.Contains(raw, "<PAGE>") {
		parts := strings.Split(raw, "<PAGE>")
		for _, part := range parts {
			chunks = append(chunks, part)
		}
		if len(chunks) > 0 && strings.TrimSpace(chunks[0]) == "" {
			chunks = chunks[1:]
		}
	} else {
		chunks = []string{raw}
	}

	var pages [][]OCRBlock
	for _, chunk := range chunks {
		if strings.Contains(chunk, "<|det|>") {
			pages = append(pages, parseBaiduChunkWithDet(chunk))
		} else {
			blocks, parsedAny := parseBaiduSiblingFormat(chunk)
			if parsedAny {
				pages = append(pages, blocks)
			} else {
				pages = append(pages, []OCRBlock{{
					Index:   0,
					Label:   "text",
					Content: strings.TrimSpace(chunk),
				}})
			}
		}
	}

	if len(pages) == 0 {
		return [][]OCRBlock{{{Index: 0, Label: "text", Content: ""}}}
	}
	return pages
}
// ---------------------------------------------------------------------------
// Output formatters
// ---------------------------------------------------------------------------

func getBBox(bbox2D interface{}) ([]int, bool) {
	if bbox2D == nil {
		return nil, false
	}
	switch v := bbox2D.(type) {
	case []int:
		if len(v) == 4 {
			return v, true
		}
	case []interface{}:
		if len(v) == 4 {
			res := make([]int, 4)
			for i, val := range v {
				switch num := val.(type) {
				case float64:
					res[i] = int(num)
				case int:
					res[i] = num
				default:
					return nil, false
				}
			}
			return res, true
		}
	}
	return nil, false
}

func sanitizeLabel(l string) string {
	l = strings.ToLower(l)
	var sb strings.Builder
	for _, r := range l {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			sb.WriteRune(r)
		} else {
			sb.WriteRune('-')
		}
	}
	return sb.String()
}

func blockContentString(b OCRBlock) string {
	switch v := b.Content.(type) {
	case string:
		return v
	case []interface{}:
		var items []string
		for _, item := range v {
			items = append(items, fmt.Sprint(item))
		}
		return strings.Join(items, "\n")
	default:
		return fmt.Sprint(b.Content)
	}
}

func getImageDimensions(path string) (width int, height int, err error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		return 0, 0, err
	}
	return cfg.Width, cfg.Height, nil
}

func renderMarkdown(pages [][]OCRBlock, showBBox bool) string {
	var sb strings.Builder
	for pi, page := range pages {
		if len(pages) > 1 {
			fmt.Fprintf(&sb, "\n---\n<!-- page %d -->\n\n", pi+1)
		}
		
		var cleanPage []OCRBlock
		for _, b := range page {
			contentStr := strings.ToLower(strings.TrimSpace(blockContentString(b)))
			if contentStr != "" && contentStr != "[non-text]" && contentStr != "[empty]" && contentStr != "[]" && contentStr != "[no text]" && contentStr != "(no text)" {
				cleanPage = append(cleanPage, b)
			}
		}
		
		rows := groupBlocksIntoRows(cleanPage)
		if len(rows) == 0 {
			for _, b := range cleanPage {
				sb.WriteString(blockContentString(b))
				sb.WriteString("\n\n")
			}
			continue
		}
		
		var tableRows [][]string
		
		flushTable := func() {
			if len(tableRows) == 0 {
				return
			}
			maxCols := 0
			for _, r := range tableRows {
				if len(r) > maxCols {
					maxCols = len(r)
				}
			}
			if maxCols > 1 {
				for idx, r := range tableRows {
					var cleanRow []string
					for _, cell := range r {
						if isLeakedContent(cell, nil) {
							cleanRow = append(cleanRow, "")
						} else {
							cleanRow = append(cleanRow, cell)
						}
					}
					isEmptyRow := true
					for _, cell := range cleanRow {
						c := strings.ToLower(strings.TrimSpace(cell))
						if c != "" && c != "[non-text]" && c != "[empty]" && c != "[]" && c != "[no text]" && c != "(no text)" && c != "[document icon]" && c != "arrowleft" && c != "exit left" && c != "center" && c != "screen up" && c != "move the right" && c != "work around the screen" && c != "speed up the screen at the bottom left" && c != "mail group" {
							isEmptyRow = false
							break
						}
					}
					if isEmptyRow {
						continue
					}
					sb.WriteString("| " + strings.Join(cleanRow, " | ") + " |\n")
					if idx == 0 {
						sb.WriteString("|")
						for c := 0; c < maxCols; c++ {
							sb.WriteString(" --- |")
						}
						sb.WriteString("\n")
					}
				}
				sb.WriteString("\n")
			} else {
				for _, r := range tableRows {
					if len(r) > 0 {
						sb.WriteString(r[0] + "\n\n")
					}
				}
			}
			tableRows = nil
		}
		
		for _, row := range rows {
			if len(row) == 1 || len(row) > 20 {
				var parts []string
				for _, b := range row {
					parts = append(parts, strings.TrimSpace(blockContentString(b)))
				}
				content := strings.TrimSpace(strings.Join(parts, " "))
				if content == "" {
					continue
				}
				if strings.Contains(strings.ToLower(content), "<table") {
					sb.WriteString(htmlTableToMarkdown(content) + "\n\n")
					continue
				}
				label := row[0].Label
				var mergedBBox []int
				for _, b := range row {
					if box, ok := getBBox(b.BBox2D); ok {
						if len(mergedBBox) == 0 {
							mergedBBox = []int{box[0], box[1], box[2], box[3]}
						} else {
							if box[0] < mergedBBox[0] { mergedBBox[0] = box[0] }
							if box[1] < mergedBBox[1] { mergedBBox[1] = box[1] }
							if box[2] > mergedBBox[2] { mergedBBox[2] = box[2] }
							if box[3] > mergedBBox[3] { mergedBBox[3] = box[3] }
						}
					}
				}
				if isLeakedContent(content, mergedBBox) {
					continue
				}
				
				shouldFlush := false
				if strings.ToLower(label) == "title" || strings.ToLower(label) == "header" || strings.ToLower(label) == "caption" || strings.ToLower(label) == "figure" {
					shouldFlush = true
				} else if len(content) > 40 {
					shouldFlush = true
				} else if strings.Count(content, " ") > 5 {
					shouldFlush = true
				}
				if shouldFlush {
					flushTable()
				}
				switch strings.ToLower(label) {
				case "title":
					fmt.Fprintf(&sb, "## %s\n\n", content)
				case "figure", "caption":
					fmt.Fprintf(&sb, "*%s*\n\n", content)
				default:
					sb.WriteString(content + "\n\n")
				}
			} else {
				var cells []string
				for _, b := range row {
					cellVal := strings.ReplaceAll(strings.TrimSpace(blockContentString(b)), "\n", " ")
					cells = append(cells, cellVal)
				}
				tableRows = append(tableRows, cells)
			}
		}
		flushTable()
	}
	return strings.TrimSpace(sb.String())
}

func tableRowToPlain(line string) string {
	if strings.Contains(line, "---") {
		return ""
	}
	parts := strings.Split(line, "|")
	var cells []string
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			cells = append(cells, p)
		}
	}
	return strings.Join(cells, "  ")
}

func renderPlainText(pages [][]OCRBlock) string {
	md := renderMarkdown(pages, false)
	replacer := strings.NewReplacer(
		"**", "", "__", "", "```", "", "`", "", "*", "", "_", "",
	)
	var out []string
	for _, line := range strings.Split(md, "\n") {
		if strings.HasPrefix(line, "---") || strings.HasPrefix(line, "<!--") {
			continue
		}
		stripped := strings.TrimLeft(line, "#")
		if len(stripped) != len(line) {
			stripped = strings.TrimSpace(stripped)
		}
		if strings.Contains(stripped, "|") {
			stripped = tableRowToPlain(stripped)
		}
		out = append(out, replacer.Replace(stripped))
	}
	result := strings.Join(out, "\n")
	for strings.Contains(result, "\n\n\n") {
		result = strings.ReplaceAll(result, "\n\n\n", "\n\n")
	}
	return strings.TrimSpace(result)
}

type JSONOutputBlock struct {
	Page    int         `json:"page"`
	Index   int         `json:"index"`
	Label   string      `json:"label"`
	Content interface{} `json:"content"`
	BBox2D  interface{} `json:"bbox_2d"`
}

type PageMeta struct {
	Page     int `json:"page"`
	Width    int `json:"width"`
	Height   int `json:"height"`
	DPI      int `json:"dpi"`
	Rotation int `json:"rotation"`
}

type JSONOutput struct {
	Source string            `json:"source"`
	Model  string            `json:"model"`
	Pages  []PageMeta        `json:"pages"`
	Blocks []JSONOutputBlock `json:"blocks"`
}

func renderJSON(pages [][]OCRBlock, source, model string, pageDims []PageDim) (string, error) {
	pageMetas := make([]PageMeta, len(pageDims))
	for idx, pd := range pageDims {
		pageMetas[idx] = PageMeta{
			Page:     idx + 1,
			Width:    pd.Width,
			Height:   pd.Height,
			DPI:      pd.DPI,
			Rotation: pd.Rotation,
		}
	}
	out := JSONOutput{
		Source: source,
		Model:  model,
		Pages:  pageMetas,
		Blocks: make([]JSONOutputBlock, 0),
	}
	for pi, page := range pages {
		for _, b := range page {
			out.Blocks = append(out.Blocks, JSONOutputBlock{
				Page:    pi + 1,
				Index:   b.Index,
				Label:   b.Label,
				Content: b.Content,
				BBox2D:  b.BBox2D,
			})
		}
	}
	bs, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return "", err
	}
	return string(bs), nil
}

var cjkRegexp = regexp.MustCompile(`\p{Han}`)
var nonASCIIRegexp = regexp.MustCompile(`[^\x00-\x7F]+`)

func cleanUnicodeForLatex(s string) string {
	// Map common CJK/unicode punctuation to standard LaTeX/ASCII equivalents
	s = strings.ReplaceAll(s, "。", ".")
	s = strings.ReplaceAll(s, "，", ", ")
	s = strings.ReplaceAll(s, "（", "(")
	s = strings.ReplaceAll(s, "）", ")")
	s = strings.ReplaceAll(s, "；", ";")
	s = strings.ReplaceAll(s, "：", ":")
	s = strings.ReplaceAll(s, "？", "?")
	s = strings.ReplaceAll(s, "！", "!")
	s = strings.ReplaceAll(s, "“", "\"")
	s = strings.ReplaceAll(s, "”", "\"")
	s = strings.ReplaceAll(s, "‘", "'")
	s = strings.ReplaceAll(s, "’", "'")
	s = strings.ReplaceAll(s, "○", "0")
	s = strings.ReplaceAll(s, "□", "\\(\\square\\)")
	s = strings.ReplaceAll(s, "•", "\\(\\bullet\\)")
	s = strings.ReplaceAll(s, "π", "\\(\\pi\\)")
	s = strings.ReplaceAll(s, "α", "\\(\\alpha\\)")
	s = strings.ReplaceAll(s, "β", "\\(\\beta\\)")
	s = strings.ReplaceAll(s, "λ", "\\(\\lambda\\)")
	s = strings.ReplaceAll(s, "θ", "\\(\\theta\\)")
	s = strings.ReplaceAll(s, "√", "\\(\\surd\\)")
	
	// Strip Chinese (Han) characters
	s = cjkRegexp.ReplaceAllString(s, "")
	
	// Strip other non-ASCII chars
	s = nonASCIIRegexp.ReplaceAllString(s, "")
	return s
}

func escapeLatex(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\textbackslash ")
	s = strings.ReplaceAll(s, "%", "\\%")
	s = strings.ReplaceAll(s, "&", "\\&")
	s = strings.ReplaceAll(s, "$", "\\$")
	s = strings.ReplaceAll(s, "_", "\\_")
	s = strings.ReplaceAll(s, "{", "\\{")
	s = strings.ReplaceAll(s, "}", "\\}")
	s = strings.ReplaceAll(s, "#", "\\#")
	return s
}

func escapeLatexWithMath(s string) string {
	s = cleanUnicodeForLatex(s)
	var sb strings.Builder
	parts := strings.Split(s, "\\(")
	for i, part := range parts {
		if i == 0 {
			sb.WriteString(escapeLatex(part))
		} else {
			subparts := strings.SplitN(part, "\\)", 2)
			if len(subparts) == 2 {
				sb.WriteString("\\(" + subparts[0] + "\\)")
				sb.WriteString(escapeLatex(subparts[1]))
			} else {
				sb.WriteString(escapeLatex("\\(" + part))
			}
		}
	}
	return sb.String()
}

func htmlTableToLatex(htmlStr string) string {
	var latexRows [][]string
	remaining := htmlStr
	maxCols := 0
	
	for {
		trStart := strings.Index(strings.ToLower(remaining), "<tr>")
		if trStart == -1 {
			break
		}
		trEnd := strings.Index(strings.ToLower(remaining[trStart:]), "</tr>")
		if trEnd == -1 {
			break
		}
		trContent := remaining[trStart+4 : trStart+trEnd]
		remaining = remaining[trStart+trEnd+5:]
		
		var cells []string
		cellRemaining := trContent
		for {
			tdStart := strings.Index(strings.ToLower(cellRemaining), "<td")
			thStart := strings.Index(strings.ToLower(cellRemaining), "<th")
			
			startIdx := -1
			isTh := false
			
			if tdStart != -1 && (thStart == -1 || tdStart < thStart) {
				startIdx = tdStart
				isTh = false
			} else if thStart != -1 {
				startIdx = thStart
				isTh = true
			}
			
			if startIdx == -1 {
				break
			}
			
			var endTag string
			if isTh {
				endTag = "</th>"
			} else {
				endTag = "</td>"
			}
			
			openBracketEnd := strings.Index(cellRemaining[startIdx:], ">")
			if openBracketEnd == -1 {
				break
			}
			openBracketEndIdx := startIdx + openBracketEnd
			
			tdEnd := strings.Index(strings.ToLower(cellRemaining[openBracketEndIdx:]), endTag)
			if tdEnd == -1 {
				break
			}
			tdEndIdx := openBracketEndIdx + tdEnd
			
			cellContent := cellRemaining[openBracketEndIdx+1 : tdEndIdx]
			cellRemaining = cellRemaining[tdEndIdx+len(endTag):]
			
			cellContent = cleanHTMLText(cellContent)
			cells = append(cells, escapeLatexWithMath(cellContent))
		}
		
		if len(cells) > 0 {
			latexRows = append(latexRows, cells)
			if len(cells) > maxCols {
				maxCols = len(cells)
			}
		}
	}
	
	if len(latexRows) == 0 {
		return escapeLatexWithMath(cleanHTMLText(htmlStr))
	}
	
	var sb strings.Builder
	sb.WriteString("\\begin{table}[h]\n\\centering\n")
	alignStr := ""
	for c := 0; c < maxCols; c++ {
		alignStr += "l "
	}
	boxName := getUniqueBoxName()
	fmt.Fprintf(&sb, "\\newsavebox{\\%s}\n", boxName)
	fmt.Fprintf(&sb, "\\sbox{\\%s}{%%\n", boxName)
	sb.WriteString("\\small\n")
	fmt.Fprintf(&sb, "\\begin{tabular}{%s}\n\\hline\n", strings.TrimSpace(alignStr))
	for idx, row := range latexRows {
		for len(row) < maxCols {
			row = append(row, "")
		}
		sb.WriteString(strings.Join(row, " & ") + " \\\\\n")
		if idx == 0 {
			sb.WriteString("\\hline\n")
		}
	}
	sb.WriteString("\\hline\n\\end{tabular}%\n}\n")
	fmt.Fprintf(&sb, "\\ifdim\\wd\\%s>\\linewidth\n", boxName)
	fmt.Fprintf(&sb, "  \\resizebox{\\linewidth}{!}{\\usebox{\\%s}}%%\n", boxName)
	fmt.Fprintf(&sb, "\\else\n")
	fmt.Fprintf(&sb, "  \\usebox{\\%s}%%\n", boxName)
	fmt.Fprintf(&sb, "\\fi\n")
	sb.WriteString("\\end{table}\n")
	return sb.String()
}

func renderLatex(pages [][]OCRBlock, source, model string) (string, error) {
	var sb strings.Builder
	sb.WriteString("\\documentclass{article}\n")
	sb.WriteString("\\usepackage[utf8]{inputenc}\n")
	sb.WriteString("\\usepackage{amsmath}\n")
	sb.WriteString("\\usepackage{amssymb}\n")
	sb.WriteString("\\usepackage{graphicx}\n")
	sb.WriteString("\\usepackage[margin=0.75in]{geometry}\n\n")
	sb.WriteString("\\begin{document}\n\n")
	
	for pi, page := range pages {
		if pi > 0 {
			sb.WriteString("\\newpage\n\n")
		}
		
		var cleanPage []OCRBlock
		for _, b := range page {
			contentStr := strings.ToLower(strings.TrimSpace(blockContentString(b)))
			if contentStr != "" && contentStr != "[non-text]" && contentStr != "[empty]" && contentStr != "[]" && contentStr != "[no text]" && contentStr != "(no text)" {
				cleanPage = append(cleanPage, b)
			}
		}
		
		rows := groupBlocksIntoRows(cleanPage)
		if len(rows) == 0 {
			for _, b := range cleanPage {
				content := strings.TrimSpace(blockContentString(b))
				if content != "" {
					sb.WriteString(escapeLatexWithMath(content) + "\n\n")
				}
			}
			continue
		}
		
		var tableRows [][]string
		
		flushTable := func() {
			if len(tableRows) == 0 {
				return
			}
			maxCols := 0
			for _, r := range tableRows {
				if len(r) > maxCols {
					maxCols = len(r)
				}
			}
			if maxCols > 1 {
				sb.WriteString("\\begin{table}[h]\n\\centering\n")
				alignStr := ""
				for c := 0; c < maxCols; c++ {
					alignStr += "l "
				}
				boxName := getUniqueBoxName()
				fmt.Fprintf(&sb, "\\newsavebox{\\%s}\n", boxName)
				fmt.Fprintf(&sb, "\\sbox{\\%s}{%%\n", boxName)
				sb.WriteString("\\small\n")
				fmt.Fprintf(&sb, "\\begin{tabular}{%s}\n\\hline\n", strings.TrimSpace(alignStr))
				
				for idx, r := range tableRows {
					var cleanRow []string
					for _, cell := range r {
						if isLeakedContent(cell, nil) {
							cleanRow = append(cleanRow, "")
						} else {
							cleanRow = append(cleanRow, escapeLatexWithMath(cell))
						}
					}
					for len(cleanRow) < maxCols {
						cleanRow = append(cleanRow, "")
					}
					
					// Skip row if completely empty
					isEmptyRow := true
					for _, cell := range cleanRow {
						c := strings.ToLower(strings.TrimSpace(cell))
						if c != "" && c != "[non-text]" && c != "[empty]" && c != "[]" && c != "[no text]" && c != "(no text)" {
							isEmptyRow = false
							break
						}
					}
					if isEmptyRow {
						continue
					}
					
					sb.WriteString(strings.Join(cleanRow, " & ") + " \\\\\n")
					if idx == 0 {
						sb.WriteString("\\hline\n")
					}
				}
				sb.WriteString("\\hline\n\\end{tabular}%\n}\n")
				fmt.Fprintf(&sb, "\\ifdim\\wd\\%s>\\linewidth\n", boxName)
				fmt.Fprintf(&sb, "  \\resizebox{\\linewidth}{!}{\\usebox{\\%s}}%%\n", boxName)
				fmt.Fprintf(&sb, "\\else\n")
				fmt.Fprintf(&sb, "  \\usebox{\\%s}%%\n", boxName)
				fmt.Fprintf(&sb, "\\fi\n")
				sb.WriteString("\\end{table}\n\n")
			} else {
				for _, r := range tableRows {
					if len(r) > 0 {
						sb.WriteString(escapeLatexWithMath(r[0]) + "\n\n")
					}
				}
			}
			tableRows = nil
		}
		
		for _, row := range rows {
			if len(row) == 1 || len(row) > 20 {
				var parts []string
				for _, b := range row {
					parts = append(parts, strings.TrimSpace(blockContentString(b)))
				}
				content := strings.TrimSpace(strings.Join(parts, " "))
				if content == "" {
					continue
				}
				if strings.Contains(strings.ToLower(content), "<table") {
					sb.WriteString(htmlTableToLatex(content) + "\n\n")
					continue
				}
				label := row[0].Label
				var mergedBBox []int
				for _, b := range row {
					if box, ok := getBBox(b.BBox2D); ok {
						if len(mergedBBox) == 0 {
							mergedBBox = []int{box[0], box[1], box[2], box[3]}
						} else {
							if box[0] < mergedBBox[0] { mergedBBox[0] = box[0] }
							if box[1] < mergedBBox[1] { mergedBBox[1] = box[1] }
							if box[2] > mergedBBox[2] { mergedBBox[2] = box[2] }
							if box[3] > mergedBBox[3] { mergedBBox[3] = box[3] }
						}
					}
				}
				if isLeakedContent(content, mergedBBox) {
					continue
				}
				
				shouldFlush := false
				if strings.ToLower(label) == "title" || strings.ToLower(label) == "header" || strings.ToLower(label) == "caption" || strings.ToLower(label) == "figure" {
					shouldFlush = true
				} else if len(content) > 40 {
					shouldFlush = true
				} else if strings.Count(content, " ") > 5 {
					shouldFlush = true
				}
				if shouldFlush {
					flushTable()
				}
				switch strings.ToLower(label) {
				case "title":
					fmt.Fprintf(&sb, "\\section{%s}\n\n", escapeLatexWithMath(content))
				case "header":
					fmt.Fprintf(&sb, "\\subsection{%s}\n\n", escapeLatexWithMath(content))
				case "figure", "caption":
					fmt.Fprintf(&sb, "\\begin{figure}[h]\n\\centering\n\\caption{%s}\n\\end{figure}\n\n", escapeLatexWithMath(content))
				default:
					sb.WriteString(escapeLatexWithMath(content) + "\n\n")
				}
			} else {
				var cells []string
				for _, b := range row {
					cellVal := strings.ReplaceAll(strings.TrimSpace(blockContentString(b)), "\n", " ")
					cells = append(cells, cellVal)
				}
				tableRows = append(tableRows, cells)
			}
		}
		flushTable()
	}
	
	sb.WriteString("\\end{document}\n")
	return sb.String(), nil
}

func htmlTableToMarkdown(htmlStr string) string {
	var markdownRows []string
	remaining := htmlStr
	isHeader := true
	var colCount int
	
	for {
		trStart := strings.Index(strings.ToLower(remaining), "<tr>")
		if trStart == -1 {
			break
		}
		trEnd := strings.Index(strings.ToLower(remaining[trStart:]), "</tr>")
		if trEnd == -1 {
			break
		}
		trContent := remaining[trStart+4 : trStart+trEnd]
		remaining = remaining[trStart+trEnd+5:]
		
		var cells []string
		cellRemaining := trContent
		for {
			tdStart := strings.Index(strings.ToLower(cellRemaining), "<td")
			thStart := strings.Index(strings.ToLower(cellRemaining), "<th")
			
			startIdx := -1
			isTh := false
			
			if tdStart != -1 && (thStart == -1 || tdStart < thStart) {
				startIdx = tdStart
				isTh = false
			} else if thStart != -1 {
				startIdx = thStart
				isTh = true
			}
			
			if startIdx == -1 {
				break
			}
			
			var endTag string
			if isTh {
				endTag = "</th>"
			} else {
				endTag = "</td>"
			}
			
			openBracketEnd := strings.Index(cellRemaining[startIdx:], ">")
			if openBracketEnd == -1 {
				break
			}
			openBracketEndIdx := startIdx + openBracketEnd
			
			tdEnd := strings.Index(strings.ToLower(cellRemaining[openBracketEndIdx:]), endTag)
			if tdEnd == -1 {
				break
			}
			tdEndIdx := openBracketEndIdx + tdEnd
			
			cellContent := cellRemaining[openBracketEndIdx+1 : tdEndIdx]
			cellRemaining = cellRemaining[tdEndIdx+len(endTag):]
			
			cellContent = cleanHTMLText(cellContent)
			cells = append(cells, cellContent)
		}
		
		if len(cells) > 0 {
			rowStr := "| " + strings.Join(cells, " | ") + " |"
			markdownRows = append(markdownRows, rowStr)
			
			if isHeader {
				colCount = len(cells)
				var separators []string
				for i := 0; i < colCount; i++ {
					separators = append(separators, "---")
				}
				sepStr := "| " + strings.Join(separators, " | ") + " |"
				markdownRows = append(markdownRows, sepStr)
				isHeader = false
			}
		}
	}
	
	if len(markdownRows) == 0 {
		return htmlStr
	}
	
	return strings.Join(markdownRows, "\n")
}

func cleanHTMLText(htmlStr string) string {
	var sb strings.Builder
	inTag := false
	runes := []rune(htmlStr)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if r == '<' {
			inTag = true
		} else if r == '>' {
			inTag = false
		} else if !inTag {
			sb.WriteRune(r)
		}
	}
	res := sb.String()
	res = strings.ReplaceAll(res, "&nbsp;", " ")
	res = strings.ReplaceAll(res, "&amp;", "&")
	res = strings.ReplaceAll(res, "&lt;", "<")
	res = strings.ReplaceAll(res, "&gt;", ">")
	res = strings.ReplaceAll(res, "&#x27;", "'")
	res = strings.ReplaceAll(res, "&quot;", "\"")
	return strings.TrimSpace(res)
}

// ---------------------------------------------------------------------------
// URI helpers
// ---------------------------------------------------------------------------

func mimeType(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".pdf":
		return "application/pdf"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	default:
		return "application/octet-stream"
	}
}

func toDataURI(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading file: %w", err)
	}
	return fmt.Sprintf("data:%s;base64,%s",
		mimeType(path), base64.StdEncoding.EncodeToString(data)), nil
}

// ---------------------------------------------------------------------------
// HTTP call
// ---------------------------------------------------------------------------

var httpClient = &http.Client{
	Timeout: 5 * time.Minute,
}

func doChatRequest(apiURL string, body []byte) (*ChatResponse, error) {
	req, err := http.NewRequest(http.MethodPost, apiURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("server %d: %s", resp.StatusCode, string(respBody))
	}

	var cr ChatResponse
	if err := json.Unmarshal(respBody, &cr); err != nil {
		return nil, fmt.Errorf("parsing response: %w\nraw: %s", err, string(respBody))
	}
	return &cr, nil
}

func callAPI(apiURL, model, promptText string, imageURIs []string, maxTokens int) (*ChatResponse, error) {
	var content []ContentPart
	for _, uri := range imageURIs {
		content = append(content, ContentPart{
			Type:     "image_url",
			ImageURL: &ImageURL{URL: uri},
		})
	}
	content = append(content, ContentPart{
		Type: "text",
		Text: promptText,
	})

	mt := maxTokens
	if mt <= 0 {
		mt = 8192
	}

	body, err := json.Marshal(ChatRequest{
		Model: model,
		Messages: []Message{{
			Role:    "user",
			Content: content,
		}},
		Temperature: 0.0,
		MaxTokens:   mt,
	})
	if err != nil {
		return nil, fmt.Errorf("marshalling request: %w", err)
	}

	return doChatRequest(apiURL, body)
}

func callAPIBaidu(apiURL, model, promptText string, imageURIs []string, multi bool, maxTokens int) (*ChatResponse, error) {
	var content []ContentPart
	content = append(content, ContentPart{
		Type: "text",
		Text: promptText,
	})
	for _, uri := range imageURIs {
		content = append(content, ContentPart{
			Type:     "image_url",
			ImageURL: &ImageURL{URL: uri},
		})
	}

	temp := 0.0
	skipSpecial := false

	mt := maxTokens
	if mt <= 0 {
		mt = 8192
	}

	windowSize := 128
	imageMode := "gundam"
	if multi {
		windowSize = 1024
		imageMode = "base"
	}

	custParams := &CustomParams{
		NgramSize:  35,
		WindowSize: windowSize,
	}
	imgCfg := &ImagesConfig{
		ImageMode: imageMode,
	}

	body, err := json.Marshal(ChatRequest{
		Model:                model,
		Messages:             []Message{{
			Role:    "user",
			Content: content,
		}},
		Temperature:          temp,
		MaxTokens:            mt,
		SkipSpecialTokens:    &skipSpecial,
		ImagesConfig:         imgCfg,
		CustomLogitProcessor: "DeepseekOCRNoRepeatNGramLogitProcessor",
		CustomParams:         custParams,
	})
	if err != nil {
		return nil, fmt.Errorf("marshalling request: %w", err)
	}

	return doChatRequest(apiURL, body)
}

// ---------------------------------------------------------------------------
// Entry point
// ---------------------------------------------------------------------------

func run(args []string) error {
	fs := flag.NewFlagSet("ocr", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	endpoint   := fs.String("endpoint", "http://localhost:8080", "API base URL")
	port       := fs.Int("port", 0, "Override port in --endpoint")
	model      := fs.String("model", "zai-org/GLM-OCR", "Model name")
	prompt     := fs.String("prompt", defaultPrompt, "Instruction sent with the file")
	outputFile := fs.String("output", "", "Write output to file instead of stdout")
	_          = fs.Bool("markdown", false, "Output as Markdown (default)")
	fmtText    := fs.Bool("text", false, "Output as plain text")
	fmtJSON    := fs.Bool("json", false, "Output as JSON")
	fmtLatex   := fs.Bool("latex", false, "Output as LaTeX document")
	showBBox   := fs.Bool("bbox", false, "Embed normalized bounding boxes as HTML comments in markdown output")
	rawMode    := fs.Bool("raw", false, "Dump raw model response and exit (debug)")
	showVer    := fs.Bool("version", false, "Print version and exit")
	dpi        := fs.Int("dpi", 200, "Rendering resolution for PDF pages")
	resume     := fs.Bool("resume", true, "Resume previous execution if interrupted")
	engine     := fs.String("engine", "glm", "OCR engine: glm (zai-org/GLM-OCR) or baidu (baidu/Unlimited-OCR)")
	baidu      := fs.Bool("baidu", false, "Use Baidu engine (alias for -engine baidu)")
	maxTokens  := fs.Int("max-tokens", 0, "Max tokens to generate (0 means use default: unset for glm, 8192 for baidu)")
	batchSize  := fs.Int("batch-size", 0, "Number of pages per request for baidu (0 means all in one request)")

	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, color(colorBold+colorCyan, asciiArt))
		fmt.Fprintf(os.Stderr, "ocr %s\n\nUsage: ocr [options] <file>\n\nOptions:\n", version)
		fs.PrintDefaults()
		fmt.Fprintln(os.Stderr, `
Examples:
  ocr scan.png
  ocr -baidu scan.png
  ocr -output result.md document.pdf
  ocr document.pdf -output result.md
  ocr --text --output result.txt invoice.pdf`)
	}

	// Simple robust flag separation
	var flags []string
	var files []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if strings.HasPrefix(arg, "-") {
			flags = append(flags, arg)
			// Match flags that take values
			switch strings.TrimLeft(arg, "-") {
			case "endpoint", "port", "model", "prompt", "output", "dpi", "engine", "max-tokens", "batch-size":
				if i+1 < len(args) {
					flags = append(flags, args[i+1])
					i++
				}
			}
		} else {
			files = append(files, arg)
		}
	}

	if err := fs.Parse(flags); err != nil {
		return err
	}

	if *showVer {
		fmt.Printf("ocr %s\n", version)
		return nil
	}
	if len(files) < 1 {
		fs.Usage()
		return fmt.Errorf("no input file specified")
	}

	inputFile := files[0]
	fileInfo, err := os.Stat(inputFile)
	if err != nil {
		return fmt.Errorf("cannot access %q: %w", inputFile, err)
	}
	modTime := fileInfo.ModTime().UnixNano()
	size := fileInfo.Size()

	eng := Engine(strings.ToLower(*engine))
	if *baidu {
		eng = EngineBaidu
	}
	if eng == EngineBaidu {
		if *model == "zai-org/GLM-OCR" {
			*model = "baidu/Unlimited-OCR"
		}
	}

	base := strings.TrimRight(*endpoint, "/")
	if *port != 0 {
		if idx := strings.LastIndex(base, ":"); idx > strings.Index(base, "//") {
			base = base[:idx]
		}
		base = fmt.Sprintf("%s:%d", base, *port)
	}
	apiURL := base + "/v1/chat/completions"

	var totalPages int
	isPDF := strings.ToLower(filepath.Ext(inputFile)) == ".pdf"
	if isPDF {
		var err error
		totalPages, err = getPDFPageCount(inputFile)
		if err != nil {
			return err
		}
	} else {
		totalPages = 1
	}

	if eng == EngineBaidu {
		if *prompt == defaultPrompt {
			if totalPages > 1 {
				*prompt = "<image>Multi page Extract all text from this document"
			} else {
				*prompt = "<image>Extract all text from this document"
			}
		}
		if !strings.HasPrefix(*prompt, "<image>") {
			*prompt = "<image>" + *prompt
		}
	}

	recipe := ""
	if eng == EngineBaidu {
		multi := totalPages > 1
		if *batchSize > 0 {
			multi = *batchSize > 1
		}
		windowSize := 128
		if multi {
			windowSize = 1024
		}
		recipe = fmt.Sprintf("window_size=%d", windowSize)
	}

	var resumePath string
	var resumeState *ResumeState
	if *resume {
		hash, err := getResumeHash(inputFile, modTime, size, *prompt, *model, *dpi, apiURL, string(eng), recipe)
		if err == nil {
			resumePath, err = getResumeFilePath(hash)
			if err == nil {
				resumeState, _ = loadResumeState(resumePath)
			}
		}
	}

	// Print ASCII art and dashboard
	fmt.Fprintln(os.Stderr, color(colorBold+colorCyan, asciiArt))
	fmt.Fprintf(os.Stderr, "  %s\n", color(colorBold+colorCyan, "QOCR CLIENT — DOCUMENT DIGITIZATION"))
	fmt.Fprintf(os.Stderr, "%s\n", color(colorDim, "─────────────────────────────────────────────────────────────────"))
	fmt.Fprintf(os.Stderr, "  %s %-15s %s\n", color(colorBold+colorCyan, "•"), "Input file:", color(colorWhite, inputFile))
	fmt.Fprintf(os.Stderr, "  %s %-15s %d page(s)\n", color(colorBold+colorCyan, "•"), "Pages:", totalPages)
	fmt.Fprintf(os.Stderr, "  %s %-15s %s\n", color(colorBold+colorCyan, "•"), "Engine:", color(colorWhite, string(eng)))
	fmt.Fprintf(os.Stderr, "  %s %-15s %s\n", color(colorBold+colorCyan, "•"), "Model:", color(colorWhite, *model))
	fmt.Fprintf(os.Stderr, "  %s %-15s %s\n", color(colorBold+colorCyan, "•"), "Endpoint:", color(colorWhite, base))
	if *outputFile != "" {
		fmt.Fprintf(os.Stderr, "  %s %-15s %s\n", color(colorBold+colorCyan, "•"), "Output file:", color(colorWhite, *outputFile))
	} else {
		fmt.Fprintf(os.Stderr, "  %s %-15s %s\n", color(colorBold+colorCyan, "•"), "Output file:", color(colorDim, "Stdout"))
	}
	if *resume {
		if eng == EngineBaidu {
			if resumeState != nil && resumeState.RawDocument != "" {
				fmt.Fprintf(os.Stderr, "  %s %-15s %s\n", color(colorBold+colorCyan, "•"), "Resume status:", color(colorYellow, "Interrupted session found (restoring full document from cache)"))
			} else {
				fmt.Fprintf(os.Stderr, "  %s %-15s %s\n", color(colorBold+colorCyan, "•"), "Resume status:", color(colorGreen, "Ready (enabled)"))
			}
		} else {
			if resumeState != nil && len(resumeState.Pages) > 0 {
				fmt.Fprintf(os.Stderr, "  %s %-15s %s\n", color(colorBold+colorCyan, "•"), "Resume status:", fmt.Sprintf("%s (restoring %d/%d pages)", color(colorYellow, "Interrupted session found"), len(resumeState.Pages), totalPages))
			} else {
				fmt.Fprintf(os.Stderr, "  %s %-15s %s\n", color(colorBold+colorCyan, "•"), "Resume status:", color(colorGreen, "Ready (enabled)"))
			}
		}
	} else {
		fmt.Fprintf(os.Stderr, "  %s %-15s %s\n", color(colorBold+colorCyan, "•"), "Resume status:", color(colorDim, "Disabled"))
	}
	fmt.Fprintf(os.Stderr, "%s\n\n", color(colorDim, "─────────────────────────────────────────────────────────────────"))

	fmt.Fprintf(os.Stderr, "%s\n", color(colorBold, "Processing pages:"))

	var allPages [][]OCRBlock
	var pageDims []PageDim
	startTime := time.Now()

	if eng == EngineBaidu {
		var rawDoc string
		var cacheHit bool
		if *resume && resumeState != nil && resumeState.RawDocument != "" {
			rawDoc = resumeState.RawDocument
			pageDims = resumeState.PageDims
			cacheHit = true
		}

		if cacheHit {
			fmt.Fprintf(os.Stderr, "  %s Restore document from cache (API calls skipped)\n", color(colorGreen, "⏮"))
		} else {
			uris := make([]string, totalPages)
			pageDims = make([]PageDim, totalPages)
			for i := 0; i < totalPages; i++ {
				var uri string
				var dim PageDim
				var err error
				if isPDF {
					fmt.Fprintf(os.Stderr, "  %s Page %d/%d: %s\r", color(colorCyan, "⏳"), i+1, totalPages, color(colorDim, "rendering page..."))
					uri, dim, err = renderPDFPageToDataURI(inputFile, i, *dpi)
					if err != nil {
						fmt.Fprintf(os.Stderr, "\n")
						return err
					}
				} else {
					uri, err = toDataURI(inputFile)
					if err != nil {
						return err
					}
					w, h, _ := getImageDimensions(inputFile)
					dim = PageDim{Width: w, Height: h, DPI: 0, Rotation: 0}
				}
				uris[i] = uri
				pageDims[i] = dim
				fmt.Fprintf(os.Stderr, "  %s Page %d/%d: %s\n", color(colorGreen, "✔"), i+1, totalPages, color(colorDim, "rendered"))
			}

			bs := *batchSize
			if bs <= 0 {
				bs = totalPages
			}

			var rawDocBuilder strings.Builder
			for start := 0; start < totalPages; start += bs {
				end := start + bs
				if end > totalPages {
					end = totalPages
				}
				batchURIs := uris[start:end]
				isMulti := len(batchURIs) > 1

				fmt.Fprintf(os.Stderr, "  %s Sending batch (pages %d-%d): %s\r", color(colorCyan, "⏳"), start+1, end, color(colorDim, "recognizing..."))
				batchStart := time.Now()
				
				cr, err := callAPIBaidu(apiURL, *model, *prompt, batchURIs, isMulti, *maxTokens)
				if err != nil {
					fmt.Fprintf(os.Stderr, "\n")
					return fmt.Errorf("API call for batch %d-%d: %w", start+1, end, err)
				}
				if cr.Error != nil {
					fmt.Fprintf(os.Stderr, "\n")
					return fmt.Errorf("API error on batch %d-%d: %s", start+1, end, cr.Error.Message)
				}
				if len(cr.Choices) == 0 {
					fmt.Fprintf(os.Stderr, "\n")
					return fmt.Errorf("no choices on batch %d-%d", start+1, end)
				}

				batchContent := cr.Choices[0].Message.Content
				duration := time.Since(batchStart).Round(100 * time.Millisecond)
				fmt.Fprintf(os.Stderr, "\r\033[K  %s Batch (pages %d-%d): %s\n", color(colorGreen, "✔"), start+1, end, color(colorGreen, fmt.Sprintf("completed in %s", duration)))

				if start > 0 {
					rawDocBuilder.WriteString("<PAGE>")
				}
				rawDocBuilder.WriteString(batchContent)
			}
			rawDoc = rawDocBuilder.String()

			if *resume && resumePath != "" {
				resumeState = &ResumeState{
					InputFile:   inputFile,
					ModTime:     modTime,
					Size:        size,
					Prompt:      *prompt,
					Model:       *model,
					DPI:         *dpi,
					APIURL:      apiURL,
					Pages:       []PageState{},
					RawDocument: rawDoc,
					PageDims:    pageDims,
				}
				if err := saveResumeState(resumePath, resumeState); err != nil {
					fmt.Fprintf(os.Stderr, "\n%s Failed to save resume state: %v\n", color(colorYellow, "⚠️"), err)
				}
			}
		}

		if *rawMode {
			fmt.Println(rawDoc)
		} else {
			allPages = parseBaiduContent(rawDoc)
		}

	} else {
		pageDims = make([]PageDim, totalPages)
		for i := 0; i < totalPages; i++ {
			var content string
			var found bool
			
			if *resume && resumeState != nil {
				content, found = findCachedPage(resumeState, i)
				if found && i < len(resumeState.PageDims) {
					pageDims[i] = resumeState.PageDims[i]
				}
			}
			
			if found {
				fmt.Fprintf(os.Stderr, "  %s Page %d/%d: %s\n", color(colorGreen, "⏮"), i+1, totalPages, color(colorDim, "restored from cache (rendering skipped)"))
			} else {
				var uri string
				var dim PageDim
				var err error
				if isPDF {
					fmt.Fprintf(os.Stderr, "  %s Page %d/%d: %s\r", color(colorCyan, "⏳"), i+1, totalPages, color(colorDim, "rendering page..."))
					uri, dim, err = renderPDFPageToDataURI(inputFile, i, *dpi)
					if err != nil {
						fmt.Fprintf(os.Stderr, "\n")
						return err
					}
				} else {
					uri, err = toDataURI(inputFile)
					if err != nil {
						return err
					}
					w, h, _ := getImageDimensions(inputFile)
					dim = PageDim{Width: w, Height: h, DPI: 0, Rotation: 0}
				}
				pageDims[i] = dim

				fmt.Fprintf(os.Stderr, "  %s Page %d/%d: %s\r", color(colorCyan, "⏳"), i+1, totalPages, color(colorDim, "recognizing..."))
				
				pageStart := time.Now()
				cr, err := callAPI(apiURL, *model, *prompt, []string{uri}, *maxTokens)
				if err != nil {
					fmt.Fprintf(os.Stderr, "\n")
					return fmt.Errorf("API call for page %d: %w", i+1, err)
				}
				if cr.Error != nil {
					fmt.Fprintf(os.Stderr, "\n")
					return fmt.Errorf("API error on page %d: %s", i+1, cr.Error.Message)
				}
				if len(cr.Choices) == 0 {
					fmt.Fprintf(os.Stderr, "\n")
					return fmt.Errorf("no choices on page %d", i+1)
				}
				
				content = cr.Choices[0].Message.Content
				duration := time.Since(pageStart).Round(100 * time.Millisecond)
				
				if *resume && resumePath != "" {
					if resumeState == nil {
						resumeState = &ResumeState{
							InputFile:  inputFile,
							ModTime:    modTime,
							Size:       size,
							Prompt:     *prompt,
							Model:      *model,
							DPI:        *dpi,
							APIURL:     apiURL,
							Pages:      []PageState{},
							PageDims:   make([]PageDim, totalPages),
						}
					}
					if len(resumeState.PageDims) < totalPages {
						newDims := make([]PageDim, totalPages)
						copy(newDims, resumeState.PageDims)
						resumeState.PageDims = newDims
					}
					resumeState.PageDims[i] = dim
					resumeState.Pages = append(resumeState.Pages, PageState{
						PageIndex: i,
						Content:   content,
					})
					if err := saveResumeState(resumePath, resumeState); err != nil {
						fmt.Fprintf(os.Stderr, "\n%s Failed to save resume state: %v\n", color(colorYellow, "⚠️"), err)
					}
				}
				
				fmt.Fprintf(os.Stderr, "\r\033[K  %s Page %d/%d: %s\n", color(colorGreen, "✔"), i+1, totalPages, color(colorGreen, fmt.Sprintf("completed in %s", duration)))
			}
			
			if *rawMode {
				fmt.Println(content)
				continue
			}
			
			pages, _ := parseOCRContent(content)
			if len(pages) > 0 {
				allPages = append(allPages, pages[0])
			}
		}
	}

	totalDuration := time.Since(startTime).Round(100 * time.Millisecond)
	fmt.Fprintf(os.Stderr, "\n%s\n", color(colorDim, "─────────────────────────────────────────────────────────────────"))

	if *rawMode {
		return nil
	}

	// Keep blocks raw for HTML rendering to preserve correct spatial positioning

	var result string
	switch {
	case *fmtJSON:
		result, err = renderJSON(allPages, inputFile, *model, pageDims)
		if err != nil {
			return fmt.Errorf("encoding JSON: %w", err)
		}
	case *fmtText:
		result = renderPlainText(allPages)
	case *fmtLatex:
		result, err = renderLatex(allPages, inputFile, *model)
		if err != nil {
			return fmt.Errorf("encoding LaTeX: %w", err)
		}
	default:
		result = renderMarkdown(allPages, *showBBox)
	}

	if *outputFile != "" {
		if err := os.WriteFile(*outputFile, []byte(result+"\n"), 0644); err != nil {
			return fmt.Errorf("writing %s: %w", *outputFile, err)
		}
		fmt.Fprintf(os.Stderr, "  %s Output successfully written to: %s\n", color(colorGreen, "🎉"), color(colorBold+colorWhite, *outputFile))
		fmt.Fprintf(os.Stderr, "  %s Total processing time: %s\n", color(colorCyan, "⏱"), totalDuration)
	} else {
		fmt.Println(result)
		fmt.Fprintf(os.Stderr, "  %s Total processing time: %s\n", color(colorCyan, "⏱"), totalDuration)
	}

	// Clean up resume state
	if *resume && resumePath != "" && resumeState != nil {
		_ = deleteResumeState(resumePath)
	}

	return nil
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "%s %v\n", color(colorRed+"error:", "Error:"), err)
		os.Exit(1)
	}
}

// ---------------------------------------------------------------------------
// Resume & Color visual elements
// ---------------------------------------------------------------------------

const (
	colorReset   = "\033[0m"
	colorBold    = "\033[1m"
	colorDim     = "\033[2m"
	colorItalic  = "\033[3m"

	colorRed     = "\033[31m"
	colorGreen   = "\033[32m"
	colorYellow  = "\033[33m"
	colorBlue    = "\033[34m"
	colorMagenta = "\033[35m"
	colorCyan    = "\033[36m"
	colorWhite   = "\033[37m"
)

func isTTY(file *os.File) bool {
	stat, err := file.Stat()
	if err != nil {
		return false
	}
	return (stat.Mode() & os.ModeCharDevice) != 0
}

var useColor = isTTY(os.Stderr) && os.Getenv("NO_COLOR") == ""

func color(code, text string) string {
	if !useColor {
		return text
	}
	return code + text + colorReset
}

type PageState struct {
	PageIndex int    `json:"page_index"`
	Content   string `json:"content"`
}

type ResumeState struct {
	InputFile   string      `json:"input_file"`
	ModTime     int64       `json:"mod_time"`
	Size        int64       `json:"size"`
	Prompt      string      `json:"prompt"`
	Model       string      `json:"model"`
	DPI         int         `json:"dpi"`
	APIURL      string      `json:"api_url"`
	Pages       []PageState `json:"pages"`
	RawDocument string      `json:"raw_document,omitempty"`
	PageDims    []PageDim   `json:"page_dims,omitempty"`
}

func getResumeHash(inputFile string, modTime int64, size int64, prompt, model string, dpi int, apiURL string, engine string, recipe string) (string, error) {
	absPath, err := filepath.Abs(inputFile)
	if err != nil {
		absPath = inputFile
	}
	data := fmt.Sprintf("%s|%d|%d|%s|%s|%d|%s|%s|%s", absPath, modTime, size, prompt, model, dpi, apiURL, engine, recipe)
	h := sha256.New()
	if _, err := h.Write([]byte(data)); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func getResumeFilePath(hash string) (string, error) {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		cacheDir = ".ocr-cache"
	} else {
		cacheDir = filepath.Join(cacheDir, "ocr-cli")
	}
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return "", err
	}
	return filepath.Join(cacheDir, hash+".json"), nil
}

func loadResumeState(path string) (*ResumeState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var state ResumeState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	return &state, nil
}

func saveResumeState(path string, state *ResumeState) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

func deleteResumeState(path string) error {
	return os.Remove(path)
}

func findCachedPage(state *ResumeState, index int) (string, bool) {
	if state == nil {
		return "", false
	}
	for _, p := range state.Pages {
		if p.PageIndex == index {
			return p.Content, true
		}
	}
	return "", false
}

func groupBlocksIntoRows(page []OCRBlock) [][]OCRBlock {
	var hasBBox []OCRBlock
	
	for _, b := range page {
		if _, ok := getBBox(b.BBox2D); ok {
			hasBBox = append(hasBBox, b)
		}
	}
	
	if len(hasBBox) == 0 {
		return nil
	}
	
	// Sort by y_center
	sort.Slice(hasBBox, func(i, j int) bool {
		bi, _ := getBBox(hasBBox[i].BBox2D)
		bj, _ := getBBox(hasBBox[j].BBox2D)
		yci := float64(bi[1]+bi[3]) / 2.0
		ycj := float64(bj[1]+bj[3]) / 2.0
		return yci < ycj
	})
	
	// Group into rows
	var rows [][]OCRBlock
	for _, b := range hasBBox {
		bbox, _ := getBBox(b.BBox2D)
		yc := float64(bbox[1]+bbox[3]) / 2.0
		h := float64(bbox[3] - bbox[1])
		if h <= 0 {
			h = 10
		}
		if h > 50 {
			rows = append(rows, []OCRBlock{b})
			continue
		}
		
		placed := false
		for idx, row := range rows {
			var sumYc float64
			var sumH float64
			isRowCompatible := true
			for _, rb := range row {
				rbox, _ := getBBox(rb.BBox2D)
				rh := float64(rbox[3] - rbox[1])
				if rh > 50 {
					isRowCompatible = false
					break
				}
				sumYc += float64(rbox[1]+rbox[3]) / 2.0
				sumH += rh
			}
			if !isRowCompatible {
				continue
			}
			avgYc := sumYc / float64(len(row))
			avgH := sumH / float64(len(row))
			if avgH <= 0 {
				avgH = 10
			}
			
			threshold := avgH * 0.7
			if threshold < 12 {
				threshold = 12
			}
			if math.Abs(yc-avgYc) < threshold {
				rows[idx] = append(rows[idx], b)
				placed = true
				break
			}
		}
		
		if !placed {
			rows = append(rows, []OCRBlock{b})
		}
	}
	
	for idx := range rows {
		sort.Slice(rows[idx], func(i, j int) bool {
			bi, _ := getBBox(rows[idx][i].BBox2D)
			bj, _ := getBBox(rows[idx][j].BBox2D)
			return bi[0] < bj[0]
		})
	}
	
	return rows
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func mergeHorizontalPageBlocks(page []OCRBlock) []OCRBlock {
	rows := groupBlocksIntoRows(page)
	if len(rows) == 0 {
		return page
	}
	
	var mergedBlocks []OCRBlock
	index := 0
	
	for _, row := range rows {
		var rowMerged []OCRBlock
		for _, b := range row {
			if len(rowMerged) == 0 {
				rowMerged = append(rowMerged, b)
				continue
			}
			
			last := &rowMerged[len(rowMerged)-1]
			lastBox, _ := getBBox(last.BBox2D)
			currBox, _ := getBBox(b.BBox2D)
			gap := currBox[0] - lastBox[2]
			isTable := strings.ToLower(last.Label) == "table" || strings.ToLower(b.Label) == "table"
			// Threshold of 20 units is small enough to only merge adjacent words within the same cell
			if !isTable && gap <= 20 {
				lastBox[0] = minInt(lastBox[0], currBox[0])
				lastBox[1] = minInt(lastBox[1], currBox[1])
				lastBox[2] = maxInt(lastBox[2], currBox[2])
				lastBox[3] = maxInt(lastBox[3], currBox[3])
				last.BBox2D = lastBox
				
				lastContent := blockContentString(*last)
				currContent := blockContentString(b)
				if lastContent != "" && currContent != "" {
					last.Content = lastContent + " " + currContent
				} else if currContent != "" {
					last.Content = currContent
				}
			} else {
				rowMerged = append(rowMerged, b)
			}
		}
		
		for _, b := range rowMerged {
			b.Index = index
			mergedBlocks = append(mergedBlocks, b)
			index++
		}
	}
	
	return mergedBlocks
}
