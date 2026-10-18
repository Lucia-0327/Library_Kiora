// Role 与服务端 RBAC 角色枚举保持一致，前端只用于展示和界面入口控制。
export type Role = "admin" | "librarian";

// User 是登录态和账号管理页面使用的安全账号视图，不包含密码字段。
export type User = {
  id: number;
  username: string;
  role: Role;
  isActive: boolean;
  createdAt: string;
  updatedAt: string;
};

// Reader 表示读者借阅档案及其当前借阅资格。
export type Reader = {
  id: number;
  readerNo: string;
  fullName: string;
  phone?: string;
  email?: string;
  status: "active" | "suspended" | "expired";
  fineBlocked: boolean;
  loanLimit: number;
  activeLoanCount: number;
  remainingLoanLimit: number;
  unpaidFineCount: number;
  unpaidFineAmount: string;
};

// Book 是书目元数据和由后端聚合出的副本统计信息。
export type Book = {
  id: number;
  workId?: number;
  categoryId: number;
  categoryName: string;
  isbn: string;
  materialType: "book" | "periodical";
  title: string;
  publisher?: string;
  publishers: string[];
  translators: string[];
  publishYear?: number;
  publishDateRaw?: string;
  editionStatement?: string;
  languageCode?: string;
  summary?: string;
  authors: { id: number; fullName: string }[];
  copyCount: number;
  availableCopies: number;
  sourceProvider?: string;
  sourceEditionKey?: string;
  sourceUrl?: string;
  verificationStatus: "unverified" | "invalid_identifier" | "source_not_found" | "source_conflict" | "source_matched" | "human_verified";
  collectionStatus: "active" | "quarantined" | "retired";
};

// BookCopy 是可通过条码流通的实体副本。
export type BookCopy = {
  id: number;
  bookId: number;
  bookTitle: string;
  materialType: "book" | "periodical";
  barcode: string;
  location?: string;
  status: "available" | "borrowed" | "reserved" | "lost" | "maintenance";
  conditionStatus: "intact" | "minor_wear" | "damaged" | "pending_inspection";
  conditionNote?: string;
};

// Loan 记录读者与实体副本之间的借阅生命周期。
export type Loan = {
  id: number;
  readerId: number;
  readerNo: string;
  readerName: string;
  bookCopyId: number;
  barcode: string;
  bookTitle: string;
  borrowedAt: string;
  dueAt: string;
  returnedAt?: string;
  renewCount: number;
  status: "active" | "returned" | "overdue" | "lost";
};

// Reservation 表示某书目的排队预约或待取状态。
export type Reservation = {
  id: number;
  readerId: number;
  readerNo: string;
  readerName: string;
  bookId: number;
  bookTitle: string;
  queuePosition: number;
  status: "waiting" | "ready" | "fulfilled" | "cancelled" | "expired";
  expiresAt?: string;
};

// Fine 表示与借阅关联的一笔罚金及处理状态。
export type Fine = {
  id: number;
  loanId: number;
  readerId: number;
  readerNo: string;
  readerName: string;
  bookTitle: string;
  barcode: string;
  amount: string;
  reason: string;
  status: "unpaid" | "paid" | "waived";
};

// Dashboard 是首页展示的馆藏和流通汇总指标。
export type Dashboard = {
  books: number;
  copies: number;
  readers: number;
  activeLoans: number;
  overdueLoans: number;
};
