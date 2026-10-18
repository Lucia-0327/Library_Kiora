package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"regexp"
	"sort"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"library-management/internal/config"
	"library-management/internal/database"
)

const (
	datasetMarker     = "REALISTIC_BUSINESS_SEED_V1"
	auditActionPrefix = "realistic_business_seed.v1."
	markerAction      = auditActionPrefix + "complete"
	readerPrefix      = "SIM-BIZ-R"
	copyNotePrefix    = datasetMarker + "|"
	advisoryLock      = "library_management.realistic_business_seed.v1"
	batchSize         = 400
)

var structuredBarcodePattern = regexp.MustCompile(`^(BK|MC)-[0-9]{4}-[0-9]+-[0-9]{4}$`)

type options struct {
	mode              string
	readerCount       int
	copyCount         int
	returnedLoanCount int
	reservationCount  int
	randomSeed        int64
	confirmCleanup    bool
}

type bookRef struct {
	id           uint64
	materialType string
	publishYear  int
	maxSequence  uint64
}

type readerRow struct {
	id          uint64
	readerNo    string
	fullName    string
	phone       string
	email       string
	status      string
	fineBlocked bool
	loanLimit   int
	createdAt   time.Time
	updatedAt   time.Time
}

type copyRow struct {
	id              uint64
	bookIndex       int
	barcode         string
	location        string
	status          string
	conditionStatus string
	conditionNote   string
	acquiredAt      time.Time
	createdAt       time.Time
	updatedAt       time.Time
}

type loanRow struct {
	id          uint64
	readerIndex int
	copyIndex   int
	borrowedAt  time.Time
	dueAt       time.Time
	returnedAt  *time.Time
	renewCount  int
	status      string
	createdAt   time.Time
	updatedAt   time.Time
}

type reservationRow struct {
	id                uint64
	readerIndex       int
	bookIndex         int
	reservedCopyIndex int
	queuePosition     int
	status            string
	expiresAt         *time.Time
	createdAt         time.Time
	updatedAt         time.Time
}

type fineRow struct {
	id          uint64
	loanIndex   int
	readerIndex int
	amount      string
	reason      string
	origin      string
	status      string
	paidAt      *time.Time
	createdAt   time.Time
	updatedAt   time.Time
}

type dataset struct {
	books        []bookRef
	readers      []readerRow
	copies       []copyRow
	loans        []loanRow
	reservations []reservationRow
	fines        []fineRow
	layout       circulationLayout
	anchor       time.Time
	randomSeed   int64
}

type circulationLayout struct {
	activeLoans      int
	overdueLoans     int
	lostLoans        int
	readyCopies      int
	maintenance      int
	activeStart      int
	overdueStart     int
	lostStart        int
	readyStart       int
	maintenanceStart int
}

type existingSummary struct {
	markers      int
	readers      int
	copies       int
	loans        int
	reservations int
	fines        int
	audits       int
}

type markerMetadata struct {
	readerCount       int
	copyCount         int
	returnedLoanCount int
	reservationCount  int
	randomSeed        int64
	loanCount         int
	fineCount         int
	auditCount        int
}

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func main() {
	log.SetFlags(0)
	opts := parseOptions()
	if err := validateOptions(opts); err != nil {
		log.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	cfg, err := config.LoadDatabase("")
	if err != nil {
		log.Fatalf("加载数据库配置失败：%v", err)
	}
	db, err := database.Open(cfg, false)
	if err != nil {
		log.Fatalf("连接数据库失败：%v", err)
	}
	defer db.Close()

	conn, err := db.Conn(ctx)
	if err != nil {
		log.Fatalf("获取数据库连接失败：%v", err)
	}
	defer conn.Close()

	if opts.mode == "plan" {
		if err := printPlan(ctx, conn, opts); err != nil {
			log.Fatal(err)
		}
		return
	}

	locked, err := acquireLock(ctx, conn)
	if err != nil {
		log.Fatal(err)
	}
	if !locked {
		log.Fatal("另一个规模化模拟数据任务正在运行，请稍后重试。")
	}
	defer releaseLock(conn)

	switch opts.mode {
	case "seed":
		err = seed(ctx, conn, opts, false)
	case "reset":
		err = seed(ctx, conn, opts, true)
	case "clean":
		err = clean(ctx, conn)
	default:
		err = fmt.Errorf("不支持的运行模式：%s", opts.mode)
	}
	if err != nil {
		log.Fatal(err)
	}
}

func parseOptions() options {
	var opts options
	flag.StringVar(&opts.mode, "mode", "seed", "运行模式：seed、plan、reset 或 clean")
	flag.IntVar(&opts.readerCount, "readers", 6000, "模拟读者数量")
	flag.IntVar(&opts.copyCount, "copies", 24000, "模拟实体副本数量")
	flag.IntVar(&opts.returnedLoanCount, "returned-loans", 36000, "已归还历史借阅数量")
	flag.IntVar(&opts.reservationCount, "reservations", 8000, "预约记录数量")
	flag.Int64Var(&opts.randomSeed, "random-seed", 20260904, "确定性随机种子")
	flag.BoolVar(&opts.confirmCleanup, "confirm-clean", false, "确认只清理本生成器标记的数据")
	flag.Parse()
	opts.mode = strings.ToLower(strings.TrimSpace(opts.mode))
	return opts
}

func validateOptions(opts options) error {
	if opts.mode != "seed" && opts.mode != "plan" && opts.mode != "reset" && opts.mode != "clean" {
		return fmt.Errorf("mode 必须是 seed、plan、reset 或 clean，当前为 %q", opts.mode)
	}
	if (opts.mode == "reset" || opts.mode == "clean") && !opts.confirmCleanup {
		return errors.New("reset 和 clean 会删除本生成器标记的数据，必须同时传入 --confirm-clean")
	}
	if opts.mode == "clean" {
		return nil
	}
	if opts.readerCount < 50 {
		return errors.New("模拟读者数量不能少于 50")
	}
	if opts.copyCount < 200 {
		return errors.New("模拟实体副本数量不能少于 200")
	}
	if opts.returnedLoanCount < 100 {
		return errors.New("已归还历史借阅数量不能少于 100")
	}
	if opts.reservationCount < 50 {
		return errors.New("预约记录数量不能少于 50")
	}
	return nil
}

func acquireLock(ctx context.Context, conn *sql.Conn) (bool, error) {
	var locked sql.NullInt64
	if err := conn.QueryRowContext(ctx, `SELECT GET_LOCK(?, 10)`, advisoryLock).Scan(&locked); err != nil {
		return false, fmt.Errorf("获取模拟数据任务锁失败：%w", err)
	}
	return locked.Valid && locked.Int64 == 1, nil
}

func releaseLock(conn *sql.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var released sql.NullInt64
	_ = conn.QueryRowContext(ctx, `SELECT RELEASE_LOCK(?)`, advisoryLock).Scan(&released)
}

func printPlan(ctx context.Context, conn *sql.Conn, opts options) error {
	books, err := loadBooks(ctx, conn)
	if err != nil {
		return err
	}
	data, err := buildDataset(opts, books, dayAnchor(time.Now()))
	if err != nil {
		return err
	}
	if err := validateDataset(data); err != nil {
		return fmt.Errorf("模拟数据规划校验失败：%w", err)
	}
	printDatasetSummary("只读规划完成，数据库尚未写入任何记录。", data, estimateAuditCount(data))
	return nil
}

func seed(ctx context.Context, conn *sql.Conn, opts options, reset bool) error {
	current, err := inspectExisting(ctx, conn)
	if err != nil {
		return err
	}
	if !reset && current.markers > 0 {
		if err := verifyExistingMarker(ctx, conn, opts, current); err != nil {
			return err
		}
		printExistingSummary("规模化模拟业务数据已存在，本次未重复写入。", current)
		return nil
	}
	if !reset && (current.readers > 0 || current.copies > 0 || current.loans > 0 || current.reservations > 0 || current.fines > 0 || current.audits > 0) {
		return errors.New("发现带模拟前缀但缺少完成标记的残留数据；请核对后使用 reset 模式重建")
	}

	tx, err := conn.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return fmt.Errorf("开始事务失败：%w", err)
	}
	defer tx.Rollback()

	if reset {
		removed, err := cleanupTransaction(ctx, tx)
		if err != nil {
			return err
		}
		printExistingSummary("已在事务内清理旧的规模化模拟数据，准备重建。", removed)
	}

	var increment, offset int
	if err := tx.QueryRowContext(ctx, `SELECT @@auto_increment_increment, @@auto_increment_offset`).Scan(&increment, &offset); err != nil {
		return fmt.Errorf("读取自增配置失败：%w", err)
	}
	if increment != 1 || offset != 1 {
		return fmt.Errorf("当前 MySQL 自增步长为 %d、偏移为 %d，生成器要求二者均为 1", increment, offset)
	}

	books, err := loadBooks(ctx, tx)
	if err != nil {
		return err
	}
	data, err := buildDataset(opts, books, dayAnchor(time.Now()))
	if err != nil {
		return err
	}
	if err := validateDataset(data); err != nil {
		return fmt.Errorf("写入前一致性校验失败：%w", err)
	}

	log.Printf("开始写入 %d 名模拟读者……", len(data.readers))
	if err := insertReaders(ctx, tx, data); err != nil {
		return err
	}
	log.Printf("开始写入 %d 册模拟实体副本……", len(data.copies))
	if err := insertCopies(ctx, tx, data); err != nil {
		return err
	}
	log.Printf("开始写入 %d 条模拟借阅流水……", len(data.loans))
	if err := insertLoans(ctx, tx, data); err != nil {
		return err
	}
	log.Printf("开始写入 %d 条模拟预约记录……", len(data.reservations))
	if err := insertReservations(ctx, tx, data); err != nil {
		return err
	}
	log.Printf("开始写入 %d 笔模拟罚金……", len(data.fines))
	if err := insertFines(ctx, tx, data); err != nil {
		return err
	}

	log.Print("开始写入带数据集标记的模拟审计日志……")
	auditCount, err := insertAudits(ctx, tx, data)
	if err != nil {
		return err
	}
	if err := insertCompletionMarker(ctx, tx, data, auditCount); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交模拟数据事务失败：%w", err)
	}
	printDatasetSummary("规模化模拟业务数据写入完成。", data, auditCount+1)
	return nil
}

func clean(ctx context.Context, conn *sql.Conn) error {
	tx, err := conn.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return fmt.Errorf("开始清理事务失败：%w", err)
	}
	defer tx.Rollback()
	removed, err := cleanupTransaction(ctx, tx)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交清理事务失败：%w", err)
	}
	printExistingSummary("已删除本生成器标记的模拟业务数据；原有书目及其他数据均保留。", removed)
	return nil
}

func cleanupTransaction(ctx context.Context, tx *sql.Tx) (existingSummary, error) {
	before, err := inspectExisting(ctx, tx)
	if err != nil {
		return existingSummary{}, err
	}
	var foreignLoans int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM loans l
		JOIN book_copies c ON c.id=l.book_copy_id AND LEFT(c.condition_note,CHAR_LENGTH(?))=?
		JOIN readers r ON r.id=l.reader_id
		WHERE r.reader_no NOT LIKE ?`, copyNotePrefix, copyNotePrefix, readerPrefix+"%").Scan(&foreignLoans); err != nil {
		return existingSummary{}, fmt.Errorf("检查模拟副本的外部借阅失败：%w", err)
	}
	var foreignReservations int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM reservations rs
		JOIN book_copies c ON c.id=rs.reserved_copy_id AND LEFT(c.condition_note,CHAR_LENGTH(?))=?
		JOIN readers r ON r.id=rs.reader_id
		WHERE r.reader_no NOT LIKE ?`, copyNotePrefix, copyNotePrefix, readerPrefix+"%").Scan(&foreignReservations); err != nil {
		return existingSummary{}, fmt.Errorf("检查模拟副本的外部预约失败：%w", err)
	}
	if foreignLoans > 0 || foreignReservations > 0 {
		return existingSummary{}, fmt.Errorf("模拟副本已被非模拟读者使用（借阅 %d 条、预约 %d 条），为避免误删，清理已取消", foreignLoans, foreignReservations)
	}

	statements := []struct {
		name  string
		query string
		args  []any
	}{
		{"罚金", `DELETE f FROM fines f JOIN loans l ON l.id=f.loan_id JOIN readers r ON r.id=l.reader_id JOIN book_copies c ON c.id=l.book_copy_id WHERE r.reader_no LIKE ? AND LEFT(c.condition_note,CHAR_LENGTH(?))=?`, []any{readerPrefix + "%", copyNotePrefix, copyNotePrefix}},
		{"预约", `DELETE rs FROM reservations rs JOIN readers r ON r.id=rs.reader_id WHERE r.reader_no LIKE ?`, []any{readerPrefix + "%"}},
		{"借阅", `DELETE l FROM loans l JOIN readers r ON r.id=l.reader_id JOIN book_copies c ON c.id=l.book_copy_id WHERE r.reader_no LIKE ? AND LEFT(c.condition_note,CHAR_LENGTH(?))=?`, []any{readerPrefix + "%", copyNotePrefix, copyNotePrefix}},
		{"实体副本", `DELETE FROM book_copies WHERE LEFT(condition_note,CHAR_LENGTH(?))=?`, []any{copyNotePrefix, copyNotePrefix}},
		{"读者", `DELETE FROM readers WHERE reader_no LIKE ?`, []any{readerPrefix + "%"}},
		{"审计日志", `DELETE FROM audit_logs WHERE action LIKE ? OR JSON_UNQUOTE(JSON_EXTRACT(details,'$.dataset'))=?`, []any{auditActionPrefix + "%", datasetMarker}},
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement.query, statement.args...); err != nil {
			return existingSummary{}, fmt.Errorf("清理模拟%s失败：%w", statement.name, err)
		}
	}
	return before, nil
}

func verifyExistingMarker(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, opts options, current existingSummary) error {
	if current.markers != 1 {
		return fmt.Errorf("发现 %d 个完成标记，无法确认数据集唯一性；请核对后使用 reset 模式重建", current.markers)
	}
	metadata, err := loadMarkerMetadata(ctx, q)
	if err != nil {
		return err
	}
	if metadata.readerCount != opts.readerCount || metadata.copyCount != opts.copyCount || metadata.returnedLoanCount != opts.returnedLoanCount || metadata.reservationCount != opts.reservationCount || metadata.randomSeed != opts.randomSeed {
		return fmt.Errorf("现有模拟数据参数与本次请求不同；现有为读者 %d、副本 %d、已归还借阅 %d、预约 %d、随机种子 %d，请使用原参数或通过 reset 模式重建", metadata.readerCount, metadata.copyCount, metadata.returnedLoanCount, metadata.reservationCount, metadata.randomSeed)
	}
	if current.readers != metadata.readerCount || current.copies != metadata.copyCount || current.loans != metadata.loanCount || current.reservations != metadata.reservationCount || current.fines != metadata.fineCount || current.audits != metadata.auditCount {
		return fmt.Errorf("现有模拟数据与完成标记不一致，可能被手工修改；标记记录读者 %d、副本 %d、借阅 %d、预约 %d、罚金 %d、审计 %d，请核对后使用 reset 模式重建", metadata.readerCount, metadata.copyCount, metadata.loanCount, metadata.reservationCount, metadata.fineCount, metadata.auditCount)
	}
	return nil
}

func loadMarkerMetadata(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) (markerMetadata, error) {
	values := make([]sql.NullInt64, 8)
	err := q.QueryRowContext(ctx, `
		SELECT
		  CAST(JSON_UNQUOTE(JSON_EXTRACT(details,'$.readers')) AS SIGNED),
		  CAST(JSON_UNQUOTE(JSON_EXTRACT(details,'$.copies')) AS SIGNED),
		  CAST(JSON_UNQUOTE(JSON_EXTRACT(details,'$.returnedLoans')) AS SIGNED),
		  CAST(JSON_UNQUOTE(JSON_EXTRACT(details,'$.reservations')) AS SIGNED),
		  CAST(JSON_UNQUOTE(JSON_EXTRACT(details,'$.randomSeed')) AS SIGNED),
		  CAST(JSON_UNQUOTE(JSON_EXTRACT(details,'$.loans')) AS SIGNED),
		  CAST(JSON_UNQUOTE(JSON_EXTRACT(details,'$.fines')) AS SIGNED),
		  CAST(JSON_UNQUOTE(JSON_EXTRACT(details,'$.auditLogs')) AS SIGNED)
		FROM audit_logs
		WHERE action=? AND JSON_UNQUOTE(JSON_EXTRACT(details,'$.dataset'))=?
		ORDER BY id DESC LIMIT 1`, markerAction, datasetMarker).Scan(&values[0], &values[1], &values[2], &values[3], &values[4], &values[5], &values[6], &values[7])
	if err != nil {
		return markerMetadata{}, fmt.Errorf("读取模拟数据完成标记失败：%w", err)
	}
	for _, value := range values {
		if !value.Valid || value.Int64 < 0 {
			return markerMetadata{}, errors.New("模拟数据完成标记缺少有效参数；请核对后使用 reset 模式重建")
		}
	}
	return markerMetadata{
		readerCount:       int(values[0].Int64),
		copyCount:         int(values[1].Int64),
		returnedLoanCount: int(values[2].Int64),
		reservationCount:  int(values[3].Int64),
		randomSeed:        values[4].Int64,
		loanCount:         int(values[5].Int64),
		fineCount:         int(values[6].Int64),
		auditCount:        int(values[7].Int64),
	}, nil
}

func inspectExisting(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) (existingSummary, error) {
	var result existingSummary
	err := q.QueryRowContext(ctx, `
		SELECT
		  (SELECT COUNT(*) FROM audit_logs WHERE action=? AND JSON_UNQUOTE(JSON_EXTRACT(details,'$.dataset'))=?),
		  (SELECT COUNT(*) FROM readers WHERE reader_no LIKE ?),
		  (SELECT COUNT(*) FROM book_copies WHERE LEFT(condition_note,CHAR_LENGTH(?))=?),
		  (SELECT COUNT(*) FROM loans l JOIN readers r ON r.id=l.reader_id WHERE r.reader_no LIKE ?),
		  (SELECT COUNT(*) FROM reservations rs JOIN readers r ON r.id=rs.reader_id WHERE r.reader_no LIKE ?),
		  (SELECT COUNT(*) FROM fines f JOIN readers r ON r.id=f.reader_id WHERE r.reader_no LIKE ?),
		  (SELECT COUNT(*) FROM audit_logs WHERE action LIKE ? OR JSON_UNQUOTE(JSON_EXTRACT(details,'$.dataset'))=?)`,
		markerAction, datasetMarker, readerPrefix+"%", copyNotePrefix, copyNotePrefix, readerPrefix+"%", readerPrefix+"%", readerPrefix+"%", auditActionPrefix+"%", datasetMarker,
	).Scan(&result.markers, &result.readers, &result.copies, &result.loans, &result.reservations, &result.fines, &result.audits)
	if err != nil {
		return existingSummary{}, fmt.Errorf("检查既有模拟数据失败：%w", err)
	}
	return result, nil
}

func loadBooks(ctx context.Context, q queryer) ([]bookRef, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT b.id,b.material_type,b.publish_year,
		       COALESCE(MAX(CASE WHEN c.barcode REGEXP '^(BK|MC)-[0-9]{4}-[0-9]+-[0-9]{4}$'
		                         THEN CAST(SUBSTRING_INDEX(c.barcode,'-',-1) AS UNSIGNED)
		                         ELSE 0 END),0)
		FROM books b
		JOIN catalog_source_records sr ON sr.id=b.primary_source_record_id AND sr.book_id=b.id AND sr.record_kind='edition'
		JOIN catalog_sources src ON src.id=sr.source_id AND src.code='openlibrary' AND src.is_active=TRUE
		JOIN catalog_import_batches ib ON ib.id=sr.batch_id AND ib.source_id=sr.source_id AND ib.status IN ('completed','completed_with_errors')
		LEFT JOIN book_copies c ON c.book_id=b.id
		WHERE b.source_provider='openlibrary'
		  AND b.verification_status IN ('source_matched','human_verified')
		  AND b.collection_status='active'
		  AND b.primary_source_record_id IS NOT NULL
		  AND b.publish_year BETWEEN 1000 AND YEAR(DATE_SUB(CURRENT_DATE, INTERVAL 8 DAY))
		  AND NOT EXISTS (
		      SELECT 1 FROM reservations existing_reservation
		      WHERE existing_reservation.book_id=b.id
		        AND existing_reservation.status IN ('waiting','ready')
		  )
		GROUP BY b.id,b.material_type,b.publish_year
		ORDER BY b.id`)
	if err != nil {
		return nil, fmt.Errorf("读取现有书目失败：%w", err)
	}
	defer rows.Close()
	var books []bookRef
	for rows.Next() {
		var book bookRef
		if err := rows.Scan(&book.id, &book.materialType, &book.publishYear, &book.maxSequence); err != nil {
			return nil, fmt.Errorf("读取书目字段失败：%w", err)
		}
		if book.materialType != "book" && book.materialType != "periodical" {
			return nil, fmt.Errorf("书目 %d 的资料类型 %q 不受支持", book.id, book.materialType)
		}
		if book.publishYear < 1000 || book.publishYear > time.Now().AddDate(0, 0, -8).Year() {
			return nil, fmt.Errorf("书目 %d 的出版年份 %d 不适合生成已入库馆藏", book.id, book.publishYear)
		}
		books = append(books, book)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历书目失败：%w", err)
	}
	if len(books) == 0 {
		return nil, errors.New("数据库中没有来自已完成 Open Library 导入批次、通过来源核验且已正式出版的启用书目，不能生成实体副本和流通数据")
	}
	return books, nil
}

func buildDataset(opts options, books []bookRef, anchor time.Time) (*dataset, error) {
	rng := rand.New(rand.NewSource(opts.randomSeed))
	layout, err := deriveLayout(opts, len(books))
	if err != nil {
		return nil, err
	}
	data := &dataset{books: books, layout: layout, anchor: anchor, randomSeed: opts.randomSeed}
	data.readers = buildReaders(opts.readerCount, anchor, rng)
	data.copies, err = buildCopies(opts.copyCount, books, layout, anchor, rng)
	if err != nil {
		return nil, err
	}
	data.loans, err = buildLoans(opts.returnedLoanCount, data, rng)
	if err != nil {
		return nil, err
	}
	data.fines = buildFines(data, rng)
	settleFinesBeforeLaterBorrow(data)
	applyFineBlocks(data)
	removeBlockedReaderRenewals(data)
	data.reservations, err = buildReservations(opts.reservationCount, data, rng)
	if err != nil {
		return nil, err
	}
	return data, nil
}

func deriveLayout(opts options, bookCount int) (circulationLayout, error) {
	activeLoans := min(opts.readerCount*7/10, opts.copyCount/4)
	overdueLoans := min(max(1, opts.readerCount/10), opts.copyCount/20)
	lostLoans := max(1, opts.copyCount/100)
	readyCopies := min(max(1, opts.reservationCount/20), bookCount)
	maintenance := max(1, opts.copyCount/25)
	needed := activeLoans + overdueLoans + lostLoans + readyCopies + maintenance
	if needed >= opts.copyCount {
		return circulationLayout{}, fmt.Errorf("实体副本数量 %d 不足以容纳计划中的流通状态，至少需要 %d", opts.copyCount, needed+1)
	}
	return circulationLayout{
		activeLoans:      activeLoans,
		overdueLoans:     overdueLoans,
		lostLoans:        lostLoans,
		readyCopies:      readyCopies,
		maintenance:      maintenance,
		activeStart:      0,
		overdueStart:     activeLoans,
		lostStart:        activeLoans + overdueLoans,
		readyStart:       activeLoans + overdueLoans + lostLoans,
		maintenanceStart: activeLoans + overdueLoans + lostLoans + readyCopies,
	}, nil
}

func buildReaders(count int, anchor time.Time, rng *rand.Rand) []readerRow {
	surnames := []string{"王", "李", "张", "刘", "陈", "杨", "黄", "赵", "吴", "周", "徐", "孙", "马", "朱", "胡", "郭", "何", "高", "林", "罗", "郑", "梁", "谢", "宋", "唐", "许", "韩", "冯", "邓", "曹", "彭", "曾", "肖", "田", "董", "潘", "袁", "蔡", "蒋", "余", "于", "杜", "叶", "程", "魏", "苏", "吕", "丁", "任", "沈", "姚", "卢", "姜", "崔", "钟", "谭", "陆", "汪", "范", "金", "石", "廖", "贾", "夏", "韦", "傅", "方", "白", "邹", "孟", "熊", "秦", "邱", "江", "尹", "薛", "闫", "段", "雷", "侯"}
	givenNames := []string{"子涵", "宇轩", "雨桐", "浩然", "思远", "嘉宁", "沐阳", "晓晨", "文博", "欣怡", "梓萱", "俊杰", "佳琪", "晨曦", "若溪", "致远", "明哲", "语彤", "一诺", "书航", "亦辰", "舒雅", "景行", "清妍", "知夏", "嘉树", "安然", "昕玥", "承泽", "静姝", "泽宇", "梦瑶", "翊宸", "可欣", "博文", "思齐", "婉清", "天佑", "锦程", "雅雯", "睿哲", "诗涵", "奕帆", "依琳", "星宇", "嘉禾", "云舒", "怀瑾", "昭宁", "予安", "弘毅", "乐瑶", "凯文", "佩珊", "俊熙", "雪晴", "彦博", "芷宁", "铭远", "月华", "庆川", "兰馨", "向晨", "心怡", "启航", "念慈", "瑞阳", "楚涵", "卓然", "令仪", "绍辉", "曼青", "修远", "映雪", "柏舟", "采薇", "景澄", "柔嘉", "正则", "清越", "明朗", "予希", "嘉言", "望舒", "南乔", "维桢", "灵均", "令闻", "星河", "若谷", "昭华", "成蹊", "怀信", "语安", "靖川", "舒窈", "彦辰", "知许", "清和", "元嘉"}
	phonePrefixes := []string{"130", "131", "132", "135", "136", "137", "138", "139", "150", "151", "152", "157", "158", "159", "166", "171", "173", "175", "176", "177", "178", "180", "181", "182", "183", "185", "186", "187", "188", "189", "191", "193", "195", "198", "199"}
	readers := make([]readerRow, count)
	for i := range readers {
		status := "active"
		switch (i + 1) % 50 {
		case 0:
			status = "suspended"
		case 1:
			status = "expired"
		}
		limit := 5
		if i%10 == 0 {
			limit = 10
		} else if i%10 <= 2 {
			limit = 8
		}
		createdAt := anchor.AddDate(0, 0, -(800 + rng.Intn(3000))).Add(time.Duration(8+rng.Intn(10)) * time.Hour)
		updatedAt := createdAt.AddDate(0, 0, 30+rng.Intn(max(31, int(anchor.Sub(createdAt).Hours()/24)-30)))
		if updatedAt.After(anchor) {
			updatedAt = anchor.Add(-time.Duration(rng.Intn(180)) * 24 * time.Hour)
		}
		readers[i] = readerRow{
			readerNo:  fmt.Sprintf("%s%06d", readerPrefix, i+1),
			fullName:  surnames[i%len(surnames)] + givenNames[(i*17+i/len(surnames))%len(givenNames)],
			phone:     fmt.Sprintf("%s****%04d", phonePrefixes[i%len(phonePrefixes)], (i*7919+2371)%10000),
			email:     fmt.Sprintf("reader%06d@demo.library.example", i+1),
			status:    status,
			loanLimit: limit,
			createdAt: createdAt,
			updatedAt: updatedAt,
		}
	}
	return readers
}

func buildCopies(count int, books []bookRef, layout circulationLayout, anchor time.Time, rng *rand.Rand) ([]copyRow, error) {
	branches := []string{"总馆", "东区分馆", "西区分馆", "少儿分馆", "大学城分馆"}
	areas := []string{"文学阅览区", "社会科学区", "自然科学区", "语言文化区", "综合借阅区", "经典藏书区"}
	sequences := make([]uint64, len(books))
	for i, book := range books {
		sequences[i] = book.maxSequence
	}
	copies := make([]copyRow, count)
	for i := range copies {
		bookIndex := i % len(books)
		sequences[bookIndex]++
		if sequences[bookIndex] > 9999 {
			return nil, fmt.Errorf("书目 %d 的副本序号将超过四位条码上限", books[bookIndex].id)
		}
		ageDays := 30 + rng.Intn(870)
		if i < count*3/4 {
			ageDays = 900 + rng.Intn(2700)
		}
		acquiredAt := anchor.AddDate(0, 0, -ageDays)
		publishedAt := time.Date(books[bookIndex].publishYear, time.January, 1, 0, 0, 0, 0, anchor.Location())
		latestAcquiredAt := anchor.AddDate(0, 0, -8)
		if publishedAt.After(latestAcquiredAt) {
			return nil, fmt.Errorf("书目 %d 的出版年份 %d 距锚点过近，无法生成不晚于锚点的可信入库及流通时间", books[bookIndex].id, books[bookIndex].publishYear)
		}
		if acquiredAt.Before(publishedAt) {
			availableDays := int(latestAcquiredAt.Sub(publishedAt).Hours() / 24)
			acquiredAt = publishedAt.AddDate(0, 0, rng.Intn(min(availableDays, 119)+1))
		}
		if acquiredAt.After(latestAcquiredAt) {
			return nil, fmt.Errorf("书目 %d 的出版时间过新，无法生成可信的馆藏入库日期", books[bookIndex].id)
		}
		ageDays = int(anchor.Sub(acquiredAt).Hours() / 24)
		prefix := "BK"
		if books[bookIndex].materialType == "periodical" {
			prefix = "MC"
		}
		status := "available"
		conditionStatus := "intact"
		conditionText := "馆藏检查完好"
		switch {
		case i >= layout.activeStart && i < layout.overdueStart:
			status = "borrowed"
		case i >= layout.overdueStart && i < layout.lostStart:
			status = "borrowed"
		case i >= layout.lostStart && i < layout.readyStart:
			status = "lost"
			if ageDays >= 365 && i%3 == 0 {
				conditionStatus = "minor_wear"
			}
			conditionText = "读者报失，等待赔付处理"
		case i >= layout.readyStart && i < layout.maintenanceStart:
			status = "reserved"
		case i >= layout.maintenanceStart && i < layout.maintenanceStart+layout.maintenance:
			status = "maintenance"
			if ageDays < 365 {
				conditionStatus = "pending_inspection"
				conditionText = "新到馆验收发现装订异常，等待复核"
			} else if i%3 == 0 {
				conditionStatus = "pending_inspection"
				conditionText = "盘点发现异常，等待复核"
			} else {
				conditionStatus = "damaged"
				conditionText = "装订开裂，已转修复"
			}
		default:
			if ageDays >= 365 && i%7 == 0 {
				conditionStatus = "minor_wear"
				conditionText = "书脊轻微磨损，可正常流通"
			}
		}
		createdAt := acquiredAt.Add(time.Duration(9+rng.Intn(8))*time.Hour + time.Duration(rng.Intn(60))*time.Minute)
		updatedAt := createdAt
		if status == "maintenance" {
			updatedAt = anchor.AddDate(0, 0, -(1 + i%180)).Add(time.Duration(8+i%9) * time.Hour)
			if updatedAt.Before(createdAt) {
				updatedAt = createdAt
			}
			if updatedAt.After(anchor) {
				updatedAt = anchor
			}
		}
		copies[i] = copyRow{
			bookIndex:       bookIndex,
			barcode:         fmt.Sprintf("%s-%04d-%d-%04d", prefix, acquiredAt.Year(), books[bookIndex].id, sequences[bookIndex]),
			location:        fmt.Sprintf("%s-%s-%c%02d-%02d-%02d", branches[(i/97)%len(branches)], areas[bookIndex%len(areas)], 'A'+rune((i/31)%6), 1+(i/13)%24, 1+(i/7)%8, 1+i%6),
			status:          status,
			conditionStatus: conditionStatus,
			conditionNote:   copyNotePrefix + conditionText,
			acquiredAt:      acquiredAt,
			createdAt:       createdAt,
			updatedAt:       updatedAt,
		}
	}
	return copies, nil
}

func buildLoans(returnedCount int, data *dataset, rng *rand.Rand) ([]loanRow, error) {
	activeReaders := make([]int, 0, len(data.readers))
	historyReaders := make([]int, 0, len(data.readers))
	for i, reader := range data.readers {
		if reader.status == "active" {
			activeReaders = append(activeReaders, i)
		}
		if reader.createdAt.Before(data.anchor.AddDate(0, 0, -730)) {
			historyReaders = append(historyReaders, i)
		}
	}
	if len(activeReaders) == 0 || len(historyReaders) == 0 {
		return nil, errors.New("缺少可用于生成借阅的读者")
	}

	cyclesPerCopy := make([]int, len(data.copies))
	cycleCapacity := make([]int, len(data.copies))
	totalCapacity := 0
	for copyIndex, copy := range data.copies {
		start := copy.acquiredAt.AddDate(0, 0, 30)
		end := historicalEndForCopy(data, copyIndex)
		capacity := int(end.Sub(start)/(120*24*time.Hour)) - 1
		capacity = min(max(capacity, 0), 4)
		cycleCapacity[copyIndex] = capacity
		totalCapacity += capacity
	}
	if totalCapacity < returnedCount {
		return nil, fmt.Errorf("现有版本的可信入库时间最多支持 %d 条历史借阅，少于请求的 %d 条；请增加较早出版的书目或降低历史借阅数量", totalCapacity, returnedCount)
	}
	remaining := returnedCount
	for remaining > 0 {
		progress := false
		for copyIndex := range data.copies {
			if remaining == 0 {
				break
			}
			if cyclesPerCopy[copyIndex] >= cycleCapacity[copyIndex] {
				continue
			}
			cyclesPerCopy[copyIndex]++
			remaining--
			progress = true
		}
		if !progress {
			return nil, errors.New("无法为历史借阅分配不重叠的时间窗口")
		}
	}
	loans := make([]loanRow, 0, returnedCount+data.layout.activeLoans+data.layout.overdueLoans+data.layout.lostLoans)
	ordinal := 0
	for copyIndex, cycles := range cyclesPerCopy {
		for cycle := 0; cycle < cycles; cycle++ {
			start := data.copies[copyIndex].acquiredAt.AddDate(0, 0, 30)
			end := historicalEndForCopy(data, copyIndex)
			if !end.After(start.AddDate(0, 0, 120)) {
				return nil, fmt.Errorf("副本 %s 没有足够的历史借阅时间窗口", data.copies[copyIndex].barcode)
			}
			span := end.Sub(start)
			borrowedAt := start.Add(time.Duration(cycle+1) * span / time.Duration(cycles+1)).Add(time.Duration(9+(ordinal%9)) * time.Hour)
			readerIndex, err := historicalReaderForLoan(historyReaders, data.readers, (copyIndex*31+cycle*101)%len(historyReaders), borrowedAt)
			if err != nil {
				return nil, fmt.Errorf("副本 %s：%w", data.copies[copyIndex].barcode, err)
			}
			renewCount := 0
			if ordinal%10 == 0 {
				renewCount = 1
			}
			dueAt := borrowedAt.AddDate(0, 0, 30*(renewCount+1))
			returnedAt := borrowedAt.AddDate(0, 0, 10+(ordinal*7)%45)
			if ordinal%12 == 0 {
				returnedAt = dueAt.AddDate(0, 0, 2+(ordinal%24))
			}
			if returnedAt.After(end) {
				returnedAt = end.Add(-24 * time.Hour)
			}
			returned := returnedAt
			loans = append(loans, loanRow{
				readerIndex: readerIndex,
				copyIndex:   copyIndex,
				borrowedAt:  borrowedAt,
				dueAt:       dueAt,
				returnedAt:  &returned,
				renewCount:  renewCount,
				status:      "returned",
				createdAt:   borrowedAt,
				updatedAt:   returnedAt,
			})
			ordinal++
		}
	}

	currentCounts := make([]int, len(data.readers))
	readerCursor := 0
	for i := 0; i < data.layout.activeLoans+data.layout.overdueLoans; i++ {
		readerIndex, nextCursor, err := nextEligibleReader(activeReaders, currentCounts, data.readers, readerCursor)
		if err != nil {
			return nil, err
		}
		readerCursor = nextCursor
		currentCounts[readerIndex]++
		copyIndex := i
		status := "active"
		borrowedAt := currentBorrowedAt(data, copyIndex)
		renewCount := 0
		if i%8 == 0 {
			renewCount = 1
		}
		dueAt := borrowedAt.AddDate(0, 0, 30*(renewCount+1))
		if i >= data.layout.overdueStart {
			status = "overdue"
			if !dueAt.Before(data.anchor) {
				dueAt = data.anchor.AddDate(0, 0, -(1 + i%30))
			}
		}
		loans = append(loans, loanRow{
			readerIndex: readerIndex,
			copyIndex:   copyIndex,
			borrowedAt:  borrowedAt,
			dueAt:       dueAt,
			renewCount:  renewCount,
			status:      status,
			createdAt:   borrowedAt,
			updatedAt:   borrowedAt,
		})
		data.copies[copyIndex].updatedAt = borrowedAt
	}

	for i := 0; i < data.layout.lostLoans; i++ {
		copyIndex := data.layout.lostStart + i
		readerIndex := activeReaders[(i*43+17)%len(activeReaders)]
		borrowedAt := currentBorrowedAt(data, copyIndex)
		lostAt := data.anchor.AddDate(0, 0, -(2 + i%45)).Add(time.Duration(10+i%7) * time.Hour)
		loans = append(loans, loanRow{
			readerIndex: readerIndex,
			copyIndex:   copyIndex,
			borrowedAt:  borrowedAt,
			dueAt:       borrowedAt.AddDate(0, 0, 30),
			status:      "lost",
			createdAt:   borrowedAt,
			updatedAt:   lostAt,
		})
		data.copies[copyIndex].updatedAt = lostAt
	}
	return loans, nil
}

func historicalReaderForLoan(candidates []int, readers []readerRow, cursor int, borrowedAt time.Time) (int, error) {
	for checked := 0; checked < len(candidates); checked++ {
		readerIndex := candidates[(cursor+checked)%len(candidates)]
		if readers[readerIndex].createdAt.AddDate(0, 0, 7).Before(borrowedAt) {
			return readerIndex, nil
		}
	}
	return 0, errors.New("没有在该借阅日期前完成登记的读者")
}

func historicalEndForCopy(data *dataset, copyIndex int) time.Time {
	if copyIndex < data.layout.readyStart {
		return currentBorrowedAt(data, copyIndex).AddDate(0, 0, -30)
	}
	return data.anchor.AddDate(0, 0, -60)
}

func currentBorrowedAt(data *dataset, copyIndex int) time.Time {
	var borrowedAt time.Time
	switch {
	case copyIndex < data.layout.overdueStart:
		borrowedAt = data.anchor.AddDate(0, 0, -(2 + copyIndex%24)).Add(time.Duration(8+copyIndex%10) * time.Hour)
	case copyIndex < data.layout.lostStart:
		borrowedAt = data.anchor.AddDate(0, 0, -(75 + (copyIndex-data.layout.overdueStart)%90)).Add(time.Duration(8+copyIndex%10) * time.Hour)
	default:
		borrowedAt = data.anchor.AddDate(0, 0, -(90 + (copyIndex-data.layout.lostStart)%180)).Add(time.Duration(8+copyIndex%10) * time.Hour)
	}
	minimum := data.copies[copyIndex].acquiredAt.AddDate(0, 0, 7).Add(9 * time.Hour)
	if borrowedAt.Before(minimum) {
		return minimum
	}
	return borrowedAt
}

func nextEligibleReader(active []int, counts []int, readers []readerRow, cursor int) (int, int, error) {
	for checked := 0; checked < len(active); checked++ {
		position := (cursor + checked) % len(active)
		readerIndex := active[position]
		if counts[readerIndex] < readers[readerIndex].loanLimit {
			return readerIndex, (position + 1) % len(active), nil
		}
	}
	return 0, cursor, errors.New("所有正常读者均已达到借阅上限，无法安排更多当前借阅")
}

func buildFines(data *dataset, rng *rand.Rand) []fineRow {
	fines := make([]fineRow, 0, len(data.loans)/12+data.layout.lostLoans)
	returnedSeen := 0
	for loanIndex, loan := range data.loans {
		if loan.status != "returned" {
			continue
		}
		if returnedSeen%12 == 0 {
			createdAt := loan.returnedAt.Add(time.Duration(1+rng.Intn(48)) * time.Hour)
			status := "paid"
			ageDays := int(data.anchor.Sub(createdAt).Hours() / 24)
			if ageDays > 365 {
				if returnedSeen%10 >= 8 {
					status = "waived"
				}
			} else {
				switch returnedSeen % 10 {
				case 0, 1, 2, 3, 4:
					status = "paid"
				case 5, 6:
					status = "waived"
				default:
					status = "unpaid"
				}
			}
			lateDays := max(1, int(loan.returnedAt.Sub(loan.dueAt).Hours()/24))
			reason := fmt.Sprintf("逾期归还 %d 天", lateDays)
			amount := fmt.Sprintf("%.2f", float64(min(lateDays*5, 100)))
			if returnedSeen%7 == 0 {
				reason = "封面污损修复费"
				amount = fmt.Sprintf("%d.00", 10+(returnedSeen%5)*5)
			}
			var paidAt *time.Time
			updatedAt := createdAt
			if status == "paid" {
				paid := createdAt.AddDate(0, 0, 1+returnedSeen%14)
				if paid.After(data.anchor) {
					paid = data.anchor.Add(-time.Duration(1+returnedSeen%72) * time.Hour)
				}
				paidAt = &paid
				updatedAt = paid
			} else if status == "waived" {
				reason += "（闭馆或特殊情况核准减免）"
				updatedAt = createdAt.AddDate(0, 0, 1+returnedSeen%5)
			}
			fines = append(fines, fineRow{loanIndex: loanIndex, readerIndex: loan.readerIndex, amount: amount, reason: reason, origin: "manual", status: status, paidAt: paidAt, createdAt: createdAt, updatedAt: updatedAt})
		}
		returnedSeen++
	}

	for loanIndex, loan := range data.loans {
		if loan.status != "lost" {
			continue
		}
		status := "unpaid"
		lostOrdinal := len(fines)
		switch lostOrdinal % 10 {
		case 7, 8:
			status = "paid"
		case 9:
			status = "waived"
		}
		createdAt := loan.updatedAt
		amount := fmt.Sprintf("%d.00", 45+(loan.copyIndex%12)*10)
		reason := "图书遗失赔偿（按馆藏定价核定）"
		var paidAt *time.Time
		updatedAt := createdAt
		if status == "paid" {
			paid := createdAt.AddDate(0, 0, 1+loan.copyIndex%10)
			if paid.After(data.anchor) {
				paid = data.anchor.Add(-time.Duration(1+loan.copyIndex%24) * time.Hour)
			}
			paidAt = &paid
			updatedAt = paid
		} else if status == "waived" {
			reason = "经馆藏委员会复核，核准减免遗失赔偿"
			updatedAt = createdAt.AddDate(0, 0, 1)
		}
		fines = append(fines, fineRow{loanIndex: loanIndex, readerIndex: loan.readerIndex, amount: amount, reason: reason, origin: "lost", status: status, paidAt: paidAt, createdAt: createdAt, updatedAt: updatedAt})
	}
	return fines
}

func applyFineBlocks(data *dataset) {
	blocked := make([]bool, len(data.readers))
	for _, fine := range data.fines {
		if fine.status == "unpaid" {
			blocked[fine.readerIndex] = true
		}
	}
	for i := range data.readers {
		data.readers[i].fineBlocked = blocked[i]
	}
}

func settleFinesBeforeLaterBorrow(data *dataset) {
	borrowsByReader := make([][]time.Time, len(data.readers))
	for _, loan := range data.loans {
		borrowsByReader[loan.readerIndex] = append(borrowsByReader[loan.readerIndex], loan.borrowedAt)
	}
	for readerIndex := range borrowsByReader {
		sort.Slice(borrowsByReader[readerIndex], func(i, j int) bool {
			return borrowsByReader[readerIndex][i].Before(borrowsByReader[readerIndex][j])
		})
	}
	for fineIndex := range data.fines {
		fine := &data.fines[fineIndex]
		if fine.status != "unpaid" {
			continue
		}
		borrows := borrowsByReader[fine.readerIndex]
		next := sort.Search(len(borrows), func(i int) bool {
			return !borrows[i].Before(fine.createdAt)
		})
		if next == len(borrows) {
			continue
		}
		paidAt := fine.createdAt.Add(48 * time.Hour)
		latestPaidAt := borrows[next].Add(-time.Hour)
		if paidAt.After(latestPaidAt) {
			paidAt = latestPaidAt
		}
		if paidAt.After(fine.createdAt) {
			fine.status = "paid"
			fine.paidAt = &paidAt
			fine.updatedAt = paidAt
		} else {
			fine.status = "waived"
			fine.paidAt = nil
			fine.updatedAt = fine.createdAt
			fine.reason += "（后续借阅前完成复核减免）"
		}
	}
}

func removeBlockedReaderRenewals(data *dataset) {
	for loanIndex := range data.loans {
		loan := &data.loans[loanIndex]
		if (loan.status != "active" && loan.status != "overdue") || !data.readers[loan.readerIndex].fineBlocked || loan.renewCount == 0 {
			continue
		}
		loan.renewCount = 0
		loan.dueAt = loan.borrowedAt.AddDate(0, 0, 30)
	}
}

func buildReservations(count int, data *dataset, rng *rand.Rand) ([]reservationRow, error) {
	candidates := make([]int, 0, len(data.readers))
	for i, reader := range data.readers {
		if reader.status == "active" && !reader.fineBlocked {
			candidates = append(candidates, i)
		}
	}
	if len(candidates) == 0 {
		return nil, errors.New("没有可用于预约的正常且未被罚金冻结的读者")
	}

	readyCount := data.layout.readyCopies
	waitingCount := min(count/5, count-readyCount)
	historicalCount := count - readyCount - waitingCount
	fulfilledCount := historicalCount * 3 / 5
	cancelledCount := historicalCount * 4 / 15
	expiredCount := historicalCount - fulfilledCount - cancelledCount
	reservations := make([]reservationRow, 0, count)
	positions := make(map[int]int)
	activePairs := make(map[string]struct{}, readyCount+waitingCount)
	readyBooks := make([]int, 0, readyCount)
	readyCreatedAt := make(map[int]time.Time, readyCount)
	readerCursor := 0

	chooseReader := func(bookIndex int) (int, error) {
		for checked := 0; checked < len(candidates); checked++ {
			position := (readerCursor + checked) % len(candidates)
			readerIndex := candidates[position]
			key := fmt.Sprintf("%d:%d", readerIndex, bookIndex)
			if _, exists := activePairs[key]; !exists {
				activePairs[key] = struct{}{}
				readerCursor = (position + 1) % len(candidates)
				return readerIndex, nil
			}
		}
		return 0, fmt.Errorf("书目 %d 找不到未重复预约的读者", data.books[bookIndex].id)
	}

	for i := 0; i < readyCount; i++ {
		copyIndex := data.layout.readyStart + i
		bookIndex := data.copies[copyIndex].bookIndex
		readerIndex, err := chooseReader(bookIndex)
		if err != nil {
			return nil, err
		}
		positions[bookIndex]++
		readyBooks = append(readyBooks, bookIndex)
		expiresAt := data.anchor.Add(36*time.Hour + time.Duration(i%35)*time.Hour)
		readyAt := expiresAt.Add(-72 * time.Hour)
		createdAt := readyAt.AddDate(0, 0, -(7 + i%45))
		expires := expiresAt
		reservations = append(reservations, reservationRow{readerIndex: readerIndex, bookIndex: bookIndex, reservedCopyIndex: copyIndex, queuePosition: positions[bookIndex], status: "ready", expiresAt: &expires, createdAt: createdAt, updatedAt: readyAt})
		readyCreatedAt[bookIndex] = createdAt
		data.copies[copyIndex].updatedAt = readyAt
	}

	if waitingCount > 0 && len(readyBooks) == 0 {
		return nil, errors.New("没有待取书目可承接等候队列")
	}
	waitingBooks := make([]int, waitingCount)
	waitingTotals := make(map[int]int, len(readyBooks))
	for i := range waitingBooks {
		waitingBooks[i] = readyBooks[(i*17+13)%len(readyBooks)]
		waitingTotals[waitingBooks[i]]++
	}
	waitingSeen := make(map[int]int, len(readyBooks))
	for _, bookIndex := range waitingBooks {
		readerIndex, err := chooseReader(bookIndex)
		if err != nil {
			return nil, err
		}
		positions[bookIndex]++
		waitingSeen[bookIndex]++
		start := readyCreatedAt[bookIndex]
		end := data.anchor.Add(-time.Hour)
		createdAt := start.Add(time.Duration(waitingSeen[bookIndex]) * end.Sub(start) / time.Duration(waitingTotals[bookIndex]+1))
		reservations = append(reservations, reservationRow{readerIndex: readerIndex, bookIndex: bookIndex, reservedCopyIndex: -1, queuePosition: positions[bookIndex], status: "waiting", createdAt: createdAt, updatedAt: createdAt})
	}

	returnedLoanCount := countLoans(data.loans, "returned")
	if fulfilledCount > returnedLoanCount {
		return nil, fmt.Errorf("已完成预约数量 %d 超过可关联的已归还借阅数量 %d", fulfilledCount, returnedLoanCount)
	}
	for i := 0; i < fulfilledCount; i++ {
		loanIndex := i * returnedLoanCount / fulfilledCount
		loan := data.loans[loanIndex]
		bookIndex := data.copies[loan.copyIndex].bookIndex
		createdAt := loan.borrowedAt.AddDate(0, 0, -(2 + i%20))
		minimum := data.readers[loan.readerIndex].createdAt.AddDate(0, 0, 1)
		if createdAt.Before(minimum) {
			createdAt = minimum
		}
		reservations = append(reservations, reservationRow{readerIndex: loan.readerIndex, bookIndex: bookIndex, reservedCopyIndex: -1, queuePosition: 1 + i%5, status: "fulfilled", createdAt: createdAt, updatedAt: loan.borrowedAt})
	}
	historicalStatuses := make([]string, 0, cancelledCount+expiredCount)
	for i := 0; i < cancelledCount; i++ {
		historicalStatuses = append(historicalStatuses, "cancelled")
	}
	for i := 0; i < expiredCount; i++ {
		historicalStatuses = append(historicalStatuses, "expired")
	}
	for i, status := range historicalStatuses {
		bookIndex := (i*29 + 7) % len(data.books)
		readerIndex := (i*47 + 19) % len(data.readers)
		createdAt := data.anchor.AddDate(0, 0, -(30 + rng.Intn(1400))).Add(time.Duration(8+i%10) * time.Hour)
		minimum := data.readers[readerIndex].createdAt.AddDate(0, 0, 2)
		if createdAt.Before(minimum) {
			createdAt = minimum
		}
		updatedAt := createdAt.AddDate(0, 0, 1+i%12)
		reservations = append(reservations, reservationRow{readerIndex: readerIndex, bookIndex: bookIndex, reservedCopyIndex: -1, queuePosition: 1 + i%5, status: status, createdAt: createdAt, updatedAt: updatedAt})
	}
	return reservations, nil
}

func validateDataset(data *dataset) error {
	if len(data.readers) == 0 || len(data.copies) == 0 || len(data.loans) == 0 {
		return errors.New("数据集不能为空")
	}
	readerNumbers := make(map[string]struct{}, len(data.readers))
	barcodes := make(map[string]struct{}, len(data.copies))
	for _, reader := range data.readers {
		if !strings.HasPrefix(reader.readerNo, readerPrefix) {
			return fmt.Errorf("读者编号 %q 缺少模拟前缀", reader.readerNo)
		}
		if _, exists := readerNumbers[reader.readerNo]; exists {
			return fmt.Errorf("读者编号重复：%s", reader.readerNo)
		}
		readerNumbers[reader.readerNo] = struct{}{}
		if reader.createdAt.After(reader.updatedAt) || reader.updatedAt.After(data.anchor) {
			return fmt.Errorf("读者 %s 的建档时间线无效", reader.readerNo)
		}
	}
	for _, copy := range data.copies {
		if copy.bookIndex < 0 || copy.bookIndex >= len(data.books) {
			return fmt.Errorf("副本 %s 引用了越界书目", copy.barcode)
		}
		if !structuredBarcodePattern.MatchString(copy.barcode) {
			return fmt.Errorf("副本条码不符合结构化规则：%s", copy.barcode)
		}
		if _, exists := barcodes[copy.barcode]; exists {
			return fmt.Errorf("副本条码重复：%s", copy.barcode)
		}
		barcodes[copy.barcode] = struct{}{}
		if !strings.HasPrefix(copy.conditionNote, copyNotePrefix) {
			return fmt.Errorf("副本 %s 缺少数据集标记", copy.barcode)
		}
		publishedAt := time.Date(data.books[copy.bookIndex].publishYear, time.January, 1, 0, 0, 0, 0, data.anchor.Location())
		if copy.acquiredAt.Before(publishedAt) || copy.acquiredAt.After(copy.createdAt) || copy.createdAt.After(copy.updatedAt) || copy.updatedAt.After(data.anchor) {
			return fmt.Errorf("副本 %s 的出版、入库或更新时间线无效", copy.barcode)
		}
		if (copy.conditionStatus == "damaged" || copy.conditionStatus == "pending_inspection") && copy.status != "maintenance" {
			return fmt.Errorf("破损或待检查副本 %s 未进入维护状态", copy.barcode)
		}
		if copy.status == "maintenance" && copy.conditionStatus != "damaged" && copy.conditionStatus != "pending_inspection" {
			return fmt.Errorf("维护副本 %s 的品相状态不合理", copy.barcode)
		}
	}

	openByCopy := make(map[int][]int)
	currentCounts := make([]int, len(data.readers))
	loansByCopy := make(map[int][]loanRow)
	loanEventKeys := make(map[string]struct{}, len(data.loans))
	for loanIndex, loan := range data.loans {
		if loan.readerIndex < 0 || loan.readerIndex >= len(data.readers) || loan.copyIndex < 0 || loan.copyIndex >= len(data.copies) {
			return fmt.Errorf("借阅 %d 引用了越界记录", loanIndex)
		}
		if loan.borrowedAt.Before(data.copies[loan.copyIndex].acquiredAt) {
			return fmt.Errorf("借阅 %d 早于副本入库日期", loanIndex)
		}
		if loan.borrowedAt.Before(data.readers[loan.readerIndex].createdAt) || !loan.createdAt.Equal(loan.borrowedAt) || loan.updatedAt.Before(loan.createdAt) {
			return fmt.Errorf("借阅 %d 的读者建档或流水时间线无效", loanIndex)
		}
		loansByCopy[loan.copyIndex] = append(loansByCopy[loan.copyIndex], loan)
		loanEventKeys[reservationLoanKey(loan.readerIndex, data.copies[loan.copyIndex].bookIndex, loan.borrowedAt)] = struct{}{}
		switch loan.status {
		case "returned":
			if loan.returnedAt == nil || loan.returnedAt.Before(loan.borrowedAt) {
				return fmt.Errorf("已归还借阅 %d 的归还时间无效", loanIndex)
			}
		case "active":
			if loan.returnedAt != nil || !loan.dueAt.After(data.anchor) {
				return fmt.Errorf("在借记录 %d 的日期或状态无效", loanIndex)
			}
			if data.readers[loan.readerIndex].status != "active" {
				return fmt.Errorf("在借记录 %d 的读者当前不是正常状态", loanIndex)
			}
			openByCopy[loan.copyIndex] = append(openByCopy[loan.copyIndex], loanIndex)
			currentCounts[loan.readerIndex]++
		case "overdue":
			if loan.returnedAt != nil || !loan.dueAt.Before(data.anchor) {
				return fmt.Errorf("逾期记录 %d 的日期或状态无效", loanIndex)
			}
			if data.readers[loan.readerIndex].status != "active" {
				return fmt.Errorf("逾期记录 %d 的读者当前不是正常状态", loanIndex)
			}
			openByCopy[loan.copyIndex] = append(openByCopy[loan.copyIndex], loanIndex)
			currentCounts[loan.readerIndex]++
		case "lost":
			if loan.returnedAt != nil {
				return fmt.Errorf("遗失记录 %d 不应有归还时间", loanIndex)
			}
			openByCopy[loan.copyIndex] = append(openByCopy[loan.copyIndex], loanIndex)
		default:
			return fmt.Errorf("借阅 %d 的状态不受支持：%s", loanIndex, loan.status)
		}
	}
	for readerIndex, count := range currentCounts {
		if count > data.readers[readerIndex].loanLimit {
			return fmt.Errorf("读者 %s 当前借阅 %d 册，超过上限 %d", data.readers[readerIndex].readerNo, count, data.readers[readerIndex].loanLimit)
		}
	}
	if err := validateLoanLimitTimeline(data); err != nil {
		return err
	}
	for copyIndex, copyLoans := range loansByCopy {
		sort.Slice(copyLoans, func(i, j int) bool { return copyLoans[i].borrowedAt.Before(copyLoans[j].borrowedAt) })
		for i := 1; i < len(copyLoans); i++ {
			previous := copyLoans[i-1]
			if previous.returnedAt == nil || copyLoans[i].borrowedAt.Before(*previous.returnedAt) {
				return fmt.Errorf("副本 %s 存在时间重叠的借阅记录：上一笔借出 %s、归还 %v，下一笔借出 %s", data.copies[copyIndex].barcode, previous.borrowedAt.Format(time.DateTime), nullableTime(previous.returnedAt), copyLoans[i].borrowedAt.Format(time.DateTime))
			}
		}
	}

	readyByCopy := make(map[int]int)
	readyByBook := make(map[int]int)
	activeReaderBookPairs := make(map[string]int)
	fulfilledLoanKeys := make(map[string]int)
	activePositions := make(map[int][]int)
	activeReservationsByBook := make(map[int][]reservationRow)
	waitingReservations := make([]int, 0)
	for reservationIndex, reservation := range data.reservations {
		if reservation.readerIndex < 0 || reservation.readerIndex >= len(data.readers) || reservation.bookIndex < 0 || reservation.bookIndex >= len(data.books) {
			return fmt.Errorf("预约 %d 引用了越界记录", reservationIndex)
		}
		if reservation.createdAt.After(reservation.updatedAt) {
			return fmt.Errorf("预约 %d 的更新时间早于创建时间", reservationIndex)
		}
		if reservation.createdAt.Before(data.readers[reservation.readerIndex].createdAt) {
			return fmt.Errorf("预约 %d 早于读者建档时间", reservationIndex)
		}
		if (reservation.status == "ready" || reservation.status == "waiting") && (data.readers[reservation.readerIndex].status != "active" || data.readers[reservation.readerIndex].fineBlocked) {
			return fmt.Errorf("有效预约 %d 的读者不可预约", reservationIndex)
		}
		if reservation.status == "ready" || reservation.status == "waiting" {
			pair := fmt.Sprintf("%d:%d", reservation.readerIndex, reservation.bookIndex)
			if previous, exists := activeReaderBookPairs[pair]; exists {
				return fmt.Errorf("预约 %d 与预约 %d 是同一读者对同一书目的重复有效预约", reservationIndex, previous)
			}
			activeReaderBookPairs[pair] = reservationIndex
		}
		if reservation.status == "ready" {
			if reservation.reservedCopyIndex < 0 || reservation.expiresAt == nil || !reservation.expiresAt.After(data.anchor) || !reservation.expiresAt.After(reservation.updatedAt) {
				return fmt.Errorf("待取预约 %d 缺少有效的预留副本或过期时间", reservationIndex)
			}
			if data.copies[reservation.reservedCopyIndex].bookIndex != reservation.bookIndex {
				return fmt.Errorf("待取预约 %d 的书目与副本不一致", reservationIndex)
			}
			if reservation.updatedAt.Before(data.copies[reservation.reservedCopyIndex].acquiredAt) {
				return fmt.Errorf("待取预约 %d 的副本分配时间早于入库时间", reservationIndex)
			}
			if _, exists := readyByCopy[reservation.reservedCopyIndex]; exists {
				return fmt.Errorf("副本 %s 被多个待取预约占用", data.copies[reservation.reservedCopyIndex].barcode)
			}
			if _, exists := readyByBook[reservation.bookIndex]; exists {
				return fmt.Errorf("书目 %d 同时存在多个待取预约", data.books[reservation.bookIndex].id)
			}
			readyByCopy[reservation.reservedCopyIndex] = reservationIndex
			readyByBook[reservation.bookIndex] = reservationIndex
			activePositions[reservation.bookIndex] = append(activePositions[reservation.bookIndex], reservation.queuePosition)
			activeReservationsByBook[reservation.bookIndex] = append(activeReservationsByBook[reservation.bookIndex], reservation)
		} else {
			if reservation.reservedCopyIndex >= 0 {
				return fmt.Errorf("非待取预约 %d 残留预留副本", reservationIndex)
			}
			if reservation.expiresAt != nil {
				return fmt.Errorf("非待取预约 %d 残留过期时间", reservationIndex)
			}
			if reservation.status == "waiting" {
				activePositions[reservation.bookIndex] = append(activePositions[reservation.bookIndex], reservation.queuePosition)
				activeReservationsByBook[reservation.bookIndex] = append(activeReservationsByBook[reservation.bookIndex], reservation)
				waitingReservations = append(waitingReservations, reservationIndex)
			} else if reservation.status == "fulfilled" {
				key := reservationLoanKey(reservation.readerIndex, reservation.bookIndex, reservation.updatedAt)
				if _, exists := loanEventKeys[key]; !exists {
					return fmt.Errorf("已完成预约 %d 缺少同一读者和书目的借阅记录", reservationIndex)
				}
				if previous, exists := fulfilledLoanKeys[key]; exists {
					return fmt.Errorf("已完成预约 %d 与预约 %d 重复关联同一笔借阅", reservationIndex, previous)
				}
				fulfilledLoanKeys[key] = reservationIndex
			}
		}
	}
	healthyAvailableByBook := make(map[int]int)
	for _, copy := range data.copies {
		if copy.status == "available" && (copy.conditionStatus == "intact" || copy.conditionStatus == "minor_wear") {
			healthyAvailableByBook[copy.bookIndex]++
		}
	}
	for _, reservationIndex := range waitingReservations {
		reservation := data.reservations[reservationIndex]
		_, hasReady := readyByBook[reservation.bookIndex]
		if !hasReady && healthyAvailableByBook[reservation.bookIndex] > 0 {
			return fmt.Errorf("等候预约 %d 所属书目仍有健康可借副本且没有队首待取预约", reservationIndex)
		}
	}
	for bookIndex, positions := range activePositions {
		sort.Ints(positions)
		for i, position := range positions {
			if position != i+1 {
				return fmt.Errorf("书目 %d 的有效预约队列不连续", data.books[bookIndex].id)
			}
		}
	}
	for bookIndex, reservations := range activeReservationsByBook {
		sort.Slice(reservations, func(i, j int) bool { return reservations[i].queuePosition < reservations[j].queuePosition })
		for i := 1; i < len(reservations); i++ {
			if reservations[i].createdAt.Before(reservations[i-1].createdAt) {
				return fmt.Errorf("书目 %d 的预约建档时间与排队顺序相反", data.books[bookIndex].id)
			}
		}
	}

	lostFineByLoan := make(map[int]int)
	unpaidByReader := make([]bool, len(data.readers))
	borrowsByReader := make([][]time.Time, len(data.readers))
	for _, loan := range data.loans {
		borrowsByReader[loan.readerIndex] = append(borrowsByReader[loan.readerIndex], loan.borrowedAt)
	}
	for readerIndex := range borrowsByReader {
		sort.Slice(borrowsByReader[readerIndex], func(i, j int) bool {
			return borrowsByReader[readerIndex][i].Before(borrowsByReader[readerIndex][j])
		})
	}
	for fineIndex, fine := range data.fines {
		if fine.loanIndex < 0 || fine.loanIndex >= len(data.loans) || fine.readerIndex != data.loans[fine.loanIndex].readerIndex {
			return fmt.Errorf("罚金 %d 与借阅读者不一致", fineIndex)
		}
		if fine.createdAt.Before(data.loans[fine.loanIndex].borrowedAt) || fine.updatedAt.Before(fine.createdAt) {
			return fmt.Errorf("罚金 %d 的生成或更新时间线无效", fineIndex)
		}
		if fine.origin == "lost" {
			if data.loans[fine.loanIndex].status != "lost" {
				return fmt.Errorf("遗失罚金 %d 没有关联遗失借阅", fineIndex)
			}
			lostFineByLoan[fine.loanIndex]++
		} else if fine.origin != "manual" {
			return fmt.Errorf("罚金 %d 的来源不受支持：%s", fineIndex, fine.origin)
		}
		if fine.status == "unpaid" {
			unpaidByReader[fine.readerIndex] = true
			borrows := borrowsByReader[fine.readerIndex]
			next := sort.Search(len(borrows), func(i int) bool {
				return !borrows[i].Before(fine.createdAt)
			})
			if next < len(borrows) {
				return fmt.Errorf("未缴罚金 %d 之后仍存在借阅，违反罚金冻结规则", fineIndex)
			}
		}
		if fine.status == "paid" && fine.paidAt == nil {
			return fmt.Errorf("已缴罚金 %d 缺少缴费时间", fineIndex)
		}
		if fine.paidAt != nil && fine.paidAt.Before(fine.createdAt) {
			return fmt.Errorf("已缴罚金 %d 的缴费时间早于罚金生成时间", fineIndex)
		}
		if fine.status != "paid" && fine.paidAt != nil {
			return fmt.Errorf("非已缴罚金 %d 不应有缴费时间", fineIndex)
		}
	}
	for loanIndex, loan := range data.loans {
		if loan.status == "lost" && lostFineByLoan[loanIndex] != 1 {
			return fmt.Errorf("遗失借阅 %d 必须且只能有一笔遗失罚金", loanIndex)
		}
	}
	for readerIndex, reader := range data.readers {
		if reader.fineBlocked != unpaidByReader[readerIndex] {
			return fmt.Errorf("读者 %s 的罚金冻结标志不一致", reader.readerNo)
		}
	}
	for loanIndex, loan := range data.loans {
		if (loan.status == "active" || loan.status == "overdue") && data.readers[loan.readerIndex].fineBlocked && loan.renewCount > 0 {
			return fmt.Errorf("罚金冻结读者 %s 的当前借阅 %d 不应保留续借次数", data.readers[loan.readerIndex].readerNo, loanIndex)
		}
	}

	for copyIndex, copy := range data.copies {
		openLoans := openByCopy[copyIndex]
		_, ready := readyByCopy[copyIndex]
		switch copy.status {
		case "borrowed":
			if len(openLoans) != 1 || (data.loans[openLoans[0]].status != "active" && data.loans[openLoans[0]].status != "overdue") || ready {
				return fmt.Errorf("借出副本 %s 与当前借阅不一致", copy.barcode)
			}
		case "lost":
			if len(openLoans) != 1 || data.loans[openLoans[0]].status != "lost" || ready {
				return fmt.Errorf("遗失副本 %s 与遗失借阅不一致", copy.barcode)
			}
		case "reserved":
			if len(openLoans) != 0 || !ready {
				return fmt.Errorf("预留副本 %s 与待取预约不一致", copy.barcode)
			}
		case "available", "maintenance":
			if len(openLoans) != 0 || ready {
				return fmt.Errorf("副本 %s 的空闲或维护状态仍占用业务记录", copy.barcode)
			}
		default:
			return fmt.Errorf("副本 %s 的状态不受支持：%s", copy.barcode, copy.status)
		}
	}
	return nil
}

func validateLoanLimitTimeline(data *dataset) error {
	type loanEvent struct {
		at    time.Time
		delta int
	}
	eventsByReader := make([][]loanEvent, len(data.readers))
	for _, loan := range data.loans {
		end := data.anchor.Add(24 * time.Hour)
		if loan.returnedAt != nil {
			end = *loan.returnedAt
		} else if loan.status == "lost" {
			end = loan.updatedAt
		}
		eventsByReader[loan.readerIndex] = append(eventsByReader[loan.readerIndex], loanEvent{at: loan.borrowedAt, delta: 1}, loanEvent{at: end, delta: -1})
	}
	for readerIndex, events := range eventsByReader {
		sort.Slice(events, func(i, j int) bool {
			if events[i].at.Equal(events[j].at) {
				return events[i].delta < events[j].delta
			}
			return events[i].at.Before(events[j].at)
		})
		current := 0
		for _, event := range events {
			current += event.delta
			if current > data.readers[readerIndex].loanLimit {
				return fmt.Errorf("读者 %s 在 %s 同时借阅 %d 册，超过额度 %d", data.readers[readerIndex].readerNo, event.at.Format(time.DateTime), current, data.readers[readerIndex].loanLimit)
			}
			if current < 0 {
				return fmt.Errorf("读者 %s 的借阅事件顺序无效", data.readers[readerIndex].readerNo)
			}
		}
	}
	return nil
}

func reservationLoanKey(readerIndex, bookIndex int, borrowedAt time.Time) string {
	return fmt.Sprintf("%d:%d:%d", readerIndex, bookIndex, borrowedAt.Unix())
}

func insertReaders(ctx context.Context, tx *sql.Tx, data *dataset) error {
	for start := 0; start < len(data.readers); start += batchSize {
		end := min(start+batchSize, len(data.readers))
		args := make([]any, 0, (end-start)*10)
		for i := start; i < end; i++ {
			row := data.readers[i]
			args = append(args, row.readerNo, row.fullName, row.phone, row.email, row.status, row.fineBlocked, row.loanLimit, row.createdAt, row.updatedAt)
		}
		firstID, err := execInsertBatch(ctx, tx, `INSERT INTO readers(reader_no,full_name,phone,email,status,fine_blocked,loan_limit,created_at,updated_at) VALUES `, 9, end-start, args)
		if err != nil {
			return fmt.Errorf("写入模拟读者失败：%w", err)
		}
		for i := start; i < end; i++ {
			data.readers[i].id = firstID + uint64(i-start)
		}
	}
	return nil
}

func insertCopies(ctx context.Context, tx *sql.Tx, data *dataset) error {
	for start := 0; start < len(data.copies); start += batchSize {
		end := min(start+batchSize, len(data.copies))
		args := make([]any, 0, (end-start)*10)
		for i := start; i < end; i++ {
			row := data.copies[i]
			args = append(args, data.books[row.bookIndex].id, row.barcode, row.location, row.status, row.conditionStatus, row.conditionNote, row.acquiredAt, row.createdAt, row.updatedAt)
		}
		firstID, err := execInsertBatch(ctx, tx, `INSERT INTO book_copies(book_id,barcode,location,status,condition_status,condition_note,acquired_at,created_at,updated_at) VALUES `, 9, end-start, args)
		if err != nil {
			return fmt.Errorf("写入模拟实体副本失败：%w", err)
		}
		for i := start; i < end; i++ {
			data.copies[i].id = firstID + uint64(i-start)
		}
	}
	return nil
}

func insertLoans(ctx context.Context, tx *sql.Tx, data *dataset) error {
	for start := 0; start < len(data.loans); start += batchSize {
		end := min(start+batchSize, len(data.loans))
		args := make([]any, 0, (end-start)*10)
		for i := start; i < end; i++ {
			row := data.loans[i]
			args = append(args, data.readers[row.readerIndex].id, data.copies[row.copyIndex].id, row.borrowedAt, row.dueAt, nullableTime(row.returnedAt), row.renewCount, row.status, row.createdAt, row.updatedAt)
		}
		firstID, err := execInsertBatch(ctx, tx, `INSERT INTO loans(reader_id,book_copy_id,borrowed_at,due_at,returned_at,renew_count,status,created_at,updated_at) VALUES `, 9, end-start, args)
		if err != nil {
			return fmt.Errorf("写入模拟借阅失败：%w", err)
		}
		for i := start; i < end; i++ {
			data.loans[i].id = firstID + uint64(i-start)
		}
	}
	return nil
}

func insertReservations(ctx context.Context, tx *sql.Tx, data *dataset) error {
	for start := 0; start < len(data.reservations); start += batchSize {
		end := min(start+batchSize, len(data.reservations))
		args := make([]any, 0, (end-start)*10)
		for i := start; i < end; i++ {
			row := data.reservations[i]
			var reservedCopyID any
			if row.reservedCopyIndex >= 0 {
				reservedCopyID = data.copies[row.reservedCopyIndex].id
			}
			args = append(args, data.readers[row.readerIndex].id, data.books[row.bookIndex].id, reservedCopyID, row.queuePosition, row.status, nullableTime(row.expiresAt), row.createdAt, row.updatedAt)
		}
		firstID, err := execInsertBatch(ctx, tx, `INSERT INTO reservations(reader_id,book_id,reserved_copy_id,queue_position,status,expires_at,created_at,updated_at) VALUES `, 8, end-start, args)
		if err != nil {
			return fmt.Errorf("写入模拟预约失败：%w", err)
		}
		for i := start; i < end; i++ {
			data.reservations[i].id = firstID + uint64(i-start)
		}
	}
	return nil
}

func insertFines(ctx context.Context, tx *sql.Tx, data *dataset) error {
	for start := 0; start < len(data.fines); start += batchSize {
		end := min(start+batchSize, len(data.fines))
		args := make([]any, 0, (end-start)*10)
		for i := start; i < end; i++ {
			row := data.fines[i]
			args = append(args, data.loans[row.loanIndex].id, data.readers[row.readerIndex].id, row.amount, row.reason, row.origin, row.status, nullableTime(row.paidAt), row.createdAt, row.updatedAt)
		}
		firstID, err := execInsertBatch(ctx, tx, `INSERT INTO fines(loan_id,reader_id,amount,reason,origin,status,paid_at,created_at,updated_at) VALUES `, 9, end-start, args)
		if err != nil {
			return fmt.Errorf("写入模拟罚金失败：%w", err)
		}
		for i := start; i < end; i++ {
			data.fines[i].id = firstID + uint64(i-start)
		}
	}
	return nil
}

func execInsertBatch(ctx context.Context, tx *sql.Tx, prefix string, columns, rows int, args []any) (uint64, error) {
	groups := make([]string, rows)
	placeholders := "(" + strings.TrimRight(strings.Repeat("?,", columns), ",") + ")"
	for i := range groups {
		groups[i] = placeholders
	}
	result, err := tx.ExecContext(ctx, prefix+strings.Join(groups, ","), args...)
	if err != nil {
		return 0, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if affected != int64(rows) {
		return 0, fmt.Errorf("计划写入 %d 行，实际写入 %d 行", rows, affected)
	}
	firstID, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}
	return uint64(firstID), nil
}

func insertAudits(ctx context.Context, tx *sql.Tx, data *dataset) (int, error) {
	type auditRow struct {
		actorID    any
		action     string
		entityType string
		entityID   uint64
		details    []byte
		createdAt  time.Time
	}
	rows := make([]auditRow, 0, estimateAuditCount(data))
	details := func(values map[string]any) []byte {
		values["dataset"] = datasetMarker
		values["simulated"] = true
		values["generatedBy"] = "seed-business-scale"
		raw, _ := json.Marshal(values)
		return raw
	}

	for i := 0; i < len(data.copies); i += 20 {
		copy := data.copies[i]
		rows = append(rows, auditRow{nil, "book_copy.create", "book_copy", copy.id, details(map[string]any{"barcode": copy.barcode, "location": copy.location}), copy.createdAt})
	}
	for i, loan := range data.loans {
		if loan.status == "returned" {
			if i%6 != 0 {
				continue
			}
			rows = append(rows,
				auditRow{nil, "loan.borrow", "loan", loan.id, details(map[string]any{"barcode": data.copies[loan.copyIndex].barcode, "readerNo": data.readers[loan.readerIndex].readerNo}), loan.borrowedAt},
				auditRow{nil, "loan.return", "loan", loan.id, details(map[string]any{"status": "returned"}), *loan.returnedAt},
			)
			continue
		}
		action := "loan.borrow"
		createdAt := loan.borrowedAt
		if loan.status == "lost" {
			action = "loan.lost"
			createdAt = loan.updatedAt
		}
		rows = append(rows, auditRow{nil, action, "loan", loan.id, details(map[string]any{"barcode": data.copies[loan.copyIndex].barcode, "readerNo": data.readers[loan.readerIndex].readerNo, "status": loan.status}), createdAt})
	}
	for _, reservation := range data.reservations {
		action := "reservation.create"
		if reservation.status != "waiting" && reservation.status != "ready" {
			action = "reservation." + reservation.status
		}
		rows = append(rows, auditRow{nil, action, "reservation", reservation.id, details(map[string]any{"readerNo": data.readers[reservation.readerIndex].readerNo, "status": reservation.status}), reservation.updatedAt})
	}
	for _, fine := range data.fines {
		action := "fine.create"
		createdAt := fine.createdAt
		if fine.status == "paid" {
			action = "fine.paid"
			createdAt = *fine.paidAt
		} else if fine.status == "waived" {
			action = "fine.waived"
			createdAt = fine.updatedAt
		}
		rows = append(rows, auditRow{nil, action, "fine", fine.id, details(map[string]any{"amount": fine.amount, "origin": fine.origin, "status": fine.status}), createdAt})
	}

	for start := 0; start < len(rows); start += batchSize {
		end := min(start+batchSize, len(rows))
		args := make([]any, 0, (end-start)*6)
		for i := start; i < end; i++ {
			row := rows[i]
			args = append(args, row.actorID, row.action, row.entityType, row.entityID, row.details, row.createdAt)
		}
		if _, err := execInsertBatch(ctx, tx, `INSERT INTO audit_logs(actor_user_id,action,entity_type,entity_id,details,created_at) VALUES `, 6, end-start, args); err != nil {
			return 0, fmt.Errorf("写入模拟审计日志失败：%w", err)
		}
	}
	return len(rows), nil
}

func insertCompletionMarker(ctx context.Context, tx *sql.Tx, data *dataset, auditCount int) error {
	completedAt := time.Now()
	details, err := json.Marshal(map[string]any{
		"dataset":       datasetMarker,
		"simulated":     true,
		"description":   "基于已审核 Open Library 真实版本书目的规模化模拟流通业务数据",
		"readers":       len(data.readers),
		"copies":        len(data.copies),
		"loans":         len(data.loans),
		"returnedLoans": countLoans(data.loans, "returned"),
		"reservations":  len(data.reservations),
		"fines":         len(data.fines),
		"auditLogs":     auditCount + 1,
		"randomSeed":    data.randomSeed,
		"anchorDate":    data.anchor.Format(time.DateOnly),
		"completedAt":   completedAt.Format(time.RFC3339),
	})
	if err != nil {
		return fmt.Errorf("生成完成标记失败：%w", err)
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO audit_logs(actor_user_id,action,entity_type,entity_id,details,created_at) VALUES(?,?,?,?,?,?)`, nil, markerAction, "system", nil, details, completedAt)
	if err != nil {
		return fmt.Errorf("写入完成标记失败：%w", err)
	}
	return nil
}

func estimateAuditCount(data *dataset) int {
	returnedSample := (countLoans(data.loans, "returned") + 5) / 6 * 2
	current := len(data.loans) - countLoans(data.loans, "returned")
	copySample := (len(data.copies) + 19) / 20
	return returnedSample + current + copySample + len(data.reservations) + len(data.fines)
}

func countLoans(loans []loanRow, status string) int {
	count := 0
	for _, loan := range loans {
		if loan.status == status {
			count++
		}
	}
	return count
}

func nullableTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return *value
}

func dayAnchor(now time.Time) time.Time {
	year, month, day := now.Date()
	return time.Date(year, month, day, 0, 0, 0, 0, now.Location())
}

func printDatasetSummary(title string, data *dataset, auditCount int) {
	fmt.Println(title)
	fmt.Printf("数据集标记：%s（所有业务记录均为模拟数据）\n", datasetMarker)
	fmt.Printf("复用已审核 Open Library 书目：%d 条；新增书目：0 条\n", len(data.books))
	fmt.Printf("模拟读者：%d 名；实体副本：%d 册\n", len(data.readers), len(data.copies))
	fmt.Printf("借阅流水：%d 条（已归还 %d、在借 %d、逾期 %d、遗失 %d）\n", len(data.loans), countLoans(data.loans, "returned"), countLoans(data.loans, "active"), countLoans(data.loans, "overdue"), countLoans(data.loans, "lost"))
	fmt.Printf("预约：%d 条；罚金：%d 笔；审计日志：%d 条\n", len(data.reservations), len(data.fines), auditCount)
}

func printExistingSummary(title string, summary existingSummary) {
	fmt.Println(title)
	fmt.Printf("完成标记：%d；模拟读者：%d；模拟副本：%d；借阅：%d；预约：%d；罚金：%d；审计日志：%d\n", summary.markers, summary.readers, summary.copies, summary.loans, summary.reservations, summary.fines, summary.audits)
}
