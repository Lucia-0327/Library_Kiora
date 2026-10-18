export const PAGE_SIZE = 50;
export const PAGE_REQUEST_SIZE = PAGE_SIZE + 1;

// pageWindow 从多取的一条记录判断是否存在下一页，并只返回当前页数据。
export function pageWindow<T>(rows: T[], pageSize = PAGE_SIZE): { items: T[]; hasMore: boolean } {
  return { items: rows.slice(0, pageSize), hasMore: rows.length > pageSize };
}

// previousOffset 保证向前翻页时偏移量不会小于零。
export function previousOffset(offset: number, pageSize = PAGE_SIZE): number {
  return Math.max(0, offset - pageSize);
}

// nextOffset 仅在服务端返回额外一条记录时推进页码。
export function nextOffset(offset: number, hasMore: boolean, pageSize = PAGE_SIZE): number {
  return hasMore ? offset + pageSize : offset;
}
