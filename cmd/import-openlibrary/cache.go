package main

import (
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func loadGzipJSON(path string, destination any) (bool, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("打开缓存 %s: %w", path, err)
	}
	defer file.Close()
	reader, err := gzip.NewReader(file)
	if err != nil {
		return false, fmt.Errorf("解压缓存 %s: %w", path, err)
	}
	defer reader.Close()
	decoder := json.NewDecoder(reader)
	if err := decoder.Decode(destination); err != nil {
		return false, fmt.Errorf("解析缓存 %s: %w", path, err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return false, fmt.Errorf("校验缓存 %s: %w", path, err)
	}
	return true, nil
}

func saveGzipJSON(path string, value any) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return fmt.Errorf("创建缓存目录: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".openlibrary-cache-*.tmp")
	if err != nil {
		return fmt.Errorf("创建临时缓存: %w", err)
	}
	temporaryPath := temporary.Name()
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(temporaryPath)
		}
	}()

	writer := gzip.NewWriter(temporary)
	encoder := json.NewEncoder(writer)
	if err := encoder.Encode(value); err != nil {
		writer.Close()
		temporary.Close()
		return fmt.Errorf("编码缓存: %w", err)
	}
	if err := writer.Close(); err != nil {
		temporary.Close()
		return fmt.Errorf("压缩缓存: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("同步缓存: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("关闭缓存: %w", err)
	}

	// Windows 不允许 Rename 覆盖已有文件；新文件已完整落盘后再替换旧检查点。
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("替换旧缓存: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("启用新缓存: %w", err)
	}
	keep = true
	return nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); errors.Is(err, io.EOF) {
		return nil
	} else if err != nil {
		return err
	}
	return fmt.Errorf("缓存包含多余 JSON")
}
