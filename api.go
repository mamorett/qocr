package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var httpClient = &http.Client{
	Timeout: 5 * time.Minute,
}

func mimeType(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".pdf":
		return "application/pdf"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	default:
		return "application/octet-stream"
	}
}

func toDataURI(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading file: %w", err)
	}
	return fmt.Sprintf("data:%s;base64,%s",
		mimeType(path), base64.StdEncoding.EncodeToString(data)), nil
}

func getImageDimensions(path string) (width int, height int, err error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()
	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		return 0, 0, err
	}
	return cfg.Width, cfg.Height, nil
}

func doChatRequest(apiURL string, body []byte) (*ChatResponse, error) {
	req, err := http.NewRequest(http.MethodPost, apiURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("server %d: %s", resp.StatusCode, string(respBody))
	}

	var cr ChatResponse
	if err := json.Unmarshal(respBody, &cr); err != nil {
		return nil, fmt.Errorf("parsing response: %w\nraw: %s", err, string(respBody))
	}
	return &cr, nil
}

func callAPI(apiURL, model, promptText string, imageURIs []string, maxTokens int) (*ChatResponse, error) {
	var content []ContentPart
	for _, uri := range imageURIs {
		content = append(content, ContentPart{
			Type:     "image_url",
			ImageURL: &ImageURL{URL: uri},
		})
	}
	content = append(content, ContentPart{
		Type: "text",
		Text: promptText,
	})

	mt := maxTokens
	if mt <= 0 {
		mt = 8192
	}

	body, err := json.Marshal(ChatRequest{
		Model: model,
		Messages: []Message{{
			Role:    "user",
			Content: content,
		}},
		Temperature: 0.0,
		MaxTokens:   mt,
	})
	if err != nil {
		return nil, fmt.Errorf("marshalling request: %w", err)
	}

	return doChatRequest(apiURL, body)
}

func callAPIBaidu(apiURL, model, promptText string, imageURIs []string, multi bool, maxTokens int) (*ChatResponse, error) {
	var content []ContentPart
	content = append(content, ContentPart{
		Type: "text",
		Text: promptText,
	})
	for _, uri := range imageURIs {
		content = append(content, ContentPart{
			Type:     "image_url",
			ImageURL: &ImageURL{URL: uri},
		})
	}

	temp := 0.0
	skipSpecial := false

	mt := maxTokens
	if mt <= 0 {
		mt = 8192
	}

	windowSize := 128
	imageMode := "gundam"
	if multi {
		windowSize = 1024
		imageMode = "base"
	}

	custParams := &CustomParams{
		NgramSize:  35,
		WindowSize: windowSize,
	}
	imgCfg := &ImagesConfig{
		ImageMode: imageMode,
	}

	body, err := json.Marshal(ChatRequest{
		Model:                model,
		Messages:             []Message{{
			Role:    "user",
			Content: content,
		}},
		Temperature:          temp,
		MaxTokens:            mt,
		SkipSpecialTokens:    &skipSpecial,
		ImagesConfig:         imgCfg,
		CustomLogitProcessor: "DeepseekOCRNoRepeatNGramLogitProcessor",
		CustomParams:         custParams,
	})
	if err != nil {
		return nil, fmt.Errorf("marshalling request: %w", err)
	}

	return doChatRequest(apiURL, body)
}
