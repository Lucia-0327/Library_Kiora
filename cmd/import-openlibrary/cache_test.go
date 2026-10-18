package main

import (
	"path/filepath"
	"testing"
	"time"
)

func TestGzipCacheRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checkpoint.json.gz")
	want := editionScanCache{
		Version:     cacheVersion,
		Fingerprint: "fingerprint",
		Complete:    true,
		Candidates: []editionCandidate{{
			EditionKey:    "/books/OL1M",
			PrimaryISBN13: "9780140449136",
		}},
		UpdatedAt: time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC),
	}
	if err := saveGzipJSON(path, &want); err != nil {
		t.Fatal(err)
	}
	var got editionScanCache
	loaded, err := loadGzipJSON(path, &got)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded || got.Fingerprint != want.Fingerprint || len(got.Candidates) != 1 {
		t.Fatalf("缓存往返结果错误：%+v", got)
	}
}
