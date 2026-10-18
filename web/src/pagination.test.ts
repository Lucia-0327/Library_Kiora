import { describe, expect, it } from "vitest";
import { nextOffset, pageWindow, previousOffset } from "./pagination";

describe("pageWindow", () => {
  it("用第 51 条记录判断下一页并只展示 50 条", () => {
    const rows = Array.from({ length: 51 }, (_, index) => index + 1);
    expect(pageWindow(rows)).toEqual({ items: rows.slice(0, 50), hasMore: true });
    expect(pageWindow(rows.slice(0, 50)).hasMore).toBe(false);
  });
});

describe("分页偏移", () => {
  it("上一页不产生负数，下一页只在有更多数据时前进", () => {
    expect(previousOffset(0)).toBe(0);
    expect(previousOffset(100)).toBe(50);
    expect(nextOffset(50, true)).toBe(100);
    expect(nextOffset(50, false)).toBe(50);
  });
});
