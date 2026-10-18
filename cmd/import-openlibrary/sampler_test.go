package main

import (
	"fmt"
	"testing"
)

func TestPublisherLiteralKeyIsStableAfterWhitespaceNormalization(t *testing.T) {
	left := literalKey("Penguin   Classics")
	right := literalKey(" penguin classics ")
	if left != right || len(left) != 64 {
		t.Fatalf("出版社字面值 key 不稳定：%q / %q", left, right)
	}
}

func TestFixedSamplerIsDeterministicAndAppliesCaps(t *testing.T) {
	config := samplerConfig{
		Target:          3,
		PoolFactor:      4,
		Seed:            "fixed-test-seed",
		MaxPerWork:      1,
		MaxPerPublisher: 2,
		MaxPerLanguage:  2,
	}
	authors := map[string]authorRecord{"/authors/OL1A": {Key: "/authors/OL1A", Name: "Author"}}
	makeCandidates := func(reverse bool) []editionCandidate {
		var values []editionCandidate
		for i := 0; i < 8; i++ {
			index := i
			if reverse {
				index = 7 - i
			}
			values = append(values, editionCandidate{
				EditionKey:    fmt.Sprintf("/books/OL%dM", index+1),
				WorkKey:       fmt.Sprintf("/works/OL%dW", index/2+1),
				PrimaryISBN13: fmt.Sprintf("978000000%04d", index),
				AuthorKeys:    []string{"/authors/OL1A"},
				Publishers:    []string{fmt.Sprintf("Publisher %d", index%3)},
				LanguageCode:  []string{"eng", "chi", "fre"}[index%3],
			})
		}
		return values
	}

	first := newFixedSampler(config, nil)
	for _, candidate := range makeCandidates(false) {
		first.offer(candidate)
	}
	selectedFirst, _ := first.selectCandidates(authors)
	second := newFixedSampler(config, nil)
	for _, candidate := range makeCandidates(true) {
		second.offer(candidate)
	}
	selectedSecond, _ := second.selectCandidates(authors)
	if len(selectedFirst) != 3 || len(selectedSecond) != 3 {
		t.Fatalf("抽样数量错误：%d / %d", len(selectedFirst), len(selectedSecond))
	}
	for i := range selectedFirst {
		if selectedFirst[i].EditionKey != selectedSecond[i].EditionKey {
			t.Fatalf("输入顺序改变了固定抽样：%v / %v", selectedFirst, selectedSecond)
		}
	}
}

func TestSelectionRequiresResolvedAuthors(t *testing.T) {
	config := samplerConfig{Target: 1, PoolFactor: 2, Seed: "seed"}
	sampler := newFixedSampler(config, nil)
	sampler.offer(editionCandidate{
		EditionKey:    "/books/OL1M",
		WorkKey:       "/works/OL1W",
		PrimaryISBN13: "9780140449136",
		AuthorKeys:    []string{"/authors/OL1A"},
		Publishers:    []string{"Publisher"},
		LanguageCode:  "eng",
	})
	selected, reasons := sampler.selectCandidates(nil)
	if len(selected) != 0 || reasons["作者姓名未解析"] != 1 {
		t.Fatalf("未解析作者未被拒绝：selected=%v reasons=%v", selected, reasons)
	}
}

func TestSameWorkUsesStableSelectionOrder(t *testing.T) {
	config := samplerConfig{Target: 2, PoolFactor: 2, Seed: "stable-work-title", MaxPerWork: 2}
	authors := map[string]authorRecord{"/authors/OL1A": {Key: "/authors/OL1A", Name: "Author"}}
	values := []editionCandidate{
		{EditionKey: "/books/OL10M", WorkKey: "/works/OL1W", Title: "Translated title", PrimaryISBN13: "9780140449136", AuthorKeys: []string{"/authors/OL1A"}, Publishers: []string{"P1"}, LanguageCode: "eng"},
		{EditionKey: "/books/OL11M", WorkKey: "/works/OL1W", Title: "另一个版本标题", PrimaryISBN13: "9780140328721", AuthorKeys: []string{"/authors/OL1A"}, Publishers: []string{"P2"}, LanguageCode: "chi"},
	}
	forward := newFixedSampler(config, nil)
	reverse := newFixedSampler(config, nil)
	for _, value := range values {
		forward.offer(value)
	}
	for index := len(values) - 1; index >= 0; index-- {
		reverse.offer(values[index])
	}
	left, _ := forward.selectCandidates(authors)
	right, _ := reverse.selectCandidates(authors)
	if len(left) != 2 || len(right) != 2 || left[0].Title != right[0].Title {
		t.Fatalf("同一作品的首次占位标题不稳定：%v / %v", left, right)
	}
}
