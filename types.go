package main

type PageDim struct {
	Width    int `json:"width"`
	Height   int `json:"height"`
	DPI      int `json:"dpi"`
	Rotation int `json:"rotation"`
}

type Engine string

const (
	EngineGLM   Engine = "glm"
	EngineBaidu Engine = "baidu"
)

type ImageURL struct {
	URL string `json:"url"`
}

type ContentPart struct {
	Type     string    `json:"type"`
	ImageURL *ImageURL `json:"image_url,omitempty"`
	Text     string    `json:"text,omitempty"`
}

type Message struct {
	Role    string        `json:"role"`
	Content []ContentPart `json:"content"`
}

type CustomParams struct {
	NgramSize  int `json:"ngram_size"`
	WindowSize int `json:"window_size"`
}

type ImagesConfig struct {
	ImageMode string `json:"image_mode,omitempty"`
}

type ChatRequest struct {
	Model                string        `json:"model"`
	Messages             []Message     `json:"messages"`
	Temperature          float64       `json:"temperature"`
	MaxTokens            int           `json:"max_tokens,omitempty"`
	SkipSpecialTokens    *bool         `json:"skip_special_tokens,omitempty"`
	ImagesConfig         *ImagesConfig `json:"images_config,omitempty"`
	CustomLogitProcessor string        `json:"custom_logit_processor,omitempty"`
	CustomParams         *CustomParams `json:"custom_params,omitempty"`
}

type Choice struct {
	Message struct {
		Content string `json:"content"`
	} `json:"message"`
}

type ChatResponse struct {
	Choices []Choice `json:"choices"`
	Error   *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

type OCRBlock struct {
	Index   int         `json:"index"`
	Label   string      `json:"label"`
	Content interface{} `json:"content"`
	BBox2D  interface{} `json:"bbox_2d"`
}

type JSONOutputBlock struct {
	Page    int         `json:"page"`
	Index   int         `json:"index"`
	Label   string      `json:"label"`
	Content interface{} `json:"content"`
	BBox2D  interface{} `json:"bbox_2d"`
}

type PageMeta struct {
	Page     int `json:"page"`
	Width    int `json:"width"`
	Height   int `json:"height"`
	DPI      int `json:"dpi"`
	Rotation int `json:"rotation"`
}

type JSONOutput struct {
	Source string            `json:"source"`
	Model  string            `json:"model"`
	Pages  []PageMeta        `json:"pages"`
	Blocks []JSONOutputBlock `json:"blocks"`
}

type PageState struct {
	PageIndex int    `json:"page_index"`
	Content   string `json:"content"`
}

type ResumeState struct {
	InputFile   string      `json:"input_file"`
	ModTime     int64       `json:"mod_time"`
	Size        int64       `json:"size"`
	Prompt      string      `json:"prompt"`
	Model       string      `json:"model"`
	DPI         int         `json:"dpi"`
	APIURL      string      `json:"api_url"`
	Pages       []PageState `json:"pages"`
	RawDocument string      `json:"raw_document,omitempty"`
	PageDims    []PageDim   `json:"page_dims,omitempty"`
}
