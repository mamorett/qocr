package main

import (
	"bytes"
	_ "embed"
	"encoding/base64"
	"fmt"
	"image"
	colorpkg "image/color"
	"image/png"
	"io"
	"os"
	"runtime"
	"strings"

	"github.com/blacktop/go-termimg"
)

// Embedded PNG logo binary bytes
//
//go:embed logo.png
var logoBytes []byte

// DetectBestProtocol detects the optimal terminal graphics protocol based on environment and OS.
func DetectBestProtocol() termimg.Protocol {
	protocol := termimg.DetectProtocol()
	if protocol != termimg.Unsupported {
		return protocol
	}

	if termimg.KittySupported() {
		return termimg.Kitty
	}
	if termimg.ITerm2Supported() {
		return termimg.ITerm2
	}
	if termimg.SixelSupported() {
		return termimg.Sixel
	}

	switch runtime.GOOS {
	case "darwin":
		if termimg.DetectITerm2FromEnvironment() {
			return termimg.ITerm2
		}
		if termimg.DetectKittyFromEnvironment() {
			return termimg.Kitty
		}
	case "linux":
		if termimg.DetectKittyFromEnvironment() {
			return termimg.Kitty
		}
		if termimg.DetectSixelFromEnvironment() {
			return termimg.Sixel
		}
	}

	return termimg.Halfblocks
}

// PrintITerm2PNG renders the logo using iTerm2 OSC 1337 with PNG encoding to preserve alpha transparency to os.Stdout.
func PrintITerm2PNG(img image.Image, cellsWidth, cellsHeight int) error {
	return PrintITerm2PNGTo(os.Stdout, img, cellsWidth, cellsHeight)
}

// PrintITerm2PNGTo renders the logo using iTerm2 OSC 1337 with PNG encoding to preserve alpha transparency to w.
func PrintITerm2PNGTo(w io.Writer, img image.Image, cellsWidth, cellsHeight int) error {
	if w == nil {
		w = os.Stdout
	}
	bounds := img.Bounds()
	targetW := uint(cellsWidth * 8)
	targetH := uint(cellsHeight * 16)

	if bounds.Dx() > 0 && bounds.Dy() > 0 {
		ratio := float64(bounds.Dx()) / float64(bounds.Dy())
		if float64(targetW)/float64(targetH) > ratio {
			targetW = uint(float64(targetH) * ratio)
		} else {
			targetH = uint(float64(targetW) / ratio)
		}
	}

	// Resize in memory keeping NRGBA/RGBA alpha channel
	resized := termimg.FastResize(img, targetW, targetH)

	// Encode to PNG (preserves transparency unlike JPEG)
	var buf bytes.Buffer
	if err := png.Encode(&buf, resized); err != nil {
		return err
	}

	b64 := base64.StdEncoding.EncodeToString(buf.Bytes())
	fmt.Fprintf(w, "\x1b]1337;File=inline=1;width=%dc;height=%dc;preserveAspectRatio=1:%s\a\n", cellsWidth, cellsHeight, b64)
	return nil
}

// PrintTransparentHalfblocks renders a 24-bit halfblock fallback image while maintaining transparent backgrounds to os.Stdout.
func PrintTransparentHalfblocks(img image.Image, width, height int) {
	PrintTransparentHalfblocksTo(os.Stdout, img, width, height)
}

// PrintTransparentHalfblocksTo renders a 24-bit halfblock fallback image while maintaining transparent backgrounds to w.
func PrintTransparentHalfblocksTo(w io.Writer, img image.Image, width, height int) {
	if w == nil {
		w = os.Stdout
	}
	// 2 vertical pixels per character cell
	resized := termimg.FastResize(img, uint(width), uint(height*2))
	bounds := resized.Bounds()

	var sb strings.Builder

	for y := bounds.Min.Y; y < bounds.Max.Y; y += 2 {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			topColor := resized.At(x, y)
			var botColor colorpkg.Color = colorpkg.NRGBA{0, 0, 0, 0}
			if y+1 < bounds.Max.Y {
				botColor = resized.At(x, y+1)
			}

			tr, tg, tb, ta := topColor.RGBA()
			br, bg, bb, ba := botColor.RGBA()

			topOpaque := ta >= 32768
			botOpaque := ba >= 32768

			if !topOpaque && !botOpaque {
				// Both sub-pixels transparent -> Reset background & output space
				sb.WriteString("\x1b[0m ")
			} else if topOpaque && !botOpaque {
				// Top pixel opaque, bottom transparent -> ▀ with reset background
				sb.WriteString(fmt.Sprintf("\x1b[0;38;2;%d;%d;%dm▀", tr>>8, tg>>8, tb>>8))
			} else if !topOpaque && botOpaque {
				// Top pixel transparent, bottom opaque -> ▄ with reset background
				sb.WriteString(fmt.Sprintf("\x1b[0;38;2;%d;%d;%dm▄", br>>8, bg>>8, bb>>8))
			} else {
				// Both sub-pixels opaque -> ▀ with foreground top, background bottom
				sb.WriteString(fmt.Sprintf("\x1b[38;2;%d;%d;%d;48;2;%d;%d;%dm▀", tr>>8, tg>>8, tb>>8, br>>8, bg>>8, bb>>8))
			}
		}
		sb.WriteString("\x1b[0m\n")
	}

	fmt.Fprint(w, sb.String())
}

// PrintLogo main entrypoint for printing terminal logo cleanly to os.Stderr.
func PrintLogo() {
	PrintLogoTo(os.Stderr)
}

// PrintLogoTo prints the embedded logo.png as truecolor ANSI blocks/graphics protocol
// if the output writer is a terminal.
func PrintLogoTo(w io.Writer) {
	if w == nil {
		w = os.Stderr
	}
	if f, ok := w.(*os.File); ok {
		if !isTTY(f) {
			return
		}
	}

	protocol := DetectBestProtocol()
	srcImg, _, err := image.Decode(bytes.NewReader(logoBytes))
	if err != nil {
		return
	}

	switch protocol {
	case termimg.ITerm2:
		if err := PrintITerm2PNGTo(w, srcImg, 50, 25); err == nil {
			return
		}
	case termimg.Kitty:
		img, err := termimg.From(bytes.NewReader(logoBytes))
		if err == nil {
			rendered, err := img.Width(50).Height(25).Scale(termimg.ScaleFit).Protocol(termimg.Kitty).Render()
			if err == nil {
				fmt.Fprint(w, rendered)
				return
			}
		}
	}

	// Fallback to alpha-preserving unicode halfblocks
	PrintTransparentHalfblocksTo(w, srcImg, 50, 25)
}

func printLogo(w io.Writer) {
	PrintLogoTo(w)
}
