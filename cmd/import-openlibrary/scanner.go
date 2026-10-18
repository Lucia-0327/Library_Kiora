package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"os"
	"strings"
	"time"
)

const maxDumpLineBytes = 32 << 20

func loadOrScanEditions(ctx context.Context, reader *sourceReader, source sourceMetadata, config samplerConfig, cachePath string, checkpointLines int64, retries int) (*editionScanCache, error) {
	fingerprint, err := configFingerprint(source, config)
	if err != nil {
		return nil, err
	}
	state := &editionScanCache{}
	loaded, err := loadGzipJSON(cachePath, state)
	if err != nil {
		return nil, err
	}
	if !loaded || state.Version != cacheVersion || state.Fingerprint != fingerprint {
		state = &editionScanCache{
			Version:     cacheVersion,
			Fingerprint: fingerprint,
			Source:      source,
			Sampler:     config,
			Stats: scanStats{
				RejectedByCause: make(map[string]int64),
			},
		}
	}
	if state.Complete {
		log.Printf("复用完整版本候选缓存：%s（候选 %d 条）", cachePath, len(state.Candidates))
		return state, nil
	}
	if state.Stats.RejectedByCause == nil {
		state.Stats.RejectedByCause = make(map[string]int64)
	}

	for attempt := 0; ; attempt++ {
		err = scanEditionsAttempt(ctx, reader, state, cachePath, checkpointLines)
		if err == nil {
			return state, nil
		}
		if saveErr := checkpointEditions(cachePath, state); saveErr != nil {
			return nil, errors.Join(err, saveErr)
		}
		if !isHTTPSource(source.Source) || attempt >= retries || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		delay := retryDelay(attempt, err)
		log.Printf("版本转储读取中断，将在 %s 后从第 %d 行检查点继续：%v", delay.Round(time.Second), state.LinesSeen, err)
		if err := waitContext(ctx, delay); err != nil {
			return nil, err
		}
	}
}

func scanEditionsAttempt(ctx context.Context, reader *sourceReader, state *editionScanCache, cachePath string, checkpointLines int64) error {
	stream, err := reader.openGzip(ctx, state.Source.Source)
	if err != nil {
		return err
	}
	defer stream.Close()

	sampler := newFixedSampler(state.Sampler, state.Candidates)
	lineScanner := bufio.NewScanner(stream)
	lineScanner.Buffer(make([]byte, 64*1024), maxDumpLineBytes)
	var physicalLine int64
	for lineScanner.Scan() {
		physicalLine++
		if physicalLine <= state.LinesSeen {
			continue
		}
		select {
		case <-ctx.Done():
			state.Candidates = sampler.snapshot()
			return ctx.Err()
		default:
		}

		state.LinesSeen = physicalLine
		state.Stats.LinesSeen = physicalLine
		record, parseErr := parseDumpRecord(lineScanner.Bytes())
		if parseErr != nil {
			state.Stats.RejectedByCause["转储行无效"]++
		} else if record.RecordType == "/type/edition" {
			state.Stats.EditionLines++
			candidate, reason := parseEditionRecord(record, time.Now().Year())
			if reason != "" {
				state.Stats.RejectedByCause[reason]++
			} else {
				sampler.offer(candidate)
				state.Stats.Accepted++
			}
		}

		if checkpointLines > 0 && physicalLine%checkpointLines == 0 {
			state.Candidates = sampler.snapshot()
			if err := checkpointEditions(cachePath, state); err != nil {
				return err
			}
			log.Printf("版本转储检查点：已读 %d 行，质量合格 %d 条，保留候选 %d 条", physicalLine, state.Stats.Accepted, len(state.Candidates))
		}
	}
	if err := lineScanner.Err(); err != nil {
		state.Candidates = sampler.snapshot()
		return fmt.Errorf("扫描版本转储: %w", err)
	}
	if physicalLine < state.LinesSeen {
		return fmt.Errorf("转储内容短于缓存检查点：当前 %d 行，缓存 %d 行", physicalLine, state.LinesSeen)
	}
	state.Candidates = sampler.snapshot()
	state.Complete = true
	return checkpointEditions(cachePath, state)
}

func checkpointEditions(path string, state *editionScanCache) error {
	state.UpdatedAt = time.Now().UTC()
	return saveGzipJSON(path, state)
}

func loadOrScanAuthors(ctx context.Context, reader *sourceReader, source sourceMetadata, candidates []editionCandidate, cachePath string, checkpointLines int64, retries int) (*authorScanCache, error) {
	neededFingerprint, needed := neededAuthorsFingerprint(candidates)
	fingerprint, err := configFingerprint(source, neededFingerprint)
	if err != nil {
		return nil, err
	}
	state := &authorScanCache{}
	loaded, err := loadGzipJSON(cachePath, state)
	if err != nil {
		return nil, err
	}
	if !loaded || state.Version != cacheVersion || state.Fingerprint != fingerprint || state.NeededFingerprint != neededFingerprint {
		state = &authorScanCache{
			Version:           cacheVersion,
			Fingerprint:       fingerprint,
			NeededFingerprint: neededFingerprint,
			Source:            source,
			Authors:           make(map[string]authorRecord),
		}
	}
	if state.Complete {
		log.Printf("复用完整作者缓存：%s（命中 %d/%d）", cachePath, len(state.Authors), len(needed))
		return state, nil
	}
	if state.Authors == nil {
		state.Authors = make(map[string]authorRecord)
	}

	for attempt := 0; ; attempt++ {
		err = scanAuthorsAttempt(ctx, reader, state, needed, cachePath, checkpointLines)
		if err == nil {
			return state, nil
		}
		if saveErr := checkpointAuthors(cachePath, state); saveErr != nil {
			return nil, errors.Join(err, saveErr)
		}
		if !isHTTPSource(source.Source) || attempt >= retries || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		delay := retryDelay(attempt, err)
		log.Printf("作者转储读取中断，将在 %s 后从第 %d 行检查点继续：%v", delay.Round(time.Second), state.LinesSeen, err)
		if err := waitContext(ctx, delay); err != nil {
			return nil, err
		}
	}
}

func scanAuthorsAttempt(ctx context.Context, reader *sourceReader, state *authorScanCache, needed map[string]struct{}, cachePath string, checkpointLines int64) error {
	remaining := make(map[string]struct{}, len(needed))
	for key := range needed {
		if strings.TrimSpace(state.Authors[key].Name) == "" {
			remaining[key] = struct{}{}
		}
	}
	if len(remaining) == 0 {
		state.Complete = true
		return checkpointAuthors(cachePath, state)
	}
	stream, err := reader.openGzip(ctx, state.Source.Source)
	if err != nil {
		return err
	}
	defer stream.Close()

	lineScanner := bufio.NewScanner(stream)
	lineScanner.Buffer(make([]byte, 64*1024), maxDumpLineBytes)
	var physicalLine int64
	for lineScanner.Scan() {
		physicalLine++
		if physicalLine <= state.LinesSeen {
			continue
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		state.LinesSeen = physicalLine
		record, err := parseDumpRecord(lineScanner.Bytes())
		if err == nil {
			if _, wanted := remaining[record.Key]; wanted {
				if author, ok := parseAuthorRecord(record); ok {
					state.Authors[author.Key] = author
					delete(remaining, author.Key)
					if len(remaining) == 0 {
						state.Complete = true
						return checkpointAuthors(cachePath, state)
					}
				}
			}
		}
		if checkpointLines > 0 && physicalLine%checkpointLines == 0 {
			if err := checkpointAuthors(cachePath, state); err != nil {
				return err
			}
			log.Printf("作者转储检查点：已读 %d 行，命中 %d/%d 个作者", physicalLine, len(state.Authors), len(needed))
		}
	}
	if err := lineScanner.Err(); err != nil {
		return fmt.Errorf("扫描作者转储: %w", err)
	}
	if physicalLine < state.LinesSeen {
		return fmt.Errorf("作者转储短于缓存检查点：当前 %d 行，缓存 %d 行", physicalLine, state.LinesSeen)
	}
	state.Complete = true
	return checkpointAuthors(cachePath, state)
}

func checkpointAuthors(path string, state *authorScanCache) error {
	state.UpdatedAt = time.Now().UTC()
	return saveGzipJSON(path, state)
}

func retryDelay(attempt int, err error) time.Duration {
	var statusErr *httpStatusError
	if errors.As(err, &statusErr) && statusErr.RetryAfter > 0 {
		return statusErr.RetryAfter
	}
	if attempt > 4 {
		attempt = 4
	}
	base := time.Second << attempt
	return base + time.Duration(rand.IntN(750))*time.Millisecond
}

func waitContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func defaultCacheDirectory() string {
	if directory, err := os.UserCacheDir(); err == nil {
		return directory + string(os.PathSeparator) + "library-management" + string(os.PathSeparator) + "openlibrary"
	}
	return ".openlibrary-cache"
}

func sourceCachePath(directory, prefix, fingerprint string) string {
	short := fingerprint
	if len(short) > 16 {
		short = short[:16]
	}
	return directory + string(os.PathSeparator) + prefix + "-" + short + ".json.gz"
}

func copyStream(destination io.Writer, source io.Reader) error {
	_, err := io.Copy(destination, source)
	return err
}
