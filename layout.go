package main

import (
	"math"
	"sort"
	"strings"
)

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
