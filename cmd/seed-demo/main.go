package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"library-management/internal/auth"
	"library-management/internal/config"
	"library-management/internal/database"
)

const seedMarker = "demo_seed.complete"

// demoCopy 定义测试副本应落在哪个书目以及要展示的流通状态。
type demoCopy struct {
	barcode         string // 仅作为演示数据幂等标识，不写入数据库条码字段。
	bookOffset      int
	location        string
	status          string
	conditionStatus string
	conditionNote   string
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	password := os.Getenv("DEMO_SEED_PASSWORD")
	if password == "" {
		log.Fatal("DEMO_SEED_PASSWORD is required")
	}
	passwordHash, err := auth.HashPassword(password)
	if err != nil {
		log.Fatalf("invalid demo password: %v", err)
	}

	cfg, err := config.LoadDatabase("")
	if err != nil {
		log.Fatal(err)
	}
	db, err := database.Open(cfg, false)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	var seeded int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_logs WHERE action=?`, seedMarker).Scan(&seeded); err != nil {
		log.Fatal(err)
	}
	if seeded > 0 {
		fmt.Println("演示测试数据已存在，未重复写入。")
		return
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		log.Fatal(err)
	}
	defer tx.Rollback()

	bookIDs, err := loadBookIDs(ctx, tx, 10)
	if err != nil {
		log.Fatal(err)
	}

	librarianID, err := ensureUser(ctx, tx, "demo_librarian", passwordHash, "librarian", true)
	if err != nil {
		log.Fatal(err)
	}
	if _, err := ensureUser(ctx, tx, "demo_disabled", passwordHash, "librarian", false); err != nil {
		log.Fatal(err)
	}

	readerIDs := make([]uint64, 8)
	readerNames := []string{"张晓晨", "李沐阳", "王思远", "陈嘉宁", "赵子涵", "孙文博", "周雨桐", "吴浩然"}
	for i, name := range readerNames {
		id, err := ensureReader(ctx, tx, fmt.Sprintf("DEMO-R%03d", i+1), name, i == 7)
		if err != nil {
			log.Fatal(err)
		}
		readerIDs[i] = id
	}

	// 副本同时覆盖可借、借出、预留、遗失和维护状态，便于逐页验证展示效果。
	copies := []demoCopy{
		{"DEMO-COPY-001", 0, "DEMO-A-01-01", "borrowed", "intact", "馆藏完好"},
		{"DEMO-COPY-002", 1, "DEMO-A-01-02", "borrowed", "intact", "馆藏完好"},
		{"DEMO-COPY-003", 2, "DEMO-A-01-03", "available", "minor_wear", "封面轻微磨损"},
		{"DEMO-COPY-004", 3, "DEMO-A-02-01", "lost", "pending_inspection", "盘点遗失"},
		{"DEMO-COPY-005", 4, "DEMO-A-02-02", "reserved", "intact", "馆藏完好"},
		{"DEMO-COPY-006", 5, "DEMO-A-02-03", "available", "intact", "馆藏完好"},
		{"DEMO-COPY-007", 6, "DEMO-B-01-01", "borrowed", "intact", "馆藏完好"},
		{"DEMO-COPY-008", 7, "DEMO-B-01-02", "maintenance", "damaged", "等待修复装订"},
		{"DEMO-COPY-009", 8, "DEMO-B-01-03", "available", "intact", "馆藏完好"},
		{"DEMO-COPY-010", 9, "DEMO-B-02-01", "available", "intact", "馆藏完好"},
		{"DEMO-COPY-011", 0, "DEMO-B-02-02", "available", "intact", "馆藏完好"},
		{"DEMO-COPY-012", 1, "DEMO-B-02-03", "available", "intact", "馆藏完好"},
	}
	copyIDs := make(map[string]uint64, len(copies))
	copyBarcodes := make(map[string]string, len(copies))
	for _, item := range copies {
		id, barcode, err := ensureCopy(ctx, tx, item, bookIDs[item.bookOffset])
		if err != nil {
			log.Fatal(err)
		}
		copyIDs[item.barcode] = id
		copyBarcodes[item.barcode] = barcode
	}

	now := time.Now()
	activeLoanID, err := createLoan(ctx, tx, readerIDs[0], copyIDs["DEMO-COPY-001"], now.AddDate(0, 0, -7), now.AddDate(0, 0, 23), nil, 0, "active")
	if err != nil {
		log.Fatal(err)
	}
	overdueLoanID, err := createLoan(ctx, tx, readerIDs[1], copyIDs["DEMO-COPY-002"], now.AddDate(0, 0, -40), now.AddDate(0, 0, -10), nil, 1, "overdue")
	if err != nil {
		log.Fatal(err)
	}
	returnedAt := now.AddDate(0, 0, -3)
	returnedLoanID, err := createLoan(ctx, tx, readerIDs[2], copyIDs["DEMO-COPY-003"], now.AddDate(0, 0, -35), now.AddDate(0, 0, -5), &returnedAt, 0, "returned")
	if err != nil {
		log.Fatal(err)
	}
	lostLoanID, err := createLoan(ctx, tx, readerIDs[3], copyIDs["DEMO-COPY-004"], now.AddDate(0, 0, -50), now.AddDate(0, 0, -20), nil, 0, "lost")
	if err != nil {
		log.Fatal(err)
	}
	if _, err := createLoan(ctx, tx, readerIDs[4], copyIDs["DEMO-COPY-007"], now.AddDate(0, 0, -2), now.AddDate(0, 0, 28), nil, 0, "active"); err != nil {
		log.Fatal(err)
	}

	// 预约状态用于检验队列顺序、待取期限和历史记录的不同显示。
	if err := createReservation(ctx, tx, readerIDs[5], bookIDs[4], copyIDs["DEMO-COPY-005"], 1, "ready", pointerTime(now.AddDate(0, 0, 3))); err != nil {
		log.Fatal(err)
	}
	if err := createReservation(ctx, tx, readerIDs[6], bookIDs[4], 0, 2, "waiting", nil); err != nil {
		log.Fatal(err)
	}
	if err := createReservation(ctx, tx, readerIDs[1], bookIDs[6], 0, 1, "waiting", nil); err != nil {
		log.Fatal(err)
	}
	if err := createReservation(ctx, tx, readerIDs[0], bookIDs[0], 0, 1, "fulfilled", nil); err != nil {
		log.Fatal(err)
	}
	if err := createReservation(ctx, tx, readerIDs[2], bookIDs[2], 0, 1, "cancelled", nil); err != nil {
		log.Fatal(err)
	}
	if err := createReservation(ctx, tx, readerIDs[3], bookIDs[3], 0, 1, "expired", nil); err != nil {
		log.Fatal(err)
	}

	if err := createFine(ctx, tx, overdueLoanID, readerIDs[1], "18.00", "逾期 10 天", "unpaid", nil); err != nil {
		log.Fatal(err)
	}
	paidAt := now.AddDate(0, 0, -2)
	if err := createFine(ctx, tx, returnedLoanID, readerIDs[2], "5.00", "轻微污损", "paid", &paidAt); err != nil {
		log.Fatal(err)
	}
	if err := createFine(ctx, tx, activeLoanID, readerIDs[0], "30.00", "馆员测试减免记录", "waived", nil); err != nil {
		log.Fatal(err)
	}
	if err := createFineWithOrigin(ctx, tx, lostLoanID, readerIDs[3], "30.00", "图书遗失赔偿", "lost", "unpaid", nil); err != nil {
		log.Fatal(err)
	}

	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_logs(actor_user_id,action,entity_type,entity_id,details) VALUES(?,?,?,?,JSON_OBJECT('purpose','local test data'))`, librarianID, seedMarker, "system", nil); err != nil {
		log.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_logs(actor_user_id,action,entity_type,entity_id,details) VALUES(?,?,?,?,JSON_OBJECT('barcode',?))`, librarianID, "demo_seed.copy", "book_copy", copyIDs["DEMO-COPY-001"], copyBarcodes["DEMO-COPY-001"]); err != nil {
		log.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_logs(actor_user_id,action,entity_type,entity_id,details) VALUES(?,?,?,?,JSON_OBJECT('loanId',?))`, librarianID, "demo_seed.loan", "loan", activeLoanID, activeLoanID); err != nil {
		log.Fatal(err)
	}

	if err := tx.Commit(); err != nil {
		log.Fatal(err)
	}
	fmt.Println("演示测试数据已写入：3 个账号、8 个读者、12 个实体副本、5 条借阅、6 条预约、3 笔罚金和审计记录。")
}

// loadBookIDs 复用现有书目，避免演示数据重复制造书目、作者和分类主数据。
func loadBookIDs(ctx context.Context, tx *sql.Tx, limit int) ([]uint64, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM books ORDER BY id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []uint64
	for rows.Next() {
		var id uint64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) < limit {
		return nil, fmt.Errorf("至少需要 %d 条书目，当前只有 %d 条", limit, len(ids))
	}
	return ids, nil
}

// ensureUser 只创建缺失的演示账号，避免误改同名账号的密码或权限。
func ensureUser(ctx context.Context, tx *sql.Tx, username, passwordHash, role string, active bool) (uint64, error) {
	var id uint64
	err := tx.QueryRowContext(ctx, `SELECT id FROM users WHERE username=?`, username).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO users(username,password_hash,role,is_active) VALUES(?,?,?,?)`, username, passwordHash, role, active)
	if err != nil {
		return 0, err
	}
	lastID, err := result.LastInsertId()
	return uint64(lastID), err
}

// ensureReader 只以 DEMO- 编号创建独立测试读者，并给最后一位读者设置为暂停状态。
func ensureReader(ctx context.Context, tx *sql.Tx, number, name string, suspended bool) (uint64, error) {
	var id uint64
	err := tx.QueryRowContext(ctx, `SELECT id FROM readers WHERE reader_no=?`, number).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	status := "active"
	if suspended {
		status = "suspended"
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO readers(reader_no,full_name,phone,email,status,loan_limit) VALUES(?,?,?,?,?,?)`, number, name, fmt.Sprintf("1380000%04d", len(number)*17), fmt.Sprintf("%s@example.test", number), status, 5)
	if err != nil {
		return 0, err
	}
	lastID, err := result.LastInsertId()
	return uint64(lastID), err
}

// ensureCopy 使用演示标识查找记录，但实际条码始终按结构化规则自动生成。
func ensureCopy(ctx context.Context, tx *sql.Tx, item demoCopy, bookID uint64) (uint64, string, error) {
	var id uint64
	var existingBarcode string
	// 旧演示数据可能已经被迁移重编，使用审计标记或书目/位置识别已有记录可保持脚本幂等。
	err := tx.QueryRowContext(ctx, `SELECT id,barcode FROM book_copies WHERE location=? AND book_id=? ORDER BY id LIMIT 1`, item.location, bookID).Scan(&id, &existingBarcode)
	if err == nil {
		return id, existingBarcode, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, "", err
	}
	var materialType string
	if err := tx.QueryRowContext(ctx, `SELECT material_type FROM books WHERE id=?`, bookID).Scan(&materialType); err != nil {
		return 0, "", err
	}
	acquiredAt := time.Now().AddDate(0, -4, 0)
	var nextSequence uint64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(CAST(SUBSTRING_INDEX(barcode,'-',-1) AS UNSIGNED)),0)+1 FROM book_copies WHERE book_id=?`, bookID).Scan(&nextSequence); err != nil {
		return 0, "", err
	}
	prefix := "BK"
	if materialType == "periodical" {
		prefix = "MC"
	}
	barcode := fmt.Sprintf("%s-%04d-%d-%04d", prefix, acquiredAt.Year(), bookID, nextSequence)
	result, err := tx.ExecContext(ctx, `INSERT INTO book_copies(book_id,barcode,location,status,condition_status,condition_note,acquired_at) VALUES(?,?,?,?,?,?,?)`, bookID, barcode, item.location, item.status, item.conditionStatus, item.conditionNote, acquiredAt.Format("2006-01-02"))
	if err != nil {
		return 0, "", err
	}
	lastID, err := result.LastInsertId()
	return uint64(lastID), barcode, err
}

// createLoan 写入不同借阅阶段的测试流水；副本状态已由演示副本数据预先设置。
func createLoan(ctx context.Context, tx *sql.Tx, readerID, copyID uint64, borrowedAt, dueAt time.Time, returnedAt *time.Time, renewCount uint, status string) (uint64, error) {
	result, err := tx.ExecContext(ctx, `INSERT INTO loans(reader_id,book_copy_id,borrowed_at,due_at,returned_at,renew_count,status) VALUES(?,?,?,?,?,?,?)`, readerID, copyID, borrowedAt, dueAt, returnedAt, renewCount, status)
	if err != nil {
		return 0, err
	}
	id, err := result.LastInsertId()
	return uint64(id), err
}

// createReservation 写入预约历史和当前队列，队首待取预约与预留副本保持对应。
func createReservation(ctx context.Context, tx *sql.Tx, readerID, bookID, reservedCopyID uint64, position uint, status string, expiresAt *time.Time) error {
	var reservedCopy any
	if reservedCopyID > 0 {
		reservedCopy = reservedCopyID
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO reservations(reader_id,book_id,reserved_copy_id,queue_position,status,expires_at) VALUES(?,?,?,?,?,?)`, readerID, bookID, reservedCopy, position, status, expiresAt)
	return err
}

// createFine 写入未缴、已缴和减免三种罚金状态，用于前端状态筛选测试。
func createFine(ctx context.Context, tx *sql.Tx, loanID, readerID uint64, amount, reason, status string, paidAt *time.Time) error {
	return createFineWithOrigin(ctx, tx, loanID, readerID, amount, reason, "manual", status, paidAt)
}

// createFineWithOrigin 写入带来源标记的演示罚金，便于状态修正区分系统遗失赔偿和人工罚金。
func createFineWithOrigin(ctx context.Context, tx *sql.Tx, loanID, readerID uint64, amount, reason, origin, status string, paidAt *time.Time) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO fines(loan_id,reader_id,amount,reason,origin,status,paid_at) VALUES(?,?,?,?,?,?,?)`, loanID, readerID, amount, reason, origin, status, paidAt); err != nil {
		return err
	}
	if status == "unpaid" {
		_, err := tx.ExecContext(ctx, `UPDATE readers SET fine_blocked=TRUE WHERE id=?`, readerID)
		return err
	}
	return nil
}

func pointerTime(value time.Time) *time.Time { return &value }
