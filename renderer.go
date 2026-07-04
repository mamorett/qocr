package main

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

var cjkRegexp = regexp.MustCompile(`\p{Han}`)
var nonASCIIRegexp = regexp.MustCompile(`[^\x00-\x7F]+`)

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
	
	// Map superscript digits to LaTeX math superscripts
	s = strings.ReplaceAll(s, "⁰", "\\(^{0}\\)")
	s = strings.ReplaceAll(s, "¹", "\\(^{1}\\)")
	s = strings.ReplaceAll(s, "²", "\\(^{2}\\)")
	s = strings.ReplaceAll(s, "³", "\\(^{3}\\)")
	s = strings.ReplaceAll(s, "⁴", "\\(^{4}\\)")
	s = strings.ReplaceAll(s, "⁵", "\\(^{5}\\)")
	s = strings.ReplaceAll(s, "⁶", "\\(^{6}\\)")
	s = strings.ReplaceAll(s, "⁷", "\\(^{7}\\)")
	s = strings.ReplaceAll(s, "⁸", "\\(^{8}\\)")
	s = strings.ReplaceAll(s, "⁹", "\\(^{9}\\)")
	
	// Map common math/arrow symbols
	s = strings.ReplaceAll(s, "↔", "\\(\\leftrightarrow\\)")
	s = strings.ReplaceAll(s, "→", "\\(\\rightarrow\\)")
	s = strings.ReplaceAll(s, "←", "\\(\\leftarrow\\)")
	
	// Map Scandinavian characters and empty-set symbols
	s = strings.ReplaceAll(s, "Ø", "\\O ")
	s = strings.ReplaceAll(s, "ø", "\\o ")
	s = strings.ReplaceAll(s, "∅", "\\O ")
	s = strings.ReplaceAll(s, "Æ", "\\AE ")
	s = strings.ReplaceAll(s, "æ", "\\ae ")
	s = strings.ReplaceAll(s, "Å", "\\AA ")
	s = strings.ReplaceAll(s, "å", "\\aa ")
	
	// Strip Chinese (Han) characters
	s = cjkRegexp.ReplaceAllString(s, "")
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
	s = strings.ReplaceAll(s, "~", "\\textasciitilde ")
	s = strings.ReplaceAll(s, "^", "\\textasciicircum ")
	s = strings.ReplaceAll(s, "<", "\\textless ")
	s = strings.ReplaceAll(s, ">", "\\textgreater ")
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
	sb.WriteString("\\sbox{\\tblbox}{%\n")
	sb.WriteString("\\small\n")
	fmt.Fprintf(&sb, "\\begin{tabular}{%s}\n\\hline\n", strings.TrimSpace(alignStr))
	for idx, row := range latexRows {
		for len(row) < maxCols {
			row = append(row, "")
		}
		sb.WriteString(strings.Join(row, " & ") + " \\\\ \\relax\n")
		if idx == 0 {
			sb.WriteString("\\hline\n")
		}
	}
	sb.WriteString("\\hline\n\\end{tabular}%\n}\n")
	sb.WriteString("\\ifdim\\wd\\tblbox>\\linewidth\n")
	sb.WriteString("  \\resizebox{\\linewidth}{!}{\\usebox{\\tblbox}}%\n")
	sb.WriteString("\\else\n")
	sb.WriteString("  \\usebox{\\tblbox}%\n")
	sb.WriteString("\\fi\n")
	sb.WriteString("\\end{table}\n")
	return sb.String()
}

func renderLatex(pages [][]OCRBlock, source, model string) (string, error) {
	var sb strings.Builder
	sb.WriteString("\\documentclass{article}\n")
	sb.WriteString("\\usepackage[T1]{fontenc}\n")
	sb.WriteString("\\usepackage[utf8]{inputenc}\n")
	sb.WriteString("\\usepackage{amsmath}\n")
	sb.WriteString("\\usepackage{amssymb}\n")
	sb.WriteString("\\usepackage{graphicx}\n")
	sb.WriteString("\\usepackage[margin=0.75in]{geometry}\n")
	sb.WriteString("\\newsavebox{\\tblbox}\n\n")
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
				sb.WriteString("\\sbox{\\tblbox}{%\n")
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
					
					sb.WriteString(strings.Join(cleanRow, " & ") + " \\\\ \\relax\n")
					if idx == 0 {
						sb.WriteString("\\hline\n")
					}
				}
				sb.WriteString("\\hline\n\\end{tabular}%\n}\n")
				sb.WriteString("\\ifdim\\wd\\tblbox>\\linewidth\n")
				sb.WriteString("  \\resizebox{\\linewidth}{!}{\\usebox{\\tblbox}}%\n")
				sb.WriteString("\\else\n")
				sb.WriteString("  \\usebox{\\tblbox}%\n")
				sb.WriteString("\\fi\n")
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
