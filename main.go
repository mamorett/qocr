package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const defaultPrompt = "Extract all text from this document"

func run(args []string) error {
	fs := flag.NewFlagSet("ocr", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	endpoint := fs.String("endpoint", "http://localhost:8080", "API base URL")
	port := fs.Int("port", 0, "Override port in --endpoint")
	model := fs.String("model", "baidu/Unlimited-OCR", "Model name")
	prompt := fs.String("prompt", defaultPrompt, "Instruction sent with the file")
	outputFile := fs.String("output", "", "Write output to file instead of stdout")
	fs.StringVar(outputFile, "o", "", "Write output to file instead of stdout")
	_ = fs.Bool("markdown", false, "Output as Markdown (default)")
	fmtText := fs.Bool("text", false, "Output as plain text")
	fmtJSON := fs.Bool("json", false, "Output as JSON")
	fmtLatex := fs.Bool("latex", false, "Output as LaTeX document")
	fmtHTML := fs.Bool("html", false, "Output as HTML document")
	showBBox := fs.Bool("bbox", false, "Embed normalized bounding boxes as HTML comments in markdown output")
	rawMode := fs.Bool("raw", false, "Dump raw model response and exit (debug)")
	showHelp := fs.Bool("help", false, "Show usage information")
	fs.BoolVar(showHelp, "h", false, "Show usage information")
	showVer := fs.Bool("version", false, "Print version and exit")
	fs.BoolVar(showVer, "v", false, "Print version and exit")
	dpi := fs.Int("dpi", 200, "Rendering resolution for PDF pages")
	resume := fs.Bool("resume", true, "Resume previous execution if interrupted")
	engine := fs.String("engine", "baidu", "OCR engine: baidu, glm, native, or hybrid (native text + Baidu table OCR)")
	baidu := fs.Bool("baidu", false, "Use Baidu engine (alias for -engine baidu)")
	glm := fs.Bool("glm", false, "Use GLM engine (alias for -engine glm)")
	native := fs.Bool("native", false, "Extract text directly from PDF text layer (no OCR, no AI, no network)")
	epubFlag := fs.Bool("epub", false, "Extract text from EPUB (auto-detected by extension; no OCR, no AI, no network)")
	hybrid := fs.Bool("hybrid", false, "Use native PDF text with Baidu layout/table OCR for complex regions")
	maxTokens := fs.Int("max-tokens", 0, "Max tokens to generate (0 means use default: unset for glm, 8192 for baidu)")
	batchSize := fs.Int("batch-size", 0, "Number of pages per request for baidu (0 means all in one request)")

	fs.Usage = func() {
		PrintLogoTo(os.Stderr)
		fmt.Fprintf(os.Stderr, "  %s %s\n\n", color(colorBold+colorCyan, "qocr"), color(colorDim, version))
		fmt.Fprintf(os.Stderr, "  %s\n", color(colorBold+colorYellow, "USAGE:"))
		fmt.Fprintf(os.Stderr, "    %s %s %s\n\n", color(colorGreen, "qocr"), color(colorCyan, "[options]"), color(colorWhite, "<file>"))

		type flagDoc struct {
			flag string
			desc string
		}

		type sectionDoc struct {
			title string
			flags []flagDoc
		}

		sections := []sectionDoc{
			{
				title: "ENGINE & OCR OPTIONS",
				flags: []flagDoc{
					{"-engine <name>", "OCR engine: baidu, glm, native, hybrid, or epub (default \"baidu\")"},
					{"-baidu", "Use Baidu engine (alias for -engine baidu)"},
					{"-glm", "Use GLM engine (alias for -engine glm)"},
					{"-native", "Extract text directly from PDF text layer (offline, zero AI/GPU)"},
					{"-hybrid", "Use native PDF text with Baidu table/layout OCR for complex regions"},
					{"-epub", "Extract text from an EPUB file (auto-detected; offline, zero AI/GPU)"},
					{"-model <name>", "Model name ID (default \"baidu/Unlimited-OCR\")"},
					{"-prompt <text>", "Instruction prompt sent with document"},
					{"-dpi <int>", "Rendering resolution for PDF pages (default 200)"},
				},
			},
			{
				title: "OUTPUT FORMAT & CONTROLS",
				flags: []flagDoc{
					{"-output, -o <file>", "Write output to specified file path instead of stdout"},
					{"-markdown", "Output as Markdown document (default)"},
					{"-text", "Output as plain text"},
					{"-json", "Output as structured JSON"},
					{"-latex", "Output as LaTeX document"},
					{"-html", "Output as HTML document"},
					{"-bbox", "Embed normalized bounding boxes as HTML comments"},
				},
			},
			{
				title: "SERVER & PERFORMANCE",
				flags: []flagDoc{
					{"-endpoint <url>", "Inference server API base URL (default \"http://localhost:8080\")"},
					{"-port <int>", "Override endpoint port"},
					{"-max-tokens <N>", "Max tokens to generate per page (0 = default/auto)"},
					{"-batch-size <N>", "Pages per request for Baidu engine (0 = all in one)"},
				},
			},
			{
				title: "EXECUTION & OTHER",
				flags: []flagDoc{
					{"-resume", "Resume previous interrupted execution from cache (default true)"},
					{"-raw", "Dump raw model response and exit (debug mode)"},
					{"-help, -h", "Show this usage information"},
					{"-version, -v", "Print version and exit"},
				},
			},
		}

		for _, sec := range sections {
			fmt.Fprintf(os.Stderr, "  %s\n", color(colorBold+colorYellow, sec.title+":"))
			for _, f := range sec.flags {
				fmt.Fprintf(os.Stderr, "    %s %s\n", color(colorCyan, fmt.Sprintf("%-22s", f.flag)), color(colorWhite, f.desc))
			}
			fmt.Fprintln(os.Stderr)
		}

		fmt.Fprintf(os.Stderr, "  %s\n", color(colorBold+colorYellow, "EXAMPLES:"))
		examples := []struct {
			comment string
			cmd     string
		}{
			{"Basic usage: extract text from image or PDF to stdout/markdown", "qocr scan.png"},
			{"Save Markdown output to a file", "qocr -output result.md document.pdf"},
			{"Fast offline extraction from digital PDFs (no GPU, no network required)", "qocr -native document.pdf -output result.md"},
			{"Convert an EPUB book to Markdown (no AI, no network, auto-detected)", "qocr book.epub -output book.md"},
			{"GLM-OCR engine with custom local vLLM endpoint", "qocr -glm -endpoint http://localhost:8000 scan.jpg"},
			{"Baidu Unlimited-OCR model with specific server endpoint", "qocr -engine baidu -model baidu/Unlimited-OCR -endpoint http://10.0.0.5:8000 paper.pdf"},
			{"Hybrid mode: native PDF text + Baidu table/layout OCR", "qocr -hybrid contract.pdf -output contract.md"},
			{"Export to HTML with bounding boxes embedded as comments", "qocr -html -bbox document.pdf -output result.html"},
			{"Export directly to LaTeX for academic papers", "qocr -latex -engine baidu -endpoint http://192.168.0.12:4000 paper.pdf -output paper.tex"},
			{"Structured JSON output for data pipelines", "qocr -json -output data.json invoice.pdf"},
			{"Batch processing large multi-page PDFs with token limits", "qocr -engine baidu -batch-size 5 -dpi 150 -max-tokens 4096 book.pdf -output book.md"},
		}

		for _, ex := range examples {
			fmt.Fprintf(os.Stderr, "    %s\n", color(colorDim, "# "+ex.comment))
			fmt.Fprintf(os.Stderr, "    %s %s\n\n", color(colorGreen, "$"), color(colorWhite, ex.cmd))
		}
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
			case "endpoint", "port", "model", "prompt", "output", "o", "dpi", "engine", "max-tokens", "batch-size":
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

	if *showHelp {
		fs.Usage()
		return nil
	}
	if *showVer {
		fmt.Printf("qocr %s\n", version)
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
	if *glm {
		eng = EngineGLM
	}
	if *native {
		eng = EngineNative
	}
	if *hybrid {
		eng = EngineHybrid
	}
	if *epubFlag {
		eng = EngineEPUB
	}
	if eng == EngineBaidu || eng == EngineHybrid {
		if *model == "zai-org/GLM-OCR" {
			*model = "baidu/Unlimited-OCR"
		}
	} else if eng == EngineGLM {
		if *model == "baidu/Unlimited-OCR" {
			*model = "zai-org/GLM-OCR"
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
	isEPUB := strings.ToLower(filepath.Ext(inputFile)) == ".epub"
	isPDF := strings.ToLower(filepath.Ext(inputFile)) == ".pdf"

	// Auto-detect EPUB: override engine to EngineEPUB and warn if user requested AI.
	if isEPUB {
		if eng != EngineNative && eng != EngineEPUB {
			fmt.Fprintf(os.Stderr, "  %s Input is an EPUB file — AI engine %q is not applicable. Switching to native EPUB extraction.\n", color(colorYellow, "⚠️"), string(eng))
		}
		eng = EngineEPUB
	}

	if isEPUB {
		var err error
		totalPages, err = getEPUBChapterCount(inputFile)
		if err != nil {
			return err
		}
	} else if isPDF {
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
				*prompt = "<image>Multi page parsing."
			} else {
				*prompt = "<image>document parsing."
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

	// Print logo and dashboard
	PrintLogoTo(os.Stderr)
	fmt.Fprintf(os.Stderr, "  %s\n", color(colorBold+colorCyan, "QOCR CLIENT — DOCUMENT DIGITIZATION"))
	fmt.Fprintf(os.Stderr, "%s\n", color(colorDim, "─────────────────────────────────────────────────────────────────"))
	fmt.Fprintf(os.Stderr, "  %s %-15s %s\n", color(colorBold+colorCyan, "•"), "Input file:", color(colorWhite, inputFile))
	if isEPUB {
		fmt.Fprintf(os.Stderr, "  %s %-15s %d chapter(s)\n", color(colorBold+colorCyan, "•"), "Chapters:", totalPages)
	} else {
		fmt.Fprintf(os.Stderr, "  %s %-15s %d page(s)\n", color(colorBold+colorCyan, "•"), "Pages:", totalPages)
	}
	fmt.Fprintf(os.Stderr, "  %s %-15s %s\n", color(colorBold+colorCyan, "•"), "Engine:", color(colorWhite, string(eng)))
	if eng == EngineNative || eng == EngineEPUB {
		fmt.Fprintf(os.Stderr, "  %s %-15s %s\n", color(colorBold+colorCyan, "•"), "Model:", color(colorDim, "N/A (text layer)"))
		fmt.Fprintf(os.Stderr, "  %s %-15s %s\n", color(colorBold+colorCyan, "•"), "Endpoint:", color(colorDim, "N/A (offline)"))
	} else {
		fmt.Fprintf(os.Stderr, "  %s %-15s %s\n", color(colorBold+colorCyan, "•"), "Model:", color(colorWhite, *model))
		fmt.Fprintf(os.Stderr, "  %s %-15s %s\n", color(colorBold+colorCyan, "•"), "Endpoint:", color(colorWhite, base))
	}
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

	if eng == EngineEPUB {
		// ── EPUB extraction (no OCR, no network) ─────────────────────────────
		pageDims = make([]PageDim, totalPages)
		ocrStartTime := time.Now()
		drawProgressBar(0, totalPages, ocrStartTime, "extracting chapters...")
		for i := 0; i < totalPages; i++ {
			var blocks []OCRBlock
			var found bool

			if *resume && resumeState != nil {
				var content string
				content, found = findCachedPage(resumeState, i)
				if found && i < len(resumeState.PageDims) {
					pageDims[i] = resumeState.PageDims[i]
					pages, _ := parseOCRContent(content)
					if len(pages) > 0 {
						blocks = pages[0]
					}
				}
			}

			if found {
				drawProgressBar(i+1, totalPages, ocrStartTime, "restored from cache")
			} else {
				drawProgressBar(i, totalPages, ocrStartTime, fmt.Sprintf("extracting chapter %d...", i+1))
				var dim PageDim
				var err error
				blocks, dim, err = extractEPUBChapter(inputFile, i)
				if err != nil {
					fmt.Fprintf(os.Stderr, "\n")
					return fmt.Errorf("EPUB chapter %d: %w", i+1, err)
				}
				pageDims[i] = dim
				drawProgressBar(i+1, totalPages, ocrStartTime, "")
			}

			allPages = append(allPages, blocks)
		}
		fmt.Fprintln(os.Stderr)

	} else if eng == EngineNative {
		// ── Native text-layer extraction (no OCR, no network) ────────────────
		if !isPDF {
			return fmt.Errorf("the -native flag requires a PDF input file (got %q)", inputFile)
		}
		pageDims = make([]PageDim, totalPages)
		ocrStartTime := time.Now()
		drawProgressBar(0, totalPages, ocrStartTime, "extracting text layer...")
		for i := 0; i < totalPages; i++ {
			var blocks []OCRBlock
			var dim PageDim
			var found bool

			if *resume && resumeState != nil {
				var content string
				content, found = findCachedPage(resumeState, i)
				if found && i < len(resumeState.PageDims) {
					pageDims[i] = resumeState.PageDims[i]
					if found {
						pages, _ := parseOCRContent(content)
						if len(pages) > 0 {
							blocks = pages[0]
						}
					}
				}
			}

			if found {
				drawProgressBar(i+1, totalPages, ocrStartTime, "restored from cache")
			} else {
				drawProgressBar(i, totalPages, ocrStartTime, fmt.Sprintf("extracting page %d...", i+1))
				var err error
				blocks, dim, err = extractNativePage(inputFile, i)
				if err != nil {
					fmt.Fprintf(os.Stderr, "\n")
					return fmt.Errorf("native extraction page %d: %w", i+1, err)
				}
				pageDims[i] = dim
				drawProgressBar(i+1, totalPages, ocrStartTime, "")
			}

			allPages = append(allPages, blocks)
		}
		fmt.Fprintln(os.Stderr)

	} else if eng == EngineHybrid {
		if !isPDF {
			return fmt.Errorf("the -hybrid flag requires a PDF input file (got %q)", inputFile)
		}
		pageDims = make([]PageDim, totalPages)
		ocrStartTime := time.Now()
		drawProgressBar(0, totalPages, ocrStartTime, "mapping page layout...")
		for i := 0; i < totalPages; i++ {
			drawProgressBar(i, totalPages, ocrStartTime, fmt.Sprintf("processing page %d...", i+1))
			blocks, dim, err := extractHybridPage(inputFile, i, apiURL, *model, *maxTokens)
			if err != nil {
				fmt.Fprintln(os.Stderr)
				return fmt.Errorf("hybrid extraction page %d: %w", i+1, err)
			}
			allPages = append(allPages, blocks)
			pageDims[i] = dim
			drawProgressBar(i+1, totalPages, ocrStartTime, "")
		}
		fmt.Fprintln(os.Stderr)

	} else if eng == EngineBaidu {
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
			if isPDF {
				fmt.Fprintf(os.Stderr, "  %s Rendering PDF pages...\r", color(colorCyan, "⏳"))
			}
			for i := 0; i < totalPages; i++ {
				var uri string
				var dim PageDim
				var err error
				if isPDF {
					fmt.Fprintf(os.Stderr, "\r\033[K  %s Rendering PDF pages: %d/%d...", color(colorCyan, "⏳"), i+1, totalPages)
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
			}
			if isPDF {
				fmt.Fprintf(os.Stderr, "\r\033[K  %s Rendered all %d pages.\n", color(colorGreen, "✔"), totalPages)
			}

			bs := *batchSize
			if bs <= 0 {
				bs = 1
			}

			ocrStartTime := time.Now()
			drawProgressBar(0, totalPages, ocrStartTime, "recognizing...")
			var rawDocBuilder strings.Builder
			for start := 0; start < totalPages; start += bs {
				end := start + bs
				if end > totalPages {
					end = totalPages
				}
				batchURIs := uris[start:end]
				isMulti := len(batchURIs) > 1

				batchPrompt := *prompt
				if *prompt == defaultPrompt || *prompt == "<image>Multi page parsing." || *prompt == "<image>document parsing." {
					if isMulti {
						batchPrompt = "<image>Multi page parsing."
					} else {
						batchPrompt = "<image>document parsing."
					}
				}

				if isMulti {
					drawProgressBar(start, totalPages, ocrStartTime, fmt.Sprintf("recognizing pages %d-%d...", start+1, end))
				} else {
					drawProgressBar(start, totalPages, ocrStartTime, fmt.Sprintf("recognizing page %d...", start+1))
				}

				cr, err := callAPIBaidu(apiURL, *model, batchPrompt, batchURIs, isMulti, *maxTokens)
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
				drawProgressBar(end, totalPages, ocrStartTime, "")

				if start > 0 {
					rawDocBuilder.WriteString("<PAGE>")
				}
				rawDocBuilder.WriteString(batchContent)
			}
			fmt.Fprintln(os.Stderr)
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
		ocrStartTime := time.Now()
		drawProgressBar(0, totalPages, ocrStartTime, "recognizing...")
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
				drawProgressBar(i+1, totalPages, ocrStartTime, "restored from cache")
			} else {
				var uri string
				var dim PageDim
				var err error
				if isPDF {
					drawProgressBar(i, totalPages, ocrStartTime, fmt.Sprintf("rendering page %d...", i+1))
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

				drawProgressBar(i, totalPages, ocrStartTime, fmt.Sprintf("recognizing page %d...", i+1))

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
				_ = time.Since(pageStart)

				if *resume && resumePath != "" {
					if resumeState == nil {
						resumeState = &ResumeState{
							InputFile: inputFile,
							ModTime:   modTime,
							Size:      size,
							Prompt:    *prompt,
							Model:     *model,
							DPI:       *dpi,
							APIURL:    apiURL,
							Pages:     []PageState{},
							PageDims:  make([]PageDim, totalPages),
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

				drawProgressBar(i+1, totalPages, ocrStartTime, "")
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
		fmt.Fprintln(os.Stderr)
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
	case *fmtHTML:
		result = renderHTML(allPages)
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
