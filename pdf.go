package main

import (
	"bytes"
	_ "embed"
	"encoding/base64"
	"fmt"
	"image"
	imgcolor "image/color"
	"image/draw"
	"image/png"
	"os"
	"time"

	"github.com/klippa-app/go-pdfium"
	"github.com/klippa-app/go-pdfium/requests"
	"github.com/klippa-app/go-pdfium/webassembly"
)

//go:embed pdfium.wasm
var pdfiumWasm []byte

var pdfiumPool pdfium.Pool

func initPDFium() error {
	if pdfiumPool != nil {
		return nil
	}

	var err error
	pdfiumPool, err = webassembly.Init(webassembly.Config{
		MinIdle:  1,
		MaxIdle:  2,
		MaxTotal: 4,
		WASM:     pdfiumWasm,
	})
	if err != nil {
		return fmt.Errorf("initializing PDFium WASM pool: %w", err)
	}
	return nil
}

func renderPDFToDataURIs(path string, dpi int) ([]string, error) {
	if err := initPDFium(); err != nil {
		return nil, err
	}

	instance, err := pdfiumPool.GetInstance(time.Minute)
	if err != nil {
		return nil, fmt.Errorf("getting PDFium instance: %w", err)
	}
	defer instance.Close()

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading PDF: %w", err)
	}

	doc, err := instance.OpenDocument(&requests.OpenDocument{
		File: &data,
	})
	if err != nil {
		return nil, fmt.Errorf("opening PDF document: %w", err)
	}
	defer instance.FPDF_CloseDocument(&requests.FPDF_CloseDocument{
		Document: doc.Document,
	})

	pageCount, err := instance.FPDF_GetPageCount(&requests.FPDF_GetPageCount{
		Document: doc.Document,
	})
	if err != nil {
		return nil, fmt.Errorf("getting page count: %w", err)
	}

	var dataURIs []string
	for i := 0; i < pageCount.PageCount; i++ {
		resp, err := instance.RenderPageInDPI(&requests.RenderPageInDPI{
			Page: requests.Page{
				ByIndex: &requests.PageByIndex{
					Document: doc.Document,
					Index:    i,
				},
			},
			DPI: dpi,
		})
		if err != nil {
			return nil, fmt.Errorf("rendering page %d: %w", i, err)
		}

		// Ensure the image has a solid white background (in case of transparent PDF pages)
		bounds := resp.Result.Image.Bounds()
		imgWithBg := image.NewRGBA(bounds)
		draw.Draw(imgWithBg, bounds, &image.Uniform{imgcolor.White}, image.Point{}, draw.Src)
		draw.Draw(imgWithBg, bounds, resp.Result.Image, image.Point{}, draw.Over)

		var buf bytes.Buffer
		if err := png.Encode(&buf, imgWithBg); err != nil {
			return nil, fmt.Errorf("encoding page %d to PNG: %w", i, err)
		}

		dataURI := fmt.Sprintf("data:image/png;base64,%s",
			base64.StdEncoding.EncodeToString(buf.Bytes()))
		dataURIs = append(dataURIs, dataURI)
	}

	return dataURIs, nil
}
