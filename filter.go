package main

import (
	"regexp"
	"strings"
)

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
	return false
}
