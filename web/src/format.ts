// formatDate 安全格式化服务端 ISO 时间；缺失或无效值统一显示占位符。
export function formatDate(value?: string): string {
  if (!value) return "-";
  const date = new Date(value);
  if (Number.isNaN(date.valueOf())) return "-";
  return new Intl.DateTimeFormat("zh-CN", { dateStyle: "medium", timeStyle: "short" }).format(date);
}

// displayRole 将服务端角色代码映射为中文，未知角色保留原值方便排查。
export function displayRole(role: string): string {
  return ({ admin: "管理员", librarian: "馆员" } as Record<string, string>)[role] ?? role;
}
