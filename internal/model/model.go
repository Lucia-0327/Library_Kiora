package model

import "time"

// 角色用于服务端鉴权，浏览器界面不能自行决定访问权限。
const (
	RoleAdmin     = "admin"
	RoleLibrarian = "librarian"
)

// User 是接口返回的安全账号表示，刻意不包含 password_hash。
type User struct {
	ID        uint64    `json:"id"`
	Username  string    `json:"username"`
	Role      string    `json:"role"`
	IsActive  bool      `json:"isActive"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Reader 是独立的读者借阅档案，包含当前额度和罚金冻结汇总。
type Reader struct {
	ID                 uint64    `json:"id"`
	ReaderNo           string    `json:"readerNo"`
	FullName           string    `json:"fullName"`
	Phone              *string   `json:"phone,omitempty"`
	Email              *string   `json:"email,omitempty"`
	Status             string    `json:"status"`
	FineBlocked        bool      `json:"fineBlocked"`
	LoanLimit          uint      `json:"loanLimit"`
	ActiveLoanCount    uint      `json:"activeLoanCount"`
	RemainingLoanLimit uint      `json:"remainingLoanLimit"`
	UnpaidFineCount    uint      `json:"unpaidFineCount"`
	UnpaidFineAmount   string    `json:"unpaidFineAmount"`
	CreatedAt          time.Time `json:"createdAt"`
	UpdatedAt          time.Time `json:"updatedAt"`
}

// Category 通过可空的父级字段表达简单的分类层级。
type Category struct {
	ID        uint64    `json:"id"`
	ParentID  *uint64   `json:"parentId,omitempty"`
	Name      string    `json:"name"`
	Code      string    `json:"code"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Author 是可复用的作者记录，通过 book_authors 与书目关联。
type Author struct {
	ID        uint64    `json:"id"`
	FullName  string    `json:"fullName"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Book 保存书目元数据；实体流通由 BookCopy 记录负责。
type Book struct {
	ID                 uint64    `json:"id"`
	WorkID             *uint64   `json:"workId,omitempty"`
	CategoryID         *uint64   `json:"categoryId,omitempty"`
	CategoryName       string    `json:"categoryName"`
	ISBN               string    `json:"isbn"`
	MaterialType       string    `json:"materialType"`
	Title              string    `json:"title"`
	Publisher          *string   `json:"publisher,omitempty"`
	Publishers         []string  `json:"publishers"`
	Translators        []string  `json:"translators"`
	PublishYear        *uint     `json:"publishYear,omitempty"`
	PublishDateRaw     *string   `json:"publishDateRaw,omitempty"`
	EditionStatement   *string   `json:"editionStatement,omitempty"`
	LanguageCode       *string   `json:"languageCode,omitempty"`
	Summary            *string   `json:"summary,omitempty"`
	Authors            []Author  `json:"authors"`
	SourceProvider     *string   `json:"sourceProvider,omitempty"`
	SourceEditionKey   *string   `json:"sourceEditionKey,omitempty"`
	SourceURL          *string   `json:"sourceUrl,omitempty"`
	VerificationStatus string    `json:"verificationStatus"`
	CollectionStatus   string    `json:"collectionStatus"`
	CopyCount          uint      `json:"copyCount"`
	Available          uint      `json:"availableCopies"`
	CreatedAt          time.Time `json:"createdAt"`
	UpdatedAt          time.Time `json:"updatedAt"`
}

// BookCopy 表示一个可通过条码定位的实体副本；品相与流通状态分别维护。
type BookCopy struct {
	ID              uint64     `json:"id"`
	BookID          uint64     `json:"bookId"`
	BookTitle       string     `json:"bookTitle"`
	MaterialType    string     `json:"materialType"`
	Barcode         string     `json:"barcode"`
	Location        *string    `json:"location,omitempty"`
	Status          string     `json:"status"`
	ConditionStatus string     `json:"conditionStatus"`
	ConditionNote   *string    `json:"conditionNote,omitempty"`
	AcquiredAt      *time.Time `json:"acquiredAt,omitempty"`
	CreatedAt       time.Time  `json:"createdAt"`
	UpdatedAt       time.Time  `json:"updatedAt"`
}

// Loan 关联读者与一个实体副本，覆盖完整借阅生命周期。
type Loan struct {
	ID         uint64     `json:"id"`
	ReaderID   uint64     `json:"readerId"`
	ReaderNo   string     `json:"readerNo"`
	BookCopyID uint64     `json:"bookCopyId"`
	Barcode    string     `json:"barcode"`
	BookTitle  string     `json:"bookTitle"`
	ReaderName string     `json:"readerName"`
	BorrowedAt time.Time  `json:"borrowedAt"`
	DueAt      time.Time  `json:"dueAt"`
	ReturnedAt *time.Time `json:"returnedAt,omitempty"`
	RenewCount uint       `json:"renewCount"`
	Status     string     `json:"status"`
}

// Reservation 记录读者对某一书目的排队预约。
type Reservation struct {
	ID            uint64     `json:"id"`
	ReaderID      uint64     `json:"readerId"`
	ReaderNo      string     `json:"readerNo"`
	ReaderName    string     `json:"readerName"`
	BookID        uint64     `json:"bookId"`
	BookTitle     string     `json:"bookTitle"`
	QueuePosition uint       `json:"queuePosition"`
	Status        string     `json:"status"`
	ExpiresAt     *time.Time `json:"expiresAt,omitempty"`
	CreatedAt     time.Time  `json:"createdAt"`
}

// Fine 同时关联借阅和读者档案，便于冻结资格与追溯具体图书。
type Fine struct {
	ID         uint64     `json:"id"`
	LoanID     uint64     `json:"loanId"`
	ReaderID   uint64     `json:"readerId"`
	ReaderNo   string     `json:"readerNo"`
	ReaderName string     `json:"readerName"`
	BookTitle  string     `json:"bookTitle"`
	Barcode    string     `json:"barcode"`
	Amount     string     `json:"amount"`
	Reason     string     `json:"reason"`
	Status     string     `json:"status"`
	PaidAt     *time.Time `json:"paidAt,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
}

// AuditLog 记录敏感馆员操作的执行人和目标对象。
type AuditLog struct {
	ID          uint64    `json:"id"`
	ActorUserID *uint64   `json:"actorUserId,omitempty"`
	ActorName   *string   `json:"actorName,omitempty"`
	Action      string    `json:"action"`
	EntityType  string    `json:"entityType"`
	EntityID    *uint64   `json:"entityId,omitempty"`
	Details     any       `json:"details,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
}
