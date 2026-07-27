package main

import (
	"archive/zip"
	"encoding/xml"
	"fmt"
	"io"
	"path"
	"strings"

	"golang.org/x/net/html"
)

// ─────────────────────────────────────────────────────────────────────────────
// Public entry points (mirror the PDF native API)
// ─────────────────────────────────────────────────────────────────────────────

// getEPUBChapterCount returns the number of spine items in an EPUB file.
func getEPUBChapterCount(epubPath string) (int, error) {
	spine, err := parseEPUBSpine(epubPath)
	if err != nil {
		return 0, err
	}
	return len(spine), nil
}

// extractEPUBChapter extracts a single spine item (chapter) as OCRBlocks.
// The PageDim is always zero — EPUB chapters have no physical dimensions.
func extractEPUBChapter(epubPath string, index int) ([]OCRBlock, PageDim, error) {
	spine, err := parseEPUBSpine(epubPath)
	if err != nil {
		return nil, PageDim{}, err
	}
	if index < 0 || index >= len(spine) {
		return nil, PageDim{}, fmt.Errorf("EPUB chapter index %d out of range (0–%d)", index, len(spine)-1)
	}

	r, err := zip.OpenReader(epubPath)
	if err != nil {
		return nil, PageDim{}, fmt.Errorf("opening EPUB archive: %w", err)
	}
	defer r.Close()

	target := spine[index]
	for _, f := range r.File {
		if f.Name == target {
			rc, err := f.Open()
			if err != nil {
				return nil, PageDim{}, fmt.Errorf("opening EPUB entry %q: %w", target, err)
			}
			defer rc.Close()

			blocks, err := xhtmlToBlocks(rc)
			if err != nil {
				return nil, PageDim{}, fmt.Errorf("parsing EPUB chapter %q: %w", target, err)
			}
			return blocks, PageDim{}, nil
		}
	}
	return nil, PageDim{}, fmt.Errorf("EPUB spine item %q not found in archive", target)
}

// ─────────────────────────────────────────────────────────────────────────────
// Spine parsing (container.xml → OPF → ordered XHTML paths)
// ─────────────────────────────────────────────────────────────────────────────

type epubContainer struct {
	Rootfiles []struct {
		FullPath string `xml:"full-path,attr"`
	} `xml:"rootfiles>rootfile"`
}

type epubPackage struct {
	Manifest struct {
		Items []struct {
			ID        string `xml:"id,attr"`
			Href      string `xml:"href,attr"`
			MediaType string `xml:"media-type,attr"`
		} `xml:"item"`
	} `xml:"manifest"`
	Spine struct {
		ItemRefs []struct {
			IDRef string `xml:"idref,attr"`
		} `xml:"itemref"`
	} `xml:"spine"`
}

// parseEPUBSpine reads the EPUB ZIP and returns an ordered list of XHTML
// file paths (relative to the archive root) for each spine item.
func parseEPUBSpine(epubPath string) ([]string, error) {
	r, err := zip.OpenReader(epubPath)
	if err != nil {
		return nil, fmt.Errorf("opening EPUB: %w", err)
	}
	defer r.Close()

	// 1. Find and parse META-INF/container.xml
	containerData, err := readZipFile(r, "META-INF/container.xml")
	if err != nil {
		return nil, fmt.Errorf("EPUB container.xml: %w", err)
	}
	var container epubContainer
	if err := xml.Unmarshal(containerData, &container); err != nil {
		return nil, fmt.Errorf("parsing container.xml: %w", err)
	}
	if len(container.Rootfiles) == 0 {
		return nil, fmt.Errorf("no rootfile in EPUB container.xml")
	}
	opfPath := container.Rootfiles[0].FullPath // e.g. "OEBPS/content.opf"
	opfDir := path.Dir(opfPath)                // e.g. "OEBPS"

	// 2. Parse the OPF file
	opfData, err := readZipFile(r, opfPath)
	if err != nil {
		return nil, fmt.Errorf("EPUB OPF file %q: %w", opfPath, err)
	}
	var pkg epubPackage
	if err := xml.Unmarshal(opfData, &pkg); err != nil {
		return nil, fmt.Errorf("parsing OPF %q: %w", opfPath, err)
	}

	// 3. Build id → href map from manifest (XHTML items only)
	idToHref := make(map[string]string)
	for _, item := range pkg.Manifest.Items {
		mt := strings.ToLower(item.MediaType)
		if strings.Contains(mt, "html") || strings.Contains(mt, "xhtml") || mt == "application/xhtml+xml" {
			// Resolve path relative to OPF directory
			var href string
			if opfDir == "." {
				href = item.Href
			} else {
				href = opfDir + "/" + item.Href
			}
			idToHref[item.ID] = href
		}
	}

	// 4. Build ordered spine slice
	var spine []string
	for _, ref := range pkg.Spine.ItemRefs {
		if href, ok := idToHref[ref.IDRef]; ok {
			spine = append(spine, href)
		}
	}
	if len(spine) == 0 {
		return nil, fmt.Errorf("EPUB spine is empty or contains no XHTML items")
	}
	return spine, nil
}

// readZipFile returns the contents of a named file inside a ZIP archive.
func readZipFile(r *zip.ReadCloser, name string) ([]byte, error) {
	for _, f := range r.File {
		if f.Name == name {
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			defer rc.Close()
			return io.ReadAll(rc)
		}
	}
	return nil, fmt.Errorf("file %q not found in archive", name)
}

// ─────────────────────────────────────────────────────────────────────────────
// XHTML → OCRBlock converter
// ─────────────────────────────────────────────────────────────────────────────

// xhtmlToBlocks parses an XHTML/HTML document and returns semantic OCRBlocks.
// Headings → title/header, paragraphs → text, tables → table (raw HTML),
// figures/figcaptions → figure/caption, img → image placeholder.
func xhtmlToBlocks(r io.Reader) ([]OCRBlock, error) {
	doc, err := html.Parse(r)
	if err != nil {
		return nil, err
	}

	var blocks []OCRBlock
	idx := 0
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type != html.ElementNode {
			// Recurse into non-element nodes (document root, etc.)
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				walk(c)
			}
			return
		}

		tag := strings.ToLower(n.Data)

		switch tag {
		case "h1", "h2":
			text := strings.TrimSpace(collectText(n))
			if text != "" {
				blocks = append(blocks, OCRBlock{Index: idx, Label: "title", Content: text})
				idx++
			}

		case "h3", "h4", "h5", "h6":
			text := strings.TrimSpace(collectText(n))
			if text != "" {
				blocks = append(blocks, OCRBlock{Index: idx, Label: "header", Content: text})
				idx++
			}

		case "p":
			text := strings.TrimSpace(collectText(n))
			if text != "" {
				blocks = append(blocks, OCRBlock{Index: idx, Label: "text", Content: text})
				idx++
			}

		case "figcaption":
			text := strings.TrimSpace(collectText(n))
			if text != "" {
				blocks = append(blocks, OCRBlock{Index: idx, Label: "caption", Content: text})
				idx++
			}

		case "figure":
			// Walk children manually: emit caption and image blocks in order
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				if c.Type == html.ElementNode {
					childTag := strings.ToLower(c.Data)
					switch childTag {
					case "figcaption":
						text := strings.TrimSpace(collectText(c))
						if text != "" {
							blocks = append(blocks, OCRBlock{Index: idx, Label: "caption", Content: text})
							idx++
						}
					case "img":
						alt := attrVal(c, "alt")
						blocks = append(blocks, OCRBlock{Index: idx, Label: "image", Content: alt})
						idx++
					}
				}
			}

		case "img":
			// Standalone image — emit placeholder with alt text
			alt := attrVal(n, "alt")
			blocks = append(blocks, OCRBlock{Index: idx, Label: "image", Content: alt})
			idx++

		case "table":
			// Serialize the table back to HTML and let the existing pipeline
			// convert it to Markdown/LaTeX/etc.
			var sb strings.Builder
			renderHTMLNode(&sb, n)
			tableHTML := strings.TrimSpace(sb.String())
			if tableHTML != "" {
				blocks = append(blocks, OCRBlock{Index: idx, Label: "table", Content: tableHTML})
				idx++
			}

		case "script", "style", "head", "nav", "aside":
			// Skip non-content nodes entirely
			return

		default:
			// For all other container elements (div, section, article, body, etc.)
			// recurse into children
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				walk(c)
			}
		}
	}

	walk(doc)
	return blocks, nil
}

// collectText recursively collects all text content from an HTML subtree,
// collapsing whitespace runs to single spaces.
func collectText(n *html.Node) string {
	var sb strings.Builder
	var gather func(*html.Node)
	gather = func(node *html.Node) {
		if node.Type == html.TextNode {
			sb.WriteString(node.Data)
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			gather(c)
		}
	}
	gather(n)
	// Collapse internal whitespace
	text := strings.Join(strings.Fields(sb.String()), " ")
	return text
}

// attrVal returns the value of a named HTML attribute, or "" if absent.
func attrVal(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, key) {
			return a.Val
		}
	}
	return ""
}

// renderHTMLNode serializes an HTML node tree back to an HTML string.
// Used to capture <table> subtrees for the existing table pipeline.
func renderHTMLNode(sb *strings.Builder, n *html.Node) {
	switch n.Type {
	case html.TextNode:
		sb.WriteString(html.EscapeString(n.Data))
	case html.ElementNode:
		tag := n.Data
		sb.WriteString("<")
		sb.WriteString(tag)
		for _, a := range n.Attr {
			sb.WriteString(" ")
			sb.WriteString(a.Key)
			sb.WriteString(`="`)
			sb.WriteString(html.EscapeString(a.Val))
			sb.WriteString(`"`)
		}
		sb.WriteString(">")
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			renderHTMLNode(sb, c)
		}
		sb.WriteString("</")
		sb.WriteString(tag)
		sb.WriteString(">")
	case html.DocumentNode:
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			renderHTMLNode(sb, c)
		}
	}
}
