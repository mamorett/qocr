package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
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

type ChatRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
	Temperature float64   `json:"temperature,omitempty"`
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
// ---------------------------------------------------------------------------
// Output formatters
// ---------------------------------------------------------------------------

func renderMarkdown(pages [][]OCRBlock) string {
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
	md := renderMarkdown(pages)
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

type JSONOutput struct {
	Source string            `json:"source"`
	Model  string            `json:"model"`
	Pages  int               `json:"pages"`
	Blocks []JSONOutputBlock `json:"blocks"`
}

func renderJSON(pages [][]OCRBlock, source, model string) (string, error) {
	out := JSONOutput{
		Source: source,
		Model:  model,
		Pages:  len(pages),
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
		MaxTokens:   16384,
		Temperature: 0.1,
	})
	if err != nil {
		return nil, fmt.Errorf("marshalling request: %w", err)
	}

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
	rawMode    := fs.Bool("raw", false, "Dump raw model response and exit (debug)")
	showVer    := fs.Bool("version", false, "Print version and exit")
	dpi        := fs.Int("dpi", 200, "Rendering resolution for PDF pages")
	resume     := fs.Bool("resume", true, "Resume previous execution if interrupted")

	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, color(colorBold+colorCyan, asciiArt))
		fmt.Fprintf(os.Stderr, "ocr %s\n\nUsage: ocr [options] <file>\n\nOptions:\n", version)
		fs.PrintDefaults()
		fmt.Fprintln(os.Stderr, `
Examples:
  ocr scan.png
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
			case "endpoint", "port", "model", "prompt", "output", "dpi":
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

	var resumePath string
	var resumeState *ResumeState
	if *resume {
		hash, err := getResumeHash(inputFile, modTime, size, *prompt, *model, *dpi, apiURL)
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
	fmt.Fprintf(os.Stderr, "  %s %-15s %s\n", color(colorBold+colorCyan, "•"), "Model:", color(colorWhite, *model))
	fmt.Fprintf(os.Stderr, "  %s %-15s %s\n", color(colorBold+colorCyan, "•"), "Endpoint:", color(colorWhite, base))
	if *outputFile != "" {
		fmt.Fprintf(os.Stderr, "  %s %-15s %s\n", color(colorBold+colorCyan, "•"), "Output file:", color(colorWhite, *outputFile))
	} else {
		fmt.Fprintf(os.Stderr, "  %s %-15s %s\n", color(colorBold+colorCyan, "•"), "Output file:", color(colorDim, "Stdout"))
	}
	if *resume {
		if resumeState != nil && len(resumeState.Pages) > 0 {
			fmt.Fprintf(os.Stderr, "  %s %-15s %s\n", color(colorBold+colorCyan, "•"), "Resume status:", fmt.Sprintf("%s (restoring %d/%d pages)", color(colorYellow, "Interrupted session found"), len(resumeState.Pages), totalPages))
		} else {
			fmt.Fprintf(os.Stderr, "  %s %-15s %s\n", color(colorBold+colorCyan, "•"), "Resume status:", color(colorGreen, "Ready (enabled)"))
		}
	} else {
		fmt.Fprintf(os.Stderr, "  %s %-15s %s\n", color(colorBold+colorCyan, "•"), "Resume status:", color(colorDim, "Disabled"))
	}
	fmt.Fprintf(os.Stderr, "%s\n\n", color(colorDim, "─────────────────────────────────────────────────────────────────"))

	fmt.Fprintf(os.Stderr, "%s\n", color(colorBold, "Processing pages:"))

	var allPages [][]OCRBlock
	startTime := time.Now()
	for i := 0; i < totalPages; i++ {
		var content string
		var found bool
		
		if *resume && resumeState != nil {
			content, found = findCachedPage(resumeState, i)
		}
		
		if found {
			fmt.Fprintf(os.Stderr, "  %s Page %d/%d: %s\n", color(colorGreen, "⏮"), i+1, totalPages, color(colorDim, "restored from cache (rendering skipped)"))
		} else {
			var uri string
			var err error
			if isPDF {
				fmt.Fprintf(os.Stderr, "  %s Page %d/%d: %s\r", color(colorCyan, "⏳"), i+1, totalPages, color(colorDim, "rendering page..."))
				uri, err = renderPDFPageToDataURI(inputFile, i, *dpi)
				if err != nil {
					fmt.Fprintf(os.Stderr, "\n")
					return err
				}
			} else {
				uri, err = toDataURI(inputFile)
				if err != nil {
					return err
				}
			}

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
					}
				}
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

	totalDuration := time.Since(startTime).Round(100 * time.Millisecond)
	fmt.Fprintf(os.Stderr, "\n%s\n", color(colorDim, "─────────────────────────────────────────────────────────────────"))

	if *rawMode {
		return nil
	}

	var result string
	switch {
	case *fmtJSON:
		result, err = renderJSON(allPages, inputFile, *model)
		if err != nil {
			return fmt.Errorf("encoding JSON: %w", err)
		}
	case *fmtText:
		result = renderPlainText(allPages)
	default:
		result = renderMarkdown(allPages)
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
	InputFile string      `json:"input_file"`
	ModTime   int64       `json:"mod_time"`
	Size      int64       `json:"size"`
	Prompt    string      `json:"prompt"`
	Model     string      `json:"model"`
	DPI       int         `json:"dpi"`
	APIURL    string      `json:"api_url"`
	Pages     []PageState `json:"pages"`
}

func getResumeHash(inputFile string, modTime int64, size int64, prompt, model string, dpi int, apiURL string) (string, error) {
	absPath, err := filepath.Abs(inputFile)
	if err != nil {
		absPath = inputFile
	}
	data := fmt.Sprintf("%s|%d|%d|%s|%s|%d|%s", absPath, modTime, size, prompt, model, dpi, apiURL)
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
