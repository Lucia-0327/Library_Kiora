// 前端仅把当前会话令牌保存在 sessionStorage，关闭浏览器标签后自动清除。
const tokenKey = "library-management-token";

// APIError 将服务端统一错误响应转换为可供页面展示和分支处理的异常。
export class APIError extends Error {
  constructor(public readonly status: number, public readonly code: string, message: string) {
    super(message);
  }
}

// getToken 读取当前标签页的 Bearer 令牌。
export function getToken(): string | null {
  return sessionStorage.getItem(tokenKey);
}

// setToken 在登录成功后保存令牌，登出或身份失效时删除令牌。
export function setToken(token: string | null): void {
  if (token) sessionStorage.setItem(tokenKey, token);
  else sessionStorage.removeItem(tokenKey);
}

// request 是全部接口调用的统一入口：补充 JSON、Bearer 令牌和 Cookie，并规范化错误。
// credentials: include 使 HttpOnly 会话 Cookie 可与前端保存的 Bearer 令牌同时工作。
export async function request<T>(path: string, options: RequestInit = {}): Promise<T> {
  const headers = new Headers(options.headers);
  headers.set("Accept", "application/json");
  if (options.body) headers.set("Content-Type", "application/json");
  const token = getToken();
  if (token) headers.set("Authorization", `Bearer ${token}`);

  const response = await fetch(path, { ...options, headers, credentials: "include" });
  const payload = await response.json().catch(() => ({}));
  if (!response.ok) {
    const error = payload.error ?? {};
    throw new APIError(response.status, error.code ?? "request_failed", error.message ?? "请求失败");
  }
  return payload.data as T;
}

// api 按业务领域封装后端路由；页面不直接拼接 fetch 请求。
export type ListQuery = { q?: string; limit?: number; offset?: number };
export type BookListQuery = ListQuery & { author?: string; categoryId?: number | ""; materialType?: "book" | "periodical" | "" };

// listPath 统一处理列表查询参数，避免搜索词或分页参数遗漏编码。
export function listPath(path: string, params: ListQuery & Record<string, unknown> = {}): string {
  const query = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (typeof value === "string" && value.trim()) query.set(key, value.trim());
    else if (typeof value === "number" && Number.isFinite(value)) query.set(key, String(value));
  }
  const suffix = query.toString();
  return `${path}${suffix ? `?${suffix}` : ""}`;
}

export const api = {
  login: (username: string, password: string) => request<{ token: string; user: unknown }>("/api/v1/auth/login", { method: "POST", body: JSON.stringify({ username, password }) }),
  logout: () => request<boolean>("/api/v1/auth/logout", { method: "POST" }),
  me: () => request<unknown>("/api/v1/auth/me"),
  dashboard: () => request<unknown>("/api/v1/dashboard"),
  categories: () => request<unknown[]>("/api/v1/categories"),
  createCategory: (data: unknown) => request<unknown>("/api/v1/categories", { method: "POST", body: JSON.stringify(data) }),
  authors: () => request<unknown[]>("/api/v1/authors"),
  users: () => request<unknown[]>("/api/v1/users"),
  createUser: (data: unknown) => request<unknown>("/api/v1/users", { method: "POST", body: JSON.stringify(data) }),
  updateUser: (id: number, data: unknown) => request<boolean>(`/api/v1/users/${id}`, { method: "PATCH", body: JSON.stringify(data) }),
  resetPassword: (id: number, newPassword: string) => request<boolean>(`/api/v1/users/${id}/force-reset-password`, { method: "POST", body: JSON.stringify({ newPassword }) }),
  books: (params: string | BookListQuery = {}) => {
    const options = typeof params === "string" ? { q: params } : params;
    return request<unknown[]>(listPath("/api/v1/books", options));
  },
  createBook: (data: unknown) => request<unknown>("/api/v1/books", { method: "POST", body: JSON.stringify(data) }),
  copies: (params: ListQuery = {}) => request<unknown[]>(listPath("/api/v1/book-copies", params)),
  createCopy: (data: unknown) => request<unknown>("/api/v1/book-copies", { method: "POST", body: JSON.stringify(data) }),
  updateCopy: (id: number, data: { location?: string; conditionStatus?: string; conditionNote?: string }) => request<boolean>(`/api/v1/book-copies/${id}`, { method: "PATCH", body: JSON.stringify(data) }),
  changeCopyCirculationStatus: (id: number, status: "available" | "maintenance" | "lost", reason: string) => request<boolean>(`/api/v1/book-copies/${id}/circulation-status`, { method: "POST", body: JSON.stringify({ status, reason }) }),
  readers: (params: string | ListQuery = {}) => request<unknown[]>(listPath("/api/v1/readers", typeof params === "string" ? { q: params } : params)),
  createReader: (data: unknown) => request<unknown>("/api/v1/readers", { method: "POST", body: JSON.stringify(data) }),
  loans: (params: ListQuery = {}) => request<unknown[]>(listPath("/api/v1/loans", params)),
  borrow: (readerId: number, barcode: string) => request<unknown>("/api/v1/loans", { method: "POST", body: JSON.stringify({ readerId, barcode }) }),
  returnLoan: (id: number) => request<boolean>(`/api/v1/loans/${id}/return`, { method: "POST" }),
  renewLoan: (id: number) => request<unknown>(`/api/v1/loans/${id}/renew`, { method: "POST" }),
  markLoanLost: (id: number, amount: number, reason: string) => request<boolean>(`/api/v1/loans/${id}/lost`, { method: "POST", body: JSON.stringify({ amount, reason }) }),
  updateLoanStatus: (id: number, data: { status: string; amount?: number; reason?: string }) => request<boolean>(`/api/v1/loans/${id}/status`, { method: "PATCH", body: JSON.stringify(data) }),
  reservations: (params: ListQuery = {}) => request<unknown[]>(listPath("/api/v1/reservations", params)),
  createReservation: (readerId: number, bookId: number) => request<unknown>("/api/v1/reservations", { method: "POST", body: JSON.stringify({ readerId, bookId }) }),
  cancelReservation: (id: number) => request<boolean>(`/api/v1/reservations/${id}/cancel`, { method: "POST" }),
  fines: (params: ListQuery = {}) => request<unknown[]>(listPath("/api/v1/fines", params)),
  createFine: (loanId: number, amount: number, reason: string) => request<unknown>("/api/v1/fines", { method: "POST", body: JSON.stringify({ loanId, amount, reason }) }),
  payFine: (id: number) => request<boolean>(`/api/v1/fines/${id}/pay`, { method: "POST" }),
  waiveFine: (id: number) => request<boolean>(`/api/v1/fines/${id}/waive`, { method: "POST" }),
  auditLogs: (params: ListQuery = {}) => request<unknown[]>(listPath("/api/v1/audit-logs", params))
};
