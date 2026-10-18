-- 书目来源注册表：来源代码供作品、版本、作者、出版社和译者稳定引用。
CREATE TABLE catalog_sources (
    id SMALLINT UNSIGNED NOT NULL AUTO_INCREMENT,
    code VARCHAR(64) NOT NULL,
    display_name VARCHAR(128) NOT NULL,
    base_url VARCHAR(2048) NULL,
    license_name VARCHAR(128) NULL,
    license_url VARCHAR(2048) NULL,
    is_active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    PRIMARY KEY (id),
    UNIQUE KEY uq_catalog_sources_code (code),
    CONSTRAINT chk_catalog_sources_code CHECK (code REGEXP '^[a-z0-9][a-z0-9_-]{0,63}$'),
    CONSTRAINT chk_catalog_sources_is_active CHECK (is_active IN (0, 1))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- 预置当前真实书目导入使用的公开来源；许可信息必须按实际使用的数据集另行登记。
INSERT INTO catalog_sources (code, display_name, base_url)
VALUES ('openlibrary', 'Open Library', 'https://openlibrary.org');

-- 作品层只表达抽象作品；具体译本、出版社、日期和实体副本仍属于 books 版本层。
CREATE TABLE works (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    preferred_title VARCHAR(512) NOT NULL,
    original_title VARCHAR(512) NULL,
    original_language VARCHAR(35) NULL,
    first_publish_year SMALLINT UNSIGNED NULL,
    source_provider VARCHAR(64) NULL,
    source_work_key VARCHAR(255) NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    PRIMARY KEY (id),
    UNIQUE KEY uq_works_source_key (source_provider, source_work_key),
    KEY idx_works_preferred_title (preferred_title(128)),
    CONSTRAINT fk_works_source_provider FOREIGN KEY (source_provider) REFERENCES catalog_sources (code) ON DELETE RESTRICT ON UPDATE RESTRICT,
    CONSTRAINT chk_works_first_publish_year CHECK (first_publish_year IS NULL OR first_publish_year BETWEEN 1 AND 9999),
    CONSTRAINT chk_works_source_identity CHECK (
        (source_provider IS NULL AND source_work_key IS NULL)
        OR (source_provider IS NOT NULL AND source_work_key IS NOT NULL)
    )
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- 每次远端抓取建立独立批次，保存 HTTP 元数据、选择方式、进度和最终状态。
CREATE TABLE catalog_import_batches (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    source_id SMALLINT UNSIGNED NOT NULL,
    source_url VARCHAR(2048) NOT NULL,
    etag VARCHAR(255) NULL,
    last_modified VARCHAR(255) NULL,
    content_length BIGINT UNSIGNED NULL,
    selection_method VARCHAR(64) NOT NULL,
    status VARCHAR(32) NOT NULL DEFAULT 'pending',
    processed_count BIGINT UNSIGNED NOT NULL DEFAULT 0,
    imported_count BIGINT UNSIGNED NOT NULL DEFAULT 0,
    rejected_count BIGINT UNSIGNED NOT NULL DEFAULT 0,
    started_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    completed_at DATETIME(6) NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    PRIMARY KEY (id),
    UNIQUE KEY uq_catalog_import_batches_id_source (id, source_id),
    KEY idx_catalog_import_batches_source_status (source_id, status, started_at),
    CONSTRAINT fk_catalog_import_batches_source FOREIGN KEY (source_id) REFERENCES catalog_sources (id) ON DELETE RESTRICT,
    CONSTRAINT chk_catalog_import_batches_status CHECK (
        status IN ('pending', 'running', 'completed', 'completed_with_errors', 'failed', 'cancelled')
    ),
    CONSTRAINT chk_catalog_import_batches_counts CHECK (
        imported_count + rejected_count <= processed_count
    ),
    CONSTRAINT chk_catalog_import_batches_dates CHECK (
        completed_at IS NULL OR completed_at >= started_at
    )
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- 原始来源记录保留抓取时的 JSON 快照和哈希；book_id 在记录成功落为版本后再关联。
CREATE TABLE catalog_source_records (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    source_id SMALLINT UNSIGNED NOT NULL,
    batch_id BIGINT UNSIGNED NOT NULL,
    book_id BIGINT UNSIGNED NULL,
    record_kind VARCHAR(32) NOT NULL,
    external_id VARCHAR(255) NOT NULL,
    source_url VARCHAR(2048) NULL,
    source_revision VARCHAR(128) NULL,
    source_modified_at DATETIME(6) NULL,
    raw_payload JSON NOT NULL,
    payload_sha256 CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    fetched_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    PRIMARY KEY (id),
    UNIQUE KEY uq_catalog_source_records_external (source_id, record_kind, external_id),
    KEY idx_catalog_source_records_batch_source (batch_id, source_id),
    KEY idx_catalog_source_records_book_id (book_id),
    KEY idx_catalog_source_records_fetched_at (source_id, fetched_at),
    CONSTRAINT fk_catalog_source_records_batch_source FOREIGN KEY (batch_id, source_id)
        REFERENCES catalog_import_batches (id, source_id) ON DELETE RESTRICT,
    CONSTRAINT fk_catalog_source_records_book FOREIGN KEY (book_id) REFERENCES books (id) ON DELETE SET NULL,
    CONSTRAINT chk_catalog_source_records_kind CHECK (
        record_kind IN ('work', 'edition', 'author', 'publisher', 'translator')
    ),
    CONSTRAINT chk_catalog_source_records_hash CHECK (payload_sha256 REGEXP '^[0-9a-f]{64}$')
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- 出版社名称本身不全局唯一；来源标识用于区分同名机构。
CREATE TABLE publishers (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    name VARCHAR(255) NOT NULL,
    normalized_name VARCHAR(255) NOT NULL,
    country_code CHAR(2) NULL,
    source_provider VARCHAR(64) NULL,
    source_publisher_key VARCHAR(255) NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    PRIMARY KEY (id),
    UNIQUE KEY uq_publishers_source_key (source_provider, source_publisher_key),
    KEY idx_publishers_normalized_name (normalized_name),
    CONSTRAINT fk_publishers_source_provider FOREIGN KEY (source_provider) REFERENCES catalog_sources (code) ON DELETE RESTRICT ON UPDATE RESTRICT,
    CONSTRAINT chk_publishers_name CHECK (CHAR_LENGTH(TRIM(name)) > 0 AND CHAR_LENGTH(TRIM(normalized_name)) > 0),
    CONSTRAINT chk_publishers_country_code CHECK (country_code IS NULL OR country_code REGEXP '^[A-Z]{2}$'),
    CONSTRAINT chk_publishers_source_identity CHECK (
        (source_provider IS NULL AND source_publisher_key IS NULL)
        OR (source_provider IS NOT NULL AND source_publisher_key IS NOT NULL)
    )
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- 译者独立于作者建模，允许同一人物在不同版本中承担不同译者关系。
CREATE TABLE translators (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    full_name VARCHAR(255) NOT NULL,
    source_provider VARCHAR(64) NULL,
    source_translator_key VARCHAR(255) NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    PRIMARY KEY (id),
    UNIQUE KEY uq_translators_source_key (source_provider, source_translator_key),
    KEY idx_translators_full_name (full_name),
    CONSTRAINT fk_translators_source_provider FOREIGN KEY (source_provider) REFERENCES catalog_sources (code) ON DELETE RESTRICT ON UPDATE RESTRICT,
    CONSTRAINT chk_translators_full_name CHECK (CHAR_LENGTH(TRIM(full_name)) > 0),
    CONSTRAINT chk_translators_source_identity CHECK (
        (source_provider IS NULL AND source_translator_key IS NULL)
        OR (source_provider IS NOT NULL AND source_translator_key IS NOT NULL)
    )
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- 作者继续沿用原表和原主键；移除姓名全局唯一后，可用来源标识区分同名作者。
ALTER TABLE authors
    ADD COLUMN source_provider VARCHAR(64) NULL AFTER full_name,
    ADD COLUMN source_author_key VARCHAR(255) NULL AFTER source_provider,
    DROP INDEX uq_authors_full_name,
    ADD KEY idx_authors_full_name (full_name),
    ADD UNIQUE KEY uq_authors_source_key (source_provider, source_author_key),
    ADD CONSTRAINT fk_authors_source_provider FOREIGN KEY (source_provider) REFERENCES catalog_sources (code) ON DELETE RESTRICT ON UPDATE RESTRICT,
    ADD CONSTRAINT chk_authors_source_identity CHECK (
        (source_provider IS NULL AND source_author_key IS NULL)
        OR (source_provider IS NOT NULL AND source_author_key IS NOT NULL)
    );

-- books 保留现有表名和主键作为“出版版本”，避免破坏副本、预约和流通历史外键。
-- work_id 本迁移保持可空，以兼容尚未双写作品关系的旧服务；旧数据仍会全部完成回填。
ALTER TABLE books
    ADD COLUMN work_id BIGINT UNSIGNED NULL,
    ADD COLUMN normalized_isbn CHAR(13) CHARACTER SET ascii COLLATE ascii_bin NULL,
    ADD COLUMN source_provider VARCHAR(64) NULL,
    ADD COLUMN source_edition_key VARCHAR(255) NULL,
    ADD COLUMN source_work_key VARCHAR(255) NULL,
    ADD COLUMN source_revision VARCHAR(128) NULL,
    ADD COLUMN source_modified_at DATETIME(6) NULL,
    ADD COLUMN source_url VARCHAR(2048) NULL,
    ADD COLUMN publish_date_raw VARCHAR(128) NULL,
    ADD COLUMN edition_statement VARCHAR(255) NULL,
    ADD COLUMN language_code VARCHAR(35) NULL,
    ADD COLUMN primary_source_record_id BIGINT UNSIGNED NULL,
    ADD COLUMN verification_status VARCHAR(32) NOT NULL DEFAULT 'unverified',
    ADD COLUMN collection_status VARCHAR(20) NOT NULL DEFAULT 'quarantined',
    ADD UNIQUE KEY uq_books_normalized_isbn (normalized_isbn),
    ADD UNIQUE KEY uq_books_source_edition (source_provider, source_edition_key),
    ADD UNIQUE KEY uq_books_primary_source_record (primary_source_record_id),
    ADD KEY idx_books_work_id (work_id),
    ADD KEY idx_books_source_work (source_provider, source_work_key),
    ADD KEY idx_books_catalog_status (collection_status, verification_status, id),
    ADD CONSTRAINT fk_books_source_provider FOREIGN KEY (source_provider) REFERENCES catalog_sources (code) ON DELETE RESTRICT ON UPDATE RESTRICT,
    ADD CONSTRAINT fk_books_primary_source_record FOREIGN KEY (primary_source_record_id) REFERENCES catalog_source_records (id) ON DELETE RESTRICT,
    ADD CONSTRAINT chk_books_normalized_isbn CHECK (normalized_isbn IS NULL OR normalized_isbn REGEXP '^[0-9]{13}$'),
    ADD CONSTRAINT chk_books_verification_status CHECK (
        verification_status IN ('unverified', 'invalid_identifier', 'source_not_found', 'source_conflict', 'source_matched', 'human_verified')
    ),
    ADD CONSTRAINT chk_books_collection_status CHECK (
        collection_status IN ('active', 'quarantined', 'retired')
    ),
    ADD CONSTRAINT chk_books_source_fields CHECK (
        source_provider IS NOT NULL
        OR (
            source_edition_key IS NULL
            AND source_work_key IS NULL
            AND source_revision IS NULL
            AND source_modified_at IS NULL
            AND source_url IS NULL
            AND primary_source_record_id IS NULL
        )
    ),
    ADD CONSTRAINT chk_books_source_matched_record CHECK (
        verification_status <> 'source_matched' OR primary_source_record_id IS NOT NULL
    ),
    ADD CONSTRAINT chk_books_active_verified CHECK (
        collection_status <> 'active' OR verification_status IN ('source_matched', 'human_verified')
    );

-- 每条旧版本先建立独立作品，避免迁移阶段仅凭相似标题错误合并不同作品。
INSERT INTO works (id, preferred_title, first_publish_year, created_at, updated_at)
SELECT id,
       title,
       CASE WHEN publish_year BETWEEN 1 AND 9999 THEN publish_year ELSE NULL END,
       created_at,
       updated_at
FROM books;

UPDATE books AS b
JOIN works AS w ON w.id = b.id
SET b.work_id = w.id
WHERE b.work_id IS NULL;

ALTER TABLE books
    ADD CONSTRAINT fk_books_work FOREIGN KEY (work_id) REFERENCES works (id) ON DELETE RESTRICT;

-- 多种标识符独立保存；迁移不把未经校验的旧 ISBN 自动标成可信标识。
CREATE TABLE book_identifiers (
    book_id BIGINT UNSIGNED NOT NULL,
    scheme VARCHAR(32) NOT NULL,
    display_value VARCHAR(255) NOT NULL,
    normalized_value VARCHAR(255) NOT NULL,
    is_primary BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (book_id, scheme, normalized_value),
    UNIQUE KEY uq_book_identifiers_value (scheme, normalized_value),
    KEY idx_book_identifiers_book_primary (book_id, is_primary),
    CONSTRAINT fk_book_identifiers_book FOREIGN KEY (book_id) REFERENCES books (id) ON DELETE CASCADE,
    CONSTRAINT chk_book_identifiers_values CHECK (
        CHAR_LENGTH(TRIM(scheme)) > 0
        AND CHAR_LENGTH(TRIM(display_value)) > 0
        AND CHAR_LENGTH(TRIM(normalized_value)) > 0
    ),
    CONSTRAINT chk_book_identifiers_is_primary CHECK (is_primary IN (0, 1))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- 一个版本可以由多个出版社联合出版，并保留著录顺序和展示名称。
CREATE TABLE book_publishers (
    book_id BIGINT UNSIGNED NOT NULL,
    publisher_id BIGINT UNSIGNED NOT NULL,
    publisher_order SMALLINT UNSIGNED NOT NULL DEFAULT 1,
    role VARCHAR(20) NOT NULL DEFAULT 'publisher',
    credited_name VARCHAR(255) NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (book_id, publisher_id, role),
    UNIQUE KEY uq_book_publishers_order (book_id, publisher_order),
    KEY idx_book_publishers_publisher (publisher_id, book_id),
    CONSTRAINT fk_book_publishers_book FOREIGN KEY (book_id) REFERENCES books (id) ON DELETE CASCADE,
    CONSTRAINT fk_book_publishers_publisher FOREIGN KEY (publisher_id) REFERENCES publishers (id) ON DELETE RESTRICT,
    CONSTRAINT chk_book_publishers_order CHECK (publisher_order > 0),
    CONSTRAINT chk_book_publishers_role CHECK (role IN ('publisher', 'co_publisher', 'imprint'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- 旧 publisher 文本整段回填为单一出版社，不按标点猜测和拆分联合出版信息。
INSERT INTO publishers (name, normalized_name)
SELECT MIN(TRIM(publisher)), LOWER(TRIM(publisher))
FROM books
WHERE publisher IS NOT NULL AND CHAR_LENGTH(TRIM(publisher)) > 0
GROUP BY LOWER(TRIM(publisher));

INSERT INTO book_publishers (book_id, publisher_id, publisher_order, role, credited_name)
SELECT b.id, p.id, 1, 'publisher', TRIM(b.publisher)
FROM books AS b
JOIN publishers AS p
  ON p.source_provider IS NULL
 AND p.normalized_name = LOWER(TRIM(b.publisher))
WHERE b.publisher IS NOT NULL AND CHAR_LENGTH(TRIM(b.publisher)) > 0;

-- 同一版本可以有多位译者，translator_order 保留来源著录顺序。
CREATE TABLE book_translators (
    book_id BIGINT UNSIGNED NOT NULL,
    translator_id BIGINT UNSIGNED NOT NULL,
    translator_order SMALLINT UNSIGNED NOT NULL DEFAULT 1,
    credited_name VARCHAR(255) NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (book_id, translator_id),
    UNIQUE KEY uq_book_translators_order (book_id, translator_order),
    KEY idx_book_translators_translator (translator_id, book_id),
    CONSTRAINT fk_book_translators_book FOREIGN KEY (book_id) REFERENCES books (id) ON DELETE CASCADE,
    CONSTRAINT fk_book_translators_translator FOREIGN KEY (translator_id) REFERENCES translators (id) ON DELETE RESTRICT,
    CONSTRAINT chk_book_translators_order CHECK (translator_order > 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
