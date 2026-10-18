-- 账号与角色表：密码仅保存 bcrypt 哈希，角色用于服务端 RBAC 鉴权。
CREATE TABLE users (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    username VARCHAR(64) NOT NULL,
    password_hash VARCHAR(255) NOT NULL,
    role VARCHAR(20) NOT NULL,
    is_active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    PRIMARY KEY (id),
    UNIQUE KEY uq_users_username (username),
    CONSTRAINT chk_users_role CHECK (role IN ('admin', 'librarian', 'reader'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- 读者借阅档案：可选关联登录账号，也允许只作为线下读者存在。
CREATE TABLE readers (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    user_id BIGINT UNSIGNED NULL,
    reader_no VARCHAR(64) NOT NULL,
    full_name VARCHAR(128) NOT NULL,
    phone VARCHAR(32) NULL,
    email VARCHAR(254) NULL,
    status VARCHAR(20) NOT NULL DEFAULT 'active',
    loan_limit INT UNSIGNED NOT NULL DEFAULT 5,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    PRIMARY KEY (id),
    UNIQUE KEY uq_readers_user_id (user_id),
    UNIQUE KEY uq_readers_reader_no (reader_no),
    CONSTRAINT fk_readers_user FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE SET NULL,
    CONSTRAINT chk_readers_status CHECK (status IN ('active', 'suspended', 'expired'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- 书目分类表：parent_id 支持有限层级的分类树。
CREATE TABLE categories (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    parent_id BIGINT UNSIGNED NULL,
    name VARCHAR(128) NOT NULL,
    code VARCHAR(64) NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    PRIMARY KEY (id),
    UNIQUE KEY uq_categories_code (code),
    KEY idx_categories_parent_id (parent_id),
    CONSTRAINT fk_categories_parent FOREIGN KEY (parent_id) REFERENCES categories (id) ON DELETE SET NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- 书目表：记录 ISBN 和出版信息，不直接代表可借的实体馆藏。
CREATE TABLE books (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    category_id BIGINT UNSIGNED NULL,
    isbn VARCHAR(17) NOT NULL,
    title VARCHAR(512) NOT NULL,
    publisher VARCHAR(255) NULL,
    publish_year SMALLINT UNSIGNED NULL,
    summary TEXT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    PRIMARY KEY (id),
    UNIQUE KEY uq_books_isbn (isbn),
    KEY idx_books_category_id (category_id),
    KEY idx_books_title (title(128)),
    CONSTRAINT fk_books_category FOREIGN KEY (category_id) REFERENCES categories (id) ON DELETE SET NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- 作者主数据：通过中间表与书目形成多对多关联。
CREATE TABLE authors (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    full_name VARCHAR(255) NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    PRIMARY KEY (id),
    UNIQUE KEY uq_authors_full_name (full_name)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- 书目与作者关联表：author_order 保留封面或著录中的作者顺序。
CREATE TABLE book_authors (
    book_id BIGINT UNSIGNED NOT NULL,
    author_id BIGINT UNSIGNED NOT NULL,
    author_order SMALLINT UNSIGNED NOT NULL DEFAULT 1,
    PRIMARY KEY (book_id, author_id),
    KEY idx_book_authors_author_id (author_id),
    CONSTRAINT fk_book_authors_book FOREIGN KEY (book_id) REFERENCES books (id) ON DELETE CASCADE,
    CONSTRAINT fk_book_authors_author FOREIGN KEY (author_id) REFERENCES authors (id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- 实体副本表：每个条码对应一个可流通的馆藏副本及其状态。
CREATE TABLE book_copies (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    book_id BIGINT UNSIGNED NOT NULL,
    barcode VARCHAR(64) NOT NULL,
    location VARCHAR(255) NULL,
    status VARCHAR(20) NOT NULL DEFAULT 'available',
    condition_note VARCHAR(255) NULL,
    acquired_at DATE NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    PRIMARY KEY (id),
    UNIQUE KEY uq_book_copies_barcode (barcode),
    KEY idx_book_copies_book_status (book_id, status),
    CONSTRAINT fk_book_copies_book FOREIGN KEY (book_id) REFERENCES books (id) ON DELETE RESTRICT,
    CONSTRAINT chk_book_copies_status CHECK (status IN ('available', 'borrowed', 'reserved', 'lost', 'maintenance'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- 借阅流水表：副本状态与借阅状态在服务端事务中同步更新。
CREATE TABLE loans (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    reader_id BIGINT UNSIGNED NOT NULL,
    book_copy_id BIGINT UNSIGNED NOT NULL,
    borrowed_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    due_at DATETIME NOT NULL,
    returned_at DATETIME NULL,
    renew_count INT UNSIGNED NOT NULL DEFAULT 0,
    status VARCHAR(20) NOT NULL DEFAULT 'active',
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    PRIMARY KEY (id),
    KEY idx_loans_reader_status (reader_id, status),
    KEY idx_loans_copy_status (book_copy_id, status),
    KEY idx_loans_due_at (due_at),
    CONSTRAINT fk_loans_reader FOREIGN KEY (reader_id) REFERENCES readers (id) ON DELETE RESTRICT,
    CONSTRAINT fk_loans_copy FOREIGN KEY (book_copy_id) REFERENCES book_copies (id) ON DELETE RESTRICT,
    CONSTRAINT chk_loans_status CHECK (status IN ('active', 'returned', 'overdue', 'lost')),
    CONSTRAINT chk_loans_dates CHECK (returned_at IS NULL OR returned_at >= borrowed_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- 预约队列表：queue_position 仅在 waiting 和 ready 状态中有业务意义。
CREATE TABLE reservations (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    reader_id BIGINT UNSIGNED NOT NULL,
    book_id BIGINT UNSIGNED NOT NULL,
    queue_position INT UNSIGNED NOT NULL,
    status VARCHAR(20) NOT NULL DEFAULT 'waiting',
    expires_at DATETIME NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    PRIMARY KEY (id),
    KEY idx_reservations_book_status_position (book_id, status, queue_position),
    KEY idx_reservations_reader_status (reader_id, status),
    CONSTRAINT fk_reservations_reader FOREIGN KEY (reader_id) REFERENCES readers (id) ON DELETE RESTRICT,
    CONSTRAINT fk_reservations_book FOREIGN KEY (book_id) REFERENCES books (id) ON DELETE RESTRICT,
    CONSTRAINT chk_reservations_status CHECK (status IN ('waiting', 'ready', 'fulfilled', 'cancelled', 'expired'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- 罚金表：付款或减免前均保持 unpaid，避免同一笔罚金被重复处理。
CREATE TABLE fines (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    loan_id BIGINT UNSIGNED NOT NULL,
    amount DECIMAL(10, 2) NOT NULL,
    reason VARCHAR(255) NOT NULL,
    status VARCHAR(20) NOT NULL DEFAULT 'unpaid',
    paid_at DATETIME NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    PRIMARY KEY (id),
    KEY idx_fines_loan_id (loan_id),
    KEY idx_fines_status (status),
    CONSTRAINT fk_fines_loan FOREIGN KEY (loan_id) REFERENCES loans (id) ON DELETE RESTRICT,
    CONSTRAINT chk_fines_amount CHECK (amount >= 0),
    CONSTRAINT chk_fines_status CHECK (status IN ('unpaid', 'paid', 'waived'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- 审计表：保留敏感操作的操作者、目标对象和可选 JSON 上下文。
CREATE TABLE audit_logs (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    actor_user_id BIGINT UNSIGNED NULL,
    action VARCHAR(128) NOT NULL,
    entity_type VARCHAR(128) NOT NULL,
    entity_id BIGINT UNSIGNED NULL,
    details JSON NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (id),
    KEY idx_audit_logs_actor_created_at (actor_user_id, created_at),
    KEY idx_audit_logs_entity (entity_type, entity_id),
    CONSTRAINT fk_audit_logs_actor FOREIGN KEY (actor_user_id) REFERENCES users (id) ON DELETE SET NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
