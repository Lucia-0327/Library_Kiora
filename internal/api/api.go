package api

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"library-management/internal/auth"
	"library-management/internal/model"
)

// Server 保存 API 的共享依赖和请求生命周期配置。
type Server struct {
	DB         *sql.DB
	SessionTTL time.Duration
}

// contextKey 避免与其他 HTTP 中间件写入的上下文值发生键冲突。
type contextKey string

const userContextKey contextKey = "authenticated-user"

// currentUser 是在请求上下文中传递的最小化认证身份。
// 该结构刻意不包含密码和会话凭据。
type currentUser struct {
	ID       uint64 `json:"id"`
	Username string `json:"username"`
	Role     string `json:"role"`
}

// structuredBarcodePattern 是馆藏副本统一使用的条码格式：类型-入库年份-书目 ID-四位副本序号。
var structuredBarcodePattern = regexp.MustCompile(`^(BK|MC)-[0-9]{4}-[0-9]+-[0-9]{4}$`)

// apiError 是 React 客户端依赖的稳定 JSON 错误结构。
type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// NewServer 创建默认会话有效期为十二小时的 HTTP API。
func NewServer(db *sql.DB) *Server { return &Server{DB: db, SessionTTL: 12 * time.Hour} }

// Handler 注册全部路由，并在路由边界应用鉴权。
// 权限必须由服务端判断，隐藏前端按钮不能作为访问控制。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /readyz", s.readyz)
	mux.HandleFunc("POST /api/v1/auth/login", s.login)
	mux.HandleFunc("POST /api/v1/auth/logout", s.requireAuth(s.logout))
	mux.HandleFunc("GET /api/v1/auth/me", s.requireAuth(s.me))
	mux.HandleFunc("POST /api/v1/auth/change-password", s.requireAuth(s.changePassword))

	mux.HandleFunc("GET /api/v1/users", s.requireRole(model.RoleAdmin, s.listUsers))
	mux.HandleFunc("POST /api/v1/users", s.requireRole(model.RoleAdmin, s.createUser))
	mux.HandleFunc("PATCH /api/v1/users/{id}", s.requireRole(model.RoleAdmin, s.updateUser))
	mux.HandleFunc("POST /api/v1/users/{id}/force-reset-password", s.requireRole(model.RoleAdmin, s.forceResetPassword))

	mux.HandleFunc("GET /api/v1/categories", s.requireStaff(s.listCategories))
	mux.HandleFunc("POST /api/v1/categories", s.requireStaff(s.createCategory))
	mux.HandleFunc("GET /api/v1/authors", s.requireStaff(s.listAuthors))
	mux.HandleFunc("GET /api/v1/books", s.requireStaff(s.listBooks))
	mux.HandleFunc("POST /api/v1/books", s.requireStaff(s.createBook))
	mux.HandleFunc("PATCH /api/v1/books/{id}", s.requireStaff(s.updateBook))
	mux.HandleFunc("GET /api/v1/book-copies", s.requireStaff(s.listCopies))
	mux.HandleFunc("POST /api/v1/book-copies", s.requireStaff(s.createCopy))
	mux.HandleFunc("PATCH /api/v1/book-copies/{id}", s.requireStaff(s.updateCopy))
	mux.HandleFunc("POST /api/v1/book-copies/{id}/circulation-status", s.requireStaff(s.changeCopyCirculationStatus))

	mux.HandleFunc("GET /api/v1/readers", s.requireStaff(s.listReaders))
	mux.HandleFunc("POST /api/v1/readers", s.requireStaff(s.createReader))
	mux.HandleFunc("PATCH /api/v1/readers/{id}", s.requireStaff(s.updateReader))

	mux.HandleFunc("GET /api/v1/loans", s.requireStaff(s.listLoans))
	mux.HandleFunc("POST /api/v1/loans", s.requireStaff(s.borrow))
	mux.HandleFunc("POST /api/v1/loans/{id}/return", s.requireStaff(s.returnLoan))
	mux.HandleFunc("POST /api/v1/loans/{id}/renew", s.requireStaff(s.renewLoan))
	mux.HandleFunc("POST /api/v1/loans/{id}/lost", s.requireStaff(s.markLoanLost))
	mux.HandleFunc("PATCH /api/v1/loans/{id}/status", s.requireStaff(s.updateLoanStatus))
	mux.HandleFunc("GET /api/v1/reservations", s.requireStaff(s.listReservations))
	mux.HandleFunc("POST /api/v1/reservations", s.requireStaff(s.createReservation))
	mux.HandleFunc("POST /api/v1/reservations/{id}/cancel", s.requireStaff(s.cancelReservation))
	mux.HandleFunc("GET /api/v1/fines", s.requireStaff(s.listFines))
	mux.HandleFunc("POST /api/v1/fines", s.requireStaff(s.createFine))
	mux.HandleFunc("POST /api/v1/fines/{id}/pay", s.requireStaff(s.payFine))
	mux.HandleFunc("POST /api/v1/fines/{id}/waive", s.requireRole(model.RoleAdmin, s.waiveFine))
	mux.HandleFunc("GET /api/v1/audit-logs", s.requireRole(model.RoleAdmin, s.listAuditLogs))
	mux.HandleFunc("GET /api/v1/dashboard", s.requireStaff(s.dashboard))
	return cors(mux)
}

// cors 仅接受两个本地 Vite 来源，并支持浏览器携带凭据请求。
// 生产部署必须将此开发白名单替换为实际前端来源。
func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin == "http://localhost:5173" || origin == "http://127.0.0.1:5173" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, OPTIONS")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// healthz 仅说明进程能够提供 HTTP 服务，不探测外部依赖。
func (s *Server) healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// readyz 会额外验证 MySQL 可访问后才声明 API 就绪。
func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.DB.PingContext(ctx); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database_unavailable", "database unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

// requireAuth 解析 Bearer 令牌或会话 Cookie，并把身份写入请求上下文。
func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, err := s.authenticate(r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized", "login required")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), userContextKey, u)))
	}
}

// requireRole 将认证与基于角色的路由访问控制组合起来。
func (s *Server) requireRole(roles string, next http.HandlerFunc) http.HandlerFunc {
	return s.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		u := userFromContext(r.Context())
		allowed := false
		for _, role := range strings.Split(roles, ",") {
			if role == u.Role {
				allowed = true
			}
		}
		if !allowed {
			writeError(w, http.StatusForbidden, "forbidden", "insufficient permissions")
			return
		}
		next(w, r)
	})
}

// requireStaff 是流通和编目操作共用的馆员权限策略。
func (s *Server) requireStaff(next http.HandlerFunc) http.HandlerFunc {
	return s.requireRole("admin,librarian", next)
}

// userFromContext 读取 requireAuth 写入上下文的身份。
func userFromContext(ctx context.Context) currentUser {
	u, _ := ctx.Value(userContextKey).(currentUser)
	return u
}

// authenticate 优先读取 Authorization Bearer 令牌，再回退到 HttpOnly Cookie。
// 查询前先哈希令牌，确保数据库不保存可直接重放的会话密钥。
func (s *Server) authenticate(r *http.Request) (currentUser, error) {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if token == "" {
		if cookie, err := r.Cookie("library_session"); err == nil {
			token = cookie.Value
		}
	}
	if token == "" {
		return currentUser{}, errors.New("missing token")
	}
	var u currentUser
	err := s.DB.QueryRowContext(r.Context(), `SELECT u.id, u.username, u.role FROM auth_sessions a JOIN users u ON u.id=a.user_id WHERE a.token_hash=? AND a.revoked_at IS NULL AND a.expires_at > NOW() AND u.is_active=TRUE`, auth.TokenHash(token)).Scan(&u.ID, &u.Username, &u.Role)
	return u, err
}

// login 校验启用中的账号，创建服务端会话并返回原始令牌。
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &in) || strings.TrimSpace(in.Username) == "" {
		return
	}
	var u currentUser
	var hash string
	var active bool
	err := s.DB.QueryRowContext(r.Context(), `SELECT id, username, password_hash, role, is_active FROM users WHERE username=?`, in.Username).Scan(&u.ID, &u.Username, &hash, &u.Role, &active)
	if err != nil || !active || auth.VerifyPassword(hash, in.Password) != nil {
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "用户名或密码错误")
		return
	}
	// auth_sessions 只写入摘要，原始令牌仅在此次响应中交给浏览器。
	raw, hashed, err := auth.NewToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "token_error", "无法创建登录会话")
		return
	}
	expires := time.Now().Add(s.SessionTTL)
	if _, err = s.DB.ExecContext(r.Context(), `INSERT INTO auth_sessions (user_id, token_hash, expires_at) VALUES (?, ?, ?)`, u.ID, hashed, expires); err != nil {
		writeError(w, http.StatusInternalServerError, "session_error", "无法保存登录会话")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "library_session", Value: raw, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, Expires: expires})
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"token": raw, "user": u, "expiresAt": expires}})
}

// logout 在 MySQL 中吊销当前会话，并使对应浏览器 Cookie 过期。
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if token == "" {
		if c, e := r.Cookie("library_session"); e == nil {
			token = c.Value
		}
	}
	if token != "" {
		_, _ = s.DB.ExecContext(r.Context(), `UPDATE auth_sessions SET revoked_at=NOW() WHERE token_hash=?`, auth.TokenHash(token))
	}
	http.SetCookie(w, &http.Cookie{Name: "library_session", Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
	writeJSON(w, http.StatusOK, map[string]any{"data": true})
}

// me 直接返回已认证身份，不再发起数据库查询。
func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"data": userFromContext(r.Context())})
}

// changePassword 要求验证当前密码，并在成功后使全部已有会话失效。
func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	var in struct {
		CurrentPassword string `json:"currentPassword"`
		NewPassword     string `json:"newPassword"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	u := userFromContext(r.Context())
	var hash string
	if err := s.DB.QueryRowContext(r.Context(), `SELECT password_hash FROM users WHERE id=?`, u.ID).Scan(&hash); err != nil || auth.VerifyPassword(hash, in.CurrentPassword) != nil {
		writeError(w, http.StatusBadRequest, "invalid_password", "当前密码错误")
		return
	}
	newHash, err := auth.HashPassword(in.NewPassword)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_password", err.Error())
		return
	}
	if _, err = s.DB.ExecContext(r.Context(), `UPDATE users SET password_hash=? WHERE id=?`, newHash, u.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "update_failed", "密码更新失败")
		return
	}
	// 密码变更后注销所有设备，也包括发起本次请求的会话。
	_, _ = s.DB.ExecContext(r.Context(), `UPDATE auth_sessions SET revoked_at=NOW() WHERE user_id=? AND revoked_at IS NULL`, u.ID)
	writeJSON(w, http.StatusOK, map[string]any{"data": true})
}

// listUsers 向管理员提供账号元数据，但绝不返回密码哈希。
func (s *Server) listUsers(w http.ResponseWriter, r *http.Request) {
	rows, err := s.DB.QueryContext(r.Context(), `SELECT id, username, role, is_active, created_at, updated_at FROM users ORDER BY id DESC`)
	if err != nil {
		writeDBError(w, err)
		return
	}
	defer rows.Close()
	out := []model.User{}
	for rows.Next() {
		var u model.User
		if err := rows.Scan(&u.ID, &u.Username, &u.Role, &u.IsActive, &u.CreatedAt, &u.UpdatedAt); err != nil {
			writeDBError(w, err)
			return
		}
		out = append(out, u)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out})
}

// createUser 在校验初始密码后创建带角色的账号。
func (s *Server) createUser(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Role     string `json:"role"`
		IsActive *bool  `json:"isActive"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.Role == "" {
		in.Role = model.RoleLibrarian
	}
	if in.Role != model.RoleAdmin && in.Role != model.RoleLibrarian {
		writeError(w, 400, "invalid_role", "角色无效")
		return
	}
	hash, err := auth.HashPassword(in.Password)
	if err != nil {
		writeError(w, 400, "invalid_password", err.Error())
		return
	}
	active := true
	if in.IsActive != nil {
		active = *in.IsActive
	}
	res, err := s.DB.ExecContext(r.Context(), `INSERT INTO users (username,password_hash,role,is_active) VALUES (?,?,?,?)`, in.Username, hash, in.Role, active)
	if err != nil {
		writeConflict(w, err)
		return
	}
	id, _ := res.LastInsertId()
	s.audit(r, "user.create", "user", uint64(id), map[string]any{"username": in.Username, "role": in.Role})
	writeJSON(w, http.StatusCreated, map[string]any{"data": map[string]any{"id": id, "username": in.Username, "role": in.Role, "isActive": active}})
}

// updateUser 修改账号元数据，同时保证系统至少保留一个启用的管理员。
func (s *Server) updateUser(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, 400, "invalid_id", "无效 ID")
		return
	}
	var in struct {
		Username *string `json:"username"`
		Role     *string `json:"role"`
		IsActive *bool   `json:"isActive"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	var username, role, oldRole string
	var active, oldActive bool
	if err := s.DB.QueryRowContext(r.Context(), `SELECT username,role,is_active FROM users WHERE id=?`, id).Scan(&username, &role, &active); err != nil {
		writeNotFound(w)
		return
	}
	oldRole, oldActive = role, active
	if in.Username != nil {
		username = strings.TrimSpace(*in.Username)
	}
	if in.Role != nil {
		role = *in.Role
	}
	if in.IsActive != nil {
		active = *in.IsActive
	}
	if role != model.RoleAdmin && role != model.RoleLibrarian {
		writeError(w, 400, "invalid_role", "角色无效")
		return
	}
	// 不允许最后一个启用的管理员被降权或停用，避免系统被锁死。
	if oldRole == model.RoleAdmin && oldActive && (role != model.RoleAdmin || !active) {
		var activeAdmins uint
		if err := s.DB.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM users WHERE role='admin' AND is_active=TRUE`).Scan(&activeAdmins); err != nil {
			writeDBError(w, err)
			return
		}
		if activeAdmins <= 1 {
			writeError(w, 409, "last_admin", "系统必须保留至少一个可用管理员")
			return
		}
	}
	if _, err := s.DB.ExecContext(r.Context(), `UPDATE users SET username=?,role=?,is_active=? WHERE id=?`, username, role, active, id); err != nil {
		writeConflict(w, err)
		return
	}
	s.audit(r, "user.update", "user", id, map[string]any{"username": username, "role": role, "isActive": active})
	writeJSON(w, 200, map[string]any{"data": true})
}

// forceResetPassword 允许管理员设置替换密码，并吊销该账号的所有会话。
func (s *Server) forceResetPassword(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, 400, "invalid_id", "无效 ID")
		return
	}
	var in struct {
		NewPassword string `json:"newPassword"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	hash, err := auth.HashPassword(in.NewPassword)
	if err != nil {
		writeError(w, 400, "invalid_password", err.Error())
		return
	}
	if _, err = s.DB.ExecContext(r.Context(), `UPDATE users SET password_hash=? WHERE id=?`, hash, id); err != nil {
		writeNotFoundOrDB(w, err)
		return
	}
	_, _ = s.DB.ExecContext(r.Context(), `UPDATE auth_sessions SET revoked_at=NOW() WHERE user_id=? AND revoked_at IS NULL`, id)
	s.audit(r, "user.force_reset_password", "user", id, nil)
	writeJSON(w, 200, map[string]any{"data": true})
}

// listCategories 按名称返回分类树节点，供书目编辑时选择。
func (s *Server) listCategories(w http.ResponseWriter, r *http.Request) {
	rows, err := s.DB.QueryContext(r.Context(), `SELECT id,parent_id,name,code,created_at,updated_at FROM categories ORDER BY name`)
	if err != nil {
		writeDBError(w, err)
		return
	}
	defer rows.Close()
	out := []model.Category{}
	for rows.Next() {
		var x model.Category
		if err := rows.Scan(&x.ID, &x.ParentID, &x.Name, &x.Code, &x.CreatedAt, &x.UpdatedAt); err != nil {
			writeDBError(w, err)
			return
		}
		out = append(out, x)
	}
	writeJSON(w, 200, map[string]any{"data": out})
}

// createCategory 创建分类，并把此变更写入审计日志。
func (s *Server) createCategory(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name     string  `json:"name"`
		Code     string  `json:"code"`
		ParentID *uint64 `json:"parentId"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	in.Code = strings.TrimSpace(in.Code)
	if in.Name == "" || in.Code == "" {
		writeError(w, http.StatusBadRequest, "invalid_category", "分类名称和编码均为必填项")
		return
	}
	res, err := s.DB.ExecContext(r.Context(), `INSERT INTO categories(parent_id,name,code) VALUES(?,?,?)`, in.ParentID, in.Name, in.Code)
	if err != nil {
		writeConflict(w, err)
		return
	}
	id, _ := res.LastInsertId()
	s.audit(r, "category.create", "category", uint64(id), nil)
	writeJSON(w, 201, map[string]any{"data": map[string]any{"id": id}})
}

// listAuthors 返回可复用的作者资料。
func (s *Server) listAuthors(w http.ResponseWriter, r *http.Request) {
	limit, offset, ok := parseListWindow(w, r, 200, 500)
	if !ok {
		return
	}
	q := "%" + strings.TrimSpace(r.URL.Query().Get("q")) + "%"
	rows, err := s.DB.QueryContext(r.Context(), `SELECT id,full_name,created_at,updated_at FROM authors WHERE full_name LIKE ? ORDER BY full_name LIMIT ? OFFSET ?`, q, limit, offset)
	if err != nil {
		writeDBError(w, err)
		return
	}
	defer rows.Close()
	out := []model.Author{}
	for rows.Next() {
		var x model.Author
		if err := rows.Scan(&x.ID, &x.FullName, &x.CreatedAt, &x.UpdatedAt); err != nil {
			writeDBError(w, err)
			return
		}
		out = append(out, x)
	}
	writeJSON(w, 200, map[string]any{"data": out})
}

// buildBookPageQuery 只选择当前书目页；副本、作者、出版社和译者在取得页内 ID 后批量补齐。
func buildBookPageQuery(q, author string, categoryID uint64, materialType string, limit, offset int) (string, []any) {
	query := `SELECT b.id,b.work_id,b.category_id,cat.name,b.isbn,b.material_type,b.title,b.publisher,b.publish_year,b.publish_date_raw,
		b.edition_statement,b.language_code,b.summary,b.source_provider,b.source_edition_key,b.source_url,
		b.verification_status,b.collection_status,b.created_at,b.updated_at
		FROM books b
		JOIN categories cat ON cat.id=b.category_id
		WHERE b.collection_status='active'`
	args := make([]any, 0, 8)
	if q != "" {
		if normalizedISBN, valid := normalizeISBN13(q); valid {
			query += ` AND b.normalized_isbn=?`
			args = append(args, normalizedISBN)
		} else {
			pattern := containsLikePattern(q)
			query += ` AND (b.title LIKE ? ESCAPE '=' OR b.isbn LIKE ? ESCAPE '=')`
			args = append(args, pattern, pattern)
		}
	}
	if author != "" {
		query += ` AND EXISTS (
			SELECT 1
			FROM book_authors ba
			JOIN authors a ON a.id=ba.author_id
			WHERE ba.book_id=b.id AND a.full_name LIKE ? ESCAPE '='
		)`
		args = append(args, containsLikePattern(author))
	}
	if categoryID > 0 {
		query += ` AND b.category_id=?`
		args = append(args, categoryID)
	}
	if materialType != "" {
		query += ` AND b.material_type=?`
		args = append(args, materialType)
	}
	query += ` ORDER BY b.id DESC LIMIT ? OFFSET ?`
	args = append(args, limit, offset)
	return query, args
}

// listBooks 支持标题/ISBN、作者、分类和资料类型组合查询，并返回分类名称。
func (s *Server) listBooks(w http.ResponseWriter, r *http.Request) {
	limit, offset, ok := parseListWindow(w, r, 100, 500)
	if !ok {
		return
	}
	authorTerm := strings.TrimSpace(r.URL.Query().Get("author"))
	materialType := strings.TrimSpace(r.URL.Query().Get("materialType"))
	categoryID := uint64(0)
	if raw := strings.TrimSpace(r.URL.Query().Get("categoryId")); raw != "" {
		parsed, parseErr := strconv.ParseUint(raw, 10, 64)
		if parseErr != nil || parsed == 0 {
			writeError(w, http.StatusBadRequest, "invalid_category", "分类筛选参数无效")
			return
		}
		categoryID = parsed
	}
	if materialType != "" && materialType != "book" && materialType != "periodical" {
		writeError(w, http.StatusBadRequest, "invalid_material_type", "资料类型筛选参数无效")
		return
	}
	query, args := buildBookPageQuery(
		strings.TrimSpace(r.URL.Query().Get("q")),
		authorTerm,
		categoryID,
		materialType,
		limit,
		offset,
	)
	rows, err := s.DB.QueryContext(r.Context(), query, args...)
	if err != nil {
		writeDBError(w, err)
		return
	}
	defer rows.Close()
	out := []model.Book{}
	bookIndexes := make(map[uint64]int)
	for rows.Next() {
		var x model.Book
		if err := rows.Scan(&x.ID, &x.WorkID, &x.CategoryID, &x.CategoryName, &x.ISBN, &x.MaterialType, &x.Title, &x.Publisher, &x.PublishYear, &x.PublishDateRaw, &x.EditionStatement, &x.LanguageCode, &x.Summary, &x.SourceProvider, &x.SourceEditionKey, &x.SourceURL, &x.VerificationStatus, &x.CollectionStatus, &x.CreatedAt, &x.UpdatedAt); err != nil {
			writeDBError(w, err)
			return
		}
		x.Authors = []model.Author{}
		x.Publishers = []string{}
		x.Translators = []string{}
		bookIndexes[x.ID] = len(out)
		out = append(out, x)
	}
	if err := rows.Err(); err != nil {
		writeDBError(w, err)
		return
	}
	if err := rows.Close(); err != nil {
		writeDBError(w, err)
		return
	}
	if len(out) > 0 {
		bookIDs := make([]uint64, 0, len(out))
		for _, book := range out {
			bookIDs = append(bookIDs, book.ID)
		}
		placeholders, args := listIDArguments(bookIDs)
		copyRows, copyErr := s.DB.QueryContext(r.Context(), `SELECT book_id,COUNT(*),COALESCE(SUM(status='available'),0) FROM book_copies WHERE book_id IN (`+placeholders+`) GROUP BY book_id`, args...)
		if copyErr != nil {
			writeDBError(w, copyErr)
			return
		}
		for copyRows.Next() {
			var bookID uint64
			var copyCount, available uint
			if err := copyRows.Scan(&bookID, &copyCount, &available); err != nil {
				copyRows.Close()
				writeDBError(w, err)
				return
			}
			if index, exists := bookIndexes[bookID]; exists {
				out[index].CopyCount = copyCount
				out[index].Available = available
			}
		}
		if err := copyRows.Err(); err != nil {
			copyRows.Close()
			writeDBError(w, err)
			return
		}
		if err := copyRows.Close(); err != nil {
			writeDBError(w, err)
			return
		}

		authorRows, authorErr := s.DB.QueryContext(r.Context(), `SELECT ba.book_id,a.id,a.full_name,a.created_at,a.updated_at FROM book_authors ba JOIN authors a ON a.id=ba.author_id WHERE ba.book_id IN (`+placeholders+`) ORDER BY ba.book_id,ba.author_order`, args...)
		if authorErr != nil {
			writeDBError(w, authorErr)
			return
		}
		for authorRows.Next() {
			var bookID uint64
			var author model.Author
			if err := authorRows.Scan(&bookID, &author.ID, &author.FullName, &author.CreatedAt, &author.UpdatedAt); err != nil {
				authorRows.Close()
				writeDBError(w, err)
				return
			}
			if index, exists := bookIndexes[bookID]; exists {
				out[index].Authors = append(out[index].Authors, author)
			}
		}
		if err := authorRows.Err(); err != nil {
			authorRows.Close()
			writeDBError(w, err)
			return
		}
		if err := authorRows.Close(); err != nil {
			writeDBError(w, err)
			return
		}

		publisherRows, publisherErr := s.DB.QueryContext(r.Context(), `SELECT bp.book_id,COALESCE(bp.credited_name,p.name) FROM book_publishers bp JOIN publishers p ON p.id=bp.publisher_id WHERE bp.book_id IN (`+placeholders+`) ORDER BY bp.book_id,bp.publisher_order`, args...)
		if publisherErr != nil {
			writeDBError(w, publisherErr)
			return
		}
		for publisherRows.Next() {
			var bookID uint64
			var name string
			if err := publisherRows.Scan(&bookID, &name); err != nil {
				publisherRows.Close()
				writeDBError(w, err)
				return
			}
			if index, exists := bookIndexes[bookID]; exists {
				out[index].Publishers = append(out[index].Publishers, name)
			}
		}
		if err := publisherRows.Err(); err != nil {
			publisherRows.Close()
			writeDBError(w, err)
			return
		}
		if err := publisherRows.Close(); err != nil {
			writeDBError(w, err)
			return
		}

		translatorRows, translatorErr := s.DB.QueryContext(r.Context(), `SELECT bt.book_id,COALESCE(bt.credited_name,t.full_name) FROM book_translators bt JOIN translators t ON t.id=bt.translator_id WHERE bt.book_id IN (`+placeholders+`) ORDER BY bt.book_id,bt.translator_order`, args...)
		if translatorErr != nil {
			writeDBError(w, translatorErr)
			return
		}
		for translatorRows.Next() {
			var bookID uint64
			var name string
			if err := translatorRows.Scan(&bookID, &name); err != nil {
				translatorRows.Close()
				writeDBError(w, err)
				return
			}
			if index, exists := bookIndexes[bookID]; exists {
				out[index].Translators = append(out[index].Translators, name)
			}
		}
		if err := translatorRows.Err(); err != nil {
			translatorRows.Close()
			writeDBError(w, err)
			return
		}
		if err := translatorRows.Close(); err != nil {
			writeDBError(w, err)
			return
		}
	}
	writeJSON(w, 200, map[string]any{"data": out})
}

// createBook 在一个事务中写入书目和作者关联，避免产生不完整书目。
func (s *Server) createBook(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ISBN         string   `json:"isbn"`
		Title        string   `json:"title"`
		CategoryID   uint64   `json:"categoryId"`
		MaterialType string   `json:"materialType"`
		Publisher    *string  `json:"publisher"`
		PublishYear  *uint    `json:"publishYear"`
		Summary      *string  `json:"summary"`
		AuthorNames  []string `json:"authorNames"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	normalizedISBN, validISBN := normalizeISBN13(in.ISBN)
	in.Title = strings.TrimSpace(in.Title)
	in.MaterialType = strings.TrimSpace(in.MaterialType)
	if in.MaterialType == "" {
		in.MaterialType = "book"
	}
	if in.CategoryID == 0 || !validISBN || in.Title == "" || (in.MaterialType != "book" && in.MaterialType != "periodical") {
		if !validISBN {
			writeError(w, http.StatusBadRequest, "invalid_isbn", "ISBN 必须是校验位正确的 ISBN-10 或 ISBN-13")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid_book", "书名、ISBN、资料类型和分类均为必填项")
		return
	}
	in.ISBN = normalizedISBN
	if in.PublishYear != nil && (*in.PublishYear < 1 || *in.PublishYear > uint(time.Now().Year()+1)) {
		writeError(w, http.StatusBadRequest, "invalid_publish_year", "出版年份超出合理范围")
		return
	}
	if in.Publisher != nil {
		trimmed := strings.TrimSpace(*in.Publisher)
		in.Publisher = &trimmed
	}
	names := make([]string, 0, len(in.AuthorNames))
	seenNames := make(map[string]struct{})
	for _, name := range in.AuthorNames {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, exists := seenNames[name]; !exists {
			names = append(names, name)
			seenNames[name] = struct{}{}
		}
	}
	if len(names) == 0 {
		writeError(w, http.StatusBadRequest, "invalid_authors", "至少录入一位作者")
		return
	}
	tx, err := s.DB.BeginTx(r.Context(), nil)
	if err != nil {
		writeDBError(w, err)
		return
	}
	defer tx.Rollback()
	var categoryExists uint64
	if err = tx.QueryRowContext(r.Context(), `SELECT id FROM categories WHERE id=? FOR SHARE`, in.CategoryID).Scan(&categoryExists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusBadRequest, "invalid_category", "所选分类不存在")
			return
		}
		writeDBError(w, err)
		return
	}
	workResult, err := tx.ExecContext(r.Context(), `INSERT INTO works(preferred_title,first_publish_year) VALUES(?,?)`, in.Title, in.PublishYear)
	if err != nil {
		writeDBError(w, err)
		return
	}
	workID, err := workResult.LastInsertId()
	if err != nil {
		writeDBError(w, err)
		return
	}
	res, err := tx.ExecContext(r.Context(), `INSERT INTO books(category_id,work_id,isbn,normalized_isbn,material_type,title,publisher,publish_year,summary,verification_status,collection_status) VALUES(?,?,?,?,?,?,?,?,?,'unverified','quarantined')`, in.CategoryID, workID, in.ISBN, in.ISBN, in.MaterialType, in.Title, in.Publisher, in.PublishYear, in.Summary)
	if err != nil {
		writeConflict(w, err)
		return
	}
	id, _ := res.LastInsertId()
	for i, name := range names {
		var authorID uint64
		authorErr := tx.QueryRowContext(r.Context(), `SELECT id FROM authors WHERE full_name=? AND source_provider IS NULL ORDER BY id LIMIT 1 FOR UPDATE`, name).Scan(&authorID)
		if errors.Is(authorErr, sql.ErrNoRows) {
			result, insertErr := tx.ExecContext(r.Context(), `INSERT INTO authors(full_name) VALUES(?)`, name)
			if insertErr != nil {
				writeConflict(w, insertErr)
				return
			}
			lastID, lastErr := result.LastInsertId()
			if lastErr != nil {
				writeDBError(w, lastErr)
				return
			}
			authorID = uint64(lastID)
		} else if authorErr != nil {
			writeDBError(w, authorErr)
			return
		}
		if _, err = tx.ExecContext(r.Context(), `INSERT INTO book_authors(book_id,author_id,author_order) VALUES(?,?,?)`, id, authorID, i+1); err != nil {
			writeConflict(w, err)
			return
		}
	}
	if in.Publisher != nil && *in.Publisher != "" {
		publisherID, publisherErr := ensureManualPublisher(r.Context(), tx, *in.Publisher)
		if publisherErr != nil {
			writeDBError(w, publisherErr)
			return
		}
		if _, err = tx.ExecContext(r.Context(), `INSERT INTO book_publishers(book_id,publisher_id,publisher_order,role,credited_name) VALUES(?,?,1,'publisher',?)`, id, publisherID, *in.Publisher); err != nil {
			writeConflict(w, err)
			return
		}
	}
	if err = tx.Commit(); err != nil {
		writeDBError(w, err)
		return
	}
	s.audit(r, "book.create", "book", uint64(id), map[string]any{"verificationStatus": "unverified", "collectionStatus": "quarantined"})
	writeJSON(w, 201, map[string]any{"data": map[string]any{"id": id}})
}

// updateBook 使用 COALESCE 保留请求中未提供的书目字段。
func (s *Server) updateBook(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, 400, "invalid_id", "无效 ID")
		return
	}
	var in struct {
		ISBN         *string `json:"isbn"`
		Title        *string `json:"title"`
		CategoryID   *uint64 `json:"categoryId"`
		MaterialType *string `json:"materialType"`
		Publisher    *string `json:"publisher"`
		PublishYear  *uint   `json:"publishYear"`
		Summary      *string `json:"summary"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.ISBN != nil {
		normalized, valid := normalizeISBN13(*in.ISBN)
		if !valid {
			writeError(w, http.StatusBadRequest, "invalid_isbn", "ISBN 必须是校验位正确的 ISBN-10 或 ISBN-13")
			return
		}
		in.ISBN = &normalized
	}
	if in.Title != nil {
		trimmed := strings.TrimSpace(*in.Title)
		if trimmed == "" {
			writeError(w, http.StatusBadRequest, "invalid_title", "书名不能为空")
			return
		}
		in.Title = &trimmed
	}
	if in.CategoryID != nil && *in.CategoryID == 0 {
		writeError(w, http.StatusBadRequest, "invalid_category", "分类无效")
		return
	}
	if in.MaterialType != nil && *in.MaterialType != "book" && *in.MaterialType != "periodical" {
		writeError(w, http.StatusBadRequest, "invalid_material_type", "资料类型无效")
		return
	}
	if in.PublishYear != nil && (*in.PublishYear < 1 || *in.PublishYear > uint(time.Now().Year()+1)) {
		writeError(w, http.StatusBadRequest, "invalid_publish_year", "出版年份超出合理范围")
		return
	}
	if in.Publisher != nil {
		trimmed := strings.TrimSpace(*in.Publisher)
		in.Publisher = &trimmed
	}
	tx, err := s.DB.BeginTx(r.Context(), nil)
	if err != nil {
		writeDBError(w, err)
		return
	}
	defer tx.Rollback()
	var sourceProvider sql.NullString
	var workID sql.NullInt64
	var currentType string
	if err = tx.QueryRowContext(r.Context(), `SELECT source_provider,work_id,material_type FROM books WHERE id=? FOR UPDATE`, id).Scan(&sourceProvider, &workID, &currentType); err != nil {
		writeNotFoundOrDB(w, err)
		return
	}
	canonicalMetadataChanged := in.ISBN != nil || in.Title != nil || in.MaterialType != nil || in.Publisher != nil || in.PublishYear != nil || in.Summary != nil
	if sourceProvider.Valid && canonicalMetadataChanged {
		writeError(w, http.StatusConflict, "source_catalog_locked", "公开来源书目的版本字段只能通过来源同步更新")
		return
	}
	if in.MaterialType != nil {
		var copyCount uint
		if err := tx.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM book_copies WHERE book_id=?`, id).Scan(&copyCount); err != nil {
			writeDBError(w, err)
			return
		}
		if copyCount > 0 && currentType != *in.MaterialType {
			writeError(w, http.StatusConflict, "material_type_locked", "书目已有实体副本，不能修改资料类型")
			return
		}
	}
	var normalizedISBN any
	if in.ISBN != nil {
		normalizedISBN = *in.ISBN
	}
	_, err = tx.ExecContext(r.Context(), `UPDATE books SET isbn=COALESCE(?,isbn),normalized_isbn=COALESCE(?,normalized_isbn),title=COALESCE(?,title),category_id=COALESCE(?,category_id),material_type=COALESCE(?,material_type),publisher=COALESCE(?,publisher),publish_year=COALESCE(?,publish_year),summary=COALESCE(?,summary) WHERE id=?`, in.ISBN, normalizedISBN, in.Title, in.CategoryID, in.MaterialType, in.Publisher, in.PublishYear, in.Summary, id)
	if err != nil {
		writeConflict(w, err)
		return
	}
	if workID.Valid && (in.Title != nil || in.PublishYear != nil) {
		if _, err = tx.ExecContext(r.Context(), `UPDATE works SET preferred_title=COALESCE(?,preferred_title),first_publish_year=COALESCE(?,first_publish_year) WHERE id=?`, in.Title, in.PublishYear, workID.Int64); err != nil {
			writeDBError(w, err)
			return
		}
	}
	if in.Publisher != nil {
		if _, err = tx.ExecContext(r.Context(), `DELETE FROM book_publishers WHERE book_id=?`, id); err != nil {
			writeDBError(w, err)
			return
		}
		if *in.Publisher != "" {
			publisherID, publisherErr := ensureManualPublisher(r.Context(), tx, *in.Publisher)
			if publisherErr != nil {
				writeDBError(w, publisherErr)
				return
			}
			if _, err = tx.ExecContext(r.Context(), `INSERT INTO book_publishers(book_id,publisher_id,publisher_order,role,credited_name) VALUES(?,?,1,'publisher',?)`, id, publisherID, *in.Publisher); err != nil {
				writeConflict(w, err)
				return
			}
		}
	}
	if err = tx.Commit(); err != nil {
		writeDBError(w, err)
		return
	}
	s.audit(r, "book.update", "book", id, map[string]any{"sourceManaged": sourceProvider.Valid})
	writeJSON(w, 200, map[string]any{"data": true})
}

// buildCopyListQuery 支持按书名、条码和馆藏位置搜索，并让空搜索直接走倒序分页。
func buildCopyListQuery(q string, limit, offset int) (string, []any) {
	query := `SELECT c.id,c.book_id,b.title,b.material_type,c.barcode,c.location,c.status,c.condition_status,c.condition_note,c.acquired_at,c.created_at,c.updated_at FROM book_copies c JOIN books b ON b.id=c.book_id WHERE b.collection_status='active'`
	args := make([]any, 0, 5)
	q = strings.TrimSpace(q)
	if q != "" {
		pattern := containsLikePattern(q)
		query += ` AND (b.title LIKE ? ESCAPE '=' OR c.barcode LIKE ? ESCAPE '=' OR c.location LIKE ? ESCAPE '=')`
		args = append(args, pattern, pattern, pattern)
	}
	query += ` ORDER BY c.id DESC LIMIT ? OFFSET ?`
	args = append(args, limit, offset)
	return query, args
}

// listCopies 返回全部实体副本、所属书目、流通状态和独立的品相状态。
func (s *Server) listCopies(w http.ResponseWriter, r *http.Request) {
	limit, offset, ok := parseListWindow(w, r, 200, 500)
	if !ok {
		return
	}
	query, args := buildCopyListQuery(r.URL.Query().Get("q"), limit, offset)
	rows, err := s.DB.QueryContext(r.Context(), query, args...)
	if err != nil {
		writeDBError(w, err)
		return
	}
	defer rows.Close()
	out := []model.BookCopy{}
	for rows.Next() {
		var x model.BookCopy
		if err := rows.Scan(&x.ID, &x.BookID, &x.BookTitle, &x.MaterialType, &x.Barcode, &x.Location, &x.Status, &x.ConditionStatus, &x.ConditionNote, &x.AcquiredAt, &x.CreatedAt, &x.UpdatedAt); err != nil {
			writeDBError(w, err)
			return
		}
		out = append(out, x)
	}
	writeJSON(w, 200, map[string]any{"data": out})
}

// createCopy 按资料类型、入库年份、书目 ID 和书目内序号自动生成结构化条码。
func (s *Server) createCopy(w http.ResponseWriter, r *http.Request) {
	var in struct {
		BookID          uint64  `json:"bookId"`
		Location        *string `json:"location"`
		ConditionStatus string  `json:"conditionStatus"`
		ConditionNote   *string `json:"conditionNote"`
		AcquiredAt      *string `json:"acquiredAt"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.BookID == 0 {
		writeError(w, http.StatusBadRequest, "invalid_book", "必须选择书目")
		return
	}
	conditionStatus := strings.TrimSpace(in.ConditionStatus)
	if !isValidConditionStatus(conditionStatus) {
		writeError(w, http.StatusBadRequest, "invalid_condition_status", "品相状态必须为完好、轻微磨损、破损或待检查")
		return
	}
	tx, err := s.DB.BeginTx(r.Context(), nil)
	if err != nil {
		writeDBError(w, err)
		return
	}
	defer tx.Rollback()
	var materialType, collectionStatus, verificationStatus string
	if err = tx.QueryRowContext(r.Context(), `SELECT material_type,collection_status,verification_status FROM books WHERE id=? FOR UPDATE`, in.BookID).Scan(&materialType, &collectionStatus, &verificationStatus); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeNotFound(w)
			return
		}
		writeDBError(w, err)
		return
	}
	if collectionStatus != "active" || (verificationStatus != "source_matched" && verificationStatus != "human_verified") {
		writeError(w, http.StatusConflict, "catalog_not_active", "该书目尚未通过来源核验，不能录入实体副本")
		return
	}
	acquiredDate := time.Now()
	if in.AcquiredAt != nil && strings.TrimSpace(*in.AcquiredAt) != "" {
		parsed, parseErr := time.Parse("2006-01-02", strings.TrimSpace(*in.AcquiredAt))
		if parseErr != nil {
			writeError(w, http.StatusBadRequest, "invalid_acquired_at", "入库日期必须为 YYYY-MM-DD")
			return
		}
		acquiredDate = parsed
	}
	var nextSequence uint64
	if err = tx.QueryRowContext(r.Context(), `SELECT COALESCE(MAX(CAST(SUBSTRING_INDEX(barcode,'-',-1) AS UNSIGNED)),0)+1 FROM book_copies WHERE book_id=?`, in.BookID).Scan(&nextSequence); err != nil {
		writeDBError(w, err)
		return
	}
	if nextSequence > 9999 {
		writeError(w, http.StatusConflict, "copy_sequence_exhausted", "同一书目副本序号已达到四位上限")
		return
	}
	prefix := "BK"
	if materialType == "periodical" {
		prefix = "MC"
	}
	barcode := fmt.Sprintf("%s-%04d-%d-%04d", prefix, acquiredDate.Year(), in.BookID, nextSequence)
	if !structuredBarcodePattern.MatchString(barcode) {
		writeError(w, http.StatusInternalServerError, "barcode_generation_failed", "无法生成结构化条码")
		return
	}
	initialStatus := "available"
	if conditionRequiresMaintenance(conditionStatus) {
		initialStatus = "maintenance"
	}
	res, err := tx.ExecContext(r.Context(), `INSERT INTO book_copies(book_id,barcode,location,status,condition_status,condition_note,acquired_at) VALUES(?,?,?,?,?,?,?)`, in.BookID, barcode, in.Location, initialStatus, conditionStatus, in.ConditionNote, acquiredDate.Format("2006-01-02"))
	if err != nil {
		writeConflict(w, err)
		return
	}
	if err = tx.Commit(); err != nil {
		writeDBError(w, err)
		return
	}
	id, _ := res.LastInsertId()
	s.audit(r, "book_copy.create", "book_copy", uint64(id), map[string]any{"barcode": barcode, "materialType": materialType, "conditionStatus": conditionStatus, "status": initialStatus})
	writeJSON(w, 201, map[string]any{"data": map[string]any{"id": id, "barcode": barcode}})
}

// updateCopy 仅更新馆藏位置和品相信息；流通状态只能走受约束的专用流程。
func (s *Server) updateCopy(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, 400, "invalid_id", "无效 ID")
		return
	}
	var in struct {
		Location        *string `json:"location"`
		ConditionStatus *string `json:"conditionStatus"`
		ConditionNote   *string `json:"conditionNote"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.Location == nil && in.ConditionStatus == nil && in.ConditionNote == nil {
		writeError(w, http.StatusBadRequest, "empty_copy_update", "至少需要填写一项馆藏信息")
		return
	}
	if in.ConditionStatus != nil {
		status := strings.TrimSpace(*in.ConditionStatus)
		if !isValidConditionStatus(status) {
			writeError(w, http.StatusBadRequest, "invalid_condition_status", "品相状态必须为完好、轻微磨损、破损或待检查")
			return
		}
		in.ConditionStatus = &status
	}
	tx, err := s.DB.BeginTx(r.Context(), nil)
	if err != nil {
		writeDBError(w, err)
		return
	}
	defer tx.Rollback()
	var currentCirculation string
	var bookID uint64
	if err = tx.QueryRowContext(r.Context(), `SELECT book_id,status FROM book_copies WHERE id=? FOR UPDATE`, id).Scan(&bookID, &currentCirculation); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeNotFound(w)
			return
		}
		writeDBError(w, err)
		return
	}
	res, err := tx.ExecContext(r.Context(), `UPDATE book_copies SET location=COALESCE(?,location),condition_status=COALESCE(?,condition_status),condition_note=COALESCE(?,condition_note) WHERE id=?`, in.Location, in.ConditionStatus, in.ConditionNote, id)
	if err != nil {
		writeConflict(w, err)
		return
	}
	if in.ConditionStatus != nil && conditionRequiresMaintenance(*in.ConditionStatus) {
		switch currentCirculation {
		case "available":
			if _, err = tx.ExecContext(r.Context(), `UPDATE book_copies SET status='maintenance' WHERE id=?`, id); err != nil {
				writeDBError(w, err)
				return
			}
		case "reserved":
			var reservationID uint64
			if err = tx.QueryRowContext(r.Context(), `SELECT id FROM reservations WHERE reserved_copy_id=? AND status='ready' FOR UPDATE`, id).Scan(&reservationID); err != nil {
				writeDBError(w, err)
				return
			}
			if _, err = tx.ExecContext(r.Context(), `UPDATE reservations SET status='waiting',expires_at=NULL,reserved_copy_id=NULL WHERE id=?`, reservationID); err != nil {
				writeDBError(w, err)
				return
			}
			if _, err = tx.ExecContext(r.Context(), `UPDATE book_copies SET status='maintenance' WHERE id=?`, id); err != nil {
				writeDBError(w, err)
				return
			}
			if err = assignNextWaitingReservation(r.Context(), tx, bookID); err != nil {
				writeDBError(w, err)
				return
			}
			if err = renumberReservations(tx, bookID); err != nil {
				writeDBError(w, err)
				return
			}
		}
	}
	if err = tx.Commit(); err != nil {
		writeDBError(w, err)
		return
	}
	_, _ = res.RowsAffected()
	s.audit(r, "book_copy.update", "book_copy", id, map[string]any{"locationUpdated": in.Location != nil, "conditionStatus": in.ConditionStatus, "conditionNoteUpdated": in.ConditionNote != nil})
	writeJSON(w, 200, map[string]any{"data": true})
}

// changeCopyCirculationStatus 仅允许非流通副本在可借和维护间切换、登记盘点遗失或将找回副本转入维护。
// 借出和待取状态由借阅、归还和预约事务维护，不能在馆藏页面手工篡改。
func (s *Server) changeCopyCirculationStatus(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_id", "无效 ID")
		return
	}
	var in struct {
		Status string `json:"status"`
		Reason string `json:"reason"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	target := strings.TrimSpace(in.Status)
	reason := strings.TrimSpace(in.Reason)
	if (target != "available" && target != "maintenance" && target != "lost") || reason == "" {
		writeError(w, http.StatusBadRequest, "invalid_copy_circulation_change", "只能调整为可借、维护或遗失，且必须填写原因")
		return
	}
	if err := s.transitionCopyCirculationStatus(r.Context(), id, target); err != nil {
		writeCopyTransitionError(w, err)
		return
	}
	s.audit(r, "book_copy.circulation_status", "book_copy", id, map[string]any{"status": target, "reason": reason})
	writeJSON(w, http.StatusOK, map[string]any{"data": true})
}

// copyTransitionError 表示副本流通状态调整中可以直接反馈给馆员的业务错误。
type copyTransitionError struct {
	status  int
	code    string
	message string
}

func (e *copyTransitionError) Error() string { return e.message }

func isValidConditionStatus(status string) bool {
	return status == "intact" || status == "minor_wear" || status == "damaged" || status == "pending_inspection"
}

func conditionRequiresMaintenance(status string) bool {
	return status == "damaged" || status == "pending_inspection"
}

// buildReaderPageQuery 先选出读者页，避免列表请求先汇总全部借阅和罚金历史。
func buildReaderPageQuery(q string, limit, offset int) (string, []any) {
	query := `SELECT r.id,r.reader_no,r.full_name,r.phone,r.email,r.status,r.loan_limit,r.created_at,r.updated_at
		FROM readers r`
	args := make([]any, 0, 5)
	if q != "" {
		pattern := containsLikePattern(q)
		query += ` WHERE r.reader_no LIKE ? ESCAPE '=' OR r.full_name LIKE ? ESCAPE '=' OR r.phone LIKE ? ESCAPE '='`
		args = append(args, pattern, pattern, pattern)
	}
	query += ` ORDER BY r.id DESC LIMIT ? OFFSET ?`
	args = append(args, limit, offset)
	return query, args
}

// listReaders 按读者编号或姓名模糊查询读者档案，并只对当前页汇总借阅和未结罚金。
func (s *Server) listReaders(w http.ResponseWriter, r *http.Request) {
	limit, offset, ok := parseListWindow(w, r, 100, 500)
	if !ok {
		return
	}
	query, args := buildReaderPageQuery(strings.TrimSpace(r.URL.Query().Get("q")), limit, offset)
	rows, err := s.DB.QueryContext(r.Context(), query, args...)
	if err != nil {
		writeDBError(w, err)
		return
	}
	out := []model.Reader{}
	readerIndexes := make(map[uint64]int)
	readerIDs := make([]uint64, 0, limit)
	for rows.Next() {
		var x model.Reader
		if err := rows.Scan(&x.ID, &x.ReaderNo, &x.FullName, &x.Phone, &x.Email, &x.Status, &x.LoanLimit, &x.CreatedAt, &x.UpdatedAt); err != nil {
			rows.Close()
			writeDBError(w, err)
			return
		}
		x.RemainingLoanLimit = x.LoanLimit
		x.UnpaidFineAmount = "0.00"
		readerIndexes[x.ID] = len(out)
		readerIDs = append(readerIDs, x.ID)
		out = append(out, x)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		writeDBError(w, err)
		return
	}
	if err := rows.Close(); err != nil {
		writeDBError(w, err)
		return
	}
	if len(out) > 0 {
		placeholders, pageArgs := listIDArguments(readerIDs)
		loanRows, loanErr := s.DB.QueryContext(r.Context(), `SELECT reader_id,COUNT(*) FROM loans WHERE reader_id IN (`+placeholders+`) AND status IN ('active','overdue') GROUP BY reader_id`, pageArgs...)
		if loanErr != nil {
			writeDBError(w, loanErr)
			return
		}
		for loanRows.Next() {
			var readerID uint64
			var activeLoanCount uint
			if err := loanRows.Scan(&readerID, &activeLoanCount); err != nil {
				loanRows.Close()
				writeDBError(w, err)
				return
			}
			if index, exists := readerIndexes[readerID]; exists {
				out[index].ActiveLoanCount = activeLoanCount
				if activeLoanCount >= out[index].LoanLimit {
					out[index].RemainingLoanLimit = 0
				} else {
					out[index].RemainingLoanLimit = out[index].LoanLimit - activeLoanCount
				}
			}
		}
		if err := loanRows.Err(); err != nil {
			loanRows.Close()
			writeDBError(w, err)
			return
		}
		if err := loanRows.Close(); err != nil {
			writeDBError(w, err)
			return
		}

		fineRows, fineErr := s.DB.QueryContext(r.Context(), `SELECT reader_id,COUNT(*),CAST(SUM(amount) AS CHAR) FROM fines WHERE reader_id IN (`+placeholders+`) AND status='unpaid' GROUP BY reader_id`, pageArgs...)
		if fineErr != nil {
			writeDBError(w, fineErr)
			return
		}
		for fineRows.Next() {
			var readerID uint64
			var unpaidFineCount uint
			var unpaidFineAmount string
			if err := fineRows.Scan(&readerID, &unpaidFineCount, &unpaidFineAmount); err != nil {
				fineRows.Close()
				writeDBError(w, err)
				return
			}
			if index, exists := readerIndexes[readerID]; exists {
				out[index].Status = "suspended"
				out[index].FineBlocked = true
				out[index].UnpaidFineCount = unpaidFineCount
				out[index].UnpaidFineAmount = unpaidFineAmount
			}
		}
		if err := fineRows.Err(); err != nil {
			fineRows.Close()
			writeDBError(w, err)
			return
		}
		if err := fineRows.Close(); err != nil {
			writeDBError(w, err)
			return
		}
	}
	writeJSON(w, 200, map[string]any{"data": out})
}

// createReader 创建独立的读者借阅档案，不要求同时创建登录账号。
func (s *Server) createReader(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ReaderNo  string  `json:"readerNo"`
		FullName  string  `json:"fullName"`
		Phone     *string `json:"phone"`
		Email     *string `json:"email"`
		LoanLimit *uint   `json:"loanLimit"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	limit := uint(5)
	if in.LoanLimit != nil {
		limit = *in.LoanLimit
	}
	res, err := s.DB.ExecContext(r.Context(), `INSERT INTO readers(reader_no,full_name,phone,email,loan_limit) VALUES(?,?,?,?,?)`, in.ReaderNo, in.FullName, in.Phone, in.Email, limit)
	if err != nil {
		writeConflict(w, err)
		return
	}
	id, _ := res.LastInsertId()
	s.audit(r, "reader.create", "reader", uint64(id), nil)
	writeJSON(w, 201, map[string]any{"data": map[string]any{"id": id}})
}

// updateReader 允许馆员修改读者联系信息、状态和借阅额度。
func (s *Server) updateReader(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, 400, "invalid_id", "无效 ID")
		return
	}
	var in struct {
		FullName  *string `json:"fullName"`
		Phone     *string `json:"phone"`
		Email     *string `json:"email"`
		Status    *string `json:"status"`
		LoanLimit *uint   `json:"loanLimit"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	res, err := s.DB.ExecContext(r.Context(), `UPDATE readers SET full_name=COALESCE(?,full_name),phone=COALESCE(?,phone),email=COALESCE(?,email),status=COALESCE(?,status),loan_limit=COALESCE(?,loan_limit) WHERE id=?`, in.FullName, in.Phone, in.Email, in.Status, in.LoanLimit, id)
	if err != nil {
		writeConflict(w, err)
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		writeNotFound(w)
		return
	}
	s.audit(r, "reader.update", "reader", id, nil)
	writeJSON(w, 200, map[string]any{"data": true})
}

// buildTextSearchListQuery 为已固定字段的业务列表追加安全参数化搜索和分页。
// 数字关键词额外按主键精确匹配，避免把数值 ID 转成字符串后丢失主键能力。
func buildTextSearchListQuery(baseSQL, orderColumn, q string, textColumns, idColumns []string, limit, offset int) (string, []any) {
	args := make([]any, 0, len(textColumns)+len(idColumns)+2)
	q = strings.TrimSpace(q)
	if q != "" {
		predicates := make([]string, 0, len(textColumns)+len(idColumns))
		pattern := containsLikePattern(q)
		for _, column := range textColumns {
			predicates = append(predicates, column+` LIKE ? ESCAPE '='`)
			args = append(args, pattern)
		}
		idText := strings.TrimSpace(strings.TrimPrefix(q, "#"))
		if id, err := strconv.ParseUint(idText, 10, 64); err == nil && id > 0 {
			for _, column := range idColumns {
				predicates = append(predicates, column+`=?`)
				args = append(args, id)
			}
		}
		baseSQL += ` WHERE (` + strings.Join(predicates, ` OR `) + `)`
	}
	baseSQL += ` ORDER BY ` + orderColumn + ` DESC LIMIT ? OFFSET ?`
	args = append(args, limit, offset)
	return baseSQL, args
}

// buildLoanListQuery 支持按借阅号、读者、书名和副本条码定位借阅历史。
func buildLoanListQuery(q string, limit, offset int) (string, []any) {
	return buildTextSearchListQuery(
		`SELECT l.id,l.reader_id,r.reader_no,l.book_copy_id,c.barcode,b.title,r.full_name,l.borrowed_at,l.due_at,l.returned_at,l.renew_count,CASE WHEN l.status='active' AND l.due_at<NOW() THEN 'overdue' ELSE l.status END FROM loans l JOIN readers r ON r.id=l.reader_id JOIN book_copies c ON c.id=l.book_copy_id JOIN books b ON b.id=c.book_id`,
		"l.id",
		q,
		[]string{"r.reader_no", "r.full_name", "b.title", "c.barcode"},
		[]string{"l.id"},
		limit,
		offset,
	)
}

// listLoans 返回借阅历史；未归还且已过期的记录在查询时显示为逾期。
func (s *Server) listLoans(w http.ResponseWriter, r *http.Request) {
	limit, offset, ok := parseListWindow(w, r, 200, 500)
	if !ok {
		return
	}
	query, args := buildLoanListQuery(r.URL.Query().Get("q"), limit, offset)
	rows, err := s.DB.QueryContext(r.Context(), query, args...)
	if err != nil {
		writeDBError(w, err)
		return
	}
	defer rows.Close()
	out := []model.Loan{}
	for rows.Next() {
		var x model.Loan
		if err := rows.Scan(&x.ID, &x.ReaderID, &x.ReaderNo, &x.BookCopyID, &x.Barcode, &x.BookTitle, &x.ReaderName, &x.BorrowedAt, &x.DueAt, &x.ReturnedAt, &x.RenewCount, &x.Status); err != nil {
			writeDBError(w, err)
			return
		}
		out = append(out, x)
	}
	writeJSON(w, 200, map[string]any{"data": out})
}

// borrow 在同一事务内锁定副本、读者和预约，原子完成借出状态转换。
// 这样并发借书不会让同一副本被借出两次，也不会跳过已就绪的预约。
func (s *Server) borrow(w http.ResponseWriter, r *http.Request) {
	if err := s.expireReservations(r.Context()); err != nil {
		writeDBError(w, err)
		return
	}
	var in struct {
		ReaderID uint64  `json:"readerId"`
		Barcode  string  `json:"barcode"`
		DueAt    *string `json:"dueAt"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	in.Barcode = strings.TrimSpace(in.Barcode)
	if !structuredBarcodePattern.MatchString(in.Barcode) {
		writeError(w, http.StatusBadRequest, "invalid_barcode", "副本条码必须符合 BK/MC-年份-书目ID-四位序号格式")
		return
	}
	tx, err := s.DB.BeginTx(r.Context(), nil)
	if err != nil {
		writeDBError(w, err)
		return
	}
	defer tx.Rollback()
	var status, conditionStatus, collectionStatus, verificationStatus string
	var copyID, bookID uint64
	var title string
	// FOR UPDATE 锁住目标副本，阻止并发请求同时将它借出或预留。
	if err = tx.QueryRowContext(r.Context(), `SELECT c.id,c.book_id,c.status,c.condition_status,b.title,b.collection_status,b.verification_status FROM book_copies c JOIN books b ON b.id=c.book_id WHERE c.barcode=? FOR UPDATE`, in.Barcode).Scan(&copyID, &bookID, &status, &conditionStatus, &title, &collectionStatus, &verificationStatus); err != nil {
		writeNotFound(w)
		return
	}
	if collectionStatus != "active" || (verificationStatus != "source_matched" && verificationStatus != "human_verified") {
		writeError(w, http.StatusConflict, "catalog_not_active", "该副本所属书目尚未通过来源核验，不能借出")
		return
	}
	var readerStatus string
	var fineBlocked bool
	var limit uint
	if err = tx.QueryRowContext(r.Context(), `SELECT status,fine_blocked,loan_limit FROM readers WHERE id=? FOR UPDATE`, in.ReaderID).Scan(&readerStatus, &fineBlocked, &limit); err != nil {
		writeNotFound(w)
		return
	}
	var unpaidFineCount uint
	if err = tx.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM fines WHERE reader_id=? AND status='unpaid'`, in.ReaderID).Scan(&unpaidFineCount); err != nil {
		writeDBError(w, err)
		return
	}
	if unpaidFineCount > 0 {
		if !fineBlocked {
			if _, err = tx.ExecContext(r.Context(), `UPDATE readers SET fine_blocked=TRUE WHERE id=?`, in.ReaderID); err != nil {
				writeDBError(w, err)
				return
			}
			// 冻结标记是读者档案的一部分，即使本次借书被拒也要持久化。
			if err = tx.Commit(); err != nil {
				writeDBError(w, err)
				return
			}
		}
		writeError(w, 409, "reader_fine_blocked", "读者存在未结罚金，暂不能借阅")
		return
	}
	if fineBlocked {
		if _, err = tx.ExecContext(r.Context(), `UPDATE readers SET fine_blocked=FALSE WHERE id=?`, in.ReaderID); err != nil {
			writeDBError(w, err)
			return
		}
	}
	if readerStatus != "active" {
		writeError(w, 409, "reader_not_active", "读者状态不允许借阅")
		return
	}
	var active uint
	if err = tx.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM loans WHERE reader_id=? AND status IN ('active','overdue')`, in.ReaderID).Scan(&active); err != nil {
		writeDBError(w, err)
		return
	}
	if active >= limit {
		writeError(w, 409, "loan_limit_reached", "已达到借阅额度")
		return
	}
	// 只有预约已就绪且条码匹配被分配副本的读者才能办理借出。
	var readyReservationID uint64
	var reservedCopyID *uint64
	readyReservationErr := tx.QueryRowContext(r.Context(), `SELECT id,reserved_copy_id FROM reservations WHERE book_id=? AND reader_id=? AND status='ready' AND expires_at>NOW() FOR UPDATE`, bookID, in.ReaderID).Scan(&readyReservationID, &reservedCopyID)
	if readyReservationErr != nil && !errors.Is(readyReservationErr, sql.ErrNoRows) {
		writeDBError(w, readyReservationErr)
		return
	}
	assignedToReader := readyReservationID > 0 && reservedCopyID != nil && *reservedCopyID == copyID
	if readyReservationID > 0 && !assignedToReader {
		writeError(w, 409, "reservation_copy_required", "该读者有待取副本，请扫描系统分配的副本条码")
		return
	}
	if conditionRequiresMaintenance(conditionStatus) || (status != "available" && !(status == "reserved" && assignedToReader)) {
		writeError(w, 409, "copy_unavailable", "副本不可借")
		return
	}
	// 其他读者已排队时，普通可借副本不能被后来者绕过预约队列。
	var otherReservations uint
	if err = tx.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM reservations WHERE book_id=? AND status IN ('waiting','ready') AND reader_id<>?`, bookID, in.ReaderID).Scan(&otherReservations); err != nil {
		writeDBError(w, err)
		return
	}
	if otherReservations > 0 && readyReservationID == 0 {
		writeError(w, 409, "reservation_pending", "该书目有其他读者预约")
		return
	}
	due := time.Now().AddDate(0, 0, 30)
	if in.DueAt != nil && *in.DueAt != "" {
		if parsed, e := time.Parse(time.RFC3339, *in.DueAt); e == nil {
			due = parsed
		}
	}
	res, err := tx.ExecContext(r.Context(), `INSERT INTO loans(reader_id,book_copy_id,due_at) VALUES(?,?,?)`, in.ReaderID, copyID, due)
	if err != nil {
		writeDBError(w, err)
		return
	}
	if _, err = tx.ExecContext(r.Context(), `UPDATE book_copies SET status='borrowed' WHERE id=?`, copyID); err != nil {
		writeDBError(w, err)
		return
	}
	if readyReservationID > 0 {
		if _, err = tx.ExecContext(r.Context(), `UPDATE reservations SET status='fulfilled',expires_at=NULL,reserved_copy_id=NULL WHERE id=?`, readyReservationID); err != nil {
			writeDBError(w, err)
			return
		}
		if err = assignNextWaitingReservation(r.Context(), tx, bookID); err != nil {
			writeDBError(w, err)
			return
		}
	}
	if err = tx.Commit(); err != nil {
		writeDBError(w, err)
		return
	}
	id, _ := res.LastInsertId()
	s.audit(r, "loan.borrow", "loan", uint64(id), map[string]any{"barcode": in.Barcode, "title": title})
	writeJSON(w, 201, map[string]any{"data": map[string]any{"id": id, "dueAt": due}})
}

// returnLoan 归还副本，并在同一事务中把它交给下一位预约者或恢复可借。
func (s *Server) returnLoan(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, 400, "invalid_id", "无效 ID")
		return
	}
	tx, err := s.DB.BeginTx(r.Context(), nil)
	if err != nil {
		writeDBError(w, err)
		return
	}
	defer tx.Rollback()
	var copyID uint64
	var status string
	if err = tx.QueryRowContext(r.Context(), `SELECT book_copy_id,status FROM loans WHERE id=? FOR UPDATE`, id).Scan(&copyID, &status); err != nil {
		writeNotFound(w)
		return
	}
	if status != "active" && status != "overdue" {
		writeError(w, 409, "loan_closed", "借阅已结束")
		return
	}
	if _, err = tx.ExecContext(r.Context(), `UPDATE loans SET status='returned',returned_at=NOW() WHERE id=?`, id); err != nil {
		writeDBError(w, err)
		return
	}
	// 队首等待预约优先获得刚归还的副本，并拥有三天取书期限。
	if err = setCopyAvailableAfterReturn(r.Context(), tx, copyID); err != nil {
		writeDBError(w, err)
		return
	}
	if err = tx.Commit(); err != nil {
		writeDBError(w, err)
		return
	}
	s.audit(r, "loan.return", "loan", id, nil)
	writeJSON(w, 200, map[string]any{"data": true})
}

// setCopyAvailableAfterReturn 在归还或状态修正为已还时，按品相决定副本进入可借、待取或维护。
func setCopyAvailableAfterReturn(ctx context.Context, tx *sql.Tx, copyID uint64) error {
	var bookID uint64
	var conditionStatus string
	if err := tx.QueryRowContext(ctx, `SELECT book_id,condition_status FROM book_copies WHERE id=? FOR UPDATE`, copyID).Scan(&bookID, &conditionStatus); err != nil {
		return err
	}
	if conditionRequiresMaintenance(conditionStatus) {
		if _, err := tx.ExecContext(ctx, `UPDATE book_copies SET status='maintenance' WHERE id=?`, copyID); err != nil {
			return err
		}
		return assignNextWaitingReservation(ctx, tx, bookID)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE book_copies SET status='available' WHERE id=?`, copyID); err != nil {
		return err
	}
	return assignNextWaitingReservation(ctx, tx, bookID)
}

// assignNextWaitingReservation 为一本书的队首等待预约分配一册品相合格的可借副本。
// 每本书同时只有一条待取预约，保持原有队列规则并避免多位读者争抢同一书目。
func assignNextWaitingReservation(ctx context.Context, tx *sql.Tx, bookID uint64) error {
	var collectionStatus string
	if err := tx.QueryRowContext(ctx, `SELECT collection_status FROM books WHERE id=? FOR SHARE`, bookID).Scan(&collectionStatus); err != nil {
		return err
	}
	if collectionStatus != "active" {
		return nil
	}
	var readyID uint64
	err := tx.QueryRowContext(ctx, `SELECT id FROM reservations WHERE book_id=? AND status='ready' ORDER BY queue_position,id LIMIT 1 FOR UPDATE`, bookID).Scan(&readyID)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var reservationID uint64
	err = tx.QueryRowContext(ctx, `SELECT id FROM reservations WHERE book_id=? AND status='waiting' ORDER BY queue_position,id LIMIT 1 FOR UPDATE`, bookID).Scan(&reservationID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var copyID uint64
	err = tx.QueryRowContext(ctx, `SELECT id FROM book_copies WHERE book_id=? AND status='available' AND condition_status IN ('intact','minor_wear') ORDER BY id LIMIT 1 FOR UPDATE`, bookID).Scan(&copyID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE book_copies SET status='reserved' WHERE id=?`, copyID); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE reservations SET status='ready',expires_at=DATE_ADD(NOW(),INTERVAL 3 DAY),reserved_copy_id=? WHERE id=?`, copyID, reservationID)
	return err
}

// loanTransitionError 表示状态修正时可直接返回给馆员的业务错误。
type loanTransitionError struct {
	status  int
	code    string
	message string
}

func (e *loanTransitionError) Error() string { return e.message }

// markLoanLost 将借阅标记为遗失，并在同一事务内创建遗失赔偿罚金、冻结读者和更新副本。
func (s *Server) markLoanLost(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_id", "无效 ID")
		return
	}
	var in struct {
		Amount float64 `json:"amount"`
		Reason string  `json:"reason"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.Amount <= 0 || strings.TrimSpace(in.Reason) == "" {
		writeError(w, http.StatusBadRequest, "invalid_lost_fine", "遗失赔偿金额必须大于 0，原因不能为空")
		return
	}
	if err := s.changeLoanStatus(r.Context(), id, "lost", &in.Amount, strings.TrimSpace(in.Reason)); err != nil {
		writeLoanTransitionError(w, err)
		return
	}
	s.audit(r, "loan.lost", "loan", id, map[string]any{"amount": in.Amount, "reason": strings.TrimSpace(in.Reason)})
	writeJSON(w, http.StatusOK, map[string]any{"data": true})
}

// updateLoanStatus 允许馆员在确认后手动修正借阅状态，避免误操作留下孤立的副本或罚金。
func (s *Server) updateLoanStatus(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_id", "无效 ID")
		return
	}
	var in struct {
		Status string   `json:"status"`
		Amount *float64 `json:"amount"`
		Reason string   `json:"reason"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	target := strings.TrimSpace(in.Status)
	if target != "active" && target != "overdue" && target != "returned" && target != "lost" {
		writeError(w, http.StatusBadRequest, "invalid_loan_status", "借阅状态不受支持")
		return
	}
	if target == "lost" && (in.Amount == nil || *in.Amount <= 0 || strings.TrimSpace(in.Reason) == "") {
		writeError(w, http.StatusBadRequest, "invalid_lost_fine", "将借阅修正为遗失时，遗失赔偿金额必须大于 0，原因不能为空")
		return
	}
	if err := s.changeLoanStatus(r.Context(), id, target, in.Amount, strings.TrimSpace(in.Reason)); err != nil {
		writeLoanTransitionError(w, err)
		return
	}
	s.audit(r, "loan.status_update", "loan", id, map[string]any{"status": target})
	writeJSON(w, http.StatusOK, map[string]any{"data": true})
}

// changeLoanStatus 统一维护 loans、book_copies、fines、readers 四张表的状态一致性。
func (s *Server) changeLoanStatus(ctx context.Context, id uint64, target string, amount *float64, reason string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var readerID, copyID uint64
	var current string
	if err = tx.QueryRowContext(ctx, `SELECT reader_id,book_copy_id,status FROM loans WHERE id=? FOR UPDATE`, id).Scan(&readerID, &copyID, &current); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return &loanTransitionError{status: http.StatusNotFound, code: "not_found", message: "借阅记录不存在"}
		}
		return err
	}
	// 锁定读者和副本，保证状态修正与借书/归还不会交叉覆盖。
	var readerStatus string
	if err = tx.QueryRowContext(ctx, `SELECT status FROM readers WHERE id=? FOR UPDATE`, readerID).Scan(&readerStatus); err != nil {
		return err
	}
	var copyStatus, conditionStatus string
	if err = tx.QueryRowContext(ctx, `SELECT status,condition_status FROM book_copies WHERE id=? FOR UPDATE`, copyID).Scan(&copyStatus, &conditionStatus); err != nil {
		return err
	}
	switch target {
	case "lost":
		if current != "active" && current != "overdue" && current != "lost" {
			return &loanTransitionError{status: http.StatusConflict, code: "loan_closed", message: "只有在借或逾期的借阅可以登记遗失；已归还副本请走馆藏盘点遗失流程"}
		}
		if current != "active" && current != "overdue" {
			if copyStatus == "reserved" {
				return &loanTransitionError{status: http.StatusConflict, code: "copy_reserved", message: "副本当前已被预约占用，请先处理预约后再修正"}
			}
			var conflicting uint
			if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM loans WHERE book_copy_id=? AND id<>? AND status IN ('active','overdue')`, copyID, id).Scan(&conflicting); err != nil {
				return err
			}
			if conflicting > 0 || copyStatus == "borrowed" {
				return &loanTransitionError{status: http.StatusConflict, code: "copy_in_use", message: "该副本已有其他在借记录"}
			}
		}
		lostAmount := 30.0
		if amount != nil {
			lostAmount = *amount
		}
		lostReason := strings.TrimSpace(reason)
		if lostReason == "" {
			lostReason = "图书遗失赔偿"
		}
		if _, err = tx.ExecContext(ctx, `UPDATE loans SET status='lost',returned_at=NULL WHERE id=?`, id); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE book_copies SET status='lost' WHERE id=?`, copyID); err != nil {
			return err
		}
		// 同一借阅只允许一笔未结遗失罚金；重复登记当前遗失状态不会重复计费。
		var fineID uint64
		fineErr := tx.QueryRowContext(ctx, `SELECT id FROM fines WHERE loan_id=? AND origin='lost' AND status='unpaid' ORDER BY id LIMIT 1 FOR UPDATE`, id).Scan(&fineID)
		if errors.Is(fineErr, sql.ErrNoRows) && current != "lost" {
			if _, err = tx.ExecContext(ctx, `INSERT INTO fines(loan_id,reader_id,amount,reason,origin,status) VALUES(?,?,?,?,?, 'unpaid')`, id, readerID, lostAmount, lostReason, "lost"); err != nil {
				return err
			}
		} else if fineErr != nil {
			return fineErr
		}
		if _, err = tx.ExecContext(ctx, `UPDATE readers SET fine_blocked=TRUE WHERE id=?`, readerID); err != nil {
			return err
		}
	case "returned":
		if _, err = tx.ExecContext(ctx, `UPDATE loans SET status='returned',returned_at=COALESCE(returned_at,NOW()) WHERE id=?`, id); err != nil {
			return err
		}
		if err = setCopyAvailableAfterReturn(ctx, tx, copyID); err != nil {
			return err
		}
		if err = waiveIncorrectLostFine(ctx, tx, id, readerID); err != nil {
			return err
		}
	case "active", "overdue":
		if copyStatus == "reserved" {
			return &loanTransitionError{status: http.StatusConflict, code: "copy_reserved", message: "副本当前已被预约占用，请先处理预约后再修正"}
		}
		if conditionRequiresMaintenance(conditionStatus) {
			return &loanTransitionError{status: http.StatusConflict, code: "condition_requires_maintenance", message: "破损或待检查的副本不能修正为在借状态"}
		}
		var conflicting uint
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM loans WHERE book_copy_id=? AND id<>? AND status IN ('active','overdue')`, copyID, id).Scan(&conflicting); err != nil {
			return err
		}
		if conflicting > 0 {
			return &loanTransitionError{status: http.StatusConflict, code: "copy_in_use", message: "该副本已有其他在借记录"}
		}
		if _, err = tx.ExecContext(ctx, `UPDATE loans SET status=?,returned_at=NULL WHERE id=?`, target, id); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE book_copies SET status='borrowed' WHERE id=?`, copyID); err != nil {
			return err
		}
		if err = waiveIncorrectLostFine(ctx, tx, id, readerID); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	_ = readerStatus
	return nil
}

// changeCopyCirculationStatus 在馆藏盘点场景维护非流通副本的状态；不会触碰读者罚金。
func (s *Server) transitionCopyCirculationStatus(ctx context.Context, id uint64, target string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var current, conditionStatus string
	var bookID uint64
	if err = tx.QueryRowContext(ctx, `SELECT book_id,status,condition_status FROM book_copies WHERE id=? FOR UPDATE`, id).Scan(&bookID, &current, &conditionStatus); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return &copyTransitionError{status: http.StatusNotFound, code: "not_found", message: "实体副本不存在"}
		}
		return err
	}
	if current == "borrowed" {
		return &copyTransitionError{status: http.StatusConflict, code: "copy_borrowed", message: "借出中的副本必须从借阅记录登记遗失或归还"}
	}
	if target == "available" {
		if current != "maintenance" {
			return &copyTransitionError{status: http.StatusConflict, code: "invalid_copy_transition", message: "只有维护中的副本可以恢复为可借"}
		}
		if conditionRequiresMaintenance(conditionStatus) {
			return &copyTransitionError{status: http.StatusConflict, code: "condition_requires_maintenance", message: "破损或待检查的副本必须先更新品相状态，才能恢复可借"}
		}
		if _, err = tx.ExecContext(ctx, `UPDATE book_copies SET status='available' WHERE id=?`, id); err != nil {
			return err
		}
		if err = assignNextWaitingReservation(ctx, tx, bookID); err != nil {
			return err
		}
	} else if target == "maintenance" {
		if current != "available" && current != "lost" {
			return &copyTransitionError{status: http.StatusConflict, code: "invalid_copy_transition", message: "只有可借或遗失副本可以转入维护"}
		}
		if _, err = tx.ExecContext(ctx, `UPDATE book_copies SET status='maintenance' WHERE id=?`, id); err != nil {
			return err
		}
	} else {
		if current == "reserved" {
			var reservationID uint64
			if err = tx.QueryRowContext(ctx, `SELECT id FROM reservations WHERE reserved_copy_id=? AND status='ready' FOR UPDATE`, id).Scan(&reservationID); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return &copyTransitionError{status: http.StatusConflict, code: "reservation_assignment_missing", message: "待取副本缺少预约分配，无法安全登记遗失"}
				}
				return err
			}
			if _, err = tx.ExecContext(ctx, `UPDATE reservations SET status='waiting',expires_at=NULL,reserved_copy_id=NULL WHERE id=?`, reservationID); err != nil {
				return err
			}
		} else if current != "available" && current != "maintenance" && current != "lost" {
			return &copyTransitionError{status: http.StatusConflict, code: "invalid_copy_transition", message: "当前副本状态不允许登记馆藏盘点遗失"}
		}
		if _, err = tx.ExecContext(ctx, `UPDATE book_copies SET status='lost' WHERE id=?`, id); err != nil {
			return err
		}
		if err = assignNextWaitingReservation(ctx, tx, bookID); err != nil {
			return err
		}
	}
	if err = renumberReservations(tx, bookID); err != nil {
		return err
	}
	return tx.Commit()
}

// waiveIncorrectLostFine 在遗失误操作被修正后，自动减免未缴的系统遗失赔偿；已缴历史不会被篡改。
func waiveIncorrectLostFine(ctx context.Context, tx *sql.Tx, loanID, readerID uint64) error {
	if _, err := tx.ExecContext(ctx, `UPDATE fines SET status='waived',paid_at=NULL WHERE loan_id=? AND origin='lost' AND status='unpaid'`, loanID); err != nil {
		return err
	}
	var unpaid uint
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM fines WHERE reader_id=? AND status='unpaid'`, readerID).Scan(&unpaid); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE readers SET fine_blocked=? WHERE id=?`, unpaid > 0, readerID)
	return err
}

// writeLoanTransitionError 将状态修正的业务错误转成统一 API 响应。
func writeLoanTransitionError(w http.ResponseWriter, err error) {
	var transitionErr *loanTransitionError
	if errors.As(err, &transitionErr) {
		writeError(w, transitionErr.status, transitionErr.code, transitionErr.message)
		return
	}
	writeDBError(w, err)
}

// writeCopyTransitionError 将受约束的副本状态操作转换为统一 API 响应。
func writeCopyTransitionError(w http.ResponseWriter, err error) {
	var transitionErr *copyTransitionError
	if errors.As(err, &transitionErr) {
		writeError(w, transitionErr.status, transitionErr.code, transitionErr.message)
		return
	}
	writeDBError(w, err)
}

// renewLoan 仅允许未逾期且未到续借上限的借阅续期；存在预约时拒绝续借。
func (s *Server) renewLoan(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, 400, "invalid_id", "无效 ID")
		return
	}
	tx, err := s.DB.BeginTx(r.Context(), nil)
	if err != nil {
		writeDBError(w, err)
		return
	}
	defer tx.Rollback()
	var readerID, bookID uint64
	var due time.Time
	var renew uint
	var status, collectionStatus string
	if err = tx.QueryRowContext(r.Context(), `SELECT l.reader_id,c.book_id,l.due_at,l.renew_count,l.status,b.collection_status FROM loans l JOIN book_copies c ON c.id=l.book_copy_id JOIN books b ON b.id=c.book_id WHERE l.id=? FOR UPDATE`, id).Scan(&readerID, &bookID, &due, &renew, &status, &collectionStatus); err != nil {
		writeNotFound(w)
		return
	}
	if collectionStatus != "active" {
		writeError(w, http.StatusConflict, "catalog_not_active", "该借阅所属书目已被隔离，不能续借")
		return
	}
	if status != "active" || due.Before(time.Now()) {
		writeError(w, 409, "renew_not_allowed", "当前借阅不可续借")
		return
	}
	var readerStatus string
	var fineBlocked bool
	if err = tx.QueryRowContext(r.Context(), `SELECT status,fine_blocked FROM readers WHERE id=? FOR UPDATE`, readerID).Scan(&readerStatus, &fineBlocked); err != nil {
		writeDBError(w, err)
		return
	}
	var unpaidFineCount uint
	if err = tx.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM fines WHERE reader_id=? AND status='unpaid'`, readerID).Scan(&unpaidFineCount); err != nil {
		writeDBError(w, err)
		return
	}
	if unpaidFineCount > 0 {
		if !fineBlocked {
			if _, err = tx.ExecContext(r.Context(), `UPDATE readers SET fine_blocked=TRUE WHERE id=?`, readerID); err != nil {
				writeDBError(w, err)
				return
			}
			// 冻结标记是读者档案的一部分，即使本次续借被拒也要持久化。
			if err = tx.Commit(); err != nil {
				writeDBError(w, err)
				return
			}
		}
		writeError(w, 409, "reader_fine_blocked", "读者存在未结罚金，暂不能续借")
		return
	}
	if fineBlocked {
		if _, err = tx.ExecContext(r.Context(), `UPDATE readers SET fine_blocked=FALSE WHERE id=?`, readerID); err != nil {
			writeDBError(w, err)
			return
		}
	}
	if readerStatus != "active" {
		writeError(w, 409, "reader_not_active", "读者状态不允许续借")
		return
	}
	if renew >= 2 {
		writeError(w, 409, "renew_limit_reached", "已达到续借上限")
		return
	}
	var waiting uint
	if err = tx.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM reservations WHERE book_id=? AND status IN ('waiting','ready')`, bookID).Scan(&waiting); err != nil {
		writeDBError(w, err)
		return
	}
	if waiting > 0 {
		writeError(w, 409, "reservation_pending", "存在预约，不能续借")
		return
	}
	newDue := due.AddDate(0, 0, 30)
	if _, err = tx.ExecContext(r.Context(), `UPDATE loans SET due_at=?,renew_count=renew_count+1 WHERE id=?`, newDue, id); err != nil {
		writeDBError(w, err)
		return
	}
	if err = tx.Commit(); err != nil {
		writeDBError(w, err)
		return
	}
	s.audit(r, "loan.renew", "loan", id, nil)
	writeJSON(w, 200, map[string]any{"data": map[string]any{"dueAt": newDue}})
}

// buildReservationListQuery 支持按预约号、读者、书目和已分配副本条码搜索。
func buildReservationListQuery(q string, limit, offset int) (string, []any) {
	baseSQL := `SELECT x.id,x.reader_id,r.reader_no,r.full_name,x.book_id,b.title,x.queue_position,x.status,x.expires_at,x.created_at FROM reservations x JOIN readers r ON r.id=x.reader_id JOIN books b ON b.id=x.book_id`
	statusLabel := `CASE x.status WHEN 'waiting' THEN '排队中' WHEN 'ready' THEN '待取' WHEN 'fulfilled' THEN '已完成' WHEN 'cancelled' THEN '已取消' WHEN 'expired' THEN '已过期' ELSE x.status END`
	textColumns := []string{"r.reader_no", "r.full_name", "b.title", "x.status", statusLabel}
	if strings.TrimSpace(q) != "" {
		baseSQL += ` LEFT JOIN book_copies c ON c.id=x.reserved_copy_id`
		textColumns = append(textColumns, "c.barcode")
	}
	return buildTextSearchListQuery(baseSQL, "x.id", q, textColumns, []string{"x.id"}, limit, offset)
}

// listReservations 先处理已过期的待取预约，再返回当前队列。
func (s *Server) listReservations(w http.ResponseWriter, r *http.Request) {
	if err := s.expireReservations(r.Context()); err != nil {
		writeDBError(w, err)
		return
	}
	limit, offset, ok := parseListWindow(w, r, 200, 500)
	if !ok {
		return
	}
	query, args := buildReservationListQuery(r.URL.Query().Get("q"), limit, offset)
	rows, err := s.DB.QueryContext(r.Context(), query, args...)
	if err != nil {
		writeDBError(w, err)
		return
	}
	defer rows.Close()
	out := []model.Reservation{}
	for rows.Next() {
		var x model.Reservation
		if err := rows.Scan(&x.ID, &x.ReaderID, &x.ReaderNo, &x.ReaderName, &x.BookID, &x.BookTitle, &x.QueuePosition, &x.Status, &x.ExpiresAt, &x.CreatedAt); err != nil {
			writeDBError(w, err)
			return
		}
		out = append(out, x)
	}
	writeJSON(w, 200, map[string]any{"data": out})
}

// createReservation 为活跃读者建立书目预约，并在有可借副本且队列为空时立即置为待取。
func (s *Server) createReservation(w http.ResponseWriter, r *http.Request) {
	if err := s.expireReservations(r.Context()); err != nil {
		writeDBError(w, err)
		return
	}
	var in struct {
		ReaderID uint64 `json:"readerId"`
		BookID   uint64 `json:"bookId"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	tx, err := s.DB.BeginTx(r.Context(), nil)
	if err != nil {
		writeDBError(w, err)
		return
	}
	defer tx.Rollback()
	var status string
	var fineBlocked bool
	if err := tx.QueryRowContext(r.Context(), `SELECT status,fine_blocked FROM readers WHERE id=? FOR UPDATE`, in.ReaderID).Scan(&status, &fineBlocked); err != nil {
		writeNotFound(w)
		return
	}
	var unpaidFineCount uint
	if err := tx.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM fines WHERE reader_id=? AND status='unpaid'`, in.ReaderID).Scan(&unpaidFineCount); err != nil {
		writeDBError(w, err)
		return
	}
	if unpaidFineCount > 0 {
		if !fineBlocked {
			if _, err := tx.ExecContext(r.Context(), `UPDATE readers SET fine_blocked=TRUE WHERE id=?`, in.ReaderID); err != nil {
				writeDBError(w, err)
				return
			}
			// 冻结标记是读者档案的一部分，即使本次预约被拒也要持久化。
			if err := tx.Commit(); err != nil {
				writeDBError(w, err)
				return
			}
		}
		writeError(w, 409, "reader_fine_blocked", "读者存在未结罚金，暂不能预约")
		return
	}
	if fineBlocked {
		if _, err := tx.ExecContext(r.Context(), `UPDATE readers SET fine_blocked=FALSE WHERE id=?`, in.ReaderID); err != nil {
			writeDBError(w, err)
			return
		}
	}
	if status != "active" {
		writeError(w, 409, "reader_not_active", "读者状态不允许预约")
		return
	}
	var existing uint
	if err := tx.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM reservations WHERE reader_id=? AND book_id=? AND status IN ('waiting','ready')`, in.ReaderID, in.BookID).Scan(&existing); err != nil {
		writeDBError(w, err)
		return
	}
	if existing > 0 {
		writeError(w, 409, "reservation_exists", "已存在预约")
		return
	}
	var position uint
	// 锁住书目行，串行化同一书目的队列位置计算和首个副本分配。
	var collectionStatus, verificationStatus string
	if err := tx.QueryRowContext(r.Context(), `SELECT collection_status,verification_status FROM books WHERE id=? FOR UPDATE`, in.BookID).Scan(&collectionStatus, &verificationStatus); err != nil {
		writeNotFound(w)
		return
	}
	if collectionStatus != "active" || (verificationStatus != "source_matched" && verificationStatus != "human_verified") {
		writeError(w, http.StatusConflict, "catalog_not_active", "该书目尚未通过来源核验，不能预约")
		return
	}
	if err := tx.QueryRowContext(r.Context(), `SELECT COALESCE(MAX(queue_position),0)+1 FROM reservations WHERE book_id=? AND status IN ('waiting','ready')`, in.BookID).Scan(&position); err != nil {
		writeDBError(w, err)
		return
	}
	reservationStatus := "waiting"
	var copyID uint64
	// 仅队首可以立即占用可借副本；后续读者保持 waiting 状态。
	if position == 1 {
		if err := tx.QueryRowContext(r.Context(), `SELECT id FROM book_copies WHERE book_id=? AND status='available' AND condition_status IN ('intact','minor_wear') ORDER BY id LIMIT 1 FOR UPDATE`, in.BookID).Scan(&copyID); err == nil {
			reservationStatus = "ready"
		} else if !errors.Is(err, sql.ErrNoRows) {
			writeDBError(w, err)
			return
		}
	}
	var expiresAt any
	if reservationStatus == "ready" {
		expiresAt = time.Now().AddDate(0, 0, 3)
	}
	var reservedCopyID any
	if copyID > 0 {
		reservedCopyID = copyID
	}
	res, err := tx.ExecContext(r.Context(), `INSERT INTO reservations(reader_id,book_id,reserved_copy_id,queue_position,status,expires_at) VALUES(?,?,?,?,?,?)`, in.ReaderID, in.BookID, reservedCopyID, position, reservationStatus, expiresAt)
	if err != nil {
		writeConflict(w, err)
		return
	}
	if copyID > 0 {
		if _, err = tx.ExecContext(r.Context(), `UPDATE book_copies SET status='reserved' WHERE id=?`, copyID); err != nil {
			writeDBError(w, err)
			return
		}
	}
	if err = tx.Commit(); err != nil {
		writeDBError(w, err)
		return
	}
	id, _ := res.LastInsertId()
	s.audit(r, "reservation.create", "reservation", uint64(id), nil)
	writeJSON(w, 201, map[string]any{"data": map[string]any{"id": id, "queuePosition": position}})
}

// cancelReservation 取消未完成预约，并在取消待取预约时转交或释放被占用副本。
func (s *Server) cancelReservation(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, 400, "invalid_id", "无效 ID")
		return
	}
	tx, err := s.DB.BeginTx(r.Context(), nil)
	if err != nil {
		writeDBError(w, err)
		return
	}
	defer tx.Rollback()
	var bookID uint64
	var status string
	var reservedCopyID *uint64
	if err = tx.QueryRowContext(r.Context(), `SELECT book_id,status,reserved_copy_id FROM reservations WHERE id=? FOR UPDATE`, id).Scan(&bookID, &status, &reservedCopyID); err != nil || (status != "waiting" && status != "ready") {
		writeNotFound(w)
		return
	}
	if _, err = tx.ExecContext(r.Context(), `UPDATE reservations SET status='cancelled',expires_at=NULL,reserved_copy_id=NULL WHERE id=?`, id); err != nil {
		writeDBError(w, err)
		return
	}
	// 取消待取预约后，已预留的副本必须转交下一位或变回可借。
	if status == "ready" {
		if reservedCopyID == nil {
			writeDBError(w, errors.New("待取预约缺少副本分配"))
			return
		}
		var conditionStatus string
		if err = tx.QueryRowContext(r.Context(), `SELECT condition_status FROM book_copies WHERE id=? FOR UPDATE`, *reservedCopyID).Scan(&conditionStatus); err != nil {
			writeDBError(w, err)
			return
		}
		nextStatus := "available"
		if conditionRequiresMaintenance(conditionStatus) {
			nextStatus = "maintenance"
		}
		if _, err = tx.ExecContext(r.Context(), `UPDATE book_copies SET status=? WHERE id=?`, nextStatus, *reservedCopyID); err != nil {
			writeDBError(w, err)
			return
		}
		if err = assignNextWaitingReservation(r.Context(), tx, bookID); err != nil {
			writeDBError(w, err)
			return
		}
	}
	if err = renumberReservations(tx, bookID); err != nil {
		writeDBError(w, err)
		return
	}
	if err = tx.Commit(); err != nil {
		writeDBError(w, err)
		return
	}
	s.audit(r, "reservation.cancel", "reservation", id, nil)
	writeJSON(w, 200, map[string]any{"data": true})
}

// buildFineListQuery 支持按罚金号、借阅号、读者、书目、条码和原因搜索。
func buildFineListQuery(q string, limit, offset int) (string, []any) {
	return buildTextSearchListQuery(
		`SELECT f.id,f.loan_id,f.reader_id,r.reader_no,r.full_name,b.title,c.barcode,CAST(f.amount AS CHAR),f.reason,f.status,f.paid_at,f.created_at FROM fines f JOIN readers r ON r.id=f.reader_id JOIN loans l ON l.id=f.loan_id JOIN book_copies c ON c.id=l.book_copy_id JOIN books b ON b.id=c.book_id`,
		"f.id",
		q,
		[]string{"r.reader_no", "r.full_name", "b.title", "c.barcode", "f.reason"},
		[]string{"f.id", "l.id"},
		limit,
		offset,
	)
}

// listFines 返回罚金及其读者、借阅和图书上下文。
func (s *Server) listFines(w http.ResponseWriter, r *http.Request) {
	limit, offset, ok := parseListWindow(w, r, 200, 500)
	if !ok {
		return
	}
	query, args := buildFineListQuery(r.URL.Query().Get("q"), limit, offset)
	rows, err := s.DB.QueryContext(r.Context(), query, args...)
	if err != nil {
		writeDBError(w, err)
		return
	}
	defer rows.Close()
	out := []model.Fine{}
	for rows.Next() {
		var x model.Fine
		if err := rows.Scan(&x.ID, &x.LoanID, &x.ReaderID, &x.ReaderNo, &x.ReaderName, &x.BookTitle, &x.Barcode, &x.Amount, &x.Reason, &x.Status, &x.PaidAt, &x.CreatedAt); err != nil {
			writeDBError(w, err)
			return
		}
		out = append(out, x)
	}
	writeJSON(w, 200, map[string]any{"data": out})
}

// createFine 为存在的借阅创建一笔非负金额的罚金，并立即冻结对应读者。
func (s *Server) createFine(w http.ResponseWriter, r *http.Request) {
	var in struct {
		LoanID uint64  `json:"loanId"`
		Amount float64 `json:"amount"`
		Reason string  `json:"reason"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.LoanID == 0 || in.Amount < 0 || strings.TrimSpace(in.Reason) == "" {
		writeError(w, http.StatusBadRequest, "invalid_fine", "借阅、金额和原因均为必填项")
		return
	}
	tx, err := s.DB.BeginTx(r.Context(), nil)
	if err != nil {
		writeDBError(w, err)
		return
	}
	defer tx.Rollback()
	var readerID uint64
	if err := tx.QueryRowContext(r.Context(), `SELECT reader_id FROM loans WHERE id=? FOR UPDATE`, in.LoanID).Scan(&readerID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeNotFound(w)
			return
		}
		writeDBError(w, err)
		return
	}
	res, err := tx.ExecContext(r.Context(), `INSERT INTO fines (loan_id,reader_id,amount,reason) VALUES (?,?,?,?)`, in.LoanID, readerID, in.Amount, strings.TrimSpace(in.Reason))
	if err != nil {
		writeDBError(w, err)
		return
	}
	if _, err = tx.ExecContext(r.Context(), `UPDATE readers SET fine_blocked=TRUE WHERE id=?`, readerID); err != nil {
		writeDBError(w, err)
		return
	}
	if err = tx.Commit(); err != nil {
		writeDBError(w, err)
		return
	}
	id, _ := res.LastInsertId()
	s.audit(r, "fine.create", "fine", uint64(id), map[string]any{"loanId": in.LoanID, "amount": in.Amount})
	writeJSON(w, http.StatusCreated, map[string]any{"data": map[string]any{"id": id}})
}

// payFine 将未缴罚金标记为已缴，并记录缴纳时间。
func (s *Server) payFine(w http.ResponseWriter, r *http.Request) { s.updateFine(w, r, "paid") }

// waiveFine 将未缴罚金标记为管理员减免。
func (s *Server) waiveFine(w http.ResponseWriter, r *http.Request) { s.updateFine(w, r, "waived") }

// updateFine 仅允许从 unpaid 状态完成一次支付或减免；全部结清后解除罚金冻结。
func (s *Server) updateFine(w http.ResponseWriter, r *http.Request, status string) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, 400, "invalid_id", "无效 ID")
		return
	}
	tx, err := s.DB.BeginTx(r.Context(), nil)
	if err != nil {
		writeDBError(w, err)
		return
	}
	defer tx.Rollback()
	var readerID uint64
	if err = tx.QueryRowContext(r.Context(), `SELECT reader_id FROM fines WHERE id=? AND status='unpaid' FOR UPDATE`, id).Scan(&readerID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeNotFound(w)
			return
		}
		writeDBError(w, err)
		return
	}
	res, err := tx.ExecContext(r.Context(), `UPDATE fines SET status=?,paid_at=CASE WHEN ?='paid' THEN NOW() ELSE NULL END WHERE id=?`, status, status, id)
	if err != nil {
		writeDBError(w, err)
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		writeNotFound(w)
		return
	}
	var unpaid uint
	if err = tx.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM fines WHERE reader_id=? AND status='unpaid'`, readerID).Scan(&unpaid); err != nil {
		writeDBError(w, err)
		return
	}
	if _, err = tx.ExecContext(r.Context(), `UPDATE readers SET fine_blocked=? WHERE id=?`, unpaid > 0, readerID); err != nil {
		writeDBError(w, err)
		return
	}
	if err = tx.Commit(); err != nil {
		writeDBError(w, err)
		return
	}
	s.audit(r, "fine."+status, "fine", id, nil)
	writeJSON(w, 200, map[string]any{"data": true})
}

// dashboard 汇总馆藏、活跃读者、在借和逾期数量，用于首页概览。
func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	var books, copies, readers, loans, overdue uint
	err := s.DB.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM books WHERE collection_status='active' AND verification_status IN ('source_matched','human_verified')`).Scan(&books)
	if err == nil {
		err = s.DB.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM book_copies c JOIN books b ON b.id=c.book_id WHERE b.collection_status='active' AND b.verification_status IN ('source_matched','human_verified')`).Scan(&copies)
	}
	if err == nil {
		err = s.DB.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM readers r WHERE r.status='active' AND NOT EXISTS (SELECT 1 FROM fines f WHERE f.reader_id=r.id AND f.status='unpaid')`).Scan(&readers)
	}
	if err == nil {
		err = s.DB.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM loans WHERE status IN ('active','overdue')`).Scan(&loans)
	}
	if err == nil {
		err = s.DB.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM loans WHERE status='active' AND due_at<NOW()`).Scan(&overdue)
	}
	if err != nil {
		writeDBError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"data": map[string]any{"books": books, "copies": copies, "readers": readers, "activeLoans": loans, "overdueLoans": overdue}})
}

// buildAuditLogListQuery 支持按操作者、动作、对象类型、详情以及审计或实体编号搜索。
func buildAuditLogListQuery(q string, limit, offset int) (string, []any) {
	return buildTextSearchListQuery(
		`SELECT a.id,a.actor_user_id,u.username,a.action,a.entity_type,a.entity_id,a.details,a.created_at FROM audit_logs a LEFT JOIN users u ON u.id=a.actor_user_id`,
		"a.id",
		q,
		[]string{"u.username", "a.action", "a.entity_type", "CAST(a.details AS CHAR)"},
		[]string{"a.id", "a.entity_id"},
		limit,
		offset,
	)
}

// listAuditLogs 分页返回审计记录，默认读取最近两百条并限制单次查询规模。
func (s *Server) listAuditLogs(w http.ResponseWriter, r *http.Request) {
	limit, offset, ok := parseListWindow(w, r, 200, 500)
	if !ok {
		return
	}
	query, args := buildAuditLogListQuery(r.URL.Query().Get("q"), limit, offset)
	rows, err := s.DB.QueryContext(r.Context(), query, args...)
	if err != nil {
		writeDBError(w, err)
		return
	}
	defer rows.Close()
	out := []model.AuditLog{}
	for rows.Next() {
		var x model.AuditLog
		var raw []byte
		if err := rows.Scan(&x.ID, &x.ActorUserID, &x.ActorName, &x.Action, &x.EntityType, &x.EntityID, &raw, &x.CreatedAt); err != nil {
			writeDBError(w, err)
			return
		}
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &x.Details)
		}
		out = append(out, x)
	}
	writeJSON(w, 200, map[string]any{"data": out})
}

// audit 以尽力而为方式记录管理操作；审计写入失败不应回滚已完成的业务事务。
func (s *Server) audit(r *http.Request, action, entity string, id uint64, details any) {
	u := userFromContext(r.Context())
	raw, _ := json.Marshal(details)
	_, err := s.DB.ExecContext(r.Context(), `INSERT INTO audit_logs(actor_user_id,action,entity_type,entity_id,details) VALUES(?,?,?,?,?)`, u.ID, action, entity, id, raw)
	if err != nil {
		log.Printf("audit log: %v", err)
	}
}

// expireReservations 在新的流通操作前处理已过期的待取预约。
// 每本书的过期预约会在一个事务内完成过期、转交或释放副本，以及队列重排。
func (s *Server) expireReservations(ctx context.Context) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT id,book_id FROM reservations WHERE status='ready' AND expires_at <= NOW() FOR UPDATE`)
	if err != nil {
		return err
	}
	type expiredReservation struct{ id, bookID uint64 }
	var expired []expiredReservation
	for rows.Next() {
		var item expiredReservation
		if err := rows.Scan(&item.id, &item.bookID); err != nil {
			rows.Close()
			return err
		}
		expired = append(expired, item)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, item := range expired {
		// 先释放该待取预约明确占用的副本，再按当前品相重新分配队首读者。
		var copyID *uint64
		if err := tx.QueryRowContext(ctx, `SELECT reserved_copy_id FROM reservations WHERE id=? FOR UPDATE`, item.id).Scan(&copyID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE reservations SET status='expired',expires_at=NULL,reserved_copy_id=NULL WHERE id=?`, item.id); err != nil {
			return err
		}
		if copyID != nil {
			var conditionStatus string
			if err := tx.QueryRowContext(ctx, `SELECT condition_status FROM book_copies WHERE id=? FOR UPDATE`, *copyID).Scan(&conditionStatus); err != nil {
				return err
			}
			nextStatus := "available"
			if conditionRequiresMaintenance(conditionStatus) {
				nextStatus = "maintenance"
			}
			if _, err := tx.ExecContext(ctx, `UPDATE book_copies SET status=? WHERE id=?`, nextStatus, *copyID); err != nil {
				return err
			}
		}
		if err := assignNextWaitingReservation(ctx, tx, item.bookID); err != nil {
			return err
		}
		if err := renumberReservations(tx, item.bookID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO audit_logs (action,entity_type,entity_id,details) VALUES ('reservation.expire','reservation',?,JSON_OBJECT('system', true))`, item.id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// renumberReservations 让同一书目的有效预约队列从一开始连续编号。
// ready 永远排在 waiting 之前，避免已分配副本的预约被后续排队记录超越。
func renumberReservations(tx *sql.Tx, bookID uint64) error {
	if _, err := tx.Exec(`SET @reservation_position := 0`); err != nil {
		return err
	}
	_, err := tx.Exec(`UPDATE reservations SET queue_position = (@reservation_position := @reservation_position + 1) WHERE book_id=? AND status IN ('waiting','ready') ORDER BY CASE status WHEN 'ready' THEN 0 ELSE 1 END, queue_position, id`, bookID)
	return err
}

// pathID 读取并校验路由中的正整数资源 ID。
func pathID(r *http.Request) (uint64, bool) {
	id, err := strconv.ParseUint(r.PathValue("id"), 10, 64)
	return id, err == nil && id > 0
}

// ensureManualPublisher 维护手工草稿的兼容出版社关系；公开来源出版社由导入器按来源键管理。
func ensureManualPublisher(ctx context.Context, tx *sql.Tx, name string) (uint64, error) {
	normalizedName := strings.ToLower(strings.Join(strings.Fields(name), " "))
	var id uint64
	err := tx.QueryRowContext(ctx, `SELECT id FROM publishers WHERE source_provider IS NULL AND normalized_name=? ORDER BY id LIMIT 1 FOR UPDATE`, normalizedName).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO publishers(name,normalized_name) VALUES(?,?)`, name, normalizedName)
	if err != nil {
		return 0, err
	}
	lastID, err := result.LastInsertId()
	return uint64(lastID), err
}

// normalizeISBN13 统一去除常见分隔符、校验校验位，并把 ISBN-10 转为 ISBN-13。
func normalizeISBN13(value string) (string, bool) {
	cleaned := strings.ToUpper(strings.NewReplacer("-", "", " ", "", "\t", "", "\r", "", "\n", "").Replace(strings.TrimSpace(value)))
	if len(cleaned) == 10 {
		sum := 0
		for i := 0; i < 10; i++ {
			digit := 0
			if i == 9 && cleaned[i] == 'X' {
				digit = 10
			} else if cleaned[i] >= '0' && cleaned[i] <= '9' {
				digit = int(cleaned[i] - '0')
			} else {
				return "", false
			}
			sum += digit * (10 - i)
		}
		if sum%11 != 0 {
			return "", false
		}
		base := "978" + cleaned[:9]
		return base + string(rune('0'+isbn13CheckDigit(base))), true
	}
	if len(cleaned) != 13 {
		return "", false
	}
	for i := range cleaned {
		if cleaned[i] < '0' || cleaned[i] > '9' {
			return "", false
		}
	}
	if int(cleaned[12]-'0') != isbn13CheckDigit(cleaned[:12]) {
		return "", false
	}
	return cleaned, true
}

func isbn13CheckDigit(base string) int {
	sum := 0
	for i := 0; i < 12; i++ {
		digit := int(base[i] - '0')
		if i%2 == 1 {
			digit *= 3
		}
		sum += digit
	}
	return (10 - sum%10) % 10
}

// containsLikePattern 把用户输入转换为字面量包含匹配，避免百分号和下划线意外扩大查询范围。
// SQL 使用等号作为 ESCAPE 字符，因此输入中的等号也需要先转义。
func containsLikePattern(value string) string {
	replacer := strings.NewReplacer("=", "==", "%", "=%", "_", "=_")
	return "%" + replacer.Replace(value) + "%"
}

// listIDArguments 为页内批量聚合构造占位符及参数；调用方只在 ID 非空时使用。
func listIDArguments(ids []uint64) (string, []any) {
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, 0, len(ids))
	for _, id := range ids {
		args = append(args, id)
	}
	return placeholders, args
}

// parseListWindow 为大列表提供统一分页边界，避免单次请求把数万行全部装入内存。
func parseListWindow(w http.ResponseWriter, r *http.Request, defaultLimit, maxLimit int) (int, int, bool) {
	limit := defaultLimit
	offset := 0
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > maxLimit {
			writeError(w, http.StatusBadRequest, "invalid_limit", fmt.Sprintf("limit 必须在 1 到 %d 之间", maxLimit))
			return 0, 0, false
		}
		limit = value
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("offset")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 0 {
			writeError(w, http.StatusBadRequest, "invalid_offset", "offset 不能小于 0")
			return 0, 0, false
		}
		offset = value
	}
	return limit, offset, true
}

// decodeJSON 拒绝空请求体和未知字段，避免客户端拼写错误被静默忽略。
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if r.Body == nil {
		writeError(w, 400, "invalid_json", "请求体不能为空")
		return false
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, 400, "invalid_json", "请求 JSON 无效")
		return false
	}
	return true
}

// writeJSON 统一写出 UTF-8 JSON 响应和 HTTP 状态码。
func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		log.Printf("write response: %v", err)
	}
}

// writeError 将业务错误包装为前端可稳定识别的 error 对象。
func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": apiError{Code: code, Message: message}})
}

// writeDBError 在服务端保留诊断信息，对客户端隐藏数据库内部细节。
func writeDBError(w http.ResponseWriter, err error) {
	log.Printf("database error: %v", err)
	writeError(w, 500, "database_error", "数据库操作失败")
}

// writeConflict 将唯一键冲突转换为可恢复的 409 响应，其余数据库错误仍按 500 处理。
func writeConflict(w http.ResponseWriter, err error) {
	if strings.Contains(strings.ToLower(err.Error()), "duplicate") || strings.Contains(strings.ToLower(err.Error()), "1062") {
		writeError(w, 409, "duplicate", "数据已存在")
		return
	}
	writeDBError(w, err)
}

// writeNotFound 返回统一的资源不存在响应。
func writeNotFound(w http.ResponseWriter) { writeError(w, 404, "not_found", "资源不存在") }

// writeNotFoundOrDB 保留不存在和基础设施错误之间的 HTTP 语义差异。
func writeNotFoundOrDB(w http.ResponseWriter, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		writeNotFound(w)
	} else {
		writeDBError(w, err)
	}
}

var _ = subtle.ConstantTimeCompare
