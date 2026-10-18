import { FormEvent, useEffect, useState } from "react";
import { api, APIError, setToken } from "./api";
import { displayRole, formatDate } from "./format";
import { nextOffset, PAGE_REQUEST_SIZE, PAGE_SIZE, pageWindow, previousOffset } from "./pagination";
import type { Book, BookCopy, Dashboard, Fine, Loan, Reader, Reservation, Role, User } from "./types";

// Page 约束左侧导航可切换的业务页面，避免散落的字符串常量。
type Page = "dashboard" | "circulation" | "books" | "readers" | "reservations" | "fines" | "users" | "audit";

// Notice 是页面操作后的短暂反馈，不保存到后端。
type Notice = { type: "success" | "error"; text: string } | null;

// asErrorMessage 将统一接口错误和未知网络异常转换为可展示的中文提示。
function asErrorMessage(error: unknown): string {
  return error instanceof APIError ? error.message : "网络或服务异常，请稍后重试";
}

// Panel 是列表和仪表盘共用的内容容器，actions 放置当前模块的操作按钮。
function Panel({ title, children, actions }: { title: string; children: React.ReactNode; actions?: React.ReactNode }) {
  return <section className="panel"><div className="panel-head"><h2>{title}</h2>{actions}</div>{children}</section>;
}

// Empty 为无数据状态提供统一占位，避免每个页面重复处理空列表。
function Empty({ children = "暂无数据" }: { children?: React.ReactNode }) { return <div className="empty">{children}</div>; }

// useDebouncedValue 延迟提交连续输入，减少万级列表搜索时的无效请求。
function useDebouncedValue<T>(value: T, delay = 300): T {
  const [debounced, setDebounced] = useState(value);
  useEffect(() => {
    const timer = window.setTimeout(() => setDebounced(value), delay);
    return () => window.clearTimeout(timer);
  }, [value, delay]);
  return debounced;
}

// usePagedRows 统一处理多取一条的分页结果，并丢弃筛选快速变化时的旧响应。
function usePagedRows<T>(load: () => Promise<unknown[]>, dependencies: readonly unknown[], onError: (message: string) => void) {
  const [items, setItems] = useState<T[]>([]);
  const [hasMore, setHasMore] = useState(false);
  const [loading, setLoading] = useState(false);
  const [refreshVersion, setRefreshVersion] = useState(0);
  useEffect(() => {
    let current = true;
    setLoading(true);
    load().then((rows) => {
      if (!current) return;
      const page = pageWindow(rows as T[]);
      setItems(page.items);
      setHasMore(page.hasMore);
    }).catch((error) => {
      if (current) onError(asErrorMessage(error));
    }).finally(() => {
      if (current) setLoading(false);
    });
    return () => { current = false; };
  }, [...dependencies, refreshVersion]);
  return { items, hasMore, loading, refresh: () => setRefreshVersion((value) => value + 1) };
}

// Pager 展示固定 50 条分页控制，hasMore 来自服务端多返回的第 51 条。
function Pager({ offset, hasMore, loading, onChange }: { offset: number; hasMore: boolean; loading: boolean; onChange: (offset: number) => void }) {
  return <div className="pager" aria-label="分页导航">
    <span>第 {Math.floor(offset / PAGE_SIZE) + 1} 页</span>
    <div><button disabled={loading || offset === 0} onClick={() => onChange(previousOffset(offset))}>上一页</button><button disabled={loading || !hasMore} onClick={() => onChange(nextOffset(offset, hasMore))}>下一页</button></div>
  </div>;
}

type SearchOption = { id: number; label: string; detail?: string; disabled?: boolean; meta?: unknown };

// ServerSearchSelect 始终独立请求候选数据，不复用业务列表当前页。
function ServerSearchSelect({ label, value, placeholder, loadOptions, onChange }: { label: string; value: number | null; placeholder: string; loadOptions: (q: string) => Promise<SearchOption[]>; onChange: (id: number | null, option?: SearchOption) => void }) {
  const [query, setQuery] = useState("");
  const [selected, setSelected] = useState<SearchOption | null>(null);
  const [options, setOptions] = useState<SearchOption[]>([]);
  const [loading, setLoading] = useState(false);
  const [failed, setFailed] = useState(false);
  const [open, setOpen] = useState(false);
  const debouncedQuery = useDebouncedValue(query);
  useEffect(() => {
    if (value === null && selected !== null) {
      setSelected(null);
      setQuery("");
    }
  }, [value, selected]);
  useEffect(() => {
    let current = true;
    setLoading(true);
    setFailed(false);
    loadOptions(debouncedQuery).then((rows) => {
      if (current) setOptions(rows);
    }).catch(() => {
      if (current) { setOptions([]); setFailed(true); }
    }).finally(() => {
      if (current) setLoading(false);
    });
    return () => { current = false; };
  }, [debouncedQuery]);
  return <label className="server-select">{label}<div className="server-select-control" onBlur={(event) => { if (!event.currentTarget.contains(event.relatedTarget as Node | null)) setOpen(false); }}>
    <input role="combobox" aria-expanded={open} aria-autocomplete="list" value={selected ? selected.label : query} onFocus={() => setOpen(true)} onChange={(event) => { setSelected(null); onChange(null); setQuery(event.target.value); setOpen(true); }} placeholder={placeholder} autoComplete="off" />
    {open && <div className="server-select-menu" role="listbox">{loading ? <span>正在搜索...</span> : failed ? <span className="danger-text">搜索失败，请重试</span> : options.length ? options.map((option) => <button type="button" role="option" aria-selected={selected?.id === option.id} disabled={option.disabled} key={option.id} onClick={() => { setSelected(option); setQuery(option.label); onChange(option.id, option); setOpen(false); }}><strong>{option.label}</strong>{option.detail && <small>{option.detail}</small>}</button>) : <span>没有匹配记录</span>}</div>}
  </div></label>;
}

// Modal 是受控弹窗基础组件，保存后的关闭和刷新由调用方决定。
function Modal({ title, children, onClose }: { title: string; children: React.ReactNode; onClose: () => void }) {
  return <div className="modal-mask" role="dialog" aria-modal="true" aria-label={title}>
    <div className="modal"><div className="modal-head"><h2>{title}</h2><button className="icon-button" onClick={onClose} aria-label="关闭">×</button></div>{children}</div>
  </div>;
}

// Badge 使用固定色调展示状态，避免各页面复制状态样式。
function Badge({ children, tone = "neutral" }: { children: React.ReactNode; tone?: "neutral" | "good" | "warn" | "bad" }) {
  return <span className={`badge ${tone}`}>{children}</span>;
}

// App 负责恢复登录态、切换业务页面，并把接口异常统一展示为通知。
export default function App() {
  const [user, setUser] = useState<User | null>(null);
  const [page, setPage] = useState<Page>("dashboard");
  const [notice, setNotice] = useState<Notice>(null);
  const [booting, setBooting] = useState(true);

  // 每次浏览器加载都向服务端确认会话，令牌过期或被吊销时会回到登录页。
  useEffect(() => {
    api.me().then((value) => setUser(value as User)).catch(() => setToken(null)).finally(() => setBooting(false));
  }, []);

  // 通知状态集中在根组件，子页面只需报告操作结果。
  const notify = (type: "success" | "error", text: string) => setNotice({ type, text });

  // 无论服务端登出是否成功，浏览器都必须放弃当前会话令牌和页面状态。
  const logout = async () => { try { await api.logout(); } finally { setToken(null); setUser(null); setPage("dashboard"); } };

  if (booting) return <div className="splash"><div className="mark">书</div><p>正在连接馆藏系统</p></div>;
  if (!user) return <Login onLogin={setUser} onNotice={notify} />;

  const menu: { id: Page; label: string; tag?: string; admin?: boolean }[] = [
    { id: "dashboard", label: "总览" }, { id: "circulation", label: "流通" }, { id: "books", label: "馆藏" },
    { id: "readers", label: "读者" }, { id: "reservations", label: "预约" }, { id: "fines", label: "罚金" },
    { id: "users", label: "馆员账号", admin: true }, { id: "audit", label: "审计", admin: true }
  ];

  return <div className="shell">
    <aside className="sidebar">
      <div className="brand"><div className="brand-mark">书</div><div><strong>书脊</strong><span>LIBRARY DESK</span></div></div>
      <nav>{menu.filter((item) => !item.admin || user.role === "admin").map((item) => <button key={item.id} className={page === item.id ? "nav-item active" : "nav-item"} onClick={() => setPage(item.id)}>{item.label}</button>)}</nav>
      <div className="user-card"><div className="avatar">{user.username.slice(0, 1).toUpperCase()}</div><div><strong>{user.username}</strong><span>{displayRole(user.role)}</span></div><button className="exit" onClick={logout}>退出</button></div>
    </aside>
    <main>
      <header className="topbar"><div><p className="eyebrow">本地管理台</p><h1>{({ dashboard: "馆务总览", circulation: "流通服务", books: "馆藏目录", readers: "读者档案", reservations: "预约队列", fines: "罚金处理", users: "馆员账号", audit: "操作审计" } as Record<Page, string>)[page]}</h1></div><div className="date-chip">{new Intl.DateTimeFormat("zh-CN", { dateStyle: "full" }).format(new Date())}</div></header>
      {notice && <div className={`notice ${notice.type}`}><span>{notice.text}</span><button onClick={() => setNotice(null)}>×</button></div>}
      <div className="content">
        {page === "dashboard" && <DashboardPage onNotice={notify} />}
        {page === "circulation" && <CirculationPage onNotice={notify} />}
        {page === "books" && <BooksPage onNotice={notify} />}
        {page === "readers" && <ReadersPage onNotice={notify} />}
        {page === "reservations" && <ReservationsPage onNotice={notify} />}
        {page === "fines" && <FinesPage onNotice={notify} admin={user.role === "admin"} />}
        {page === "users" && user.role === "admin" && <UsersPage onNotice={notify} />}
        {page === "audit" && user.role === "admin" && <AuditPage onNotice={notify} />}
      </div>
    </main>
  </div>;
}

// Login 提交凭据并保存后端下发的令牌；后端仍是唯一的身份和权限判断方。
function Login({ onLogin, onNotice }: { onLogin: (user: User) => void; onNotice: (type: "success" | "error", text: string) => void }) {
  const [username, setUsername] = useState("admin"); const [password, setPassword] = useState(""); const [busy, setBusy] = useState(false);
  const submit = async (event: FormEvent) => { event.preventDefault(); setBusy(true); try { const result = await api.login(username, password) as { token: string; user: User }; setToken(result.token); onLogin(result.user); onNotice("success", "登录成功"); } catch (error) { onNotice("error", asErrorMessage(error)); } finally { setBusy(false); } };
  return <div className="login"><section className="login-copy"><div className="brand"><div className="brand-mark">书</div><div><strong>书脊</strong><span>LIBRARY DESK</span></div></div><h1>把每一次流通，<br />留在清楚的秩序里。</h1><p>面向馆员的本地图书馆管理系统。登录后管理馆藏、读者、借阅与预约。</p><div className="rule" /></section><form className="login-card" onSubmit={submit}><p className="eyebrow">馆务人员入口</p><h2>欢迎回来</h2><label>用户名<input value={username} onChange={(e) => setUsername(e.target.value)} autoComplete="username" required /></label><label>密码<input type="password" value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="current-password" required /></label><button className="primary full" disabled={busy}>{busy ? "正在验证..." : "进入管理台"}</button><p className="login-hint">首次使用请在后端执行 <code>tools/create-admin.ps1</code> 创建管理员。</p></form></div>;
}

// DashboardPage 并行读取首页汇总与近期借阅记录，失败时显示统一通知。
function DashboardPage({ onNotice }: { onNotice: (type: "success" | "error", text: string) => void }) {
  const [data, setData] = useState<Dashboard | null>(null); const [loans, setLoans] = useState<Loan[]>([]);
  const refresh = () => Promise.all([api.dashboard(), api.loans({ limit: 8, offset: 0 })]).then(([summary, loanData]) => { setData(summary as Dashboard); setLoans(loanData as Loan[]); }).catch((e) => onNotice("error", asErrorMessage(e)));
  useEffect(() => { void refresh(); }, []);
  const cards = data ? [["馆藏书目", data.books, "种"], ["实体副本", data.copies, "册"], ["活跃读者", data.readers, "人"], ["在借图书", data.activeLoans, "册"], ["逾期未还", data.overdueLoans, "册"]] : [];
  return <><div className="stat-grid">{cards.map(([label, value, unit]) => <article className="stat" key={String(label)}><p>{label}</p><strong>{value}</strong><span>{unit}</span></article>)}</div><Panel title="近期借阅" actions={<button className="text-button" onClick={() => void refresh()}>刷新</button>}>{loans.length ? <table><thead><tr><th>读者</th><th>图书</th><th>条码</th><th>应还时间</th><th>状态</th></tr></thead><tbody>{loans.map((loan) => <tr key={loan.id}><td>{loan.readerName}</td><td>{loan.bookTitle}</td><td><code>{loan.barcode}</code></td><td>{formatDate(loan.dueAt)}</td><td><LoanStatus value={loan.status} /></td></tr>)}</tbody></table> : <Empty>暂无借阅记录</Empty>}</Panel></>;
}

// CirculationPage 集中处理借出、归还、续借、遗失和状态修正；所有状态联动由后端事务保证。
function CirculationPage({ onNotice }: { onNotice: (type: "success" | "error", text: string) => void }) {
  const [q, setQ] = useState("");
  const debouncedQ = useDebouncedValue(q);
  const [offset, setOffset] = useState(0);
  const [readerId, setReaderId] = useState<number | null>(null);
  const [barcode, setBarcode] = useState("");
  const [busy, setBusy] = useState(false);
  const [lostLoan, setLostLoan] = useState<Loan | null>(null);
  const [statusLoan, setStatusLoan] = useState<Loan | null>(null);
  const page = usePagedRows<Loan>(() => api.loans({ q: debouncedQ, limit: PAGE_REQUEST_SIZE, offset }), [debouncedQ, offset], (message) => onNotice("error", message));
  const loadReaders = (search: string) => api.readers({ q: search, limit: 21, offset: 0 }).then((rows) => (rows as Reader[]).map((reader) => ({ id: reader.id, label: `${reader.readerNo} · ${reader.fullName}`, detail: reader.status === "active" && !reader.fineBlocked ? `剩余可借 ${reader.remainingLoanLimit} 册` : "当前不可借阅", disabled: reader.status !== "active" || reader.fineBlocked || reader.remainingLoanLimit <= 0 })));
  const borrow = async (event: FormEvent) => { event.preventDefault(); if (readerId === null) return; setBusy(true); try { await api.borrow(readerId, barcode); setReaderId(null); setBarcode(""); onNotice("success", "借阅已登记"); page.refresh(); } catch (e) { onNotice("error", asErrorMessage(e)); } finally { setBusy(false); } };
  const action = async (fn: () => Promise<unknown>, text: string) => { try { await fn(); onNotice("success", text); page.refresh(); } catch (e) { onNotice("error", asErrorMessage(e)); } };
  return <><Panel title="办理借书"><form className="inline-form" onSubmit={borrow}><ServerSearchSelect label="读者" value={readerId} placeholder="输入编号、姓名或电话" loadOptions={loadReaders} onChange={setReaderId} /><label>副本条码<input value={barcode} onChange={(e) => setBarcode(e.target.value)} placeholder="扫描或输入条码" required /></label><button className="primary" disabled={busy || readerId === null}>{busy ? "处理中" : "确认借出"}</button></form><p className="muted">系统会在提交时校验读者状态、未结罚金和当前在借数量。</p></Panel><div className="toolbar list-toolbar"><input className="search" value={q} onChange={(e) => { setQ(e.target.value); setOffset(0); }} placeholder="搜索读者、书名、条码或借阅编号" /></div><Panel title="借阅清单" actions={<button className="text-button" onClick={page.refresh}>刷新</button>}>{page.items.length ? <table><thead><tr><th>读者</th><th>图书 / 条码</th><th>借出时间</th><th>应还时间</th><th>实际归还时间</th><th>续借</th><th>状态</th><th></th></tr></thead><tbody>{page.items.map((loan) => <tr key={loan.id}><td>{loan.readerName}<small>{loan.readerNo}</small></td><td><strong>{loan.bookTitle}</strong><small><code>{loan.barcode}</code></small></td><td>{formatDate(loan.borrowedAt)}</td><td>{formatDate(loan.dueAt)}</td><td>{formatDate(loan.returnedAt)}</td><td>{loan.renewCount}</td><td><LoanStatus value={loan.status} /></td><td className="actions">{(loan.status === "active" || loan.status === "overdue") && <><button onClick={() => void action(() => api.returnLoan(loan.id), "已完成归还")}>归还</button>{loan.status === "active" && <button onClick={() => void action(() => api.renewLoan(loan.id), "续借成功")}>续借</button>}<button className="danger-button" onClick={() => setLostLoan(loan)}>遗失</button></>}<button onClick={() => setStatusLoan(loan)}>状态修正</button></td></tr>)}</tbody></table> : <Empty>{page.loading ? "正在加载借阅记录" : "暂无借阅记录"}</Empty>}<Pager offset={offset} hasMore={page.hasMore} loading={page.loading} onChange={setOffset} /></Panel>{lostLoan && <LostLoanModal loan={lostLoan} onClose={() => setLostLoan(null)} onSaved={() => { setLostLoan(null); onNotice("success", "已登记遗失赔偿并冻结读者借阅资格"); page.refresh(); }} onError={(message) => onNotice("error", message)} />}{statusLoan && <LoanStatusModal loan={statusLoan} onClose={() => setStatusLoan(null)} onSaved={() => { setStatusLoan(null); onNotice("success", "借阅状态已修正"); page.refresh(); }} onError={(message) => onNotice("error", message)} />}</>;
}

// BooksPage 管理书目、分类和实体副本；作者只能在新建书目时录入。
function BooksPage({ onNotice }: { onNotice: (type: "success" | "error", text: string) => void }) {
  const [q, setQ] = useState("");
  const [authorQ, setAuthorQ] = useState("");
  const [copyQ, setCopyQ] = useState("");
  const debouncedQ = useDebouncedValue(q);
  const debouncedAuthorQ = useDebouncedValue(authorQ);
  const debouncedCopyQ = useDebouncedValue(copyQ);
  const [bookOffset, setBookOffset] = useState(0);
  const [copyOffset, setCopyOffset] = useState(0);
  const [categoryId, setCategoryId] = useState("");
  const [materialType, setMaterialType] = useState<"" | "book" | "periodical">("");
  const [modal, setModal] = useState<"book" | "copy" | "category" | null>(null);
  const [editingCopy, setEditingCopy] = useState<BookCopy | null>(null);
  const [statusCopy, setStatusCopy] = useState<BookCopy | null>(null);
  const [categories, setCategories] = useState<{ id: number; name: string }[]>([]);
  const books = usePagedRows<Book>(() => api.books({ q: debouncedQ, author: debouncedAuthorQ, categoryId: categoryId ? Number(categoryId) : "", materialType, limit: PAGE_REQUEST_SIZE, offset: bookOffset }), [debouncedQ, debouncedAuthorQ, categoryId, materialType, bookOffset], (message) => onNotice("error", message));
  const copies = usePagedRows<BookCopy>(() => api.copies({ q: debouncedCopyQ, limit: PAGE_REQUEST_SIZE, offset: copyOffset }), [debouncedCopyQ, copyOffset], (message) => onNotice("error", message));
  const refreshCategories = () => api.categories().then((rows) => setCategories(rows as { id: number; name: string }[])).catch((error) => onNotice("error", asErrorMessage(error)));
  const refresh = () => { books.refresh(); copies.refresh(); void refreshCategories(); };
  useEffect(() => { void refreshCategories(); }, []);
  return <>
    <div className="toolbar">
      <input className="search" value={q} onChange={(e) => { setQ(e.target.value); setBookOffset(0); }} placeholder="按书名或 ISBN 搜索" />
      <input className="search" value={authorQ} onChange={(e) => { setAuthorQ(e.target.value); setBookOffset(0); }} placeholder="按作者搜索" />
      <select value={categoryId} onChange={(e) => { setCategoryId(e.target.value); setBookOffset(0); }} aria-label="按分类筛选"><option value="">全部分类</option>{categories.map((category) => <option value={category.id} key={category.id}>{category.name}</option>)}</select>
      <select value={materialType} onChange={(e) => { setMaterialType(e.target.value as "" | "book" | "periodical"); setBookOffset(0); }} aria-label="按资料类型筛选"><option value="">全部资料类型</option><option value="book">图书</option><option value="periodical">期刊</option></select>
      <div className="tool-actions"><button onClick={() => setModal("category")}>新建分类</button><button onClick={() => setModal("copy")}>录入副本</button><button className="primary catalog-create" onClick={() => setModal("book")}>提交书目草稿</button></div>
    </div>
    <Panel title={`书目（当前页 ${books.items.length}）`}>{books.items.length ? <table><thead><tr><th>书名 / ISBN</th><th>资料类型</th><th>分类</th><th>作者 / 译者</th><th>馆藏</th><th>版本与来源</th></tr></thead><tbody>{books.items.map((book) => <tr key={book.id}><td><strong>{book.title}</strong><small><code>{book.isbn}</code></small></td><td>{book.materialType === "periodical" ? "期刊" : "图书"}</td><td>{book.categoryName || "-"}</td><td>{book.authors.map((author) => author.fullName).join("、") || "-"}{book.translators.length > 0 && <small>译：{book.translators.join("、")}</small>}</td><td>{book.availableCopies} 可借 / {book.copyCount} 册</td><td>{book.publishers.join(" / ") || book.publisher || "-"}{book.publishDateRaw ? ` · ${book.publishDateRaw}` : book.publishYear ? ` · ${book.publishYear}` : ""}<small>{book.sourceProvider === "openlibrary" ? `Open Library ${book.sourceEditionKey ?? ""}` : "待核验来源"}</small></td></tr>)}</tbody></table> : <Empty>{books.loading ? "正在加载书目" : "尚未录入可用书目"}</Empty>}<Pager offset={bookOffset} hasMore={books.hasMore} loading={books.loading} onChange={setBookOffset} /></Panel>
    <div className="toolbar list-toolbar"><input className="search" value={copyQ} onChange={(event) => { setCopyQ(event.target.value); setCopyOffset(0); }} placeholder="搜索副本条码、书名或馆藏位置" /></div>
    <Panel title="实体副本" actions={<button className="text-button" onClick={copies.refresh}>刷新</button>}>{copies.items.length ? <table><thead><tr><th>条码</th><th>书名</th><th>馆藏位置</th><th>品相状态</th><th>品相备注</th><th>流通状态</th><th>操作</th></tr></thead><tbody>{copies.items.map((copy) => <tr key={copy.id}><td><code>{copy.barcode}</code></td><td>{copy.bookTitle}</td><td>{copy.location ?? "-"}</td><td><ConditionStatus value={copy.conditionStatus} /></td><td>{copy.conditionNote ?? "-"}</td><td><CopyStatus value={copy.status} /></td><td className="actions"><button onClick={() => setEditingCopy(copy)}>编辑馆藏信息</button><button onClick={() => setStatusCopy(copy)}>状态调整</button></td></tr>)}</tbody></table> : <Empty>{copies.loading ? "正在加载副本" : "尚未录入副本"}</Empty>}<Pager offset={copyOffset} hasMore={copies.hasMore} loading={copies.loading} onChange={setCopyOffset} /></Panel>
    {modal === "book" && <BookModal categories={categories} onClose={() => setModal(null)} onSaved={() => { setModal(null); onNotice("success", "书目草稿已进入隔离待核验区"); void refresh(); }} onError={(e) => onNotice("error", e)} />}
    {modal === "copy" && <CopyModal onClose={() => setModal(null)} onSaved={() => { setModal(null); onNotice("success", "副本已录入，条码由系统自动生成"); refresh(); }} onError={(e) => onNotice("error", e)} />}
    {modal === "category" && <SimpleModal title="新建分类" label="分类名称" placeholder="例如：文学" action={(value) => api.createCategory({ name: value, code: `CAT-${Date.now()}` })} onClose={() => setModal(null)} onSaved={() => { setModal(null); onNotice("success", "分类已创建"); void refresh(); }} onError={(e) => onNotice("error", e)} />}
    {editingCopy && <CopyInfoModal copy={editingCopy} onClose={() => setEditingCopy(null)} onSaved={() => { setEditingCopy(null); onNotice("success", "馆藏信息已更新"); void refresh(); }} onError={(e) => onNotice("error", e)} />}
    {statusCopy && <CopyCirculationStatusModal copy={statusCopy} onClose={() => setStatusCopy(null)} onSaved={() => { setStatusCopy(null); onNotice("success", "副本流通状态已调整"); void refresh(); }} onError={(e) => onNotice("error", e)} />}
  </>;
}

// ReadersPage 提供读者检索和建档入口，搜索词由后端执行模糊匹配。
function ReadersPage({ onNotice }: { onNotice: (type: "success" | "error", text: string) => void }) {
  const [q, setQ] = useState(""); const [offset, setOffset] = useState(0); const [open, setOpen] = useState(false);
  const debouncedQ = useDebouncedValue(q);
  const page = usePagedRows<Reader>(() => api.readers({ q: debouncedQ, limit: PAGE_REQUEST_SIZE, offset }), [debouncedQ, offset], (message) => onNotice("error", message));
  return <><div className="toolbar"><input className="search" value={q} onChange={(e) => { setQ(e.target.value); setOffset(0); }} placeholder="读者编号、姓名或电话" /><button className="primary" onClick={() => setOpen(true)}>新建读者</button></div><Panel title={`读者（当前页 ${page.items.length}）`} actions={<button className="text-button" onClick={page.refresh}>刷新</button>}>{page.items.length ? <table><thead><tr><th>编号 / 姓名</th><th>联系方式</th><th>借阅额度</th><th>未结罚金</th><th>状态</th></tr></thead><tbody>{page.items.map((reader) => <tr key={reader.id}><td><strong>{reader.fullName}</strong><small><code>{reader.readerNo}</code></small></td><td>{reader.phone ?? reader.email ?? "-"}</td><td>{reader.activeLoanCount} / {reader.loanLimit} 册<small>剩余 {reader.remainingLoanLimit} 册</small></td><td>{reader.unpaidFineCount ? <span className="danger-text">¥ {reader.unpaidFineAmount}<small>{reader.unpaidFineCount} 笔未结</small></span> : "-"}</td><td><ReaderStatus value={reader.status} />{reader.fineBlocked && <small className="danger-text">罚金冻结</small>}</td></tr>)}</tbody></table> : <Empty>{page.loading ? "正在加载读者" : "尚未建立读者档案"}</Empty>}<Pager offset={offset} hasMore={page.hasMore} loading={page.loading} onChange={setOffset} /></Panel>{open && <ReaderModal onClose={() => setOpen(false)} onSaved={() => { setOpen(false); onNotice("success", "读者已建立"); page.refresh(); }} onError={(e) => onNotice("error", e)} />}</>;
}

// ReservationsPage 展示预约队列；后端读取时会先处理已过期的待取预约。
function ReservationsPage({ onNotice }: { onNotice: (type: "success" | "error", text: string) => void }) {
  const [q, setQ] = useState(""); const [offset, setOffset] = useState(0); const [readerId, setReaderId] = useState<number | null>(null); const [bookId, setBookId] = useState<number | null>(null);
  const debouncedQ = useDebouncedValue(q);
  const page = usePagedRows<Reservation>(() => api.reservations({ q: debouncedQ, limit: PAGE_REQUEST_SIZE, offset }), [debouncedQ, offset], (message) => onNotice("error", message));
  const loadReaders = (search: string) => api.readers({ q: search, limit: 21, offset: 0 }).then((rows) => (rows as Reader[]).map((reader) => ({ id: reader.id, label: `${reader.readerNo} · ${reader.fullName}`, detail: reader.status === "active" && !reader.fineBlocked ? "可预约" : "当前不可预约", disabled: reader.status !== "active" || reader.fineBlocked })));
  const loadBooks = (search: string) => api.books({ q: search, limit: 21, offset: 0 }).then((rows) => (rows as Book[]).map((book) => ({ id: book.id, label: book.title, detail: `${book.authors.map((author) => author.fullName).join("、") || "作者未知"} · ${book.isbn}` })));
  const submit = async (event: FormEvent) => { event.preventDefault(); if (readerId === null || bookId === null) return; try { await api.createReservation(readerId, bookId); setReaderId(null); setBookId(null); onNotice("success", "预约已加入队列"); page.refresh(); } catch (e) { onNotice("error", asErrorMessage(e)); } };
  return <><Panel title="登记预约"><form className="inline-form" onSubmit={submit}><ServerSearchSelect label="读者" value={readerId} placeholder="输入编号、姓名或电话" loadOptions={loadReaders} onChange={setReaderId} /><ServerSearchSelect label="书目" value={bookId} placeholder="输入书名或 ISBN" loadOptions={loadBooks} onChange={setBookId} /><button className="primary" disabled={readerId === null || bookId === null}>加入队列</button></form></Panel><div className="toolbar list-toolbar"><input className="search" value={q} onChange={(event) => { setQ(event.target.value); setOffset(0); }} placeholder="搜索预约读者、书目或状态" /></div><Panel title="预约记录" actions={<button className="text-button" onClick={page.refresh}>刷新</button>}>{page.items.length ? <table><thead><tr><th>图书</th><th>读者</th><th>序位</th><th>状态</th><th>截止</th><th></th></tr></thead><tbody>{page.items.map((item) => <tr key={item.id}><td>{item.bookTitle}</td><td>{item.readerName}<small>{item.readerNo}</small></td><td>{item.queuePosition}</td><td><ReservationStatus value={item.status} /></td><td>{formatDate(item.expiresAt)}</td><td>{(item.status === "waiting" || item.status === "ready") && <button onClick={() => api.cancelReservation(item.id).then(() => { onNotice("success", "预约已取消"); page.refresh(); }).catch((e) => onNotice("error", asErrorMessage(e)))}>取消</button>}</td></tr>)}</tbody></table> : <Empty>{page.loading ? "正在加载预约" : "没有预约记录"}</Empty>}<Pager offset={offset} hasMore={page.hasMore} loading={page.loading} onChange={setOffset} /></Panel></>;
}

// FinesPage 允许馆员缴纳罚金，减免入口只对管理员显示。
function FinesPage({ onNotice, admin }: { onNotice: (type: "success" | "error", text: string) => void; admin: boolean }) {
  const [q, setQ] = useState(""); const [offset, setOffset] = useState(0); const [loanId, setLoanId] = useState<number | null>(null); const [amount, setAmount] = useState(""); const [reason, setReason] = useState("");
  const debouncedQ = useDebouncedValue(q);
  const page = usePagedRows<Fine>(() => api.fines({ q: debouncedQ, limit: PAGE_REQUEST_SIZE, offset }), [debouncedQ, offset], (message) => onNotice("error", message));
  const loadLoans = (search: string) => api.loans({ q: search, limit: 21, offset: 0 }).then((rows) => (rows as Loan[]).map((loan) => ({ id: loan.id, label: `#${loan.id} · ${loan.readerName}`, detail: `${loan.bookTitle} · ${loan.barcode}` })));
  const update = (id: number, type: "pay" | "waive") => type === "pay" ? api.payFine(id) : api.waiveFine(id);
  const create = async (event: FormEvent) => { event.preventDefault(); if (loanId === null) return; try { await api.createFine(loanId, Number(amount), reason); setLoanId(null); setAmount(""); setReason(""); onNotice("success", "罚金已登记"); page.refresh(); } catch (e) { onNotice("error", asErrorMessage(e)); } };
  const finishUpdate = (item: Fine, type: "pay" | "waive") => void update(item.id, type).then(() => { onNotice("success", type === "pay" ? "罚金已缴清" : "罚金已减免"); page.refresh(); }).catch((e) => onNotice("error", asErrorMessage(e)));
  return <><Panel title="登记罚金"><form className="inline-form" onSubmit={create}><ServerSearchSelect label="借阅记录" value={loanId} placeholder="输入借阅编号、读者、书名或条码" loadOptions={loadLoans} onChange={setLoanId} /><label>金额<input type="number" min="0" step="0.01" value={amount} onChange={(e) => setAmount(e.target.value)} required /></label><label>原因<input value={reason} onChange={(e) => setReason(e.target.value)} placeholder="例如：逾期 3 天" required /></label><button className="primary" disabled={loanId === null}>登记罚金</button></form></Panel><div className="toolbar list-toolbar"><input className="search" value={q} onChange={(event) => { setQ(event.target.value); setOffset(0); }} placeholder="搜索读者、书目、条码或罚金原因" /></div><Panel title="罚金记录" actions={<button className="text-button" onClick={page.refresh}>刷新</button>}>{page.items.length ? <table><thead><tr><th>读者</th><th>借阅 / 图书</th><th>金额</th><th>原因</th><th>状态</th><th></th></tr></thead><tbody>{page.items.map((item) => <tr key={item.id}><td>{item.readerName}<small>{item.readerNo}</small></td><td><code>#{item.loanId}</code><small>{item.bookTitle} · {item.barcode}</small></td><td>¥ {item.amount}</td><td>{item.reason}</td><td><FineStatus value={item.status} /></td><td>{item.status === "unpaid" && <span className="actions"><button onClick={() => finishUpdate(item, "pay")}>缴清</button>{admin && <button onClick={() => finishUpdate(item, "waive")}>减免</button>}</span>}</td></tr>)}</tbody></table> : <Empty>{page.loading ? "正在加载罚金" : "暂无罚金记录"}</Empty>}<Pager offset={offset} hasMore={page.hasMore} loading={page.loading} onChange={setOffset} /></Panel></>;
}

// UsersPage 是管理员账号管理页，包含角色修改、启停和强制重置密码入口。
function UsersPage({ onNotice }: { onNotice: (type: "success" | "error", text: string) => void }) {
  const [users, setUsers] = useState<User[]>([]); const [modal, setModal] = useState<"create" | number | null>(null); const refresh = () => api.users().then((data) => setUsers(data as User[])).catch((e) => onNotice("error", asErrorMessage(e))); useEffect(() => { void refresh(); }, []);
  return <><Panel title="系统账号" actions={<button className="primary" onClick={() => setModal("create")}>创建账号</button>}>{users.length ? <table><thead><tr><th>用户名</th><th>角色</th><th>状态</th><th>创建时间</th><th></th></tr></thead><tbody>{users.map((account) => <tr key={account.id}><td><strong>{account.username}</strong><small>ID {account.id}</small></td><td>{displayRole(account.role)}</td><td><Badge tone={account.isActive ? "good" : "bad"}>{account.isActive ? "启用" : "停用"}</Badge></td><td>{formatDate(account.createdAt)}</td><td><button onClick={() => setModal(account.id)}>管理</button></td></tr>)}</tbody></table> : <Empty />}</Panel>{modal === "create" && <UserModal onClose={() => setModal(null)} onSaved={() => { setModal(null); onNotice("success", "账号已创建"); void refresh(); }} onError={(e) => onNotice("error", e)} />}{typeof modal === "number" && <UserModal account={users.find((x) => x.id === modal)} onClose={() => setModal(null)} onSaved={() => { setModal(null); onNotice("success", "账号已更新"); void refresh(); }} onError={(e) => onNotice("error", e)} />}</>;
}

// AuditPage 分页读取操作历史，不在浏览器中缓存万级敏感记录。
function AuditPage({ onNotice }: { onNotice: (type: "success" | "error", text: string) => void }) { const [q, setQ] = useState(""); const [offset, setOffset] = useState(0); const debouncedQ = useDebouncedValue(q); const page = usePagedRows<Record<string, any>>(() => api.auditLogs({ q: debouncedQ, limit: PAGE_REQUEST_SIZE, offset }), [debouncedQ, offset], (message) => onNotice("error", message)); return <><div className="toolbar"><input className="search" value={q} onChange={(event) => { setQ(event.target.value); setOffset(0); }} placeholder="搜索操作者、操作、对象或详情" /></div><Panel title="操作记录" actions={<button className="text-button" onClick={page.refresh}>刷新</button>}>{page.items.length ? <table><thead><tr><th>时间</th><th>操作者</th><th>操作</th><th>对象</th><th>详情</th></tr></thead><tbody>{page.items.map((item) => <tr key={item.id}><td>{formatDate(item.createdAt)}</td><td>{item.actorName ?? "系统"}</td><td><code>{item.action}</code></td><td>{item.entityType} #{item.entityId ?? "-"}</td><td><code>{item.details ? JSON.stringify(item.details) : "-"}</code></td></tr>)}</tbody></table> : <Empty>{page.loading ? "正在加载审计记录" : "暂时没有审计记录"}</Empty>}<Pager offset={offset} hasMore={page.hasMore} loading={page.loading} onChange={setOffset} /></Panel></>; }

// SimpleModal 复用单字段新增表单，当前用于分类和作者等简单主数据。

function SimpleModal({ title, label, placeholder, action, onClose, onSaved, onError }: { title: string; label: string; placeholder: string; action: (value: string) => Promise<unknown>; onClose: () => void; onSaved: () => void; onError: (message: string) => void }) { const [value,setValue]=useState(""); const submit=async(e:FormEvent)=>{e.preventDefault();try{await action(value);onSaved()}catch(err){onError(asErrorMessage(err))}};return <Modal title={title} onClose={onClose}><form className="form-grid" onSubmit={submit}><label>{label}<input value={value} onChange={e=>setValue(e.target.value)} placeholder={placeholder} required /></label><div className="modal-actions"><button type="button" onClick={onClose}>取消</button><button className="primary">保存</button></div></form></Modal> }
// BookModal 只提交待核验草稿；通过来源核验前不能建立实体副本或参与流通。
function BookModal({ categories, onClose, onSaved, onError }: { categories: { id:number;name:string }[];onClose:()=>void;onSaved:()=>void;onError:(s:string)=>void }) {
  const [isbn, setISBN] = useState(""); const [title, setTitle] = useState(""); const [categoryId, setCategoryId] = useState(""); const [materialType, setMaterialType] = useState<"book" | "periodical">("book"); const [publisher, setPublisher] = useState(""); const [publishYear, setPublishYear] = useState(""); const [authorNames, setAuthorNames] = useState("");
  const submit = async (event: FormEvent) => { event.preventDefault(); const names = authorNames.split(/[、，,;；]+/).map((name) => name.trim()).filter(Boolean); try { await api.createBook({ isbn, title, categoryId: Number(categoryId), materialType, publisher: publisher || undefined, publishYear: publishYear ? Number(publishYear) : undefined, authorNames: names }); onSaved(); } catch (error) { onError(asErrorMessage(error)); } };
  return <Modal title="提交书目草稿" onClose={onClose}><form className="form-grid" onSubmit={submit}><label>ISBN<input value={isbn} onChange={(event) => setISBN(event.target.value)} required /></label><label>书名<input value={title} onChange={(event) => setTitle(event.target.value)} required /></label><label>资料类型<select value={materialType} onChange={(event) => setMaterialType(event.target.value as "book" | "periodical")} required><option value="book">图书（BK）</option><option value="periodical">期刊（MC）</option></select></label><label>分类<select value={categoryId} onChange={(event) => setCategoryId(event.target.value)} required><option value="">请选择分类</option>{categories.map((category) => <option value={category.id} key={category.id}>{category.name}</option>)}</select></label><label>作者姓名<input value={authorNames} onChange={(event) => setAuthorNames(event.target.value)} placeholder="必填；多位作者用顿号或逗号分隔" required /></label><label>出版社<input value={publisher} onChange={(event) => setPublisher(event.target.value)} /></label><label>出版年份<input inputMode="numeric" value={publishYear} onChange={(event) => setPublishYear(event.target.value)} /></label><div className="modal-actions"><button type="button" onClick={onClose}>取消</button><button className="primary">提交待核验</button></div></form></Modal>;
}
// CopyModal 独立搜索全部书目，避免只能从馆藏列表当前页选择。
function CopyModal({onClose,onSaved,onError}:{onClose:()=>void;onSaved:()=>void;onError:(s:string)=>void}) {
  const [bookId, setBookId] = useState<number | null>(null); const [materialType, setMaterialType] = useState<Book["materialType"] | null>(null); const [location, setLocation] = useState(""); const [conditionStatus, setConditionStatus] = useState<BookCopy["conditionStatus"]>("intact"); const [conditionNote, setConditionNote] = useState(""); const [acquiredAt, setAcquiredAt] = useState(new Date().toISOString().slice(0, 10));
  const loadBooks = (search: string) => api.books({ q: search, limit: 21, offset: 0 }).then((rows) => (rows as Book[]).map((book) => ({ id: book.id, label: book.title, detail: `${book.materialType === "periodical" ? "期刊" : "图书"} · ${book.isbn}`, meta: book.materialType })));
  const submit = async (event: FormEvent) => { event.preventDefault(); if (bookId === null) return; try { await api.createCopy({ bookId, location: location || undefined, conditionStatus, conditionNote: conditionNote || undefined, acquiredAt }); onSaved(); } catch (error) { onError(asErrorMessage(error)); } };
  return <Modal title="录入实体副本" onClose={onClose}><form className="form-grid" onSubmit={submit}><ServerSearchSelect label="书目" value={bookId} placeholder="输入书名或 ISBN" loadOptions={loadBooks} onChange={(id, option) => { setBookId(id); setMaterialType((option?.meta as Book["materialType"] | undefined) ?? null); }} /><label>入库日期<input type="date" value={acquiredAt} onChange={(event) => setAcquiredAt(event.target.value)} required /></label><label>馆藏位置<input value={location} onChange={(event) => setLocation(event.target.value)} placeholder="例如 A-03-02" /></label><label>品相状态<select value={conditionStatus} onChange={(event) => setConditionStatus(event.target.value as BookCopy["conditionStatus"])} required><option value="intact">完好</option><option value="minor_wear">轻微磨损</option><option value="damaged">破损</option><option value="pending_inspection">待检查</option></select></label><label>品相备注<input value={conditionNote} onChange={(event) => setConditionNote(event.target.value)} placeholder="例如：封面右下角轻微磨损" /></label>{(conditionStatus === "damaged" || conditionStatus === "pending_inspection") && <p className="muted">破损或待检查的副本录入后自动进入维护，不能直接借阅或预约。</p>}<p className="muted">条码将自动生成：{materialType === "periodical" ? "MC" : "BK"}-{acquiredAt.slice(0, 4) || "年份"}-{bookId ?? "书目ID"}-0001（末四位为该书目内递增序号）。</p><div className="modal-actions"><button type="button" onClick={onClose}>取消</button><button className="primary" disabled={bookId === null}>录入副本</button></div></form></Modal>;
}

// CopyInfoModal 编辑不改变借阅责任的馆藏字段；品相不合格时服务端会同步移出可借状态。
function CopyInfoModal({copy,onClose,onSaved,onError}:{copy:BookCopy;onClose:()=>void;onSaved:()=>void;onError:(s:string)=>void}){const[location,setLocation]=useState(copy.location??"");const[conditionStatus,setConditionStatus]=useState<BookCopy["conditionStatus"]>(copy.conditionStatus);const[conditionNote,setConditionNote]=useState(copy.conditionNote??"");const submit=async(e:FormEvent)=>{e.preventDefault();try{await api.updateCopy(copy.id,{location,conditionStatus,conditionNote});onSaved()}catch(err){onError(asErrorMessage(err))}};return <Modal title={`编辑馆藏信息：${copy.bookTitle}`} onClose={onClose}><form className="form-grid" onSubmit={submit}><p className="muted">条码：{copy.barcode}</p><label>馆藏位置<input value={location} onChange={e=>setLocation(e.target.value)} placeholder="例如 A-03-02" /></label><label>品相状态<select value={conditionStatus} onChange={e=>setConditionStatus(e.target.value as BookCopy["conditionStatus"])} required><option value="intact">完好</option><option value="minor_wear">轻微磨损</option><option value="damaged">破损</option><option value="pending_inspection">待检查</option></select></label><label>品相备注<input value={conditionNote} onChange={e=>setConditionNote(e.target.value)} placeholder="例如：封面右下角轻微磨损" /></label>{(conditionStatus === "damaged" || conditionStatus === "pending_inspection") && <p className="muted">可借副本会自动转为维护；待取副本会释放给预约队列重新分配；借出副本会在归还后进入维护。</p>}<div className="modal-actions"><button type="button" onClick={onClose}>取消</button><button className="primary">保存馆藏信息</button></div></form></Modal>}

// CopyCirculationStatusModal 只暴露人工盘点可执行的流通状态转换，读者责任仍从借阅记录处理。
function CopyCirculationStatusModal({copy,onClose,onSaved,onError}:{copy:BookCopy;onClose:()=>void;onSaved:()=>void;onError:(s:string)=>void}){const[status,setStatus]=useState<"available"|"maintenance"|"lost">(copy.status === "maintenance" ? "available" : copy.status === "available" ? "maintenance" : copy.status === "lost" ? "maintenance" : "lost");const[reason,setReason]=useState("");const canChange=copy.status === "available" || copy.status === "maintenance" || copy.status === "reserved" || copy.status === "lost";const submit=async(e:FormEvent)=>{e.preventDefault();if(!window.confirm(`确认将副本 ${copy.barcode} 调整为“${({available:"可借",maintenance:"维护",lost:"遗失"} as Record<string,string>)[status]}”吗？`))return;try{await api.changeCopyCirculationStatus(copy.id,status,reason);onSaved()}catch(err){onError(asErrorMessage(err))}};if(!canChange)return <Modal title={`状态调整：${copy.bookTitle}`} onClose={onClose}><div className="form-grid"><p className="muted">该副本正在借出。归还、续借和读者借阅中遗失必须从“流通服务”的借阅记录操作，以保持借阅、罚金和读者冻结状态一致。</p><div className="modal-actions"><button type="button" onClick={onClose}>关闭</button></div></div></Modal>;return <Modal title={`状态调整：${copy.bookTitle}`} onClose={onClose}><form className="form-grid" onSubmit={submit}><p className="muted">当前流通状态：{({available:"可借",borrowed:"借出",reserved:"待取",lost:"遗失",maintenance:"维护"} as Record<string,string>)[copy.status]}</p><label>目标流通状态<select value={status} onChange={e=>setStatus(e.target.value as "available"|"maintenance"|"lost")}><option value="maintenance" disabled={copy.status !== "available" && copy.status !== "lost"}>维护</option><option value="available" disabled={copy.status !== "maintenance"}>可借</option><option value="lost" disabled={copy.status === "lost"}>馆藏盘点遗失</option></select></label><label>调整原因<input value={reason} onChange={e=>setReason(e.target.value)} placeholder="必填，例如：盘点未找到" required /></label>{copy.status === "reserved" && status === "lost" && <p className="muted">登记后会解除当前待取分配，并将同书目的下一册可借副本分配给该读者；没有可借副本时，预约回到排队中。</p>}{copy.status === "lost" && status === "maintenance" && <p className="muted">找回副本会先进入维护，请完成品相复核后再恢复可借。</p>}{status === "lost" && <p className="muted">这是馆藏盘点遗失，不会创建读者罚金或冻结读者。</p>}<div className="modal-actions"><button type="button" onClick={onClose}>取消</button><button className="primary">确认调整</button></div></form></Modal>}
// ReaderModal 建立借阅档案；电话和邮箱为空时不向接口传递空字符串。
function ReaderModal({onClose,onSaved,onError}:{onClose:()=>void;onSaved:()=>void;onError:(s:string)=>void}){const[readerNo,setReaderNo]=useState("");const[fullName,setFullName]=useState("");const[phone,setPhone]=useState("");const[email,setEmail]=useState("");const[loanLimit,setLoanLimit]=useState("5");const submit=async(e:FormEvent)=>{e.preventDefault();try{await api.createReader({readerNo,fullName,phone:phone||undefined,email:email||undefined,loanLimit:Number(loanLimit)});onSaved()}catch(err){onError(asErrorMessage(err))}};return <Modal title="新建读者" onClose={onClose}><form className="form-grid" onSubmit={submit}><label>读者编号<input value={readerNo} onChange={e=>setReaderNo(e.target.value)} required /></label><label>姓名<input value={fullName} onChange={e=>setFullName(e.target.value)} required /></label><label>手机<input value={phone} onChange={e=>setPhone(e.target.value)} /></label><label>邮箱<input type="email" value={email} onChange={e=>setEmail(e.target.value)} /></label><label>借阅额度<input inputMode="numeric" value={loanLimit} onChange={e=>setLoanLimit(e.target.value)} required /></label><div className="modal-actions"><button type="button" onClick={onClose}>取消</button><button className="primary">建立档案</button></div></form></Modal>}
// LostLoanModal 登记遗失赔偿金额，默认 30 元但允许馆员按馆规调整。
function LostLoanModal({loan,onClose,onSaved,onError}:{loan:Loan;onClose:()=>void;onSaved:()=>void;onError:(s:string)=>void}){const[amount,setAmount]=useState("30");const[reason,setReason]=useState("图书遗失赔偿");const submit=async(e:FormEvent)=>{e.preventDefault();try{await api.markLoanLost(loan.id,Number(amount),reason);onSaved()}catch(err){onError(asErrorMessage(err))}};return <Modal title={`登记遗失：${loan.bookTitle}`} onClose={onClose}><form className="form-grid" onSubmit={submit}><p className="muted">读者：{loan.readerName}（{loan.readerNo}）</p><label>赔偿金额<input type="number" min="0.01" step="0.01" value={amount} onChange={e=>setAmount(e.target.value)} required /></label><label>原因<input value={reason} onChange={e=>setReason(e.target.value)} required /></label><p className="muted">提交后副本会标记为遗失，读者档案将冻结，直到所有未结罚金处理完成。</p><div className="modal-actions"><button type="button" onClick={onClose}>取消</button><button className="primary">确认遗失</button></div></form></Modal>}

// LoanStatusModal 用二次确认保护状态修正；修正遗失记录时可补充新的赔偿金额。
function LoanStatusModal({loan,onClose,onSaved,onError}:{loan:Loan;onClose:()=>void;onSaved:()=>void;onError:(s:string)=>void}){const[status,setStatus]=useState<Loan["status"]>(loan.status);const[amount,setAmount]=useState("30");const[reason,setReason]=useState("图书遗失赔偿");const submit=async(e:FormEvent)=>{e.preventDefault();if(!window.confirm(`确认将借阅 #${loan.id} 状态改为“${({active:"在借",overdue:"逾期",returned:"已还",lost:"遗失"}as Record<string,string>)[status]}”吗？`))return;try{await api.updateLoanStatus(loan.id,{status,amount:status==="lost"?Number(amount):undefined,reason:status==="lost"?reason:undefined});onSaved()}catch(err){onError(asErrorMessage(err))}};return <Modal title={`修正借阅状态：${loan.bookTitle}`} onClose={onClose}><form className="form-grid" onSubmit={submit}><p className="muted">读者：{loan.readerName}（{loan.readerNo}）</p><label>目标状态<select value={status} onChange={e=>setStatus(e.target.value as Loan["status"])}><option value="active">在借</option><option value="overdue">逾期</option><option value="returned">已还</option><option value="lost">遗失</option></select></label>{status==="lost"&&<><label>赔偿金额<input type="number" min="0.01" step="0.01" value={amount} onChange={e=>setAmount(e.target.value)} required /></label><label>原因<input value={reason} onChange={e=>setReason(e.target.value)} required /></label></>}<p className="muted">修正会同步副本状态和归还时间；误登记的系统遗失罚金会自动减免，已缴罚金保留历史。</p><div className="modal-actions"><button type="button" onClick={onClose}>取消</button><button className="primary">保存修正</button></div></form></Modal>}
// UserModal 创建或更新管理员/馆员账号；读者仅保留借阅档案，不创建登录账号。
function UserModal({account,onClose,onSaved,onError}:{account?:User;onClose:()=>void;onSaved:()=>void;onError:(s:string)=>void}){const[username,setUsername]=useState(account?.username??"");const[role,setRole]=useState<Role>(account?.role??"librarian");const[password,setPassword]=useState("");const[active,setActive]=useState(account?.isActive??true);const submit=async(e:FormEvent)=>{e.preventDefault();try{if(account){await api.updateUser(account.id,{username,role,isActive:active});if(password){await api.resetPassword(account.id,password)}}else{await api.createUser({username,password,role,isActive:active})}onSaved()}catch(err){onError(asErrorMessage(err))}};return <Modal title={account?"管理账号":"创建账号"} onClose={onClose}><form className="form-grid" onSubmit={submit}><label>用户名<input value={username} onChange={e=>setUsername(e.target.value)} required /></label><label>角色<select value={role} onChange={e=>setRole(e.target.value as Role)}><option value="admin">管理员</option><option value="librarian">馆员</option></select></label><label>{account?"强制重置密码（可选）":"初始密码"}<input type="password" value={password} onChange={e=>setPassword(e.target.value)} required={!account} minLength={10} /></label><label className="check"><input type="checkbox" checked={active} onChange={e=>setActive(e.target.checked)} />账号启用</label><div className="modal-actions"><button type="button" onClick={onClose}>取消</button><button className="primary">保存</button></div></form></Modal>}

function LoanStatus({value}:{value:Loan["status"]}){return <Badge tone={value==="overdue"?"bad":value==="active"?"warn":"good"}>{({active:"在借",returned:"已还",overdue:"逾期",lost:"遗失"}as Record<string,string>)[value]}</Badge>}
function CopyStatus({value}:{value:BookCopy["status"]}){return <Badge tone={value==="available"?"good":value==="lost"?"bad":value==="borrowed"?"warn":"neutral"}>{({available:"可借",borrowed:"借出",reserved:"待取",lost:"遗失",maintenance:"维护"}as Record<string,string>)[value]}</Badge>}
function ConditionStatus({value}:{value:BookCopy["conditionStatus"]}){return <Badge tone={value==="intact"?"good":value==="minor_wear"?"warn":value==="damaged"?"bad":"neutral"}>{({intact:"完好",minor_wear:"轻微磨损",damaged:"破损",pending_inspection:"待检查"}as Record<string,string>)[value]}</Badge>}
function ReaderStatus({value}:{value:Reader["status"]}){return <Badge tone={value==="active"?"good":value==="suspended"?"bad":"warn"}>{({active:"正常",suspended:"暂停",expired:"过期"}as Record<string,string>)[value]}</Badge>}
function ReservationStatus({value}:{value:Reservation["status"]}){return <Badge tone={value==="ready"?"warn":value==="waiting"?"neutral":value==="fulfilled"?"good":"bad"}>{({waiting:"排队中",ready:"待取",fulfilled:"已完成",cancelled:"已取消",expired:"已过期"}as Record<string,string>)[value]}</Badge>}
function FineStatus({value}:{value:Fine["status"]}){return <Badge tone={value==="unpaid"?"bad":"good"}>{({unpaid:"未缴",paid:"已缴",waived:"已减免"}as Record<string,string>)[value]}</Badge>}
