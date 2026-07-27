# 📄 qocr

![logo.png](logo.png)

[![Go Report Card](https://goreportcard.com/badge/github.com/mamorett/qocr)](https://goreportcard.com/report/github.com/mamorett/qocr)
[![Go Reference](https://pkg.go.dev/badge/github.com/mamorett/qocr.svg)](https://pkg.go.dev/github.com/mamorett/qocr)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)

A lightweight, **self-contained** CLI that extracts structured text from images and multi-page PDFs using either the **Baidu Unlimited-OCR** model (default), the **GLM-OCR** model, or the built-in **native text-layer extractor** (no inference engine needed, no GPU, no network) — all selectable via flags.

> [!IMPORTANT]
> The `-native` mode requires **no inference engine**. It reads text directly from the PDF's embedded text layer. The AI-powered modes still require an OpenAI-compatible inference engine (such as **vLLM**). See the [Native Text Extraction](#-native-text-extraction-no-ocr) section for details.

---

## 📋 Prerequisites

### Inference Engine

The CLI sends rendered page images to a chat-completions endpoint. By default it expects the server at `http://localhost:8080`.

**Quick start Baidu Unlimited-OCR with vLLM (recommended):**

```bash
docker run --gpus all \
  --privileged --ipc=host -p 8000:8000 \
  -v ~/.cache/huggingface:/root/.cache/huggingface \
  vllm/vllm-openai:unlimited-ocr baidu/Unlimited-OCR \
  --trust-remote-code \
  --logits_processors vllm.model_executor.models.unlimited_ocr:NGramPerReqLogitsProcessor \
  --no-enable-prefix-caching \
  --mm-processor-cache-gb 0 \
  --tensor-parallel-size 1
```

**Quick start GLM-OCR with vLLM:**

```bash
vllm serve zai-org/GLM-OCR \
  --allowed-local-media-path / \
  --port 8000 \
  --gpu-memory-utilization 0.75 \
  --speculative-config '{"method": "mtp", "num_speculative_tokens": 1}'
```

**Quick start with Ollama:**

If you are using **Ollama** (which runs on port `11434` by default), you can run GLM-OCR locally.

> [!TIP]
> By default, Ollama configures model instances with a small context window (`num_ctx 2048`) and output generation limit (`num_predict 128`).
> High-resolution images (like the default 200 DPI PDF renders) translate to a high number of visual tokens, filling up the default context window and causing Ollama to truncate its responses early.
> 
> To run with Ollama, you have two options:
> 
> * **Option A (Zero-Setup Sweetspot):** Just run the CLI with a lower resolution of **`-dpi 75`** (requires no changes to Ollama):
>   ```bash
>   ocr -endpoint http://localhost:11434 -model glm-ocr:latest -dpi 75 document.pdf
>   ```
> * **Option B (Use full 200 DPI):** Create a custom model in Ollama with expanded limits:
>   1. Create a text file named `Modelfile` containing:
>      ```dockerfile
>      FROM glm-ocr:latest
>      PARAMETER num_ctx 8192
>      PARAMETER num_predict 4096
>      ```
>   2. Register the customized model version in Ollama:
>      ```bash
>      ollama create glm-ocr-large -f Modelfile
>      ```
>   3. Run the CLI targeting the new model and Ollama endpoint:
>      ```bash
>      ocr -endpoint http://localhost:11434 -model glm-ocr-large document.pdf
>      ```

**Remote server:**

If the engine runs on another host, simply specify the endpoint. Images are automatically embedded and sent as base64 data-URIs:

```bash
qocr -endpoint http://10.0.0.5:8000 document.pdf
```

---

## ✨ Key Features

- 🚀 **Zero Dependencies**: Built with pure Go + WebAssembly. No need for `poppler`, `mupdf`, or any system-level PDF tools.
- 📦 **Self-Contained**: PDF rendering is embedded inside the binary. Single file, works everywhere.
- 🔌 **Multi-Engine Support**: Powered by **Baidu Unlimited-OCR** by default (`-engine baidu`), with support for **GLM-OCR** (`-engine glm` / `-glm`) and the built-in **native text-layer extractor** (`-native`) — same CLI flags, same output formats.
- 📑 **Robust Multi-Page PDF Support**: Renders pages locally, then dispatches to the engine — page-by-page by default (use `-batch-size` to group multiple pages per request for Baidu to leverage native multi-page reasoning), or sequentially for GLM-OCR.
- 🔤 **Native Text Extraction**: For digitally-born PDFs, extract text, headings, and tables directly from the PDF's internal text layer with **zero inference** — offline, instant, GPU-free.
- 🎯 **Multiple Outputs**: Get results in **Markdown**, **HTML**, **Plain Text**, **JSON**, or **LaTeX**.
- 🌍 **Cross-Platform**: Compiled for Linux, macOS, and Windows (AMD64 & ARM64).

---

## 🛠️ Build & Install

Ensure you have **Go 1.25+** installed.

```bash
# Build for your current platform (default target)
make

# Cross-compile for all supported platforms (linux, darwin, windows for amd64 & arm64)
make build-all

# Install to your Go bin directory
make install
```

The resulting binaries will be placed in the `dist/` folder. You can install the `qocr` binary to your system PATH using:

```bash
# After building
sudo cp dist/qocr /usr/local/bin/qocr  # Linux/macOS
# or
copy dist\qocr.exe "C:\Program Files\qocr\qocr.exe"  # Windows
```

The resulting binaries will be placed in the `dist/` folder.

---

## 📖 Usage

The CLI supports flags in any position (before or after the input file). You can use either a single dash `-` or a double dash `--`.

```bash
ocr [options] <file>
```

### Options

| Flag | Description | Default |
| :--- | :--- | :--- |
| `-endpoint` | API base URL | `http://localhost:8080` |
| `-port` | Override port in endpoint URL | `0` (uses port from endpoint) |
| `-model` | Model name | `baidu/Unlimited-OCR` (or `zai-org/GLM-OCR` in glm mode) |
| `-engine` | OCR engine to use: `baidu`, `glm`, `native`, or `hybrid` | `baidu` |
| `-baidu` | Use Baidu engine (alias for `-engine baidu`) | `false` |
| `-glm` | Use GLM engine (alias for `-engine glm`) | `false` |
| `-native` | Extract text from PDF text layer — no OCR, no AI, no network | `false` |
| `-hybrid` | Use native PDF text with Baidu table OCR | `false` |
| `-prompt` | Instruction sent with the file | Automatic prompt recipe (`<image>document parsing.` / `<image>Multi page parsing.`) |
| `-output` | Write output to file instead of stdout | `stdout` |
| `-dpi` | PDF rendering resolution | `200` |
| `-resume` | Resume previous execution if interrupted | `true` |
| `-markdown` | Output as Markdown | `true` |
| `-html` | Output as HTML document (1:1 detection indexing with inline metadata) | `false` |
| `-text` | Output as plain text (flattens tables) | `false` |
| `-json` | Output as structured JSON (includes dimensions & rotation metadata) | `false` |
| `-latex` | Output as LaTeX document fragment (tables are auto-scaled) | `false` |
| `-bbox` | Embed normalized bounding boxes as HTML comments in markdown | `false` |
| `-batch-size` | Number of pages per request (Baidu mode only, 0 = one per request) | `0` (one page per request) |
| `-max-tokens` | Max tokens to generate (0 means use default: 8192 for baidu, unset for glm) | `0` |
| `-raw` | Dump raw model response (debug) | `false` |
| `-help` | Show usage information | `false` |
| `-version` | Print version and exit | `false` |

---

## 💡 Examples

### Basic OCR
Prints formatted Markdown to your terminal:
```bash
qocr scan.png
```

### Multi-page PDF to File
Renders all pages and combines them into a single Markdown document. Flags can follow the filename:
```bash
qocr document.pdf -output result.md -dpi 150
```

### Remote Server
Specify the custom endpoint when the vLLM server is on a different machine:
```bash
qocr -endpoint http://10.0.0.5 invoice.pdf
```

### Structured Data
Extract raw JSON data for programmatic use:
```bash
qocr -json -output result.json document.pdf
```

### Using the Baidu Alias
Use the handy `-baidu` flag as an alias:
```bash
qocr -baidu document.pdf -output result.txt
```

### Using the Baidu Unlimited-OCR Engine
The Baidu engine is the default, so you can omit `-engine baidu` and just set a custom endpoint or model:
```bash
qocr -endpoint http://192.168.0.12:4000 -model baidu/Unlimited-OCR document.pdf -latex -output result.tex
```

### Native PDF Extraction (No OCR)
For digitally-born PDFs — reports, papers, word-processor exports — extract text and tables instantly without any inference engine:
```bash
qocr -native document.pdf -output result.md
```

---


Both the **GLM-OCR** and **Baidu Unlimited-OCR** models require images as input. Since neither model can process raw PDF blobs directly, this CLI performs the following steps (engine-dependent behaviors are noted inline):

1. **PDF Rendering**: Uses `go-pdfium` running on the `wazero` WebAssembly engine to render PDF pages into images. The default is **200 DPI**, which is optimal for balance between speed and OCR quality.
2. **Sequential vs. Batched Processing**: With the **`glm`** engine, pages are sent one at a time to avoid overwhelming the GPU or hitting context limits. With the **`baidu`** engine, pages are sent one at a time by default (use `-batch-size` to group multiple pages into a single request for native multi-page reasoning). The CLI prints a beautiful, color-coded real-time dashboard of current progress and timing.
3. **Automatic Resuming**: If `-resume` is enabled, the CLI computes a unique SHA-256 hash representing the input file (path, size, modification time) and API parameters (including the chosen engine). Every successfully processed page is saved locally to your system cache directory (`~/.cache/ocr-cli/` or equivalent). If interrupted, re-running the same command will restore all cached pages and skip API calls, resuming right where it left off. Cache files are cleaned up upon successful completion.
4. **Structured Parsing**: The results are combined and parsed into the chosen format. Engine-specific output formats (GLM-OCR's JSON array of blocks, Baidu's markdown laced with `<|det|>` grounding tokens and `<PAGE>` page markers) are normalized into the requested output format. If the model returns mixed content, the CLI extracts the JSON part automatically.

---

## 📦 Output Formats

### 📝 Markdown (Default)
Maps block labels (title, text, table, figure) to appropriate Markdown elements. Multi-page documents are separated by `---` lines and include page comments.

### 🌐 HTML (`-html`)
Converts detections directly into structured, editable HTML elements (`<h1>`, `<h2>`, `<p>`, `ocr-table`, `ocr-image`, `ocr-page-number`) with `data-detection-index` attributes for 1:1 indexing and DOM manipulation/translation.

### 📄 Plain Text (`-text`)
Strips all Markdown decoration and flattens tables for easy copy-pasting or grep-ing.

### 🔢 JSON (`-json`)
Returns a full structured object containing the source path, model used, a list of page metadata (width, height, DPI, rotation), and a list of all detected blocks with their coordinates (`bbox_2d`).

### 🧮 LaTeX (`-latex`)
Returns a LaTeX document fragment containing the OCRed text paragraphs and tables. Tables are dynamically measured: if a table's natural width exceeds the page's text line width, it is auto-scaled down using a native LaTeX savebox conditional wrapper to fit within the margins; narrow tables are left at their natural size to prevent ugly layout stretching.

---

## 🤖 Baidu Unlimited-OCR Engine

The CLI supports the **Baidu `Unlimited-OCR`** model via `-engine baidu`. Key features of this integration:
- **Recipes**: Automatic instruction tuning based on page count (`<image>document parsing.` for single page, `<image>Multi page parsing.` for multi-page).
- **Logit Processor Configuration**: Passes the official `"custom_logit_processor": "DeepseekOCRNoRepeatNGramLogitProcessor"` and `"custom_params"` (`ngram_size` and `window_size`) configuration parameters, preventing infinite loops and text repetition on the server.
- **Batching**: Processes page-by-page sequentially by default (batch size of 1) for maximum memory stability, avoiding out-of-memory errors on large documents. You can customize the batch size using the `-batch-size` flag.
- **Special Tokens**: Preserves grounding coordinates and page tokens returned by the server to construct layout-accurate 2D mappings.
- **Cache**: Unique caching strategy that serializes the full raw document output to skip inference.

Example usage:
```bash
# Using Baidu engine with custom model and endpoint
qocr -engine baidu -model <your-vllm-model-id> -endpoint http://192.168.0.12:4000 document.pdf -latex -output result.tex
```

---

## 🔤 Native Text Extraction (No-OCR)

For **digitally-born PDFs** — exported from Word, LaTeX, InDesign, or any PDF writer — qocr can extract text, headings, and tables **directly from the PDF's internal text layer** with zero inference engine, zero GPU, and zero network calls.

```bash
qocr -native document.pdf -output result.md
```

### How it works

The native mode uses a **two-tier detection strategy**:

#### Tier 1 — Tagged PDFs (Word exports, InDesign, PDF/UA, accessibility-compliant docs)

Many professionally-produced PDFs embed a full **logical structure tree** with semantic `<Table>`, `<TR>`, `<TD>`, `<TH>`, `<H1>`–`<H6>`, `<P>` elements. When detected, qocr reads this tree directly via PDFium's `FPDF_StructTree` API — the document author's own structural intent, stored in the file. Table cells are extracted **exactly as authored**, with zero spatial guessing.

#### Tier 2 — Untagged PDFs (LaTeX output, older tools)

For PDFs without a structure tree, qocr uses **[GxPDF](https://github.com/coregx/gxpdf)'s 4-Pass Hybrid table detection** — a pure-Go, MIT-licensed library with no CGO:

- **Pass 1**: Gap detection (adaptive threshold)
- **Pass 2**: Overlap detection (Tabula-inspired)
- **Pass 3**: Alignment detection (geometric column clustering)
- **Pass 4**: Multi-line cell merging

#### Heading detection (both tiers)

Headings are detected using **actual font size metadata from the PDF's internal font tables**, read via `GetPageTextStructured(CollectFontInformation=true)`. The modal (most-frequent) font size on the page becomes the body-text baseline:

| Ratio vs. body font size | Markdown output |
|:---|:---|
| ≥ 1.6× | `## Heading` (title) |
| ≥ 1.25× | `### Heading` (header) |
| Bold at body size, short line | `### Heading` (header) |
| Otherwise | Paragraph |

### Limitations

- **Scanned PDFs**: Produces empty output. Use the default OCR mode for scanned documents.
- **Complex layouts**: Multi-column or magazine-style layouts may have reading-order issues. Use OCR mode for maximum fidelity.

---

## ⚖️ License
This project is licensed under the **MIT License** (see [LICENSE](https://github.com/mamorett/qocr/blob/main/LICENSE)).
