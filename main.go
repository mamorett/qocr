package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"html"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const defaultPrompt = "Extract all text from this document"

const asciiArt = `
  ____ _     __  __          ___   ____ ____  
 / ___| |   |  \/  |        / _ \ / ___|  _ \ 
| |  _| |   | |\/| | _____ | | | | |   | |_) |
| |_| | |___| |  | ||_____|| |_| | |___|  _ < 
 \____|_____|_|  |_|        \___/ \____|_| \_\`

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

type VLLMXargs struct {
	NgramSize  int `json:"ngram_size"`
	WindowSize int `json:"window_size"`
}

type ImagesConfig struct {
	ImageMode string `json:"image_mode,omitempty"`
}

type ChatRequest struct {
	Model             string        `json:"model"`
	Messages          []Message     `json:"messages"`
	Temperature       float64       `json:"temperature,omitempty"`
	MaxTokens         int           `json:"max_tokens,omitempty"`
	SkipSpecialTokens *bool         `json:"skip_special_tokens,omitempty"`
	VLLMXargs         *VLLMXargs    `json:"vllm_xargs,omitempty"`
	ImagesConfig      *ImagesConfig `json:"images_config,omitempty"`
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
// GLM-OCR structured response
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
		blockContent = strings.ReplaceAll(blockContent, "<|ref|>", "")
		blockContent = strings.ReplaceAll(blockContent, "<|/ref|>", "")
		blockContent = strings.ReplaceAll(blockContent, "</|ref>", "")
		blockContent = strings.ReplaceAll(blockContent, "<|/ref>", "")
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
						content = strings.ReplaceAll(content, "<|ref|>", "")
						content = strings.ReplaceAll(content, "<|/ref|>", "")
						content = strings.ReplaceAll(content, "</|ref>", "")
						content = strings.ReplaceAll(content, "<|/ref>", "")
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
		for _, b := range page {
			var content string
			switch v := b.Content.(type) {
			case string:
				content = strings.TrimSpace(v)
			case []interface{}:
				var items []string
				for _, item := range v {
					s := fmt.Sprint(item)
					if s != "" && !strings.HasPrefix(s, "- ") && !strings.HasPrefix(s, "* ") {
						items = append(items, "- "+s)
					} else {
						items = append(items, s)
					}
				}
				content = strings.Join(items, "\n")
			default:
				content = fmt.Sprint(b.Content)
			}

			if content == "" {
				continue
			}

			if strings.Contains(strings.ToLower(content), "<table") {
				content = htmlTableToMarkdown(content)
			}

			if showBBox {
				if bbox, ok := getBBox(b.BBox2D); ok {
					fmt.Fprintf(&sb, "<!-- bbox: %d,%d,%d,%d -->\n", bbox[0], bbox[1], bbox[2], bbox[3])
				}
			}

			switch strings.ToLower(b.Label) {
			case "title":
				fmt.Fprintf(&sb, "## %s\n\n", content)
			case "figure", "caption":
				fmt.Fprintf(&sb, "*%s*\n\n", content)
			default:
				sb.WriteString(content)
				sb.WriteString("\n\n")
			}
		}
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

func renderHTML(pages [][]OCRBlock, source, model string, pageDims []PageDim) (string, error) {
	var sb strings.Builder
	sb.WriteString("<!doctype html>\n<html>\n<head>\n<meta charset=\"utf-8\">\n")
	fmt.Fprintf(&sb, "<title>%s</title>\n", html.EscapeString(source))
	sb.WriteString(`<style>
body {
  background-color: #f0f2f5;
  font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif;
  margin: 0;
  padding: 24px;
  color: #333;
}
.ocr-page {
  position: relative;
  background: #fff;
  margin: 0 auto 24px;
  box-shadow: 0 4px 6px rgba(0,0,0,0.1);
  border: 1px solid #ccc;
  box-sizing: border-box;
  overflow: hidden;
}
.ocr-page-header {
  text-align: center;
  font-size: 14px;
  color: #666;
  margin-bottom: 8px;
}
.ocr-block {
  box-sizing: border-box;
  padding: 2px 4px;
  overflow: hidden;
  white-space: pre-wrap;
  word-break: break-word;
}
.ocr-title {
  font-weight: bold;
  font-size: 1.2em;
}
.ocr-header {
  font-weight: bold;
  font-size: 1.1em;
}
.ocr-section {
  font-weight: bold;
}
.ocr-image, .ocr-figure, .ocr-caption {
  font-style: italic;
  color: #666;
}
.ocr-table {
  font-family: monospace;
  background-color: #f8f9fa;
  border: 1px solid #dee2e6;
  padding: 0;
}
.ocr-table table {
  width: 100%;
  height: 100%;
  border-collapse: collapse;
}
.ocr-table th, .ocr-table td {
  border: 1px solid #dee2e6;
  padding: 4px 8px;
  font-size: 11px;
}
.ocr-table th {
  background-color: #f1f3f5;
  font-weight: bold;
}
.ocr-linear-page {
  background: #fff;
  max-width: 800px;
  margin: 0 auto 24px;
  padding: 24px;
  box-shadow: 0 4px 6px rgba(0,0,0,0.1);
  border: 1px solid #ccc;
  box-sizing: border-box;
}
.ocr-linear-block {
  margin-bottom: 12px;
  white-space: pre-wrap;
}
.ocr-linear-block table {
  width: 100%;
  border-collapse: collapse;
  margin-top: 8px;
}
.ocr-linear-block th, .ocr-linear-block td {
  border: 1px solid #dee2e6;
  padding: 6px 12px;
}
.ocr-linear-block th {
  background-color: #f1f3f5;
}
</style>
</head>
<body>
`)

	for pi, page := range pages {
		var pd *PageDim
		if pi < len(pageDims) {
			pd = &pageDims[pi]
		}

		useAbsolute := false
		var w, h int
		if pd != nil && pd.Width > 0 && pd.Height > 0 {
			for _, b := range page {
				if _, ok := getBBox(b.BBox2D); ok {
					useAbsolute = true
					break
				}
			}
			w = pd.Width
			h = pd.Height
		}

		fmt.Fprintf(&sb, "<div class=\"ocr-page-header\">Page %d</div>\n", pi+1)

		if useAbsolute {
			fmt.Fprintf(&sb, "<div class=\"ocr-page\" style=\"width:%dpx; height:%dpx;\">\n", w, h)
			for _, b := range page {
				contentStr := blockContentString(b)
				labelClass := "ocr-" + sanitizeLabel(b.Label)
				bbox, ok := getBBox(b.BBox2D)
				
				var contentVal string
				if strings.ToLower(b.Label) == "table" || strings.Contains(strings.ToLower(contentStr), "<table") {
					contentVal = contentStr
				} else {
					contentVal = html.EscapeString(contentStr)
				}

				if !ok {
					fmt.Fprintf(&sb, "  <div class=\"ocr-block %s\" style=\"position:absolute; left:0px; top:0px;\">%s</div>\n", labelClass, contentVal)
					continue
				}
				x1, y1, x2, y2 := bbox[0], bbox[1], bbox[2], bbox[3]
				left := float64(x1) / 999.0 * float64(w)
				top := float64(y1) / 999.0 * float64(h)
				width := float64(x2-x1) / 999.0 * float64(w)
				height := float64(y2-y1) / 999.0 * float64(h)
				if left < 0 { left = 0 }
				if top < 0 { top = 0 }
				if width < 0 { width = 0 }
				if height < 0 { height = 0 }

				fmt.Fprintf(&sb, "  <div class=\"ocr-block %s\" style=\"position:absolute; left:%.1fpx; top:%.1fpx; width:%.1fpx; height:%.1fpx;\">%s</div>\n",
					labelClass, left, top, width, height, contentVal)
			}
			sb.WriteString("</div>\n")
		} else {
			sb.WriteString("<div class=\"ocr-linear-page\">\n")
			for _, b := range page {
				contentStr := blockContentString(b)
				labelClass := "ocr-" + sanitizeLabel(b.Label)
				var contentVal string
				if strings.ToLower(b.Label) == "table" || strings.Contains(strings.ToLower(contentStr), "<table") {
					contentVal = contentStr
				} else {
					contentVal = html.EscapeString(contentStr)
				}
				fmt.Fprintf(&sb, "  <div class=\"ocr-linear-block %s\">%s</div>\n", labelClass, contentVal)
			}
			sb.WriteString("</div>\n")
		}
	}

	sb.WriteString("</body>\n</html>\n")
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

func callAPI(apiURL, model, promptText string, imageURIs []string) (*ChatResponse, error) {
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

	body, err := json.Marshal(ChatRequest{
		Model: model,
		Messages: []Message{{
			Role:    "user",
			Content: content,
		}},
		Temperature: 0.1,
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

	vllmX := &VLLMXargs{
		NgramSize:  35,
		WindowSize: windowSize,
	}
	imgCfg := &ImagesConfig{
		ImageMode: imageMode,
	}

	body, err := json.Marshal(ChatRequest{
		Model:             model,
		Messages:          []Message{{
			Role:    "user",
			Content: content,
		}},
		Temperature:       temp,
		MaxTokens:         mt,
		SkipSpecialTokens: &skipSpecial,
		VLLMXargs:         vllmX,
		ImagesConfig:      imgCfg,
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
	fmtHTML    := fs.Bool("html", false, "Output as HTML with blocks positioned by bounding box (2D layout reconstruction)")
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

	if eng == EngineBaidu && *prompt == defaultPrompt {
		if totalPages > 1 {
			*prompt = "<image>Multi page parsing."
		} else {
			*prompt = "<image>document parsing."
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
	fmt.Fprintf(os.Stderr, "  %s\n", color(colorBold+colorCyan, "GLM-OCR CLIENT — DOCUMENT DIGITIZATION"))
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
				cr, err := callAPI(apiURL, *model, *prompt, []string{uri})
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

	var result string
	switch {
	case *fmtJSON:
		result, err = renderJSON(allPages, inputFile, *model, pageDims)
		if err != nil {
			return fmt.Errorf("encoding JSON: %w", err)
		}
	case *fmtText:
		result = renderPlainText(allPages)
	case *fmtHTML:
		result, err = renderHTML(allPages, inputFile, *model, pageDims)
		if err != nil {
			return fmt.Errorf("encoding HTML: %w", err)
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
