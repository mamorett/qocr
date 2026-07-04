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
