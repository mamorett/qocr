package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

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
