package main

import (
	"encoding/json"
	"time"
)

const (
	cacheVersion          = 1
	defaultEditionsSource = "https://openlibrary.org/data/ol_dump_editions_latest.txt.gz"
	defaultAuthorsSource  = "https://openlibrary.org/data/ol_dump_authors_latest.txt.gz"
)

// identifier 保存版本记录中的标准标识。NormalizedValue 用于唯一校验，DisplayValue 保留来源原文。
type identifier struct {
	Scheme          string `json:"scheme"`
	DisplayValue    string `json:"displayValue"`
	NormalizedValue string `json:"normalizedValue"`
	IsPrimary       bool   `json:"isPrimary"`
}

// translatorCredit 只保存来源中明确标注为译者的署名，不从普通贡献者姓名推测角色。
type translatorCredit struct {
	Name         string `json:"name"`
	CreditedName string `json:"creditedName"`
	Order        int    `json:"order"`
}

// editionCandidate 是通过基本真实性和完整性校验的 Open Library 版本候选记录。
type editionCandidate struct {
	SelectionHash    string             `json:"selectionHash"`
	EditionKey       string             `json:"editionKey"`
	WorkKey          string             `json:"workKey"`
	Title            string             `json:"title"`
	Subtitle         string             `json:"subtitle,omitempty"`
	Summary          string             `json:"summary,omitempty"`
	EditionStatement string             `json:"editionStatement,omitempty"`
	PrimaryISBN13    string             `json:"primaryIsbn13"`
	Identifiers      []identifier       `json:"identifiers"`
	AuthorKeys       []string           `json:"authorKeys"`
	Publishers       []string           `json:"publishers"`
	LanguageCode     string             `json:"languageCode"`
	PublishDateRaw   string             `json:"publishDateRaw"`
	PublishYear      uint16             `json:"publishYear"`
	Contributions    []string           `json:"contributions,omitempty"`
	Translators      []translatorCredit `json:"translators,omitempty"`
	SourceRecords    []string           `json:"sourceRecords,omitempty"`
	SourceRevision   string             `json:"sourceRevision"`
	SourceModified   *time.Time         `json:"sourceModified,omitempty"`
	SourceURL        string             `json:"sourceUrl"`
	PayloadSHA256    string             `json:"payloadSha256"`
	RawPayload       json.RawMessage    `json:"rawPayload"`
}

// authorRecord 是 Authors 转储中解析出的来源身份和姓名。
type authorRecord struct {
	Key            string          `json:"key"`
	Name           string          `json:"name"`
	SourceRevision string          `json:"sourceRevision"`
	SourceModified *time.Time      `json:"sourceModified,omitempty"`
	SourceURL      string          `json:"sourceUrl"`
	PayloadSHA256  string          `json:"payloadSha256"`
	RawPayload     json.RawMessage `json:"rawPayload"`
}

type samplerConfig struct {
	Target          int    `json:"target"`
	PoolFactor      int    `json:"poolFactor"`
	Seed            string `json:"seed"`
	MaxPerWork      int    `json:"maxPerWork"`
	MaxPerPublisher int    `json:"maxPerPublisher"`
	MaxPerLanguage  int    `json:"maxPerLanguage"`
}

func (c samplerConfig) poolSize() int {
	return c.Target * c.PoolFactor
}

type scanStats struct {
	LinesSeen       int64            `json:"linesSeen"`
	EditionLines    int64            `json:"editionLines"`
	Accepted        int64            `json:"accepted"`
	RejectedByCause map[string]int64 `json:"rejectedByCause"`
}

type sourceMetadata struct {
	Source        string `json:"source"`
	ETag          string `json:"etag,omitempty"`
	LastModified  string `json:"lastModified,omitempty"`
	ContentLength int64  `json:"contentLength,omitempty"`
}

type editionScanCache struct {
	Version     int                `json:"version"`
	Fingerprint string             `json:"fingerprint"`
	Source      sourceMetadata     `json:"source"`
	Sampler     samplerConfig      `json:"sampler"`
	Complete    bool               `json:"complete"`
	LinesSeen   int64              `json:"linesSeen"`
	Stats       scanStats          `json:"stats"`
	Candidates  []editionCandidate `json:"candidates"`
	UpdatedAt   time.Time          `json:"updatedAt"`
}

type authorScanCache struct {
	Version           int                     `json:"version"`
	Fingerprint       string                  `json:"fingerprint"`
	NeededFingerprint string                  `json:"neededFingerprint"`
	Source            sourceMetadata          `json:"source"`
	Complete          bool                    `json:"complete"`
	LinesSeen         int64                   `json:"linesSeen"`
	Authors           map[string]authorRecord `json:"authors"`
	UpdatedAt         time.Time               `json:"updatedAt"`
}
