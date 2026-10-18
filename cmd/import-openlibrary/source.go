package main

import (
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type sourceReader struct {
	client    *http.Client
	userAgent string
}

func newSourceReader(userAgent string) *sourceReader {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 45 * time.Second
	transport.IdleConnTimeout = 90 * time.Second
	return &sourceReader{
		client:    &http.Client{Transport: transport},
		userAgent: userAgent,
	}
}

func (s *sourceReader) inspect(ctx context.Context, source string) (sourceMetadata, error) {
	if !isHTTPSource(source) {
		absolute, err := filepath.Abs(source)
		if err != nil {
			return sourceMetadata{}, fmt.Errorf("解析本地转储路径: %w", err)
		}
		info, err := os.Stat(absolute)
		if err != nil {
			return sourceMetadata{}, fmt.Errorf("读取本地转储信息: %w", err)
		}
		if info.IsDir() {
			return sourceMetadata{}, fmt.Errorf("转储来源不能是目录: %s", absolute)
		}
		return sourceMetadata{
			Source:        absolute,
			LastModified:  info.ModTime().UTC().Format(time.RFC3339Nano),
			ContentLength: info.Size(),
		}, nil
	}

	requestContext, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, http.MethodHead, source, nil)
	if err != nil {
		return sourceMetadata{}, fmt.Errorf("创建转储探测请求: %w", err)
	}
	s.setHeaders(request)
	response, err := s.client.Do(request)
	if err != nil {
		return sourceMetadata{}, fmt.Errorf("探测转储来源: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 400 {
		return sourceMetadata{}, newHTTPStatusError(response)
	}
	return sourceMetadata{
		Source:        source,
		ETag:          strings.TrimSpace(response.Header.Get("ETag")),
		LastModified:  strings.TrimSpace(response.Header.Get("Last-Modified")),
		ContentLength: response.ContentLength,
	}, nil
}

func (s *sourceReader) openGzip(ctx context.Context, source string) (io.ReadCloser, error) {
	var compressed io.ReadCloser
	if isHTTPSource(source) {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
		if err != nil {
			return nil, fmt.Errorf("创建转储下载请求: %w", err)
		}
		s.setHeaders(request)
		response, err := s.client.Do(request)
		if err != nil {
			return nil, fmt.Errorf("下载转储: %w", err)
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			defer response.Body.Close()
			return nil, newHTTPStatusError(response)
		}
		compressed = response.Body
	} else {
		file, err := os.Open(source)
		if err != nil {
			return nil, fmt.Errorf("打开本地转储: %w", err)
		}
		compressed = file
	}

	reader, err := gzip.NewReader(compressed)
	if err != nil {
		compressed.Close()
		return nil, fmt.Errorf("打开 gzip 转储: %w", err)
	}
	return &joinedReadCloser{Reader: reader, closers: []io.Closer{reader, compressed}}, nil
}

func (s *sourceReader) setHeaders(request *http.Request) {
	request.Header.Set("User-Agent", s.userAgent)
	request.Header.Set("Accept", "application/gzip, application/octet-stream;q=0.9, */*;q=0.1")
	request.Header.Set("Accept-Encoding", "identity")
}

func isHTTPSource(source string) bool {
	parsed, err := url.Parse(source)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https")
}

type joinedReadCloser struct {
	io.Reader
	closers []io.Closer
}

func (r *joinedReadCloser) Close() error {
	var first error
	for _, closer := range r.closers {
		if err := closer.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

type httpStatusError struct {
	StatusCode int
	RetryAfter time.Duration
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("远程转储返回 HTTP %d", e.StatusCode)
}

func newHTTPStatusError(response *http.Response) error {
	return &httpStatusError{
		StatusCode: response.StatusCode,
		RetryAfter: parseRetryAfter(response.Header.Get("Retry-After")),
	}
}

func parseRetryAfter(value string) time.Duration {
	value = strings.TrimSpace(value)
	if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	if date, err := http.ParseTime(value); err == nil {
		if delay := time.Until(date); delay > 0 {
			return delay
		}
	}
	return 0
}
