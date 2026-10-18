package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"library-management/internal/config"
	"library-management/internal/database"
)

type commandConfig struct {
	EditionsSource  string
	AuthorsSource   string
	CacheDirectory  string
	Contact         string
	CategoryCode    string
	CategoryName    string
	CheckpointLines int64
	Retries         int
	BatchSize       int
	Apply           bool
	Sampler         samplerConfig
}

type selectionCache struct {
	Version     int                     `json:"version"`
	Fingerprint string                  `json:"fingerprint"`
	Editions    []editionCandidate      `json:"editions"`
	Authors     map[string]authorRecord `json:"authors"`
	CreatedAt   time.Time               `json:"createdAt"`
}

func main() {
	log.SetFlags(log.Ldate | log.Ltime)
	configValue, err := parseCommandConfig()
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, configValue); err != nil {
		log.Fatal(err)
	}
}

func parseCommandConfig() (commandConfig, error) {
	value := commandConfig{}
	flag.StringVar(&value.EditionsSource, "editions", defaultEditionsSource, "Open Library Editions gzip 转储 URL 或本地路径")
	flag.StringVar(&value.AuthorsSource, "authors", defaultAuthorsSource, "Open Library Authors gzip 转储 URL 或本地路径")
	flag.StringVar(&value.CacheDirectory, "cache-dir", defaultCacheDirectory(), "检查点 gzip 缓存目录")
	flag.StringVar(&value.Contact, "contact", "", "下载标识中的维护者邮箱或电话")
	flag.StringVar(&value.CategoryCode, "category-code", "OL-UNCLASSIFIED", "来源书目暂存分类编码")
	flag.StringVar(&value.CategoryName, "category-name", "外部书目待分类", "来源书目暂存分类名称")
	flag.Int64Var(&value.CheckpointLines, "checkpoint-lines", 1_000_000, "每读取多少行保存一次检查点")
	flag.IntVar(&value.Retries, "retries", 4, "远程流中断后的最大重试次数")
	flag.IntVar(&value.BatchSize, "batch-size", 250, "正式写入时每个数据库事务的版本数")
	flag.BoolVar(&value.Apply, "apply", false, "连接数据库并写入；默认只扫描、校验和缓存")
	flag.IntVar(&value.Sampler.Target, "target", 10_500, "最终选择的版本记录数")
	flag.IntVar(&value.Sampler.PoolFactor, "pool-factor", 8, "哈希候选池相对目标数量的倍数")
	flag.StringVar(&value.Sampler.Seed, "seed", "library-management-openlibrary-v1", "固定哈希抽样种子")
	flag.IntVar(&value.Sampler.MaxPerWork, "max-per-work", 3, "同一作品最多保留的版本数，0 表示不限")
	flag.IntVar(&value.Sampler.MaxPerPublisher, "max-per-publisher", 250, "同一出版社最多保留的版本数，0 表示不限")
	flag.IntVar(&value.Sampler.MaxPerLanguage, "max-per-language", 3_500, "同一语言最多保留的版本数，0 表示不限")
	flag.Parse()

	value.EditionsSource = strings.TrimSpace(value.EditionsSource)
	value.AuthorsSource = strings.TrimSpace(value.AuthorsSource)
	value.CacheDirectory = strings.TrimSpace(value.CacheDirectory)
	value.Contact = strings.NewReplacer("\r", "", "\n", "").Replace(strings.TrimSpace(value.Contact))
	value.CategoryCode = strings.TrimSpace(value.CategoryCode)
	value.CategoryName = strings.TrimSpace(value.CategoryName)
	if value.EditionsSource == "" || value.AuthorsSource == "" || value.CacheDirectory == "" {
		return value, fmt.Errorf("版本来源、作者来源和缓存目录不能为空")
	}
	if value.Sampler.Target < 1 || value.Sampler.PoolFactor < 1 {
		return value, fmt.Errorf("target 和 pool-factor 必须大于 0")
	}
	for name, limit := range map[string]int{
		"max-per-work":      value.Sampler.MaxPerWork,
		"max-per-publisher": value.Sampler.MaxPerPublisher,
		"max-per-language":  value.Sampler.MaxPerLanguage,
	} {
		if limit < 0 {
			return value, fmt.Errorf("%s 不能小于 0", name)
		}
	}
	if value.CheckpointLines < 1 || value.Retries < 0 || value.BatchSize < 1 || value.BatchSize > 1000 {
		return value, fmt.Errorf("checkpoint-lines、retries 或 batch-size 参数无效")
	}
	if value.Apply && (value.CategoryCode == "" || value.CategoryName == "") {
		return value, fmt.Errorf("正式写入时分类编码和名称不能为空")
	}
	return value, nil
}

func run(ctx context.Context, command commandConfig) error {
	userAgent := "LibraryManagementCatalogImporter/1.0"
	if command.Contact != "" {
		userAgent += " (" + command.Contact + ")"
	}
	reader := newSourceReader(userAgent)

	log.Printf("核对 Editions 转储来源：%s", command.EditionsSource)
	editionsMetadata, err := inspectWithRetry(ctx, reader, command.EditionsSource, command.Retries)
	if err != nil {
		return err
	}
	editionsFingerprint, err := configFingerprint(editionsMetadata, command.Sampler)
	if err != nil {
		return err
	}
	editionsCachePath := sourceCachePath(command.CacheDirectory, "editions", editionsFingerprint)
	editionsState, err := loadOrScanEditions(ctx, reader, editionsMetadata, command.Sampler, editionsCachePath, command.CheckpointLines, command.Retries)
	if err != nil {
		return err
	}
	if len(editionsState.Candidates) < command.Sampler.Target {
		return fmt.Errorf("质量合格候选池只有 %d 条，少于目标 %d 条", len(editionsState.Candidates), command.Sampler.Target)
	}

	log.Printf("核对 Authors 转储来源：%s", command.AuthorsSource)
	authorsMetadata, err := inspectWithRetry(ctx, reader, command.AuthorsSource, command.Retries)
	if err != nil {
		return err
	}
	neededFingerprint, _ := neededAuthorsFingerprint(editionsState.Candidates)
	authorsFingerprint, err := configFingerprint(authorsMetadata, neededFingerprint)
	if err != nil {
		return err
	}
	authorsCachePath := sourceCachePath(command.CacheDirectory, "authors", authorsFingerprint)
	authorsState, err := loadOrScanAuthors(ctx, reader, authorsMetadata, editionsState.Candidates, authorsCachePath, command.CheckpointLines, command.Retries)
	if err != nil {
		return err
	}

	sampler := newFixedSampler(command.Sampler, editionsState.Candidates)
	selected, selectionRejects := sampler.selectCandidates(authorsState.Authors)
	if len(selected) < command.Sampler.Target {
		return fmt.Errorf("应用作者校验和配额后只有 %d/%d 条；原因：%s；请增大 pool-factor 或调整配额", len(selected), command.Sampler.Target, formatCounts(selectionRejects))
	}
	selectedAuthors := selectAuthors(selected, authorsState.Authors)
	selectionFingerprint, err := configFingerprint(editionsFingerprint, authorsFingerprint, command.Sampler)
	if err != nil {
		return err
	}
	selectionPath := sourceCachePath(command.CacheDirectory, "selection", selectionFingerprint)
	if err := saveGzipJSON(selectionPath, &selectionCache{
		Version:     cacheVersion,
		Fingerprint: selectionFingerprint,
		Editions:    selected,
		Authors:     selectedAuthors,
		CreatedAt:   time.Now().UTC(),
	}); err != nil {
		return err
	}
	log.Printf("版本选择完成：%d 条；语言分布：%s", len(selected), formatCounts(languageCounts(selected)))
	log.Printf("可复用选择缓存：%s", selectionPath)

	if !command.Apply {
		log.Printf("当前为只校验模式，未连接数据库、未写入任何表；正式导入需显式添加 -apply。")
		return nil
	}

	databaseConfig, err := config.LoadDatabase("")
	if err != nil {
		return err
	}
	db, err := database.Open(databaseConfig, false)
	if err != nil {
		return err
	}
	defer db.Close()
	store := &catalogStore{db: db}
	sourceID, err := store.sourceID(ctx)
	if err != nil {
		return err
	}
	categoryID, err := store.ensureImportCategory(ctx, command.CategoryCode, command.CategoryName)
	if err != nil {
		return err
	}
	batchID, err := store.beginBatch(ctx, sourceID, editionsMetadata, "sha256-stratified-v1:"+selectionFingerprint[:16])
	if err != nil {
		return err
	}
	log.Printf("开始写入书目导入批次 #%d，每批 %d 条。", batchID, command.BatchSize)
	outcome, err := store.importCatalog(ctx, batchID, sourceID, categoryID, selected, selectedAuthors, command.BatchSize)
	if err != nil {
		return err
	}
	log.Printf("导入批次 #%d 完成：处理 %d，成功 %d，冲突跳过 %d；冲突：%s", batchID, outcome.Processed, outcome.Imported, outcome.Rejected, formatCounts64(outcome.Conflicts))
	return nil
}

func inspectWithRetry(ctx context.Context, reader *sourceReader, source string, retries int) (sourceMetadata, error) {
	var lastErr error
	for attempt := 0; attempt <= retries; attempt++ {
		metadata, err := reader.inspect(ctx, source)
		if err == nil {
			return metadata, nil
		}
		lastErr = err
		if !isHTTPSource(source) || attempt == retries || !retryableInspectError(err) {
			break
		}
		if err := waitContext(ctx, retryDelay(attempt, err)); err != nil {
			return sourceMetadata{}, err
		}
	}
	return sourceMetadata{}, lastErr
}

func retryableInspectError(err error) bool {
	var statusErr *httpStatusError
	if !errors.As(err, &statusErr) {
		return true
	}
	return statusErr.StatusCode == 429 || statusErr.StatusCode >= 500
}

func selectAuthors(candidates []editionCandidate, all map[string]authorRecord) map[string]authorRecord {
	selected := make(map[string]authorRecord)
	for _, candidate := range candidates {
		for _, key := range candidate.AuthorKeys {
			if author, exists := all[key]; exists {
				selected[key] = author
			}
		}
	}
	return selected
}

func languageCounts(candidates []editionCandidate) map[string]int {
	counts := make(map[string]int)
	for _, candidate := range candidates {
		counts[candidate.LanguageCode]++
	}
	return counts
}

func formatCounts(values map[string]int) string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", key, values[key]))
	}
	if len(parts) == 0 {
		return "无"
	}
	return strings.Join(parts, "，")
}

func formatCounts64(values map[string]int64) string {
	converted := make(map[string]int, len(values))
	for key, value := range values {
		converted[key] = int(value)
	}
	return formatCounts(converted)
}

func cacheFileExists(path string) bool {
	_, err := os.Stat(filepath.Clean(path))
	return err == nil
}
