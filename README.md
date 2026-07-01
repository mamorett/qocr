# 📄 QOCR CLI

```text
   __ _  ___   ___ _ __
 / _` |/ _ \ / __| '__|
| (_| | (_) | (__| |
 \__, |\___/ \___|_|
    |_|
```

[![Go Report Card](https://goreportcard.com/badge/github.com/mamorett/qocr)](https://goreportcard.com/report/github.com/mamorett/qocr)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)

A lightweight, **self-contained** CLI that extracts structured text from images and multi-page PDFs using either the **GLM-OCR** model or the **Baidu Unlimited-OCR** model — selectable via the `-engine` flag (`glm` is the default).

> [!IMPORTANT]
> This tool does **not** bundle the model. You must run an OpenAI-compatible inference engine (such as **vLLM**) serving either the `zai-org/GLM-OCR` model (default engine, `-engine glm`) or the `baidu/Unlimited-OCR` model (switch with `-engine baidu`). See the [Prerequisites](#-prerequisites) and [Baidu Unlimited-OCR Engine](#-baidu-unlimited-ocr-engine) sections below for setup details for each.

---

## 📋 Prerequisites

### Inference Engine

The CLI sends rendered page images to a chat-completions endpoint. By default it expects the server at `http://localhost:8080`.

**Quick start GLM-OCR  with vLLM (recommended):**

```bash
vllm serve zai-org/GLM-OCR \
  --allowed-local-media-path / \
  --port 8000 \
  --gpu-memory-utilization 0.75 \
  --speculative-config '{"method": "mtp", "num_speculative_tokens": 1}'
```

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

**Quick start with Ollama:**

If you are using **Ollama** (which runs on port `11434` by default), you can run GLM-OCR locally. 

> [!WARNING]
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
ocr -endpoint http://10.0.0.5:8000 document.pdf
```

---

## ✨ Key Features

- 🚀 **Zero Dependencies**: Built with pure Go + WebAssembly. No need for `poppler`, `mupdf`, or any system-level PDF tools.
- 📦 **Self-Contained**: PDF rendering is embedded inside the binary. Single file, works everywhere.
- 📑 **Robust Multi-Page PDF Support**: Renders pages locally and processes them sequentially to avoid GPU memory limits.
- 🎯 **Multiple Outputs**: Get results in **Markdown**, **Plain Text**, or **JSON**.
- 🌍 **Cross-Platform**: Compiled for Linux, macOS, and Windows (AMD64 & ARM64).

---

## 🛠️ Build & Install

Ensure you have **Go 1.25+** installed.

```bash
# Build for your current platform (default target)
make

# Cross-compile for all supported platforms (linux, darwin, windows for amd64 & arm64)
make build-all
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
| `-model` | Model name | `zai-org/GLM-OCR` (or `baidu/Unlimited-OCR` in baidu mode) |
| `-engine` | OCR engine to use: `glm` or `baidu` | `glm` |
| `-prompt` | Instruction sent with the file | `Extract all text from this document` (or automatic prompt recipes in baidu mode) |
| `-output` | Write output to file instead of stdout | `stdout` |
| `-dpi` | PDF rendering resolution | `200` |
| `-resume` | Resume previous execution if interrupted | `true` |
| `-markdown` | Output as Markdown | `true` |
| `-text` | Output as plain text (flattens tables) | `false` |
| `-json` | Output as structured JSON (includes dimensions & rotation metadata) | `false` |
| `-latex` | Output as LaTeX document fragment (tables are auto-scaled) | `false` |
| `-bbox` | Embed normalized bounding boxes as HTML comments in markdown | `false` |
| `-batch-size` | Number of pages per request (Baidu mode only, 0 sends all at once) | `0` |
| `-max-tokens` | Max tokens to generate (0 means use default: unset for glm, 8192 for baidu) | `0` |
| `-raw` | Dump raw model response (debug) | `false` |

---

## 💡 Examples

### Basic OCR
Prints formatted Markdown to your terminal:
```bash
ocr scan.png
```

### Multi-page PDF to File
Renders all pages and combines them into a single Markdown document. Flags can follow the filename:
```bash
ocr document.pdf -output result.md -dpi 150
```

### Remote Server
Specify the custom endpoint when the vLLM server is on a different machine:
```bash
ocr -endpoint http://10.0.0.5 invoice.pdf
```

### Structured Data
Extract raw JSON data for programmatic use:
```bash
ocr -json -output result.json document.pdf
```

### Using the Baidu Unlimited-OCR Engine
Switch to Baidu's model with `-engine baidu` for a different prompt recipe and per-document batching:
```bash
ocr -engine baidu -endpoint http://192.168.0.12:4000 -model baidu/Unlimited-OCR document.pdf -latex -output result.tex
```

---

## ⚙️ How it Works

Both the **GLM-OCR** and **Baidu Unlimited-OCR** models require images as input. Since neither model can process raw PDF blobs directly, this CLI performs the following steps (engine-dependent behaviors are noted inline):

1. **PDF Rendering**: Uses `go-pdfium` running on the `wazero` WebAssembly engine to render PDF pages into images. The default is **200 DPI**, which is optimal for balance between speed and OCR quality.
2. **Sequential Processing**: To ensure reliability and avoid overwhelming the GPU or hitting context limits, pages are processed one by one. The CLI prints a beautiful, color-coded real-time dashboard of current progress and timing.
3. **Automatic Resuming**: If `-resume` is enabled, the CLI computes a unique SHA-256 hash representing the input file (path, size, modification time) and API parameters. Every successfully processed page is saved locally to your system cache directory (`~/.cache/ocr-cli/` or equivalent). If interrupted, re-running the same command will restore all cached pages and skip API calls, resuming right where it left off. Cache files are cleaned up upon successful completion.
4. **Structured Parsing**: The results are combined and parsed into the chosen format. If the model returns mixed content, the CLI extracts the JSON part automatically.

---

## 📦 Output Formats

### 📝 Markdown (Default)
Maps block labels (title, text, table, figure) to appropriate Markdown elements. Multi-page documents are separated by `---` lines and include page comments.

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
- **Batching**: Processes all pages in a single API request by default, preserving the model's native multi-page reasoning. You can limit batch size using `-batch-size`.
- **Special Tokens**: Preserves grounding coordinates and page tokens returned by the server to construct layout-accurate 2D mappings.
- **Cache**: Unique caching strategy that serializes the full raw document output to skip inference.

Example usage:
```bash
# Using Baidu engine with custom model and endpoint
ocr -engine baidu -model <your-vllm-model-id> -endpoint http://192.168.0.12:4000 document.pdf -latex -output result.tex
```

---

## ⚖️ License
This project is licensed under the **MIT License** (see [LICENSE](file:///gorgon/ia/glm-ocr/LICENSE)).
