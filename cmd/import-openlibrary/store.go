package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const sourceCode = "openlibrary"

type catalogStore struct {
	db *sql.DB
}

type importOutcome struct {
	Processed int64
	Imported  int64
	Rejected  int64
	Conflicts map[string]int64
}

type catalogConflict struct {
	Reason string
}

func (e *catalogConflict) Error() string { return e.Reason }

type sourceRecordState struct {
	ID             uint64
	BookID         sql.NullInt64
	Revision       sql.NullString
	SourceModified sql.NullTime
	PayloadSHA256  string
}

func (s *catalogStore) sourceID(ctx context.Context) (uint16, error) {
	var id uint16
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM catalog_sources WHERE code=? AND is_active=TRUE`, sourceCode).Scan(&id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, fmt.Errorf("未找到启用的 Open Library 来源，请先执行 000008 迁移")
		}
		return 0, fmt.Errorf("查询书目来源: %w", err)
	}
	return id, nil
}

func (s *catalogStore) ensureImportCategory(ctx context.Context, code, name string) (uint64, error) {
	var id uint64
	err := s.db.QueryRowContext(ctx, `SELECT id FROM categories WHERE code=?`, code).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("查询导入分类: %w", err)
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO categories(parent_id,name,code) VALUES(NULL,?,?)`, name, code)
	if err != nil {
		// 名称或编码被并发创建时重新按编码读取；其他冲突仍返回原错误。
		if queryErr := s.db.QueryRowContext(ctx, `SELECT id FROM categories WHERE code=?`, code).Scan(&id); queryErr == nil {
			return id, nil
		}
		return 0, fmt.Errorf("创建导入分类: %w", err)
	}
	inserted, err := result.LastInsertId()
	return uint64(inserted), err
}

func (s *catalogStore) beginBatch(ctx context.Context, sourceID uint16, source sourceMetadata, selectionMethod string) (uint64, error) {
	var contentLength any
	if source.ContentLength > 0 {
		contentLength = source.ContentLength
	}
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO catalog_import_batches(
			source_id,source_url,etag,last_modified,content_length,selection_method,status,started_at
		) VALUES(?,?,?,?,?,?,'running',CURRENT_TIMESTAMP(6))`,
		sourceID, source.Source, nullableString(source.ETag), nullableString(source.LastModified), contentLength, selectionMethod)
	if err != nil {
		return 0, fmt.Errorf("创建导入批次: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("读取导入批次编号: %w", err)
	}
	return uint64(id), nil
}

func (s *catalogStore) importCatalog(ctx context.Context, batchID uint64, sourceID uint16, categoryID uint64, candidates []editionCandidate, authors map[string]authorRecord, batchSize int) (outcome importOutcome, resultErr error) {
	outcome.Conflicts = make(map[string]int64)
	defer func() {
		if resultErr == nil {
			return
		}
		status := "failed"
		if errors.Is(resultErr, context.Canceled) || errors.Is(resultErr, context.DeadlineExceeded) {
			status = "cancelled"
		}
		_ = s.finishBatch(context.Background(), batchID, status, outcome)
	}()

	for start := 0; start < len(candidates); start += batchSize {
		end := start + batchSize
		if end > len(candidates) {
			end = len(candidates)
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return outcome, fmt.Errorf("开始第 %d 批事务: %w", start/batchSize+1, err)
		}
		chunkImported := int64(0)
		chunkRejected := int64(0)
		chunkConflicts := make(map[string]int64)
		for _, candidate := range candidates[start:end] {
			if _, err := tx.ExecContext(ctx, `SAVEPOINT import_openlibrary_record`); err != nil {
				_ = tx.Rollback()
				return outcome, fmt.Errorf("建立单条导入保存点: %w", err)
			}
			if err := importEdition(ctx, tx, batchID, sourceID, categoryID, candidate, authors); err != nil {
				var conflict *catalogConflict
				if errors.As(err, &conflict) {
					if _, rollbackErr := tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT import_openlibrary_record`); rollbackErr != nil {
						_ = tx.Rollback()
						return outcome, fmt.Errorf("回滚冲突记录 %s: %w", candidate.EditionKey, rollbackErr)
					}
					_, _ = tx.ExecContext(ctx, `RELEASE SAVEPOINT import_openlibrary_record`)
					chunkRejected++
					chunkConflicts[conflict.Reason]++
					continue
				}
				_ = tx.Rollback()
				return outcome, fmt.Errorf("导入版本 %s: %w", candidate.EditionKey, err)
			}
			if _, err := tx.ExecContext(ctx, `RELEASE SAVEPOINT import_openlibrary_record`); err != nil {
				_ = tx.Rollback()
				return outcome, fmt.Errorf("释放单条导入保存点: %w", err)
			}
			chunkImported++
		}
		if err := tx.Commit(); err != nil {
			return outcome, fmt.Errorf("提交第 %d 批事务: %w", start/batchSize+1, err)
		}
		outcome.Processed += int64(end - start)
		outcome.Imported += chunkImported
		outcome.Rejected += chunkRejected
		for reason, count := range chunkConflicts {
			outcome.Conflicts[reason] += count
		}
		if err := s.updateBatchProgress(ctx, batchID, outcome); err != nil {
			return outcome, err
		}
	}

	status := "completed"
	if outcome.Rejected > 0 {
		status = "completed_with_errors"
	}
	if err := s.finishBatch(ctx, batchID, status, outcome); err != nil {
		return outcome, err
	}
	return outcome, nil
}

func importEdition(ctx context.Context, tx *sql.Tx, batchID uint64, sourceID uint16, categoryID uint64, candidate editionCandidate, authors map[string]authorRecord) error {
	existingBookID, err := findCompatibleBook(ctx, tx, candidate)
	if err != nil {
		return err
	}
	record, replaceRecord, err := upsertSourceRecord(ctx, tx, sourceID, batchID, "edition", candidate.EditionKey, candidate.SourceURL, candidate.SourceRevision, candidate.SourceModified, candidate.RawPayload, candidate.PayloadSHA256)
	if err != nil {
		return err
	}
	if record.BookID.Valid {
		if existingBookID == 0 {
			existingBookID = uint64(record.BookID.Int64)
		} else if uint64(record.BookID.Int64) != existingBookID {
			return &catalogConflict{Reason: "来源记录已绑定其他书目"}
		}
	}

	workID, err := ensureWork(ctx, tx, candidate)
	if err != nil {
		return err
	}
	newBook := existingBookID == 0
	if existingBookID == 0 {
		existingBookID, err = insertBook(ctx, tx, categoryID, workID, record.ID, candidate)
		if err != nil {
			return err
		}
	} else if replaceRecord {
		if err := updateImportedBook(ctx, tx, existingBookID, workID, record.ID, candidate); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE catalog_source_records SET book_id=? WHERE id=? AND (book_id IS NULL OR book_id=?)`, existingBookID, record.ID, existingBookID); err != nil {
		return fmt.Errorf("关联版本来源记录: %w", err)
	}
	if (newBook || replaceRecord) && !newBook {
		if err := clearEditionRelations(ctx, tx, existingBookID); err != nil {
			return err
		}
	}

	if err := ensureIdentifiers(ctx, tx, existingBookID, candidate.Identifiers); err != nil {
		return err
	}
	if err := ensureAuthors(ctx, tx, sourceID, batchID, existingBookID, candidate.AuthorKeys, authors); err != nil {
		return err
	}
	if err := ensurePublishers(ctx, tx, existingBookID, candidate.Publishers); err != nil {
		return err
	}
	if err := ensureTranslators(ctx, tx, existingBookID, candidate.EditionKey, candidate.Translators); err != nil {
		return err
	}
	return nil
}

func findCompatibleBook(ctx context.Context, tx *sql.Tx, candidate editionCandidate) (uint64, error) {
	legacyValues := []string{candidate.PrimaryISBN13}
	for _, value := range candidate.Identifiers {
		if value.Scheme == "isbn_10" {
			legacyValues = append(legacyValues, value.NormalizedValue)
		}
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(legacyValues)), ",")
	query := `
		SELECT id,COALESCE(source_provider,''),COALESCE(source_edition_key,''),COALESCE(normalized_isbn,''),isbn
		FROM books
		WHERE (source_provider=? AND source_edition_key=?)
		   OR normalized_isbn=?
		   OR REPLACE(REPLACE(UPPER(isbn),'-',''),' ','') IN (` + placeholders + `)
		FOR UPDATE`
	arguments := []any{sourceCode, candidate.EditionKey, candidate.PrimaryISBN13}
	for _, value := range legacyValues {
		arguments = append(arguments, value)
	}
	rows, err := tx.QueryContext(ctx, query, arguments...)
	if err != nil {
		return 0, fmt.Errorf("检查版本冲突: %w", err)
	}
	defer rows.Close()
	var compatible uint64
	for rows.Next() {
		var id uint64
		var provider, editionKey, normalized, displayISBN string
		if err := rows.Scan(&id, &provider, &editionKey, &normalized, &displayISBN); err != nil {
			return 0, fmt.Errorf("读取版本冲突: %w", err)
		}
		if provider != sourceCode || editionKey != candidate.EditionKey {
			return 0, &catalogConflict{Reason: "ISBN 已属于其他或旧书目"}
		}
		if normalized != "" && normalized != candidate.PrimaryISBN13 {
			return 0, &catalogConflict{Reason: "来源版本的 ISBN 已变化"}
		}
		if compatible != 0 && compatible != id {
			return 0, &catalogConflict{Reason: "来源版本命中多个书目"}
		}
		compatible = id
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("遍历版本冲突: %w", err)
	}
	return compatible, nil
}

func upsertSourceRecord(ctx context.Context, tx *sql.Tx, sourceID uint16, batchID uint64, kind, externalID, sourceURL, revision string, modified *time.Time, raw []byte, payloadHash string) (sourceRecordState, bool, error) {
	var state sourceRecordState
	err := tx.QueryRowContext(ctx, `
		SELECT id,book_id,source_revision,source_modified_at,payload_sha256
		FROM catalog_source_records
		WHERE source_id=? AND record_kind=? AND external_id=?
		FOR UPDATE`, sourceID, kind, externalID).Scan(&state.ID, &state.BookID, &state.Revision, &state.SourceModified, &state.PayloadSHA256)
	if errors.Is(err, sql.ErrNoRows) {
		result, insertErr := tx.ExecContext(ctx, `
			INSERT INTO catalog_source_records(
				source_id,batch_id,record_kind,external_id,source_url,source_revision,source_modified_at,raw_payload,payload_sha256
			) VALUES(?,?,?,?,?,?,?,?,?)`,
			sourceID, batchID, kind, externalID, sourceURL, nullableString(revision), modified, raw, payloadHash)
		if insertErr != nil {
			return state, false, fmt.Errorf("写入 %s 来源记录: %w", kind, insertErr)
		}
		id, insertErr := result.LastInsertId()
		state.ID = uint64(id)
		state.PayloadSHA256 = payloadHash
		return state, true, insertErr
	}
	if err != nil {
		return state, false, fmt.Errorf("查询 %s 来源记录: %w", kind, err)
	}
	if state.PayloadSHA256 == payloadHash {
		_, err = tx.ExecContext(ctx, `UPDATE catalog_source_records SET batch_id=?,fetched_at=CURRENT_TIMESTAMP(6) WHERE id=?`, batchID, state.ID)
		return state, false, err
	}
	if !sourceRecordIsNewer(state, revision, modified) {
		return state, false, &catalogConflict{Reason: kind + " 来源修订冲突"}
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE catalog_source_records
		SET batch_id=?,source_url=?,source_revision=?,source_modified_at=?,raw_payload=?,payload_sha256=?,fetched_at=CURRENT_TIMESTAMP(6)
		WHERE id=?`, batchID, sourceURL, nullableString(revision), modified, raw, payloadHash, state.ID)
	if err != nil {
		return state, false, fmt.Errorf("更新 %s 来源记录: %w", kind, err)
	}
	state.PayloadSHA256 = payloadHash
	return state, true, nil
}

func sourceRecordIsNewer(existing sourceRecordState, newRevision string, newModified *time.Time) bool {
	oldNumber, oldErr := strconv.ParseUint(existing.Revision.String, 10, 64)
	newNumber, newErr := strconv.ParseUint(newRevision, 10, 64)
	if oldErr == nil && newErr == nil {
		return newNumber > oldNumber
	}
	return newModified != nil && (!existing.SourceModified.Valid || newModified.After(existing.SourceModified.Time))
}

func ensureWork(ctx context.Context, tx *sql.Tx, candidate editionCandidate) (uint64, error) {
	// Editions 转储不含权威作品标题。首次按固定抽样顺序取一个来源版本标题占位，后续版本修订不能覆盖共享作品标题。
	// 将来接入 Works 转储时，应由作品来源记录独立更新 preferred_title。
	result, err := tx.ExecContext(ctx, `
		INSERT INTO works(preferred_title,source_provider,source_work_key)
		VALUES(?,?,?)
		ON DUPLICATE KEY UPDATE id=LAST_INSERT_ID(id)`, candidate.Title, sourceCode, candidate.WorkKey)
	if err != nil {
		return 0, fmt.Errorf("写入作品: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil || id == 0 {
		return 0, fmt.Errorf("读取作品编号: %w", err)
	}
	return uint64(id), nil
}

func clearEditionRelations(ctx context.Context, tx *sql.Tx, bookID uint64) error {
	statements := []struct {
		name  string
		query string
	}{
		{"ISBN 标识符", `DELETE FROM book_identifiers WHERE book_id=? AND scheme IN ('isbn_10','isbn_13')`},
		{"作者关系", `DELETE FROM book_authors WHERE book_id=?`},
		{"出版社关系", `DELETE FROM book_publishers WHERE book_id=?`},
		{"译者关系", `DELETE FROM book_translators WHERE book_id=?`},
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement.query, bookID); err != nil {
			return fmt.Errorf("重建版本%s: %w", statement.name, err)
		}
	}
	return nil
}

func insertBook(ctx context.Context, tx *sql.Tx, categoryID, workID, sourceRecordID uint64, candidate editionCandidate) (uint64, error) {
	result, err := tx.ExecContext(ctx, `
		INSERT INTO books(
			category_id,isbn,material_type,title,publisher,publish_year,summary,
			work_id,normalized_isbn,source_provider,source_edition_key,source_work_key,
			source_revision,source_modified_at,source_url,publish_date_raw,edition_statement,
			language_code,primary_source_record_id,verification_status,collection_status
		) VALUES(?,?,'book',?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,'source_matched','active')`,
		categoryID, candidate.PrimaryISBN13, candidate.Title, candidate.Publishers[0], candidate.PublishYear, nullableString(candidate.Summary),
		workID, candidate.PrimaryISBN13, sourceCode, candidate.EditionKey, candidate.WorkKey,
		nullableString(candidate.SourceRevision), candidate.SourceModified, candidate.SourceURL, candidate.PublishDateRaw, nullableString(candidate.EditionStatement),
		candidate.LanguageCode, sourceRecordID)
	if err != nil {
		return 0, fmt.Errorf("写入书目版本: %w", err)
	}
	id, err := result.LastInsertId()
	return uint64(id), err
}

func updateImportedBook(ctx context.Context, tx *sql.Tx, bookID, workID, sourceRecordID uint64, candidate editionCandidate) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE books SET
			isbn=?,title=?,publisher=?,publish_year=?,summary=?,work_id=?,normalized_isbn=?,
			source_work_key=?,source_revision=?,source_modified_at=?,source_url=?,publish_date_raw=?,
			edition_statement=?,language_code=?,primary_source_record_id=?,verification_status='source_matched',collection_status='active'
		WHERE id=? AND source_provider=? AND source_edition_key=?`,
		candidate.PrimaryISBN13, candidate.Title, candidate.Publishers[0], candidate.PublishYear, nullableString(candidate.Summary), workID, candidate.PrimaryISBN13,
		candidate.WorkKey, nullableString(candidate.SourceRevision), candidate.SourceModified, candidate.SourceURL, candidate.PublishDateRaw,
		nullableString(candidate.EditionStatement), candidate.LanguageCode, sourceRecordID, bookID, sourceCode, candidate.EditionKey)
	if err != nil {
		return fmt.Errorf("更新来源书目版本: %w", err)
	}
	return nil
}

func ensureIdentifiers(ctx context.Context, tx *sql.Tx, bookID uint64, values []identifier) error {
	for _, value := range values {
		var existing uint64
		err := tx.QueryRowContext(ctx, `SELECT book_id FROM book_identifiers WHERE scheme=? AND normalized_value=? FOR UPDATE`, value.Scheme, value.NormalizedValue).Scan(&existing)
		if err == nil && existing != bookID {
			return &catalogConflict{Reason: "附加标识符已属于其他书目"}
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("检查书目标识符: %w", err)
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO book_identifiers(book_id,scheme,display_value,normalized_value,is_primary)
			VALUES(?,?,?,?,?)
			ON DUPLICATE KEY UPDATE display_value=VALUES(display_value),is_primary=VALUES(is_primary)`,
			bookID, value.Scheme, value.DisplayValue, value.NormalizedValue, value.IsPrimary)
		if err != nil {
			return fmt.Errorf("写入书目标识符: %w", err)
		}
	}
	return nil
}

func ensureAuthors(ctx context.Context, tx *sql.Tx, sourceID uint16, batchID, bookID uint64, keys []string, records map[string]authorRecord) error {
	for index, key := range keys {
		author, exists := records[key]
		if !exists || strings.TrimSpace(author.Name) == "" {
			return &catalogConflict{Reason: "作者来源记录缺失"}
		}
		if _, _, err := upsertSourceRecord(ctx, tx, sourceID, batchID, "author", author.Key, author.SourceURL, author.SourceRevision, author.SourceModified, author.RawPayload, author.PayloadSHA256); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `
			INSERT INTO authors(full_name,source_provider,source_author_key)
			VALUES(?,?,?)
			ON DUPLICATE KEY UPDATE id=LAST_INSERT_ID(id),full_name=VALUES(full_name)`, author.Name, sourceCode, author.Key)
		if err != nil {
			return fmt.Errorf("写入作者: %w", err)
		}
		authorID, err := result.LastInsertId()
		if err != nil || authorID == 0 {
			return fmt.Errorf("读取作者编号: %w", err)
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO book_authors(book_id,author_id,author_order)
			VALUES(?,?,?)
			ON DUPLICATE KEY UPDATE author_order=VALUES(author_order)`, bookID, authorID, index+1)
		if err != nil {
			return fmt.Errorf("关联书目作者: %w", err)
		}
	}
	return nil
}

func ensurePublishers(ctx context.Context, tx *sql.Tx, bookID uint64, names []string) error {
	for index, name := range names {
		normalized := normalizeLiteral(name)
		// Open Library 未提供出版社权威 ID；该 key 明确表示“来源字面值”，不冒充机构身份标识。
		sourcePublisherKey := "literal-sha256:" + literalKey(normalized)
		result, err := tx.ExecContext(ctx, `
			INSERT INTO publishers(name,normalized_name,source_provider,source_publisher_key)
			VALUES(?,?,?,?)
			ON DUPLICATE KEY UPDATE id=LAST_INSERT_ID(id),name=VALUES(name),normalized_name=VALUES(normalized_name)`,
			name, normalized, sourceCode, sourcePublisherKey)
		if err != nil {
			return fmt.Errorf("写入出版社: %w", err)
		}
		inserted, err := result.LastInsertId()
		if err != nil || inserted == 0 {
			return fmt.Errorf("读取出版社编号: %w", err)
		}
		publisherID := uint64(inserted)
		role := "publisher"
		if index > 0 {
			role = "co_publisher"
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO book_publishers(book_id,publisher_id,publisher_order,role,credited_name)
			VALUES(?,?,?,?,?)
			ON DUPLICATE KEY UPDATE credited_name=VALUES(credited_name)`, bookID, publisherID, index+1, role, name)
		if err != nil {
			return fmt.Errorf("关联书目出版社: %w", err)
		}
	}
	return nil
}

func ensureTranslators(ctx context.Context, tx *sql.Tx, bookID uint64, editionKey string, credits []translatorCredit) error {
	for _, credit := range credits {
		// Open Library 只有贡献者自由文本，没有稳定译者 ID；以“版本+署名位置”标识来源事实，避免跨版本猜测同名人物。
		externalKey := fmt.Sprintf("%s#contribution:%d", editionKey, credit.Order)
		result, err := tx.ExecContext(ctx, `
			INSERT INTO translators(full_name,source_provider,source_translator_key)
			VALUES(?,?,?)
			ON DUPLICATE KEY UPDATE id=LAST_INSERT_ID(id),full_name=VALUES(full_name)`, credit.Name, sourceCode, externalKey)
		if err != nil {
			return fmt.Errorf("写入译者署名: %w", err)
		}
		translatorID, err := result.LastInsertId()
		if err != nil || translatorID == 0 {
			return fmt.Errorf("读取译者编号: %w", err)
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO book_translators(book_id,translator_id,translator_order,credited_name)
			VALUES(?,?,?,?)
			ON DUPLICATE KEY UPDATE credited_name=VALUES(credited_name)`, bookID, translatorID, credit.Order, credit.CreditedName)
		if err != nil {
			return fmt.Errorf("关联书目译者: %w", err)
		}
	}
	return nil
}

func (s *catalogStore) updateBatchProgress(ctx context.Context, batchID uint64, outcome importOutcome) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE catalog_import_batches
		SET processed_count=?,imported_count=?,rejected_count=?
		WHERE id=?`, outcome.Processed, outcome.Imported, outcome.Rejected, batchID)
	if err != nil {
		return fmt.Errorf("更新导入批次进度: %w", err)
	}
	return nil
}

func (s *catalogStore) finishBatch(ctx context.Context, batchID uint64, status string, outcome importOutcome) error {
	finishContext, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_, err := s.db.ExecContext(finishContext, `
		UPDATE catalog_import_batches
		SET status=?,processed_count=?,imported_count=?,rejected_count=?,completed_at=CURRENT_TIMESTAMP(6)
		WHERE id=?`, status, outcome.Processed, outcome.Imported, outcome.Rejected, batchID)
	if err != nil {
		return fmt.Errorf("完成导入批次: %w", err)
	}
	return nil
}

func nullableString(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func literalKey(value string) string {
	sum := sha256.Sum256([]byte(normalizeLiteral(value)))
	return hex.EncodeToString(sum[:])
}
