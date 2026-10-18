package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseEditionRecord(t *testing.T) {
	payload := `{"key":"/books/OL7355481M","type":{"key":"/type/edition"},"title":"Crime and Punishment","subtitle":"A Novel","description":{"type":"/type/text","value":"A source description."},"edition_name":"Penguin Classics","isbn_10":["0140449132"],"isbn_13":["9780140449136"],"authors":[{"key":"/authors/OL22242A"}],"works":[{"key":"/works/OL166894W"}],"publishers":["Penguin Classics"],"languages":[{"key":"/languages/eng"}],"publish_date":"2003","contributions":["David McDuff (Introduction, Translator)","Someone Else (Introduction)"],"source_records":["ia:crimepunishment00dost_0"]}`
	record := dumpRecord{
		RecordType:   "/type/edition",
		Key:          "/books/OL7355481M",
		Revision:     "13",
		LastModified: "2020-05-11T13:50:06.292730",
		RawPayload:   json.RawMessage(payload),
	}
	candidate, reason := parseEditionRecord(record, 2026)
	if reason != "" {
		t.Fatalf("版本被拒绝：%s", reason)
	}
	if candidate.PrimaryISBN13 != "9780140449136" || candidate.WorkKey != "/works/OL166894W" {
		t.Fatalf("版本标识解析错误：%+v", candidate)
	}
	if candidate.SourceModified == nil {
		t.Fatal("来源更新时间未解析")
	}
	if len(candidate.Translators) != 1 || candidate.Translators[0].Name != "David McDuff" {
		t.Fatalf("译者解析结果错误：%+v", candidate.Translators)
	}
	if candidate.PayloadSHA256 == "" || !strings.HasSuffix(candidate.SourceURL, candidate.EditionKey) {
		t.Fatal("来源追溯字段不完整")
	}
}

func TestExtractTranslatorsOnlyAcceptsExplicitRoles(t *testing.T) {
	credits := extractTranslators([]string{
		"Jane Doe",
		"Jane Doe (Editor)",
		"Translated by Jane Doe",
		"李明（译者）",
		"译者：王芳",
	})
	if len(credits) != 3 {
		t.Fatalf("明确译者数量 = %d，期望 3：%+v", len(credits), credits)
	}
	for _, credit := range credits {
		if credit.Name == "Jane Doe (Editor)" {
			t.Fatal("编辑被误判为译者")
		}
	}
}

func TestParseEditionRejectsMissingRequiredMetadata(t *testing.T) {
	payload := `{"key":"/books/OL1M","type":{"key":"/type/edition"},"title":"No Publisher","isbn_13":["9780140449136"],"authors":[{"key":"/authors/OL1A"}],"works":[{"key":"/works/OL1W"}],"publish_date":"2003"}`
	candidate, reason := parseEditionRecord(dumpRecord{
		RecordType:   "/type/edition",
		Key:          "/books/OL1M",
		Revision:     "1",
		LastModified: "2026-01-02T03:04:05.000000",
		RawPayload:   json.RawMessage(payload),
	}, 2026)
	if reason != "缺少出版社" {
		t.Fatalf("拒绝原因 = %q，候选=%+v", reason, candidate)
	}
}

func TestParseAuthorRecordPreservesRawSource(t *testing.T) {
	payload := json.RawMessage(`{"key":"/authors/OL22242A","name":"Fyodor Dostoevsky"}`)
	author, ok := parseAuthorRecord(dumpRecord{
		RecordType:   "/type/author",
		Key:          "/authors/OL22242A",
		Revision:     "7",
		LastModified: "2021-01-02T03:04:05.000000",
		RawPayload:   payload,
	})
	if !ok || author.Name != "Fyodor Dostoevsky" || author.PayloadSHA256 == "" {
		t.Fatalf("作者解析失败：%+v ok=%v", author, ok)
	}
	if string(author.RawPayload) != string(payload) {
		t.Fatal("作者原始 JSON 未原样保留")
	}
}
