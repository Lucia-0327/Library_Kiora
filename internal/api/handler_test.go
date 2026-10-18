package api

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// TestHealthz 确保进程存活检查不依赖数据库连接。
func TestHealthz(t *testing.T) {
	server := NewServer(nil)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)

	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if contentType := recorder.Header().Get("Content-Type"); contentType != "application/json; charset=utf-8" {
		t.Fatalf("content type = %q", contentType)
	}
}

// TestProtectedEndpointRequiresLogin 确保受保护路由在访问数据库前拒绝匿名请求。
func TestProtectedEndpointRequiresLogin(t *testing.T) {
	server := NewServer(nil)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/dashboard", nil)

	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusUnauthorized)
	}
}

// TestCORSPreflight 验证本地 Vite 来源可以完成带凭据请求的预检。
func TestCORSPreflight(t *testing.T) {
	server := NewServer(nil)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodOptions, "/api/v1/auth/login", nil)
	request.Header.Set("Origin", "http://127.0.0.1:5173")

	server.Handler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNoContent)
	}
	if origin := recorder.Header().Get("Access-Control-Allow-Origin"); origin != "http://127.0.0.1:5173" {
		t.Fatalf("origin = %q", origin)
	}
}

// TestParseListWindow 验证列表分页采用默认值，并拒绝过大的单次读取。
func TestParseListWindow(t *testing.T) {
	t.Run("默认值", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/api/v1/books", nil)
		limit, offset, ok := parseListWindow(recorder, request, 100, 500)
		if !ok || limit != 100 || offset != 0 {
			t.Fatalf("limit=%d offset=%d ok=%v", limit, offset, ok)
		}
	})

	t.Run("自定义值", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/api/v1/books?limit=50&offset=150", nil)
		limit, offset, ok := parseListWindow(recorder, request, 100, 500)
		if !ok || limit != 50 || offset != 150 {
			t.Fatalf("limit=%d offset=%d ok=%v", limit, offset, ok)
		}
	})

	t.Run("超过上限", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/api/v1/books?limit=501", nil)
		_, _, ok := parseListWindow(recorder, request, 100, 500)
		if ok || recorder.Code != http.StatusBadRequest {
			t.Fatalf("status=%d ok=%v", recorder.Code, ok)
		}
	})
}

// TestNormalizeISBN13 覆盖合法 ISBN-13、ISBN-10 转换和错误校验位。
func TestNormalizeISBN13(t *testing.T) {
	tests := []struct {
		input string
		want  string
		ok    bool
	}{
		{"978-7-5063-6543-7", "9787506365437", true},
		{"0-14-044913-2", "9780140449136", true},
		{"9787119236845", "", false},
		{"not-an-isbn", "", false},
	}
	for _, test := range tests {
		got, ok := normalizeISBN13(test.input)
		if got != test.want || ok != test.ok {
			t.Errorf("normalizeISBN13(%q)=(%q,%v)，期望 (%q,%v)", test.input, got, ok, test.want, test.ok)
		}
	}
}

// TestContainsLikePattern 验证搜索词中的 SQL 通配符只按普通字符匹配。
func TestContainsLikePattern(t *testing.T) {
	if got, want := containsLikePattern("50%_="), "%50=%=_==%"; got != want {
		t.Fatalf("containsLikePattern()=%q，期望 %q", got, want)
	}
}

// TestBuildBookPageQuery 验证书目先分页且合法 ISBN 使用规范化唯一键查询。
func TestBuildBookPageQuery(t *testing.T) {
	query, args := buildBookPageQuery("0-14-044913-2", "", 0, "", 50, 100)
	for _, unexpected := range []string{"book_copies", "GROUP BY", "b.title LIKE"} {
		if strings.Contains(query, unexpected) {
			t.Fatalf("书目分页 SQL 不应包含 %q：%s", unexpected, query)
		}
	}
	if !strings.Contains(query, "b.normalized_isbn=?") {
		t.Fatalf("合法 ISBN 未使用规范化索引字段：%s", query)
	}
	wantArgs := []any{"9780140449136", 50, 100}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Fatalf("args=%#v，期望 %#v", args, wantArgs)
	}

	query, args = buildBookPageQuery("数据库_100%", "张_三%", 7, "book", 20, 40)
	for _, expected := range []string{"b.title LIKE ? ESCAPE '='", "a.full_name LIKE ? ESCAPE '='", "b.category_id=?", "b.material_type=?"} {
		if !strings.Contains(query, expected) {
			t.Errorf("书目筛选 SQL 缺少 %q：%s", expected, query)
		}
	}
	wantArgs = []any{"%数据库=_100=%%", "%数据库=_100=%%", "%张=_三=%%", uint64(7), "book", 20, 40}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Fatalf("args=%#v，期望 %#v", args, wantArgs)
	}
}

// TestBuildReaderPageQuery 防止读者分页 SQL 再次提前聚合全量借阅或罚金。
func TestBuildReaderPageQuery(t *testing.T) {
	query, args := buildReaderPageQuery("SIM_BIZ%", 25, 75)
	for _, unexpected := range []string{"JOIN loans", "JOIN fines", "GROUP BY"} {
		if strings.Contains(query, unexpected) {
			t.Fatalf("读者分页 SQL 不应包含 %q：%s", unexpected, query)
		}
	}
	wantArgs := []any{"%SIM=_BIZ=%%", "%SIM=_BIZ=%%", "%SIM=_BIZ=%%", 25, 75}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Fatalf("args=%#v，期望 %#v", args, wantArgs)
	}
}

// TestBuildCopyListQuery 验证副本支持馆藏位置搜索，且空关键词不会生成全匹配 LIKE。
func TestBuildCopyListQuery(t *testing.T) {
	query, args := buildCopyListQuery("", 20, 40)
	if strings.Contains(query, "LIKE") {
		t.Fatalf("空关键词不应生成 LIKE：%s", query)
	}
	if want := []any{20, 40}; !reflect.DeepEqual(args, want) {
		t.Fatalf("args=%#v，期望 %#v", args, want)
	}

	query, args = buildCopyListQuery("A_03%", 20, 40)
	for _, expected := range []string{"b.title LIKE ? ESCAPE '='", "c.barcode LIKE ? ESCAPE '='", "c.location LIKE ? ESCAPE '='"} {
		if !strings.Contains(query, expected) {
			t.Errorf("副本搜索 SQL 缺少 %q：%s", expected, query)
		}
	}
	pattern := "%A=_03=%%"
	if want := []any{pattern, pattern, pattern, 20, 40}; !reflect.DeepEqual(args, want) {
		t.Fatalf("args=%#v，期望 %#v", args, want)
	}
}

// TestBuildBusinessSearchQueries 覆盖借阅、预约和罚金的文本及数字编号搜索合同。
func TestBuildBusinessSearchQueries(t *testing.T) {
	tests := []struct {
		name       string
		build      func(string, int, int) (string, []any)
		q          string
		contains   []string
		argCount   int
		lastValues []any
	}{
		{
			name:       "借阅数字编号",
			build:      buildLoanListQuery,
			q:          "#42",
			contains:   []string{"r.reader_no LIKE", "r.full_name LIKE", "b.title LIKE", "c.barcode LIKE", "l.id=?"},
			argCount:   7,
			lastValues: []any{uint64(42), 20, 40},
		},
		{
			name:       "预约副本条码",
			build:      buildReservationListQuery,
			q:          "BK-2026-1-0001",
			contains:   []string{"LEFT JOIN book_copies", "r.reader_no LIKE", "b.title LIKE", "x.status LIKE", "CASE x.status", "c.barcode LIKE"},
			argCount:   8,
			lastValues: []any{20, 40},
		},
		{
			name:       "罚金及借阅编号",
			build:      buildFineListQuery,
			q:          "#42",
			contains:   []string{"r.reader_no LIKE", "c.barcode LIKE", "f.reason LIKE", "f.id=?", "l.id=?"},
			argCount:   9,
			lastValues: []any{uint64(42), uint64(42), 20, 40},
		},
		{
			name:       "审计及实体编号",
			build:      buildAuditLogListQuery,
			q:          "#42",
			contains:   []string{"u.username LIKE", "a.action LIKE", "a.entity_type LIKE", "CAST(a.details AS CHAR) LIKE", "a.id=?", "a.entity_id=?"},
			argCount:   8,
			lastValues: []any{uint64(42), uint64(42), 20, 40},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			query, args := test.build(test.q, 20, 40)
			for _, expected := range test.contains {
				if !strings.Contains(query, expected) {
					t.Errorf("搜索 SQL 缺少 %q：%s", expected, query)
				}
			}
			if len(args) != test.argCount {
				t.Fatalf("参数数量=%d，期望 %d：%#v", len(args), test.argCount, args)
			}
			gotTail := args[len(args)-len(test.lastValues):]
			if !reflect.DeepEqual(gotTail, test.lastValues) {
				t.Fatalf("尾部参数=%#v，期望 %#v", gotTail, test.lastValues)
			}
		})
	}
}

// TestListIDArguments 验证页内聚合使用的占位符和 ID 参数顺序一致。
func TestListIDArguments(t *testing.T) {
	placeholders, args := listIDArguments([]uint64{9, 3, 1})
	if placeholders != "?,?,?" {
		t.Fatalf("placeholders=%q，期望 %q", placeholders, "?,?,?")
	}
	if want := []any{uint64(9), uint64(3), uint64(1)}; !reflect.DeepEqual(args, want) {
		t.Fatalf("args=%#v，期望 %#v", args, want)
	}
}
