package services

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func resolveUploadsDir() string {
	dir := strings.TrimSpace(os.Getenv("UPLOADS_DIR"))
	if dir == "" {
		return "uploads"
	}
	return dir
}

func SaveBase64Image(b64 string, subdir string) (string, error) {
	if idx := strings.Index(b64, "base64,"); idx >= 0 {
		b64 = b64[idx+7:]
	}

	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", fmt.Errorf("decode base64: %w", err)
	}

	baseDir := resolveUploadsDir()
	dir := filepath.Join(baseDir, subdir)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("mkdir uploads dir: %w", err)
	}

	filename := fmt.Sprintf("%d.jpg", time.Now().UnixNano())
	fullpath := filepath.Join(dir, filename)

	if err := os.WriteFile(fullpath, data, 0644); err != nil {
		return "", fmt.Errorf("write file: %w", err)
	}

	// เก็บลง DB เป็น "faces/xxx.jpg"
	return filepath.ToSlash(filepath.Join(subdir, filename)), nil
}
