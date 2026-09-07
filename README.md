# 📄 qocr

![logo.png](logo.png)

[![Go Report Card](https://goreportcard.com/badge/github.com/mamorett/qocr)](https://goreportcard.com/report/github.com/mamorett/qocr)
[![Go Reference](https://pkg.go.dev/badge/github.com/mamorett/qocr.svg)](https://pkg.go.dev/github.com/mamorett/qocr)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)

A lightweight, **self-contained** CLI that extracts structured text from images, multi-page PDFs, and **EPUB books** using either the **Baidu Unlimited-OCR** model (default), the **GLM-OCR** model, or the built-in **native text-layer extractor** (no inference engine needed, no GPU, no network) — all selectable via flags.

> [!IMPORTANT]
> The `-native` mode (PDF) and EPUB mode require **no inference engine**. They read text directly from the document's embedded text layer. The AI-powered modes still require an OpenAI-compatible inference engine (such as **vLLM**). See the [Native Text Extraction](#-native-text-extraction-no-ocr) and [EPUB Conversion](#-epub-conversion-no-ocr) sections for details.

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
>   qocr -endpoint http://localhost:11434 -model glm-ocr:latest -dpi 75 document.pdf
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
>      qocr -endpoint http://localhost:11434 -model glm-ocr-large document.pdf
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
- 🔌 **Multi-Engine Support**: Powered by **Baidu Unlimited-OCR** by default (`-engine baidu`), with support for **GLM-OCR** (`-engine glm`) and the built-in **native text-layer extractor** (`-native`) — same CLI flags, same output formats.
- 📑 **Robust Multi-Page PDF Support**: Renders pages locally, then dispatches to the engine — page-by-page by default (use `-batch-size` to group multiple pages per request for Baidu to leverage native multi-page reasoning), or sequentially for GLM-OCR.
- 🔤 **Native Text Extraction**: For digitally-born PDFs, extract text directly from the PDF's internal text layer with **zero inference** — offline, instant, GPU-free. On tagged PDFs, headings and tables are recovered from the document's own structure tree.
- 🧩 **Hybrid Mode**: Combine native PDF text with Baidu layout/table OCR for complex pages (`-hybrid`).
- 📚 **EPUB Conversion**: Convert EPUB books to Markdown, HTML, JSON, LaTeX, or plain text — **no AI, no OCR, no network**. Auto-detected by file extension.
- 🎯 **Multiple Outputs**: Get results in **Markdown**, **HTML**, **Plain Text**, **JSON**, or **LaTeX**.
- 🌍 **Cross-Platform**: Compiled for Linux (AMD64, ARM64, ARM), macOS, and Windows (AMD64 & ARM64).

---

## 🛠️ Build & Install

Ensure you have **Go 1.26+** installed.

```bash
# Build for your current platform (default target)
make

# Cross-compile for all supported platforms (linux amd64/arm64/arm, darwin & windows amd64/arm64)
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

---

## 📖 Usage

The CLI supports flags in any position (before or after the input file). You can use either a single dash `-` or a double dash `--`.

```bash
qocr [options] <file>
```

### Options

| Flag | Description | Default |
| :--- | :--- | :--- |
| `-endpoint` | API base URL | `http://localhost:8080` |
| `-port` | Override port in endpoint URL | `0` (uses port from endpoint) |
| `-model` | Model name | `baidu/Unlimited-OCR` (or `zai-org/GLM-OCR` in glm mode) |
| `-engine` | OCR engine to use: `baidu`, `glm`, `native`, `hybrid`, or `epub` | `baidu` |
| `-baidu` | Use Baidu engine (alias for `-engine baidu`) | `false` |
| `-glm` | Use GLM engine (alias for `-engine glm`) | `false` |
| `-native` | Extract text from PDF text layer — no OCR, no AI, no network | `false` |
| `-hybrid` | Use native PDF text with Baidu table OCR | `false` |
| `-epub` | Force EPUB extraction mode (auto-detected by `.epub` extension) | `false` |
| `-prompt` | Instruction sent with the file | `Extract all text from this document` (Baidu engine auto-replaces this with `<image>document parsing.` / `<image>Multi page parsing.` based on page count) |
| `-output` | Write output to file instead of stdout | `stdout` |
| `-dpi` | PDF rendering resolution | `200` |
| `-resume` | Resume previous execution if interrupted | `true` |
| `-markdown` | Output as Markdown (the default format when no other format flag is set) | `false` |
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
For digitally-born PDFs — reports, papers, word-processor exports — extract text instantly without any inference engine (headings and tables are recovered too when the PDF is tagged):
```bash
qocr -native document.pdf -output result.md
```

### EPUB Conversion (No OCR)
Convert any EPUB book to your desired output format instantly. The `.epub` extension is auto-detected — no flags needed:
```bash
qocr book.epub -output book.md
qocr book.epub -json -output book.json
qocr book.epub -latex -output book.tex
```

### Hybrid Mode (Native Text + Baidu Table OCR)
Keep the PDF's native text layer and let Baidu re-OCR only the complex regions (tables, figures) — requires a running Baidu endpoint:
```bash
qocr -hybrid contract.pdf -output contract.md
```

---

## ⚙️ How It Works (AI Engines)

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

For **digitally-born PDFs** — exported from Word, LaTeX, InDesign, or any PDF writer — qocr extracts text **directly from the PDF's internal text layer** with zero inference engine, zero GPU, and zero network calls. On **tagged PDFs**, headings and tables are recovered from the document's own structure tree.

```bash
qocr -native document.pdf -output result.md
```

### How it works

The native mode uses a **two-tier detection strategy**:

#### Tier 1 — Tagged PDFs (Word exports, InDesign, PDF/UA, accessibility-compliant docs)

Many professionally-produced PDFs embed a full **logical structure tree** with semantic `<Table>`, `<TR>`, `<TD>`, `<TH>`, `<H1>`–`<H6>`, `<P>` elements. When detected, qocr reads this tree directly via PDFium's `FPDF_StructTree` API — the document author's own structural intent, stored in the file. Table cells are extracted **exactly as authored**, with zero spatial guessing. Headings come from the document's own heading tags: `<H1>`/`<H2>` become title blocks, `<H3>`–`<H6>` become section headings.

#### Tier 2 — Untagged PDFs (LaTeX output, older tools)

For PDFs without a structure tree, qocr falls back to PDFium's plain text-stream extraction: the page text is emitted in reading order as paragraphs. No table or heading reconstruction is attempted in this tier — if you need tables or headings from an untagged PDF, use `-hybrid` or one of the OCR engines instead.

### Limitations

- **Scanned PDFs**: Produces empty output. Use the default OCR mode for scanned documents.
- **Untagged PDFs**: Without a structure tree there is no heading or table reconstruction (see Tier 2 above). Use `-hybrid` or an OCR engine when you need them.
- **Complex layouts**: Multi-column or magazine-style layouts may have reading-order issues. Use OCR mode for maximum fidelity.

---

## 📚 EPUB Conversion (No-OCR)

For **EPUB books** — novels, textbooks, technical documentation, Project Gutenberg titles — qocr can extract text, headings, and tables **directly from the embedded XHTML** with zero inference engine, zero GPU, and zero network calls.

The `.epub` extension is **auto-detected**; no flag is required:
```bash
qocr book.epub -output book.md
```

You can also force EPUB mode explicitly with `-epub` or `-engine epub`.

> [!IMPORTANT]
> If you pass an EPUB with an AI engine flag (e.g. `-baidu book.epub`), qocr will warn you and **automatically switch** to native EPUB extraction. EPUB files contain machine-readable XHTML text — no OCR is needed or beneficial.

### How it works

EPUB files are standard ZIP archives containing:
- `META-INF/container.xml` → points to the OPF package file
- An OPF file (`content.opf`) → lists all content items and the reading-order **spine**
- XHTML chapter files referenced by the spine

qocr reads the spine in order and converts each XHTML chapter to structured blocks:

| XHTML element | OCRBlock label | Markdown output |
|:---|:---|:---|
| `<h1>`, `<h2>` | `title` | `## Heading` |
| `<h3>`–`<h6>` | `header` | `### Heading` |
| `<p>` | `text` | Paragraph |
| `<table>` | `table` | Markdown table |
| `<figcaption>` | `caption` | *Italic caption* |
| `<img>` | `image` | `![alt text]()` placeholder |
| `<nav>`, `<script>`, `<style>` | — | Skipped |

Tables are extracted as raw HTML and passed through the **same rendering pipeline** as PDF tables, so they are correctly converted to Markdown, LaTeX tabular environments, JSON blocks, etc.

### Limitations

- **DRM-protected EPUBs**: Cannot be read (the ZIP is encrypted). Remove DRM with a compatible tool first.
- **Fixed-layout EPUBs**: Comics, picture books, and heavily graphical EPUBs may produce minimal text output since their content is image-based.
- **Embedded fonts/styles**: Visual formatting (bold, italic within paragraphs) is stripped; only the text content is extracted.

---

## ⚖️ License
This project is licensed under the **MIT License** (see [LICENSE](https://github.com/mamorett/qocr/blob/main/LICENSE)).
