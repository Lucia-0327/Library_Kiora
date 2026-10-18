package main

import (
	"sort"
	"strings"
)

// normalizeISBN 只移除 ISBN 允许出现的空白和连字符；其他字符会使该标识失效。
func normalizeISBN(value string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(value) {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == 'x' || r == 'X':
			b.WriteByte('X')
		case r == '-' || r == ' ' || r == '\t':
			// ISBN 展示值常包含这些分隔符。
		default:
			return ""
		}
	}
	return b.String()
}

func validISBN10(value string) bool {
	if len(value) != 10 {
		return false
	}
	sum := 0
	for i := 0; i < 10; i++ {
		var digit int
		if i == 9 && value[i] == 'X' {
			digit = 10
		} else if value[i] >= '0' && value[i] <= '9' {
			digit = int(value[i] - '0')
		} else {
			return false
		}
		sum += (10 - i) * digit
	}
	return sum%11 == 0
}

func validISBN13(value string) bool {
	if len(value) != 13 {
		return false
	}
	sum := 0
	for i := 0; i < 13; i++ {
		if value[i] < '0' || value[i] > '9' {
			return false
		}
		weight := 1
		if i%2 == 1 {
			weight = 3
		}
		sum += int(value[i]-'0') * weight
	}
	return sum%10 == 0
}

func isbn10To13(value string) (string, bool) {
	if !validISBN10(value) {
		return "", false
	}
	prefix := "978" + value[:9]
	sum := 0
	for i := 0; i < len(prefix); i++ {
		weight := 1
		if i%2 == 1 {
			weight = 3
		}
		sum += int(prefix[i]-'0') * weight
	}
	check := byte((10 - sum%10) % 10)
	return prefix + string('0'+check), true
}

// collectISBNs 要求一个来源版本只能归一到一个 ISBN-13，避免把错误合并的多个产品当成同一版本。
func collectISBNs(isbn10Values, isbn13Values []string) (string, []identifier, bool) {
	type rawIdentifier struct {
		scheme  string
		display string
		normal  string
	}
	var values []rawIdentifier
	canonical13 := make(map[string]struct{})
	seen := make(map[string]struct{})

	for _, display := range isbn13Values {
		normal := normalizeISBN(display)
		if !validISBN13(normal) {
			continue
		}
		canonical13[normal] = struct{}{}
		key := "isbn_13\x00" + normal
		if _, exists := seen[key]; !exists {
			seen[key] = struct{}{}
			values = append(values, rawIdentifier{"isbn_13", display, normal})
		}
	}
	for _, display := range isbn10Values {
		normal := normalizeISBN(display)
		if !validISBN10(normal) {
			continue
		}
		converted, _ := isbn10To13(normal)
		canonical13[converted] = struct{}{}
		key := "isbn_10\x00" + normal
		if _, exists := seen[key]; !exists {
			seen[key] = struct{}{}
			values = append(values, rawIdentifier{"isbn_10", display, normal})
		}
	}
	if len(canonical13) != 1 {
		return "", nil, false
	}

	var primary string
	for value := range canonical13 {
		primary = value
	}
	if _, exists := seen["isbn_13\x00"+primary]; !exists {
		values = append(values, rawIdentifier{"isbn_13", primary, primary})
	}
	sort.Slice(values, func(i, j int) bool {
		if values[i].scheme != values[j].scheme {
			return values[i].scheme < values[j].scheme
		}
		return values[i].normal < values[j].normal
	})

	identifiers := make([]identifier, 0, len(values))
	for _, value := range values {
		identifiers = append(identifiers, identifier{
			Scheme:          value.scheme,
			DisplayValue:    strings.TrimSpace(value.display),
			NormalizedValue: value.normal,
			IsPrimary:       value.scheme == "isbn_13" && value.normal == primary,
		})
	}
	return primary, identifiers, true
}
