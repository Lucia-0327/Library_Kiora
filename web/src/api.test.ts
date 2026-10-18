import { describe, expect, it } from "vitest";
import { listPath } from "./api";

describe("listPath", () => {
  it("编码搜索和分页参数并忽略空筛选", () => {
    expect(listPath("/api/v1/books", { q: " 百年孤独 ", limit: 51, offset: 100, author: "", categoryId: 7 }))
      .toBe("/api/v1/books?q=%E7%99%BE%E5%B9%B4%E5%AD%A4%E7%8B%AC&limit=51&offset=100&categoryId=7");
  });
});
