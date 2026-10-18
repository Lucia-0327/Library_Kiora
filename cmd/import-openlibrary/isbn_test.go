package main

import "testing"

func TestISBN10To13(t *testing.T) {
	converted, ok := isbn10To13("0140449132")
	if !ok {
		t.Fatal("有效 ISBN-10 被拒绝")
	}
	if converted != "9780140449136" {
		t.Fatalf("转换结果 = %s，期望 9780140449136", converted)
	}
}

func TestCollectISBNsRejectsAmbiguousEdition(t *testing.T) {
	_, _, ok := collectISBNs(nil, []string{"9780140449136", "9780140328721"})
	if ok {
		t.Fatal("同一版本含多个不同 ISBN-13 时不应自动选取")
	}
}

func TestCollectISBNsKeepsEquivalentIdentifiers(t *testing.T) {
	primary, identifiers, ok := collectISBNs([]string{"0-14-044913-2"}, []string{"978-0-14-044913-6"})
	if !ok || primary != "9780140449136" {
		t.Fatalf("归一化失败：primary=%q ok=%v", primary, ok)
	}
	if len(identifiers) != 2 {
		t.Fatalf("标识符数量 = %d，期望 2", len(identifiers))
	}
}

func TestInvalidISBN(t *testing.T) {
	if validISBN13("9780140449137") {
		t.Fatal("错误校验位的 ISBN-13 被接受")
	}
	if validISBN10("0140449133") {
		t.Fatal("错误校验位的 ISBN-10 被接受")
	}
}
