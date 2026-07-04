package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

func getResumeHash(inputFile string, modTime int64, size int64, prompt, model string, dpi int, apiURL string, engine string, recipe string) (string, error) {
	absPath, err := filepath.Abs(inputFile)
	if err != nil {
		absPath = inputFile
	}
	data := fmt.Sprintf("%s|%d|%d|%s|%s|%d|%s|%s|%s", absPath, modTime, size, prompt, model, dpi, apiURL, engine, recipe)
	h := sha256.New()
	if _, err := h.Write([]byte(data)); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func getResumeFilePath(hash string) (string, error) {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		cacheDir = ".ocr-cache"
	} else {
		cacheDir = filepath.Join(cacheDir, "ocr-cli")
	}
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return "", err
	}
	return filepath.Join(cacheDir, hash+".json"), nil
}

func loadResumeState(path string) (*ResumeState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var state ResumeState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	return &state, nil
}

func saveResumeState(path string, state *ResumeState) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

func deleteResumeState(path string) error {
	return os.Remove(path)
}

func findCachedPage(state *ResumeState, index int) (string, bool) {
	if state == nil {
		return "", false
	}
	for _, p := range state.Pages {
		if p.PageIndex == index {
			return p.Content, true
		}
	}
	return "", false
}
