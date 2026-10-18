package main

import (
	"math/rand"
	"strings"
	"testing"
	"time"
)

func TestBuildDefaultDataset(t *testing.T) {
	opts := options{
		mode:              "plan",
		readerCount:       6000,
		copyCount:         24000,
		returnedLoanCount: 36000,
		reservationCount:  8000,
		randomSeed:        20260904,
	}
	books := testBooks(1000)
	anchor := time.Date(2026, time.September, 4, 0, 0, 0, 0, time.Local)

	data, err := buildDataset(opts, books, anchor)
	if err != nil {
		t.Fatalf("生成默认规模数据失败：%v", err)
	}
	if err := validateDataset(data); err != nil {
		t.Fatalf("默认规模数据未通过一致性校验：%v", err)
	}

	if got := len(data.readers); got != 6000 {
		t.Fatalf("读者数量错误：得到 %d，期望 6000", got)
	}
	if got := len(data.copies); got != 24000 {
		t.Fatalf("副本数量错误：得到 %d，期望 24000", got)
	}
	if got := countLoans(data.loans, "returned"); got != 36000 {
		t.Fatalf("已归还借阅数量错误：得到 %d，期望 36000", got)
	}
	if got := countLoans(data.loans, "active"); got != 4200 {
		t.Fatalf("在借数量错误：得到 %d，期望 4200", got)
	}
	if got := countLoans(data.loans, "overdue"); got != 600 {
		t.Fatalf("逾期数量错误：得到 %d，期望 600", got)
	}
	if got := countLoans(data.loans, "lost"); got != 240 {
		t.Fatalf("遗失数量错误：得到 %d，期望 240", got)
	}
	if got := len(data.reservations); got != 8000 {
		t.Fatalf("预约数量错误：得到 %d，期望 8000", got)
	}
	if got := len(data.fines); got != 3240 {
		t.Fatalf("罚金数量错误：得到 %d，期望 3240", got)
	}

	reservationStatuses := map[string]int{}
	for _, reservation := range data.reservations {
		reservationStatuses[reservation.status]++
	}
	for _, status := range []string{"waiting", "ready", "fulfilled", "cancelled", "expired"} {
		if reservationStatuses[status] == 0 {
			t.Fatalf("预约状态 %s 没有样本", status)
		}
	}
	fineStatuses := map[string]int{}
	for _, fine := range data.fines {
		fineStatuses[fine.status]++
	}
	for _, status := range []string{"unpaid", "paid", "waived"} {
		if fineStatuses[status] == 0 {
			t.Fatalf("罚金状态 %s 没有样本", status)
		}
	}
	currentLoans := make([]int, len(data.readers))
	for _, loan := range data.loans {
		if loan.status == "active" || loan.status == "overdue" {
			currentLoans[loan.readerIndex]++
		}
	}
	for readerIndex, count := range currentLoans {
		if count > data.readers[readerIndex].loanLimit {
			t.Fatalf("读者 %s 当前借阅 %d 册，超过上限 %d", data.readers[readerIndex].readerNo, count, data.readers[readerIndex].loanLimit)
		}
	}
}

func TestValidationRejectsReadyReservationWithoutCopy(t *testing.T) {
	opts := options{
		mode:              "plan",
		readerCount:       100,
		copyCount:         500,
		returnedLoanCount: 600,
		reservationCount:  200,
		randomSeed:        7,
	}
	data, err := buildDataset(opts, testBooks(100), time.Date(2026, time.September, 4, 0, 0, 0, 0, time.Local))
	if err != nil {
		t.Fatalf("生成小规模测试数据失败：%v", err)
	}
	for i := range data.reservations {
		if data.reservations[i].status == "ready" {
			data.reservations[i].reservedCopyIndex = -1
			break
		}
	}
	if err := validateDataset(data); err == nil {
		t.Fatal("缺少预留副本的待取预约未被一致性校验拦截")
	}
}

func TestValidationRejectsWaitingWhenHealthyCopyCanBeAssigned(t *testing.T) {
	opts := options{
		mode:              "plan",
		readerCount:       100,
		copyCount:         500,
		returnedLoanCount: 600,
		reservationCount:  200,
		randomSeed:        11,
	}
	data, err := buildDataset(opts, testBooks(100), time.Date(2026, time.September, 4, 0, 0, 0, 0, time.Local))
	if err != nil {
		t.Fatalf("生成小规模测试数据失败：%v", err)
	}
	readyBooks := map[int]bool{}
	for _, reservation := range data.reservations {
		if reservation.status == "ready" {
			readyBooks[reservation.bookIndex] = true
		}
	}
	targetBook := -1
	for _, copy := range data.copies {
		if copy.status == "available" && !readyBooks[copy.bookIndex] {
			targetBook = copy.bookIndex
			break
		}
	}
	if targetBook < 0 {
		t.Fatal("测试数据中没有可用于反例的健康可借书目")
	}
	for i := range data.reservations {
		if data.reservations[i].status == "waiting" {
			data.reservations[i].bookIndex = targetBook
			data.reservations[i].queuePosition = 1
			break
		}
	}
	if err := validateDataset(data); err == nil {
		t.Fatal("有健康可借副本且无待取队首的 waiting 预约未被一致性校验拦截")
	}
}

func TestValidationRejectsDuplicateFulfilledReservationForOneLoan(t *testing.T) {
	data := mustBuildTestDataset(t, 13)
	for _, reservation := range data.reservations {
		if reservation.status != "fulfilled" {
			continue
		}
		data.reservations = append(data.reservations, reservation)
		err := validateDataset(data)
		if err == nil || !strings.Contains(err.Error(), "重复关联同一笔借阅") {
			t.Fatalf("同一笔借阅被两个已完成预约关联时应被明确拦截，实际错误：%v", err)
		}
		return
	}
	t.Fatal("测试数据中没有已完成预约")
}

func TestValidationRejectsCopyAcquiredBeforePublicationYear(t *testing.T) {
	data := mustBuildTestDataset(t, 17)
	copy := &data.copies[0]
	publishYear := data.books[copy.bookIndex].publishYear
	copy.acquiredAt = time.Date(publishYear-1, time.December, 31, 0, 0, 0, 0, data.anchor.Location())
	copy.createdAt = copy.acquiredAt.Add(9 * time.Hour)
	copy.updatedAt = copy.createdAt

	err := validateDataset(data)
	if err == nil || !strings.Contains(err.Error(), "出版、入库或更新时间线无效") {
		t.Fatalf("副本早于出版年份入库时应被明确拦截，实际错误：%v", err)
	}
}

func TestBuildCopiesKeepsRecentPublicationMaintenanceTimelineValid(t *testing.T) {
	anchor := time.Date(2026, time.September, 4, 0, 0, 0, 0, time.Local)
	opts := options{readerCount: 6000, copyCount: 24000, returnedLoanCount: 36000, reservationCount: 8000}
	layout, err := deriveLayout(opts, 1000)
	if err != nil {
		t.Fatalf("生成测试布局失败：%v", err)
	}
	books := testBooks(1000)
	for i := range books {
		books[i].publishYear = 2025
	}
	copies, err := buildCopies(opts.copyCount, books, layout, anchor, rand.New(rand.NewSource(20260904)))
	if err != nil {
		t.Fatalf("生成新近出版书目的副本失败：%v", err)
	}
	for i := layout.maintenanceStart; i < layout.maintenanceStart+layout.maintenance; i++ {
		copy := copies[i]
		if copy.acquiredAt.After(copy.createdAt) || copy.createdAt.After(copy.updatedAt) || copy.updatedAt.After(anchor) {
			t.Fatalf("维护副本 %s 的时间线无效：入库 %s，录入 %s，更新 %s，锚点 %s", copy.barcode, copy.acquiredAt, copy.createdAt, copy.updatedAt, anchor)
		}
	}
}

func TestBuildCopiesSupportsCurrentYearWhenTimelineWindowExists(t *testing.T) {
	anchor := time.Date(2026, time.September, 4, 0, 0, 0, 0, time.Local)
	opts := options{readerCount: 100, copyCount: 500, returnedLoanCount: 600, reservationCount: 200}
	layout, err := deriveLayout(opts, 100)
	if err != nil {
		t.Fatalf("生成测试布局失败：%v", err)
	}
	books := testBooks(100)
	for i := range books {
		books[i].publishYear = anchor.Year()
	}
	copies, err := buildCopies(opts.copyCount, books, layout, anchor, rand.New(rand.NewSource(23)))
	if err != nil {
		t.Fatalf("锚点前存在足够窗口的当年书目应能生成副本：%v", err)
	}
	for _, copy := range copies {
		if copy.acquiredAt.Year() != anchor.Year() || copy.acquiredAt.After(copy.createdAt) || copy.createdAt.After(copy.updatedAt) || copy.updatedAt.After(anchor) {
			t.Fatalf("当年出版副本 %s 的时间线无效", copy.barcode)
		}
		if copy.status == "available" && copy.conditionStatus != "intact" {
			t.Fatalf("入库不足一年的可借副本 %s 不应按原始随机馆龄标记磨损", copy.barcode)
		}
	}
}

func TestBuildCopiesRejectsCurrentYearWithoutTimelineWindow(t *testing.T) {
	anchor := time.Date(2026, time.January, 5, 0, 0, 0, 0, time.Local)
	layout := circulationLayout{maintenanceStart: 200}
	books := []bookRef{{id: 1, materialType: "book", publishYear: anchor.Year()}}

	_, err := buildCopies(200, books, layout, anchor, rand.New(rand.NewSource(29)))
	if err == nil || !strings.Contains(err.Error(), "距锚点过近") {
		t.Fatalf("年初尚无可信时间窗口的当年书目应被明确拦截，实际错误：%v", err)
	}
}

func TestValidationRejectsCopyTimestampAfterAnchor(t *testing.T) {
	data := mustBuildTestDataset(t, 31)
	data.copies[0].updatedAt = data.anchor.Add(time.Hour)

	err := validateDataset(data)
	if err == nil || !strings.Contains(err.Error(), "出版、入库或更新时间线无效") {
		t.Fatalf("副本更新时间晚于锚点时应被明确拦截，实际错误：%v", err)
	}
}

func TestValidationRejectsBorrowAfterUnpaidFine(t *testing.T) {
	data := mustBuildTestDataset(t, 19)
	copyHasLoan := make([]bool, len(data.copies))
	for _, loan := range data.loans {
		copyHasLoan[loan.copyIndex] = true
	}
	copyIndex := -1
	for i, copy := range data.copies {
		if !copyHasLoan[i] && copy.status == "available" {
			copyIndex = i
			break
		}
	}
	if copyIndex < 0 {
		t.Fatal("测试数据中没有未产生借阅的可借副本")
	}

	for _, fine := range data.fines {
		if fine.status != "unpaid" || fine.createdAt.After(data.anchor.Add(-48*time.Hour)) {
			continue
		}
		borrowedAt := fine.createdAt.Add(time.Hour)
		returnedAt := borrowedAt.Add(time.Hour)
		reader := &data.readers[fine.readerIndex]
		reader.loanLimit = 100
		data.loans = append(data.loans, loanRow{
			readerIndex: fine.readerIndex,
			copyIndex:   copyIndex,
			borrowedAt:  borrowedAt,
			dueAt:       borrowedAt.AddDate(0, 0, 30),
			returnedAt:  &returnedAt,
			status:      "returned",
			createdAt:   borrowedAt,
			updatedAt:   returnedAt,
		})
		err := validateDataset(data)
		if err == nil || !strings.Contains(err.Error(), "未缴罚金") {
			t.Fatalf("未缴罚金产生后又借书时应被明确拦截，实际错误：%v", err)
		}
		return
	}
	t.Fatal("测试数据中没有可用于反例的未缴罚金")
}

func TestValidationRejectsHistoricalLoanLimitExceeded(t *testing.T) {
	anchor := time.Date(2026, time.September, 4, 0, 0, 0, 0, time.Local)
	borrowedAt := time.Date(2024, time.March, 1, 9, 0, 0, 0, time.Local)
	returnedAt := borrowedAt.AddDate(0, 0, 10)
	data := &dataset{
		books: []bookRef{{id: 1, materialType: "book", publishYear: 2020}},
		readers: []readerRow{{
			readerNo:  readerPrefix + "000001",
			fullName:  "测试读者",
			status:    "active",
			loanLimit: 1,
			createdAt: time.Date(2022, time.January, 1, 9, 0, 0, 0, time.Local),
			updatedAt: anchor.Add(-24 * time.Hour),
		}},
		copies: []copyRow{
			{bookIndex: 0, barcode: "BK-2022-1-0001", status: "available", conditionStatus: "intact", conditionNote: copyNotePrefix + "测试", acquiredAt: time.Date(2022, time.January, 1, 0, 0, 0, 0, time.Local), createdAt: time.Date(2022, time.January, 1, 9, 0, 0, 0, time.Local), updatedAt: anchor.Add(-24 * time.Hour)},
			{bookIndex: 0, barcode: "BK-2022-1-0002", status: "available", conditionStatus: "intact", conditionNote: copyNotePrefix + "测试", acquiredAt: time.Date(2022, time.January, 2, 0, 0, 0, 0, time.Local), createdAt: time.Date(2022, time.January, 2, 9, 0, 0, 0, time.Local), updatedAt: anchor.Add(-24 * time.Hour)},
		},
		anchor: anchor,
	}
	for copyIndex := range data.copies {
		returned := returnedAt
		data.loans = append(data.loans, loanRow{
			readerIndex: 0,
			copyIndex:   copyIndex,
			borrowedAt:  borrowedAt,
			dueAt:       borrowedAt.AddDate(0, 0, 30),
			returnedAt:  &returned,
			status:      "returned",
			createdAt:   borrowedAt,
			updatedAt:   returned,
		})
	}

	err := validateDataset(data)
	if err == nil || !strings.Contains(err.Error(), "超过额度") {
		t.Fatalf("历史时点同时借阅超过额度时应被明确拦截，实际错误：%v", err)
	}
}

func mustBuildTestDataset(t *testing.T, randomSeed int64) *dataset {
	t.Helper()
	opts := options{
		mode:              "plan",
		readerCount:       100,
		copyCount:         500,
		returnedLoanCount: 600,
		reservationCount:  200,
		randomSeed:        randomSeed,
	}
	data, err := buildDataset(opts, testBooks(100), time.Date(2026, time.September, 4, 0, 0, 0, 0, time.Local))
	if err != nil {
		t.Fatalf("生成小规模测试数据失败：%v", err)
	}
	return data
}

func testBooks(count int) []bookRef {
	books := make([]bookRef, count)
	for i := range books {
		materialType := "book"
		if i%20 == 0 {
			materialType = "periodical"
		}
		books[i] = bookRef{id: uint64(i + 1), materialType: materialType, publishYear: 2010 + i%10, maxSequence: uint64(i % 5)}
	}
	return books
}
