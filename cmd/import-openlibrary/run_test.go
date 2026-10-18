package main

import (
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestRunDryModeDoesNotRequireDatabaseCredentials(t *testing.T) {
	t.Setenv("DB_USER", "")
	t.Setenv("DB_PASSWORD", "")
	directory := t.TempDir()
	editionsPath := filepath.Join(directory, "editions.txt.gz")
	authorsPath := filepath.Join(directory, "authors.txt.gz")
	editionJSON := `{"key":"/books/OL7355481M","type":{"key":"/type/edition"},"title":"Crime and Punishment","isbn_10":["0140449132"],"isbn_13":["9780140449136"],"authors":[{"key":"/authors/OL22242A"}],"works":[{"key":"/works/OL166894W"}],"publishers":["Penguin Classics"],"languages":[{"key":"/languages/eng"}],"publish_date":"2003"}`
	authorJSON := `{"key":"/authors/OL22242A","name":"Fyodor Dostoevsky"}`
	writeTestGzip(t, editionsPath, "/type/edition\t/books/OL7355481M\t13\t2020-05-11T13:50:06.292730\t"+editionJSON+"\n")
	writeTestGzip(t, authorsPath, "/type/author\t/authors/OL22242A\t7\t2021-01-02T03:04:05.000000\t"+authorJSON+"\n")

	err := run(context.Background(), commandConfig{
		EditionsSource:  editionsPath,
		AuthorsSource:   authorsPath,
		CacheDirectory:  filepath.Join(directory, "cache"),
		CheckpointLines: 1,
		Retries:         0,
		BatchSize:       1,
		Sampler: samplerConfig{
			Target:          1,
			PoolFactor:      2,
			Seed:            "dry-run-test",
			MaxPerWork:      1,
			MaxPerPublisher: 1,
			MaxPerLanguage:  1,
		},
		Apply: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := filepath.Glob(filepath.Join(directory, "cache", "selection-*.json.gz"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("选择缓存数量 = %d，错误=%v", len(matches), err)
	}
}

func writeTestGzip(t *testing.T, path, content string) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	writer := gzip.NewWriter(file)
	if _, err := writer.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}
