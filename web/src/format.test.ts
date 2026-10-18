import { describe, expect, it } from "vitest";
import { displayRole, formatDate } from "./format";

// 角色显示和日期格式化是纯函数，适合用快速单元测试覆盖边界输入。
describe("displayRole", () => {
  it("translates system roles", () => {
    expect(displayRole("admin")).toBe("管理员");
    expect(displayRole("unknown")).toBe("unknown");
  });
});

describe("formatDate", () => {
  it("handles absent or invalid values", () => {
    expect(formatDate()).toBe("-");
    expect(formatDate("not-a-date")).toBe("-");
  });
});
