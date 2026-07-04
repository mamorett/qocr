package main

import (
	"fmt"
	"os"
	"strings"
	"time"
)

const asciiArt = `
   __ _  ___   ___ _ __ 
  / _` + "`" + ` |/ _ \ / __| '__|
 | (_| | (_) | (__| |   
  \__, |\___/ \___|_|   
     |_|                `

var version = "dev"

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

func drawProgressBar(completed, total int, startTime time.Time, status string) {
	if total <= 0 {
		return
	}
	pct := float64(completed) / float64(total)
	if pct > 1.0 {
		pct = 1.0
	}
	barWidth := 20
	filled := int(pct * float64(barWidth))
	
	var bar strings.Builder
	for i := 0; i < barWidth; i++ {
		if i < filled {
			bar.WriteString("█")
		} else {
			bar.WriteString("░")
		}
	}
	
	elapsed := time.Since(startTime)
	speedStr := "--s/page"
	etaStr := "--s"
	if completed > 0 {
		avg := elapsed / time.Duration(completed)
		speedStr = fmt.Sprintf("%.1fs/page", avg.Seconds())
		remaining := total - completed
		eta := avg * time.Duration(remaining)
		
		etaVal := eta.Round(time.Second)
		if etaVal >= time.Minute {
			etaStr = fmt.Sprintf("%dm%ds", int(etaVal.Minutes()), int(etaVal.Seconds())%60)
		} else {
			etaStr = fmt.Sprintf("%ds", int(etaVal.Seconds()))
		}
	}
	
	elapsedVal := elapsed.Round(time.Second)
	var elapsedStr string
	if elapsedVal >= time.Minute {
		elapsedStr = fmt.Sprintf("%dm%ds", int(elapsedVal.Minutes()), int(elapsedVal.Seconds())%60)
	} else {
		elapsedStr = fmt.Sprintf("%ds", int(elapsedVal.Seconds()))
	}
	
	statusIcon := color(colorCyan, "⏳")
	if completed == total {
		statusIcon = color(colorGreen, "✔")
	}
	
	suffix := ""
	if status != "" {
		suffix = " | " + color(colorDim, status)
	}
	
	fmt.Fprintf(os.Stderr, "\r\033[K  %s [%s] %3d%% | %d/%d pages | %s | Elapsed: %s | ETA: %s%s",
		statusIcon,
		color(colorCyan, bar.String()),
		int(pct*100),
		completed,
		total,
		speedStr,
		elapsedStr,
		etaStr,
		suffix,
	)
}
