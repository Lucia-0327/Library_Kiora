package main

import (
	"container/heap"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

type candidateHeap []editionCandidate

func (h candidateHeap) Len() int { return len(h) }
func (h candidateHeap) Less(i, j int) bool {
	return candidateLess(h[j], h[i])
}
func (h candidateHeap) Swap(i, j int)   { h[i], h[j] = h[j], h[i] }
func (h *candidateHeap) Push(value any) { *h = append(*h, value.(editionCandidate)) }
func (h *candidateHeap) Pop() any {
	old := *h
	last := old[len(old)-1]
	*h = old[:len(old)-1]
	return last
}

type fixedSampler struct {
	config samplerConfig
	items  candidateHeap
}

func newFixedSampler(config samplerConfig, cached []editionCandidate) *fixedSampler {
	items := append(candidateHeap(nil), cached...)
	heap.Init(&items)
	for len(items) > config.poolSize() {
		heap.Pop(&items)
	}
	return &fixedSampler{config: config, items: items}
}

func (s *fixedSampler) offer(candidate editionCandidate) {
	sum := sha256.Sum256([]byte(s.config.Seed + "\x00" + candidate.EditionKey))
	candidate.SelectionHash = hex.EncodeToString(sum[:])
	if len(s.items) < s.config.poolSize() {
		heap.Push(&s.items, candidate)
		return
	}
	if candidateLess(candidate, s.items[0]) {
		heap.Pop(&s.items)
		heap.Push(&s.items, candidate)
	}
}

func (s *fixedSampler) snapshot() []editionCandidate {
	return append([]editionCandidate(nil), s.items...)
}

// selectCandidates 先按固定哈希排序，再执行配额和 ISBN 去重，因此同一快照与参数总会得到相同结果。
func (s *fixedSampler) selectCandidates(authors map[string]authorRecord) ([]editionCandidate, map[string]int) {
	candidates := s.snapshot()
	sort.Slice(candidates, func(i, j int) bool { return candidateLess(candidates[i], candidates[j]) })

	selected := make([]editionCandidate, 0, s.config.Target)
	reasons := make(map[string]int)
	seenISBN := make(map[string]struct{})
	workCounts := make(map[string]int)
	publisherCounts := make(map[string]int)
	languageCounts := make(map[string]int)

	for _, candidate := range candidates {
		if !allAuthorsResolved(candidate.AuthorKeys, authors) {
			reasons["作者姓名未解析"]++
			continue
		}
		if _, exists := seenISBN[candidate.PrimaryISBN13]; exists {
			reasons["ISBN 冲突"]++
			continue
		}
		if s.config.MaxPerWork > 0 && workCounts[candidate.WorkKey] >= s.config.MaxPerWork {
			reasons["作品配额"]++
			continue
		}
		language := candidate.LanguageCode
		if s.config.MaxPerLanguage > 0 && languageCounts[language] >= s.config.MaxPerLanguage {
			reasons["语言配额"]++
			continue
		}
		publisherKeys := normalizedPublishers(candidate.Publishers)
		blocked := false
		for _, publisher := range publisherKeys {
			if s.config.MaxPerPublisher > 0 && publisherCounts[publisher] >= s.config.MaxPerPublisher {
				blocked = true
				break
			}
		}
		if blocked {
			reasons["出版社配额"]++
			continue
		}

		seenISBN[candidate.PrimaryISBN13] = struct{}{}
		workCounts[candidate.WorkKey]++
		languageCounts[language]++
		for _, publisher := range publisherKeys {
			publisherCounts[publisher]++
		}
		selected = append(selected, candidate)
		if len(selected) == s.config.Target {
			break
		}
	}
	return selected, reasons
}

func candidateLess(left, right editionCandidate) bool {
	if left.SelectionHash != right.SelectionHash {
		return left.SelectionHash < right.SelectionHash
	}
	return left.EditionKey < right.EditionKey
}

func allAuthorsResolved(keys []string, authors map[string]authorRecord) bool {
	if len(keys) == 0 {
		return false
	}
	for _, key := range keys {
		name := strings.TrimSpace(authors[key].Name)
		if name == "" || len([]rune(name)) > 255 {
			return false
		}
	}
	return true
}

func normalizeLiteral(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(value)), " "))
}

func normalizedPublishers(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		normal := normalizeLiteral(value)
		if normal == "" {
			continue
		}
		if _, exists := seen[normal]; exists {
			continue
		}
		seen[normal] = struct{}{}
		result = append(result, normal)
	}
	return result
}

func configFingerprint(values ...any) (string, error) {
	encoded, err := json.Marshal(values)
	if err != nil {
		return "", fmt.Errorf("计算配置指纹: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func neededAuthorsFingerprint(candidates []editionCandidate) (string, map[string]struct{}) {
	needed := make(map[string]struct{})
	for _, candidate := range candidates {
		for _, key := range candidate.AuthorKeys {
			needed[key] = struct{}{}
		}
	}
	keys := make([]string, 0, len(needed))
	for key := range needed {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	sum := sha256.Sum256([]byte(strings.Join(keys, "\n")))
	return hex.EncodeToString(sum[:]), needed
}
