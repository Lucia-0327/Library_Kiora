package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	editionKeyPattern = regexp.MustCompile(`^/books/OL[0-9]+M$`)
	workKeyPattern    = regexp.MustCompile(`^/works/OL[0-9]+W$`)
	authorKeyPattern  = regexp.MustCompile(`^/authors/OL[0-9]+A$`)
	yearPattern       = regexp.MustCompile(`(?:^|[^0-9])([12][0-9]{3})(?:$|[^0-9])`)
	englishRole       = regexp.MustCompile(`(?i)^\s*(.+?)\s*[\(\[][^\)\]]*\btranslator\b[^\)\]]*[\)\]]\s*$`)
	englishPrefix     = regexp.MustCompile(`(?i)^\s*translated\s+by\s+(.+?)\s*$`)
	chinesePrefix     = regexp.MustCompile(`^\s*(?:译者|翻译|译)\s*[：:]\s*(.+?)\s*$`)
	chineseRole       = regexp.MustCompile(`^\s*(.+?)\s*[（(]\s*(?:译者|翻译|译)\s*[）)]\s*$`)
)

type olReference struct {
	Key string `json:"key"`
}

type olText struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

type flexibleText string

func (t *flexibleText) UnmarshalJSON(data []byte) error {
	var plain string
	if err := json.Unmarshal(data, &plain); err == nil {
		*t = flexibleText(strings.TrimSpace(plain))
		return nil
	}
	var block olText
	if err := json.Unmarshal(data, &block); err != nil {
		return err
	}
	*t = flexibleText(strings.TrimSpace(block.Value))
	return nil
}

type olEdition struct {
	Key           string        `json:"key"`
	Type          olReference   `json:"type"`
	Title         string        `json:"title"`
	Subtitle      string        `json:"subtitle"`
	Description   flexibleText  `json:"description"`
	EditionName   string        `json:"edition_name"`
	ISBN10        []string      `json:"isbn_10"`
	ISBN13        []string      `json:"isbn_13"`
	Authors       []olReference `json:"authors"`
	Works         []olReference `json:"works"`
	Publishers    []string      `json:"publishers"`
	Languages     []olReference `json:"languages"`
	PublishDate   string        `json:"publish_date"`
	Contributions []string      `json:"contributions"`
	SourceRecords []string      `json:"source_records"`
}

type olAuthor struct {
	Key  string `json:"key"`
	Name string `json:"name"`
}

type dumpRecord struct {
	RecordType   string
	Key          string
	Revision     string
	LastModified string
	RawPayload   json.RawMessage
}

func parseDumpRecord(line []byte) (dumpRecord, error) {
	parts := bytes.SplitN(bytes.TrimSuffix(line, []byte{'\r'}), []byte{'\t'}, 5)
	if len(parts) != 5 {
		return dumpRecord{}, fmt.Errorf("转储行不是 5 列")
	}
	record := dumpRecord{
		RecordType:   string(parts[0]),
		Key:          string(parts[1]),
		Revision:     string(parts[2]),
		LastModified: string(parts[3]),
		RawPayload:   append(json.RawMessage(nil), parts[4]...),
	}
	if !json.Valid(record.RawPayload) {
		return dumpRecord{}, fmt.Errorf("转储 JSON 无效")
	}
	return record, nil
}

func parseEditionRecord(record dumpRecord, currentYear int) (editionCandidate, string) {
	if record.RecordType != "/type/edition" {
		return editionCandidate{}, "非版本记录"
	}
	if !editionKeyPattern.MatchString(record.Key) {
		return editionCandidate{}, "版本标识无效"
	}
	if len(record.RawPayload) > 1<<20 {
		return editionCandidate{}, "来源记录过大"
	}
	revision := strings.TrimSpace(record.Revision)
	if revision == "" || len(revision) > 128 {
		return editionCandidate{}, "来源修订号无效"
	}
	modified, err := parseSourceTime(record.LastModified)
	if err != nil {
		return editionCandidate{}, "来源更新时间无效"
	}
	var payload olEdition
	if err := json.Unmarshal(record.RawPayload, &payload); err != nil {
		return editionCandidate{}, "版本 JSON 无法解析"
	}
	if payload.Key != record.Key || payload.Type.Key != "/type/edition" {
		return editionCandidate{}, "版本标识不一致"
	}
	title := strings.TrimSpace(payload.Title)
	if title == "" || len([]rune(title)) > 512 {
		return editionCandidate{}, "缺少书名"
	}
	if len(payload.Works) != 1 || !workKeyPattern.MatchString(payload.Works[0].Key) {
		return editionCandidate{}, "缺少唯一作品"
	}
	primaryISBN, identifiers, ok := collectISBNs(payload.ISBN10, payload.ISBN13)
	if !ok {
		return editionCandidate{}, "缺少唯一有效 ISBN-13"
	}
	authorKeys := uniqueReferenceKeys(payload.Authors, authorKeyPattern)
	if len(authorKeys) == 0 {
		return editionCandidate{}, "缺少作者"
	}
	publishers := uniqueNonEmpty(payload.Publishers)
	if len(publishers) == 0 {
		return editionCandidate{}, "缺少出版社"
	}
	for _, publisher := range publishers {
		if len([]rune(publisher)) > 255 {
			return editionCandidate{}, "出版社名称过长"
		}
	}
	publishDate := strings.TrimSpace(payload.PublishDate)
	if publishDate == "" || len([]rune(publishDate)) > 128 {
		return editionCandidate{}, "缺少出版日期"
	}
	publishYear, ok := extractPublishYear(publishDate, currentYear)
	if !ok {
		return editionCandidate{}, "出版日期无法校验"
	}

	payloadHash := sha256.Sum256(record.RawPayload)
	summary := strings.TrimSpace(string(payload.Description))
	if len([]byte(summary)) > 65_535 {
		// 完整说明仍保留在原始 JSON 中，避免截断后冒充来源原文。
		summary = ""
	}
	editionStatement := strings.TrimSpace(payload.EditionName)
	if len([]rune(editionStatement)) > 255 {
		editionStatement = ""
	}
	candidate := editionCandidate{
		EditionKey:       payload.Key,
		WorkKey:          payload.Works[0].Key,
		Title:            title,
		Subtitle:         strings.TrimSpace(payload.Subtitle),
		Summary:          summary,
		EditionStatement: editionStatement,
		PrimaryISBN13:    primaryISBN,
		Identifiers:      identifiers,
		AuthorKeys:       authorKeys,
		Publishers:       publishers,
		LanguageCode:     primaryLanguage(payload.Languages),
		PublishDateRaw:   publishDate,
		PublishYear:      uint16(publishYear),
		Contributions:    uniqueNonEmpty(payload.Contributions),
		SourceRecords:    uniqueNonEmpty(payload.SourceRecords),
		SourceRevision:   revision,
		SourceModified:   &modified,
		SourceURL:        "https://openlibrary.org" + payload.Key,
		PayloadSHA256:    hex.EncodeToString(payloadHash[:]),
		RawPayload:       append(json.RawMessage(nil), record.RawPayload...),
	}
	candidate.Translators = extractTranslators(candidate.Contributions)
	return candidate, ""
}

func parseAuthorRecord(record dumpRecord) (authorRecord, bool) {
	if record.RecordType != "/type/author" || !authorKeyPattern.MatchString(record.Key) {
		return authorRecord{}, false
	}
	revision := strings.TrimSpace(record.Revision)
	modified, err := parseSourceTime(record.LastModified)
	if revision == "" || len(revision) > 128 || err != nil || len(record.RawPayload) > 1<<20 {
		return authorRecord{}, false
	}
	var payload olAuthor
	if err := json.Unmarshal(record.RawPayload, &payload); err != nil {
		return authorRecord{}, false
	}
	name := strings.TrimSpace(payload.Name)
	if payload.Key != record.Key || name == "" || len([]rune(name)) > 255 {
		return authorRecord{}, false
	}
	payloadHash := sha256.Sum256(record.RawPayload)
	author := authorRecord{
		Key:            payload.Key,
		Name:           name,
		SourceRevision: revision,
		SourceModified: &modified,
		SourceURL:      "https://openlibrary.org" + payload.Key,
		PayloadSHA256:  hex.EncodeToString(payloadHash[:]),
		RawPayload:     append(json.RawMessage(nil), record.RawPayload...),
	}
	return author, true
}

func extractPublishYear(value string, currentYear int) (int, bool) {
	match := yearPattern.FindStringSubmatch(value)
	if len(match) != 2 {
		return 0, false
	}
	year, err := strconv.Atoi(match[1])
	if err != nil || year < 1000 || year > currentYear+1 {
		return 0, false
	}
	return year, true
}

func parseSourceTime(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	for _, layout := range []string{
		time.RFC3339Nano,
		"2006-01-02T15:04:05.999999",
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05.999999",
		"2006-01-02 15:04:05",
	} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("来源时间格式无效")
}

func uniqueReferenceKeys(values []olReference, pattern *regexp.Regexp) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		key := strings.TrimSpace(value.Key)
		if !pattern.MatchString(key) {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, key)
	}
	return result
}

func uniqueNonEmpty(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func primaryLanguage(values []olReference) string {
	for _, value := range values {
		if strings.HasPrefix(value.Key, "/languages/") {
			code := strings.TrimPrefix(value.Key, "/languages/")
			if len(code) >= 2 && len(code) <= 35 {
				return strings.ToLower(code)
			}
		}
	}
	return "und"
}

func extractTranslators(contributions []string) []translatorCredit {
	var result []translatorCredit
	for _, contribution := range contributions {
		credit := strings.TrimSpace(contribution)
		if credit == "" {
			continue
		}
		name := ""
		for _, pattern := range []*regexp.Regexp{englishRole, englishPrefix, chinesePrefix, chineseRole} {
			match := pattern.FindStringSubmatch(credit)
			if len(match) == 2 {
				name = strings.Trim(strings.TrimSpace(match[1]), "，,。.;；")
				break
			}
		}
		if name == "" || len([]rune(name)) > 255 || len([]rune(credit)) > 255 {
			continue
		}
		result = append(result, translatorCredit{
			Name:         name,
			CreditedName: credit,
			Order:        len(result) + 1,
		})
	}
	return result
}
