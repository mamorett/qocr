package main

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	imgcolor "image/color"
	"image/draw"
	"image/png"
	"math"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/klippa-app/go-pdfium/requests"
	"github.com/klippa-app/go-pdfium/responses"
)

const hybridLayoutPrompt = `<image>Analyze this document page for layout only. Return each region in reading order using grounding tokens with a 0-999 coordinate grid. Allowed labels: text, table, figure, caption. Do not transcribe any text. Emit one <|det|>label [x1,y1,x2,y2]<|/det|> token per region.`
const hybridTablePrompt = `<image>Recognize this table exactly. Return only one valid GitHub-flavored Markdown table. Preserve rows, columns, headers, and empty cells. Do not add explanation.`

type hybridRegion struct {
	kind string
	bbox []int
}

func extractHybridPage(path string, index int, apiURL, model string, maxTokens int) ([]OCRBlock, PageDim, error) {
	lowURI, dim, err := renderPDFPageToDataURI(path, index, 96)
	if err != nil {
		return nil, PageDim{}, err
	}
	layoutResp, err := callAPIBaidu(apiURL, model, hybridLayoutPrompt, []string{lowURI}, false, 2048)
	if err != nil || layoutResp == nil || len(layoutResp.Choices) == 0 {
		return extractNativePage(path, index)
	}
	regions := hybridRegions(parseBaiduContent(layoutResp.Choices[0].Message.Content))
	if len(regions) == 0 {
		return extractNativePage(path, index)
	}
	chars, pageWidth, pageHeight, err := nativePageChars(path, index)
	if err != nil {
		return extractNativePage(path, index)
	}
	var blocks []OCRBlock
	for _, region := range regions {
		switch region.kind {
		case "text", "caption":
			if text := nativeTextInRegion(chars, region.bbox, pageWidth, pageHeight); text != "" {
				blocks = append(blocks, OCRBlock{Index: len(blocks), Label: "hybrid-text", Content: text, BBox2D: region.bbox})
			}
		case "table":
			highURI, _, renderErr := renderPDFPageToDataURI(path, index, 250)
			if renderErr != nil {
				continue
			}
			crop, cropErr := cropDataURI(highURI, region.bbox)
			if cropErr != nil {
				continue
			}
			tableResp, tableErr := callAPIBaidu(apiURL, model, hybridTablePrompt, []string{crop}, false, maxTokens)
			if tableErr != nil || tableResp == nil || len(tableResp.Choices) == 0 {
				continue
			}
			if table := strings.TrimSpace(tableResp.Choices[0].Message.Content); table != "" {
				blocks = append(blocks, OCRBlock{Index: len(blocks), Label: "hybrid-table", Content: table, BBox2D: region.bbox})
			}
		case "figure":
			blocks = append(blocks, OCRBlock{Index: len(blocks), Label: "hybrid-figure", Content: "[Figure]", BBox2D: region.bbox})
		}
	}
	if len(blocks) == 0 {
		return extractNativePage(path, index)
	}
	return blocks, dim, nil
}

func hybridRegions(pages [][]OCRBlock) []hybridRegion {
	if len(pages) == 0 {
		return nil
	}
	var regions []hybridRegion
	for _, block := range pages[0] {
		kind := strings.ToLower(strings.TrimSpace(block.Label))
		if kind != "text" && kind != "table" && kind != "figure" && kind != "caption" {
			continue
		}
		bbox, ok := getBBox(block.BBox2D)
		if !ok || bbox[2] <= bbox[0] || bbox[3] <= bbox[1] {
			continue
		}
		regions = append(regions, hybridRegion{kind: kind, bbox: bbox})
	}
	sort.SliceStable(regions, func(i, j int) bool {
		if regions[i].bbox[1] == regions[j].bbox[1] {
			return regions[i].bbox[0] < regions[j].bbox[0]
		}
		return regions[i].bbox[1] < regions[j].bbox[1]
	})
	return regions
}

func nativePageChars(path string, index int) ([]*responses.GetPageTextStructuredChar, float64, float64, error) {
	if err := initPDFium(); err != nil {
		return nil, 0, 0, err
	}
	instance, err := pdfiumPool.GetInstance(2 * time.Minute)
	if err != nil {
		return nil, 0, 0, err
	}
	defer instance.Close()
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, 0, err
	}
	doc, err := instance.OpenDocument(&requests.OpenDocument{File: &data})
	if err != nil {
		return nil, 0, 0, err
	}
	defer instance.FPDF_CloseDocument(&requests.FPDF_CloseDocument{Document: doc.Document}) //nolint:errcheck
	size, err := instance.FPDF_GetPageSizeByIndex(&requests.FPDF_GetPageSizeByIndex{Document: doc.Document, Index: index})
	if err != nil {
		return nil, 0, 0, err
	}
	resp, err := instance.GetPageTextStructured(&requests.GetPageTextStructured{Page: requests.Page{ByIndex: &requests.PageByIndex{Document: doc.Document, Index: index}}, Mode: requests.GetPageTextStructuredModeChars})
	if err != nil {
		return nil, 0, 0, err
	}
	return resp.Chars, size.Width, size.Height, nil
}

func nativeTextInRegion(chars []*responses.GetPageTextStructuredChar, bbox []int, width, height float64) string {
	if width <= 0 || height <= 0 {
		return ""
	}
	var selected []*responses.GetPageTextStructuredChar
	for _, char := range chars {
		if char == nil {
			continue
		}
		x := (char.PointPosition.Left + char.PointPosition.Right) * 500 / width
		y := (char.PointPosition.Top + char.PointPosition.Bottom) * 500 / height
		if x >= float64(bbox[0]) && x <= float64(bbox[2]) && y >= float64(bbox[1]) && y <= float64(bbox[3]) {
			selected = append(selected, char)
		}
	}
	lines := groupCharsIntoLines(selected)
	var parts []string
	for _, line := range lines {
		if text := strings.TrimSpace(assembleLineText(line)); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n")
}

func cropDataURI(uri string, bbox []int) (string, error) {
	const prefix = "data:image/png;base64,"
	if !strings.HasPrefix(uri, prefix) {
		return "", fmt.Errorf("unsupported image URI")
	}
	data, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(uri, prefix))
	if err != nil {
		return "", err
	}
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	bounds := src.Bounds()
	x1, y1 := int(math.Floor(float64(bounds.Dx())*float64(bbox[0])/1000)), int(math.Floor(float64(bounds.Dy())*float64(bbox[1])/1000))
	x2, y2 := int(math.Ceil(float64(bounds.Dx())*float64(bbox[2])/1000)), int(math.Ceil(float64(bounds.Dy())*float64(bbox[3])/1000))
	if x1 < 0 {
		x1 = 0
	}
	if y1 < 0 {
		y1 = 0
	}
	if x2 > bounds.Dx() {
		x2 = bounds.Dx()
	}
	if y2 > bounds.Dy() {
		y2 = bounds.Dy()
	}
	if x2 <= x1 || y2 <= y1 {
		return "", fmt.Errorf("empty crop")
	}
	cropBounds := image.Rect(x1, y1, x2, y2)
	dst := image.NewRGBA(image.Rect(0, 0, cropBounds.Dx(), cropBounds.Dy()))
	draw.Draw(dst, dst.Bounds(), &image.Uniform{imgcolor.White}, image.Point{}, draw.Src)
	draw.Draw(dst, dst.Bounds(), src, cropBounds.Min, draw.Src)
	var out bytes.Buffer
	if err := png.Encode(&out, dst); err != nil {
		return "", err
	}
	return prefix + base64.StdEncoding.EncodeToString(out.Bytes()), nil
}
