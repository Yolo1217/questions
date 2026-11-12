package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/BwCloudWeGo/bw-cli/internal/order/dto"
	"github.com/BwCloudWeGo/bw-cli/internal/order/entity"
	"github.com/BwCloudWeGo/bw-cli/internal/order/repo"
)

// 交易域测试依赖本地 MySQL（root/root，库 questions），可用 TRADE_TEST_DSN 覆盖。
// 数据库不可用时整组用例跳过，保证不干扰其他单元测试。
var (
	testDB   *gorm.DB
	testRepo *repo.GormRepository
	testSvc  *TradeService
)

// exitNoDB 同 repo 包：CI 置 TRADE_REQUIRE_DB=1 时连不上库即失败，避免假绿。
func exitNoDB(msg string, cause error) {
	if os.Getenv("TRADE_REQUIRE_DB") == "1" {
		fmt.Printf("db required but unavailable: %s: %v\n", msg, cause)
		os.Exit(1)
	}
	fmt.Println("skip tests:", msg, cause)
	os.Exit(0)
}

func TestMain(m *testing.M) {
	// 默认使用独立测试库 questions_test，避免测试清表影响开发库 questions。
	dsn := os.Getenv("TRADE_TEST_DSN")
	if dsn == "" {
		server := "root:root@tcp(127.0.0.1:3306)/?charset=utf8mb4&parseTime=True&loc=Local"
		bootstrap, err := gorm.Open(mysql.Open(server), &gorm.Config{Logger: gormlogger.Discard})
		if err != nil {
			fmt.Println("skip trade tests: mysql unavailable:", err)
			os.Exit(0)
		}
		if err := bootstrap.Exec("CREATE DATABASE IF NOT EXISTS questions_test DEFAULT CHARACTER SET utf8mb4").Error; err != nil {
			fmt.Println("skip trade tests: create test database failed:", err)
			os.Exit(0)
		}
		dsn = "root:root@tcp(127.0.0.1:3306)/questions_test?charset=utf8mb4&parseTime=True&loc=Local"
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{Logger: gormlogger.Discard})
	if err != nil {
		exitNoDB("trade tests: mysql unavailable", err)
	}
	if err := repo.AutoMigrate(db); err != nil {
		exitNoDB("trade tests: migrate failed", err)
	}
	testDB = db
	testRepo = repo.NewGormRepository(db, zap.NewNop())
	testSvc = NewTradeService(testRepo, TradeConfig{Keep: 10 * time.Minute, RefundFailFirst: true}, zap.NewNop())
	os.Exit(m.Run())
}

func resetTables(t *testing.T) {
	t.Helper()
	for _, table := range []string{"shop_trade_ledgers", "shop_pay_callbacks", "shop_exception_pays", "shop_trade_orders", "shop_products"} {
		if err := testDB.Exec("DELETE FROM " + table).Error; err != nil {
			t.Fatalf("reset %s failed: %v", table, err)
		}
	}
	if err := testSvc.SeedProducts(context.Background()); err != nil {
		t.Fatalf("seed products failed: %v", err)
	}
}

func product(t *testing.T, id string) *entity.Product {
	t.Helper()
	item, err := testRepo.FindProduct(context.Background(), id)
	if err != nil {
		t.Fatalf("find product failed: %v", err)
	}
	return item
}

func order(t *testing.T, id string) *entity.TradeOrder {
	t.Helper()
	item, err := testRepo.FindTradeOrder(context.Background(), id)
	if err != nil {
		t.Fatalf("find order failed: %v", err)
	}
	return item
}

func ledgersOf(t *testing.T, id string, kind string) []entity.Ledger {
	t.Helper()
	all, err := testRepo.ListLedgers(context.Background(), id)
	if err != nil {
		t.Fatalf("list ledgers failed: %v", err)
	}
	matched := make([]entity.Ledger, 0, len(all))
	for _, item := range all {
		if item.Kind == kind {
			matched = append(matched, item)
		}
	}
	return matched
}

// S2 场景：20 路并发争抢 5 件库存，只能成功 5 单，不超卖。
func TestPlaceOrderConcurrentNoOversell(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	var mu sync.Mutex
	success := 0
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			if _, err := testSvc.PlaceOrder(ctx, placeCmd(fmt.Sprintf("buyer%d", index%3+1), "P1", 1)); err == nil {
				mu.Lock()
				success++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if success != 5 {
		t.Fatalf("expected 5 successful orders, got %d", success)
	}
	if got := product(t, "P1").Available; got != 0 {
		t.Fatalf("expected available 0, got %d", got)
	}
}

// 幂等用例 1：同一通知连推 5 次，订单只迁移一次、支付流水只一条。
func TestPayNotifyRepeatedIsIdempotent(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	placed, err := testSvc.PlaceOrder(ctx, placeCmd("buyer1", "P1", 1))
	if err != nil {
		t.Fatalf("place order failed: %v", err)
	}
	for i := 0; i < 5; i++ {
		result, err := testSvc.PayNotify(ctx, notifyCmd("CH-1", placed.ID, placed.AmountCents))
		if err != nil {
			t.Fatalf("notify failed: %v", err)
		}
		if !result.Success {
			t.Fatalf("duplicate notify must return success, got %+v", result)
		}
	}
	if got := order(t, placed.ID).Status; got != entity.OrderStatusPaid {
		t.Fatalf("expected PAID, got %s", got)
	}
	if pays := ledgersOf(t, placed.ID, entity.LedgerPay); len(pays) != 1 {
		t.Fatalf("expected exactly 1 pay ledger, got %d", len(pays))
	}
}

// 幂等用例 3：订单关闭后通知到达 → 订单保持 CLOSED，异常表新增「关单后到账」，库存不重复占用。
func TestLatePayAfterCloseGoesToException(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	placed, err := testSvc.PlaceOrder(ctx, placeCmd("buyer1", "P1", 1))
	if err != nil {
		t.Fatalf("place order failed: %v", err)
	}
	if _, err := testSvc.CancelOrder(ctx, placed.ID, "buyer1"); err != nil {
		t.Fatalf("cancel failed: %v", err)
	}
	availableAfterRelease := product(t, "P1").Available
	result, err := testSvc.PayNotify(ctx, notifyCmd("CH-LATE", placed.ID, placed.AmountCents))
	if err != nil {
		t.Fatalf("late notify failed: %v", err)
	}
	if !result.Success {
		t.Fatalf("late notify should return success to stop retry, got %+v", result)
	}
	if got := order(t, placed.ID).Status; got != entity.OrderStatusClosed {
		t.Fatalf("closed order must stay CLOSED, got %s", got)
	}
	if got := product(t, "P1").Available; got != availableAfterRelease {
		t.Fatalf("late payment must not re-occupy stock: %d -> %d", availableAfterRelease, got)
	}
	exceptions, err := testRepo.ListExceptions(ctx, true)
	if err != nil {
		t.Fatalf("list exceptions failed: %v", err)
	}
	if len(exceptions) != 1 || exceptions[0].Type != entity.ExcLatePay {
		t.Fatalf("expected one 关单后到账 exception, got %+v", exceptions)
	}
}

// 幂等用例 4：金额差 1 分 → 不入账，异常表新增「金额不符」。
func TestAmountMismatchRejected(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	placed, err := testSvc.PlaceOrder(ctx, placeCmd("buyer1", "P1", 2))
	if err != nil {
		t.Fatalf("place order failed: %v", err)
	}
	result, err := testSvc.PayNotify(ctx, notifyCmd("CH-BAD", placed.ID, placed.AmountCents+1))
	if err != nil {
		t.Fatalf("notify failed: %v", err)
	}
	if result.Success {
		t.Fatalf("amount mismatch must not succeed")
	}
	if got := order(t, placed.ID).Status; got != entity.OrderStatusCreated {
		t.Fatalf("order must stay CREATED, got %s", got)
	}
	exceptions, err := testRepo.ListExceptions(ctx, true)
	if err != nil || len(exceptions) != 1 || exceptions[0].Type != entity.ExcAmountMismatch {
		t.Fatalf("expected 金额不符 exception, got %+v err=%v", exceptions, err)
	}
}

// S5 场景：退款申请与发货并发，只能形成一种合法结果。
func TestRefundAndShipRace(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	placed, err := testSvc.PlaceOrder(ctx, placeCmd("buyer1", "P1", 1))
	if err != nil {
		t.Fatalf("place order failed: %v", err)
	}
	if _, err := testSvc.PayNotify(ctx, notifyCmd("CH-RACE", placed.ID, placed.AmountCents)); err != nil {
		t.Fatalf("notify failed: %v", err)
	}
	var wg sync.WaitGroup
	var shipErr, refundErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, shipErr = testSvc.ShipOrder(ctx, shipCmd(placed.ID))
	}()
	go func() {
		defer wg.Done()
		_, refundErr = testSvc.ApplyRefund(ctx, placed.ID, "buyer1")
	}()
	wg.Wait()
	if (shipErr == nil) == (refundErr == nil) {
		t.Fatalf("exactly one of ship/refund must succeed: shipErr=%v refundErr=%v", shipErr, refundErr)
	}
	status := order(t, placed.ID).Status
	if status != entity.OrderStatusShipped && status != entity.OrderStatusRefunding {
		t.Fatalf("unexpected final status %s", status)
	}
}

// S3 场景：渠道首次退款失败保留待处理，重试成功并回补库存。
func TestRefundFailThenRetryRestocks(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	placed, err := testSvc.PlaceOrder(ctx, placeCmd("buyer1", "P1", 1))
	if err != nil {
		t.Fatalf("place order failed: %v", err)
	}
	if _, err := testSvc.PayNotify(ctx, notifyCmd("CH-REFUND", placed.ID, placed.AmountCents)); err != nil {
		t.Fatalf("notify failed: %v", err)
	}
	if _, err := testSvc.ApplyRefund(ctx, placed.ID, "buyer1"); err != nil {
		t.Fatalf("apply refund failed: %v", err)
	}
	availableBeforeRefund := product(t, "P1").Available
	if _, err := testSvc.AuditRefund(ctx, auditCmd(placed.ID, true, "")); err == nil {
		t.Fatalf("first refund must fail in fail-first mode")
	}
	if got := order(t, placed.ID).Status; got != entity.OrderStatusRefunding {
		t.Fatalf("order must stay REFUNDING after channel failure, got %s", got)
	}
	if _, err := testSvc.AuditRefund(ctx, auditCmd(placed.ID, true, "")); err != nil {
		t.Fatalf("retry refund failed: %v", err)
	}
	if got := order(t, placed.ID).Status; got != entity.OrderStatusRefunded {
		t.Fatalf("expected REFUNDED, got %s", got)
	}
	if got := product(t, "P1").Available; got != availableBeforeRefund+1 {
		t.Fatalf("sale refund must restock: %d -> %d", availableBeforeRefund, got)
	}
	// 退款台账应为三条：申请受理(processing) → 渠道失败(failed) → 重试成功(success)
	if refunds := ledgersOf(t, placed.ID, entity.LedgerRefund); len(refunds) != 3 {
		t.Fatalf("expected processing + failed + success refund ledgers, got %d", len(refunds))
	}
}

// S4 场景：迟到支付退款，订单保持 CLOSED，不回补库存（不重复释放）。
func TestLatePayRefundDoesNotRestock(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	placed, err := testSvc.PlaceOrder(ctx, placeCmd("buyer1", "P1", 2))
	if err != nil {
		t.Fatalf("place order failed: %v", err)
	}
	if _, err := testSvc.CancelOrder(ctx, placed.ID, "buyer1"); err != nil {
		t.Fatalf("cancel failed: %v", err)
	}
	availableAfterRelease := product(t, "P1").Available
	if _, err := testSvc.PayNotify(ctx, notifyCmd("CH-LATE2", placed.ID, placed.AmountCents)); err != nil {
		t.Fatalf("late notify failed: %v", err)
	}
	exceptions, err := testRepo.ListExceptions(ctx, true)
	if err != nil || len(exceptions) != 1 {
		t.Fatalf("expected one exception, got %+v err=%v", exceptions, err)
	}
	if _, err := testSvc.HandleException(ctx, handleCmd(exceptions[0].ID, "refund", "")); err != nil {
		t.Fatalf("handle exception failed: %v", err)
	}
	if got := order(t, placed.ID).Status; got != entity.OrderStatusClosed {
		t.Fatalf("late refund must keep CLOSED, got %s", got)
	}
	if got := product(t, "P1").Available; got != availableAfterRelease {
		t.Fatalf("late refund must not restock: %d -> %d", availableAfterRelease, got)
	}
	closed, err := testRepo.FindException(ctx, exceptions[0].ID)
	if err != nil || closed.Status != entity.ExcStatusClosed {
		t.Fatalf("exception must be closed with evidence, got %+v err=%v", closed, err)
	}
}

func placeCmd(buyerID string, productID string, qty int32) dto.PlaceOrderCommand {
	return dto.PlaceOrderCommand{BuyerID: buyerID, BuyerName: buyerID, ProductID: productID, Qty: qty}
}

func notifyCmd(tradeNo string, orderID string, amount int64) dto.PayNotifyCommand {
	return dto.PayNotifyCommand{
		TradeNo:     tradeNo,
		OrderID:     orderID,
		AmountCents: amount,
		Signature:   SignPayNotify(DefaultChannelSecret, tradeNo, orderID, amount),
	}
}

func forgedNotifyCmd(tradeNo string, orderID string, amount int64) dto.PayNotifyCommand {
	return dto.PayNotifyCommand{
		TradeNo:     tradeNo,
		OrderID:     orderID,
		AmountCents: amount,
		Signature:   SignPayNotify("attacker-secret", tradeNo, orderID, amount),
	}
}

func shipCmd(orderID string) dto.ShipOrderCommand {
	return dto.ShipOrderCommand{OrderID: orderID, Carrier: "顺丰速运", TrackingNo: "SF" + orderID, Operator: "仓管小周"}
}

func auditCmd(orderID string, approve bool, reason string) dto.AuditRefundCommand {
	return dto.AuditRefundCommand{OrderID: orderID, Approve: approve, Reason: reason, Operator: "客服小时"}
}

func handleCmd(exceptionID string, action string, reason string) dto.HandleExceptionCommand {
	return dto.HandleExceptionCommand{ExceptionID: exceptionID, Action: action, Reason: reason, Operator: "客服小时"}
}

// 边界：同一订单重复关闭不得重复释放库存。
func TestDuplicateCloseDoesNotReleaseTwice(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	before := product(t, "P1").Available
	placed, err := testSvc.PlaceOrder(ctx, placeCmd("buyer1", "P1", 2))
	if err != nil {
		t.Fatalf("place order failed: %v", err)
	}
	if _, err := testSvc.CancelOrder(ctx, placed.ID, "buyer1"); err != nil {
		t.Fatalf("first cancel failed: %v", err)
	}
	if _, err := testSvc.CancelOrder(ctx, placed.ID, "buyer1"); err == nil {
		t.Fatalf("second cancel must be rejected")
	}
	if got := product(t, "P1").Available; got != before {
		t.Fatalf("stock must be released exactly once: before=%d after=%d", before, got)
	}
}

// 边界：运营调价不影响已有订单金额（下单价格快照）。
func TestPriceSnapshotImmutable(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	placed, err := testSvc.PlaceOrder(ctx, placeCmd("buyer1", "P1", 1))
	if err != nil {
		t.Fatalf("place order failed: %v", err)
	}
	original := placed.AmountCents
	if _, err := testSvc.UpsertProduct(ctx, dto.UpsertProductCommand{
		ID: "P1", Name: "羊绒围巾 · 燕麦色", PriceCents: 12000, Available: 5, OnSale: true,
	}); err != nil {
		t.Fatalf("upsert product failed: %v", err)
	}
	detail, err := testSvc.GetOrder(ctx, placed.ID)
	if err != nil {
		t.Fatalf("get order failed: %v", err)
	}
	if detail.AmountCents != original {
		t.Fatalf("existing order amount must stay %d, got %d", original, detail.AmountCents)
	}
	// 按新价发起通知应判金额不符，按快照价才成功
	mismatch, err := testSvc.PayNotify(ctx, notifyCmd("CH-NEWPRICE", placed.ID, 12000))
	if err != nil || mismatch.Success {
		t.Fatalf("notify with new price must be rejected, got %+v err=%v", mismatch, err)
	}
	ok, err := testSvc.PayNotify(ctx, notifyCmd("CH-SNAPSHOT", placed.ID, original))
	if err != nil || !ok.Success {
		t.Fatalf("notify with snapshot price must succeed, got %+v err=%v", ok, err)
	}
}

// 验收：同一渠道交易号不得归属于两个订单。
func TestChannelTradeNoCannotBindTwoOrders(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	first, err := testSvc.PlaceOrder(ctx, placeCmd("buyer1", "P1", 1))
	if err != nil {
		t.Fatalf("place first order failed: %v", err)
	}
	second, err := testSvc.PlaceOrder(ctx, placeCmd("buyer2", "P1", 1))
	if err != nil {
		t.Fatalf("place second order failed: %v", err)
	}
	tradeNo := "CH-SHARED"
	if result, err := testSvc.PayNotify(ctx, notifyCmd(tradeNo, first.ID, first.AmountCents)); err != nil || !result.Success {
		t.Fatalf("first notify should succeed: %+v err=%v", result, err)
	}
	result, err := testSvc.PayNotify(ctx, notifyCmd(tradeNo, second.ID, second.AmountCents))
	if err != nil {
		t.Fatalf("second notify returned error: %v", err)
	}
	if !result.Duplicate || !result.Success {
		t.Fatalf("same trade no on another order must be treated as duplicate success, got %+v", result)
	}
	if got := order(t, second.ID).Status; got != entity.OrderStatusCreated {
		t.Fatalf("second order must stay CREATED, got %s", got)
	}
	if pays := ledgersOf(t, second.ID, entity.LedgerPay); len(pays) != 0 {
		t.Fatalf("second order must have no pay ledger, got %d", len(pays))
	}
}

// 验签用例：签名错误/缺失不得入账，回调记录 FAILED，可用渠道流水号反查异常。
func TestPayNotifyBadSignatureRejected(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	placed, err := testSvc.PlaceOrder(ctx, placeCmd("buyer1", "P1", 1))
	if err != nil {
		t.Fatalf("place order failed: %v", err)
	}
	result, err := testSvc.PayNotify(ctx, forgedNotifyCmd("CH-FORGED", placed.ID, placed.AmountCents))
	if err != nil {
		t.Fatalf("notify with forged signature returned error: %v", err)
	}
	if result.Success || result.Code != "verify_failed" {
		t.Fatalf("forged signature must be rejected, got %+v", result)
	}
	if got := order(t, placed.ID).Status; got != entity.OrderStatusCreated {
		t.Fatalf("order must stay CREATED, got %s", got)
	}
	callback, err := testSvc.GetCallback(ctx, "CH-FORGED")
	if err != nil {
		t.Fatalf("query callback failed: %v", err)
	}
	if callback.Status != entity.CallbackFailed || callback.ExceptionType != entity.ExcBadSignature {
		t.Fatalf("callback must be FAILED with 验签失败 exception, got %+v", callback)
	}
	if pays := ledgersOf(t, placed.ID, entity.LedgerPay); len(pays) != 0 {
		t.Fatalf("forged signature must not create pay ledger, got %d", len(pays))
	}
}

// 反查用例：重复通知只累加命中次数，不得把已完成记录降级为 RECEIVED。
func TestCallbackStatusKeepsDoneAfterDuplicate(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	placed, err := testSvc.PlaceOrder(ctx, placeCmd("buyer1", "P1", 1))
	if err != nil {
		t.Fatalf("place order failed: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, err := testSvc.PayNotify(ctx, notifyCmd("CH-HITS", placed.ID, placed.AmountCents)); err != nil {
			t.Fatalf("notify %d failed: %v", i, err)
		}
	}
	callback, err := testSvc.GetCallback(ctx, "CH-HITS")
	if err != nil {
		t.Fatalf("query callback failed: %v", err)
	}
	if callback.Status != entity.CallbackDone {
		t.Fatalf("duplicate notify must keep DONE, got %s", callback.Status)
	}
	if callback.Hits != 3 {
		t.Fatalf("expected 3 hits, got %d", callback.Hits)
	}
}

// 查询用例：无主支付（订单不存在）可用渠道交易号查到 FAILED 与异常类型。
func TestCallbackQueryForNoOwnerPay(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	cmd := notifyCmd("CH-NOOWNER", "SO-NOT-EXIST", 10000)
	result, err := testSvc.PayNotify(ctx, cmd)
	if err != nil {
		t.Fatalf("notify failed: %v", err)
	}
	if result.Success || result.Code != "order_not_found" {
		t.Fatalf("unknown order must fail with order_not_found, got %+v", result)
	}
	callback, err := testSvc.GetCallback(ctx, "CH-NOOWNER")
	if err != nil {
		t.Fatalf("query callback failed: %v", err)
	}
	if callback.Status != entity.CallbackFailed || callback.ExceptionType != entity.ExcNoOwner || callback.ExceptionStatus != entity.ExcStatusPending {
		t.Fatalf("callback must show 无主支付 pending exception, got %+v", callback)
	}
	list, err := testSvc.ListCallbacks(ctx, "CH-NOOWNER", "")
	if err != nil || len(list) != 1 || list[0].TradeNo != "CH-NOOWNER" {
		t.Fatalf("keyword query failed: %+v err=%v", list, err)
	}
	if _, err := testSvc.GetCallback(ctx, "CH-UNKNOWN"); !errors.Is(err, entity.ErrCallbackNotFound) {
		t.Fatalf("unknown trade no must return ErrCallbackNotFound, got %v", err)
	}
}

// 重复退款通知：已退款订单再次审核按幂等成功，不重复打款、不回补库存。
func TestAuditRefundDuplicateIsIdempotent(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	placed, err := testSvc.PlaceOrder(ctx, placeCmd("buyer1", "P1", 1))
	if err != nil {
		t.Fatalf("place order failed: %v", err)
	}
	if _, err := testSvc.PayNotify(ctx, notifyCmd("CH-IDEM", placed.ID, placed.AmountCents)); err != nil {
		t.Fatalf("notify failed: %v", err)
	}
	if _, err := testSvc.ApplyRefund(ctx, placed.ID, "buyer1"); err != nil {
		t.Fatalf("apply refund failed: %v", err)
	}
	if _, err := testSvc.AuditRefund(ctx, auditCmd(placed.ID, true, "")); err == nil {
		t.Fatalf("first refund must fail in fail-first mode")
	}
	first, err := testSvc.AuditRefund(ctx, auditCmd(placed.ID, true, ""))
	if err != nil {
		t.Fatalf("retry refund failed: %v", err)
	}
	if first.Duplicate {
		t.Fatalf("first successful refund must not be duplicate")
	}
	restocked := product(t, "P1").Available
	refundLedgers := len(ledgersOf(t, placed.ID, entity.LedgerRefund))

	again, err := testSvc.AuditRefund(ctx, auditCmd(placed.ID, true, ""))
	if err != nil {
		t.Fatalf("duplicate audit must return idempotent success, got error: %v", err)
	}
	if !again.Duplicate || again.Order.Status != entity.OrderStatusRefunded {
		t.Fatalf("duplicate audit must report duplicate=true with REFUNDED, got %+v", again)
	}
	if got := product(t, "P1").Available; got != restocked {
		t.Fatalf("duplicate refund must not restock again: %d -> %d", restocked, got)
	}
	if got := len(ledgersOf(t, placed.ID, entity.LedgerRefund)); got != refundLedgers {
		t.Fatalf("duplicate refund must not add ledger: %d -> %d", refundLedgers, got)
	}
}

// 异常处置：原因必填，且处置结果必须进入订单台账（运营按订单可查）。
func TestHandleExceptionRequiresReasonAndWritesLedger(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	placed, err := testSvc.PlaceOrder(ctx, placeCmd("buyer1", "P1", 1))
	if err != nil {
		t.Fatalf("place order failed: %v", err)
	}
	if _, err := testSvc.CancelOrder(ctx, placed.ID, "buyer1"); err != nil {
		t.Fatalf("cancel failed: %v", err)
	}
	if _, err := testSvc.PayNotify(ctx, notifyCmd("CH-LEDGER", placed.ID, placed.AmountCents)); err != nil {
		t.Fatalf("late notify failed: %v", err)
	}
	exceptions, err := testRepo.ListExceptions(ctx, true)
	if err != nil || len(exceptions) != 1 {
		t.Fatalf("expected one exception, got %+v err=%v", exceptions, err)
	}
	if _, err := testSvc.HandleException(ctx, handleCmd(exceptions[0].ID, "verify", "")); !errors.Is(err, entity.ErrReasonRequired) {
		t.Fatalf("verify without reason must be rejected, got %v", err)
	}
	if _, err := testSvc.HandleException(ctx, handleCmd(exceptions[0].ID, "ignore", "渠道确认该笔已撤销")); err != nil {
		t.Fatalf("ignore with reason failed: %v", err)
	}
	found := false
	for _, ledger := range ledgersOf(t, placed.ID, entity.LedgerException) {
		if strings.Contains(ledger.Text, "渠道确认该笔已撤销") {
			found = true
		}
	}
	if !found {
		t.Fatalf("exception handling must be written into order ledger")
	}
}

// 弃单闭环：未付款关闭释放的库存，其他买家能再次买到。
func TestReleasedStockBuyableByOthers(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	var first *dto.TradeOrderDTO
	for i := 0; i < 5; i++ {
		placed, err := testSvc.PlaceOrder(ctx, placeCmd(fmt.Sprintf("buyer%d", i%3+1), "P1", 1))
		if err != nil {
			t.Fatalf("place order %d failed: %v", i, err)
		}
		first = placed
	}
	if got := product(t, "P1").Available; got != 0 {
		t.Fatalf("stock must be fully reserved, got %d", got)
	}
	if _, err := testSvc.PlaceOrder(ctx, placeCmd("buyer2", "P1", 1)); !errors.Is(err, entity.ErrInsufficientStock) {
		t.Fatalf("sixth order must be rejected, got %v", err)
	}
	if _, err := testSvc.CancelOrder(ctx, first.ID, first.BuyerID); err != nil {
		t.Fatalf("cancel failed: %v", err)
	}
	if got := product(t, "P1").Available; got != 1 {
		t.Fatalf("cancelled stock must be released, got %d", got)
	}
	if _, err := testSvc.PlaceOrder(ctx, placeCmd("buyer3", "P1", 1)); err != nil {
		t.Fatalf("released stock must be buyable by others: %v", err)
	}
	if got := product(t, "P1").Available; got != 0 {
		t.Fatalf("released stock must be re-reserved, got %d", got)
	}
}

// E02 / E01：下架商品与超量下单都必须拒单且不产生订单。
func TestOffSaleAndInvalidQtyRejected(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	if _, err := testSvc.UpsertProduct(ctx, dto.UpsertProductCommand{
		ID: "P1", Name: "羊绒围巾 · 燕麦色", PriceCents: 10000, Available: 5, OnSale: false,
	}); err != nil {
		t.Fatalf("off sale failed: %v", err)
	}
	if _, err := testSvc.PlaceOrder(ctx, placeCmd("buyer1", "P1", 1)); !errors.Is(err, entity.ErrProductOffSale) {
		t.Fatalf("off-sale product must be rejected, got %v", err)
	}
	if _, err := testSvc.UpsertProduct(ctx, dto.UpsertProductCommand{
		ID: "P1", Name: "羊绒围巾 · 燕麦色", PriceCents: 10000, Available: 5, OnSale: true,
	}); err != nil {
		t.Fatalf("on sale failed: %v", err)
	}
	if _, err := testSvc.PlaceOrder(ctx, placeCmd("buyer1", "P1", 3)); !errors.Is(err, entity.ErrInvalidQty) {
		t.Fatalf("qty over 2 must be rejected, got %v", err)
	}
	if got := product(t, "P1").Available; got != 5 {
		t.Fatalf("rejected orders must not occupy stock, got %d", got)
	}
}

// E07：三路并发投递同一通知，只有一路真正入账。
func TestConcurrentNotifyOnlyOneApplies(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	placed, err := testSvc.PlaceOrder(ctx, placeCmd("buyer1", "P1", 1))
	if err != nil {
		t.Fatalf("place order failed: %v", err)
	}
	var wg sync.WaitGroup
	results := make([]*dto.PayNotifyResult, 3)
	errs := make([]error, 3)
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			results[index], errs[index] = testSvc.PayNotify(ctx, notifyCmd("CH-CONCURRENT", placed.ID, placed.AmountCents))
		}(i)
	}
	wg.Wait()
	success := 0
	for i := 0; i < 3; i++ {
		if errs[i] != nil {
			t.Fatalf("notify %d returned error: %v", i, errs[i])
		}
		if results[i].Success {
			success++
		}
	}
	if success != 3 {
		t.Fatalf("all duplicate notifications must return success, got %d", success)
	}
	if pays := ledgersOf(t, placed.ID, entity.LedgerPay); len(pays) != 1 {
		t.Fatalf("exactly one pay ledger expected, got %d", len(pays))
	}
	if got := order(t, placed.ID).Status; got != entity.OrderStatusPaid {
		t.Fatalf("order must be PAID once, got %s", got)
	}
}

// E18：已发货订单不受理售前退款。
func TestShippedOrderRefundRejected(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	placed, err := testSvc.PlaceOrder(ctx, placeCmd("buyer1", "P1", 1))
	if err != nil {
		t.Fatalf("place order failed: %v", err)
	}
	if _, err := testSvc.PayNotify(ctx, notifyCmd("CH-SHIPPED", placed.ID, placed.AmountCents)); err != nil {
		t.Fatalf("notify failed: %v", err)
	}
	if _, err := testSvc.ShipOrder(ctx, shipCmd(placed.ID)); err != nil {
		t.Fatalf("ship failed: %v", err)
	}
	if _, err := testSvc.ApplyRefund(ctx, placed.ID, "buyer1"); !errors.Is(err, entity.ErrRefundNotAllowed) {
		t.Fatalf("shipped order refund must be rejected, got %v", err)
	}
	if got := order(t, placed.ID).Status; got != entity.OrderStatusShipped {
		t.Fatalf("order must stay SHIPPED, got %s", got)
	}
}

// S6：汇总与流水一致（已收款 / 已退款 / 可售 / 预占）。
func TestSummaryMatchesLedgers(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	first, err := testSvc.PlaceOrder(ctx, placeCmd("buyer1", "P1", 1))
	if err != nil {
		t.Fatalf("place order failed: %v", err)
	}
	if _, err := testSvc.PayNotify(ctx, notifyCmd("CH-SUM-1", first.ID, first.AmountCents)); err != nil {
		t.Fatalf("notify failed: %v", err)
	}
	second, err := testSvc.PlaceOrder(ctx, placeCmd("buyer2", "P2", 1))
	if err != nil {
		t.Fatalf("place order failed: %v", err)
	}
	if _, err := testSvc.PayNotify(ctx, notifyCmd("CH-SUM-2", second.ID, second.AmountCents)); err != nil {
		t.Fatalf("notify failed: %v", err)
	}
	if _, err := testSvc.ApplyRefund(ctx, second.ID, "buyer2"); err != nil {
		t.Fatalf("apply refund failed: %v", err)
	}
	if _, err := testSvc.AuditRefund(ctx, auditCmd(second.ID, true, "")); err == nil {
		t.Fatalf("first refund must fail in fail-first mode")
	}
	if _, err := testSvc.AuditRefund(ctx, auditCmd(second.ID, true, "")); err != nil {
		t.Fatalf("retry refund failed: %v", err)
	}
	// 再放一笔待支付订单，验证预占件数
	if _, err := testSvc.PlaceOrder(ctx, placeCmd("buyer3", "P1", 2)); err != nil {
		t.Fatalf("place pending order failed: %v", err)
	}
	summary, err := testSvc.Summary(ctx)
	if err != nil {
		t.Fatalf("summary failed: %v", err)
	}
	if summary.PaidTotalCents != first.AmountCents+second.AmountCents {
		t.Fatalf("paid total mismatch: %d", summary.PaidTotalCents)
	}
	if summary.RefundedTotalCents != second.AmountCents {
		t.Fatalf("refunded total mismatch: %d", summary.RefundedTotalCents)
	}
	if summary.ReservedTotal != 2 {
		t.Fatalf("reserved total mismatch: %d", summary.ReservedTotal)
	}
	if summary.AvailableTotal != product(t, "P1").Available+product(t, "P2").Available {
		t.Fatalf("available total mismatch: %d", summary.AvailableTotal)
	}
	if summary.OrdersByStatus[entity.OrderStatusShipped] != 0 && summary.OrdersByStatus[entity.OrderStatusPaid] != 1 {
		t.Fatalf("status distribution mismatch: %+v", summary.OrdersByStatus)
	}
}

// 发货具名拒绝：已关闭、已退款订单必须返回各自明确的错误，而不是笼统的非法迁移。
func TestShipRejectsClosedAndRefundedOrder(t *testing.T) {
	resetTables(t)
	ctx := context.Background()

	closed, err := testSvc.PlaceOrder(ctx, placeCmd("buyer1", "P1", 1))
	if err != nil {
		t.Fatalf("place order failed: %v", err)
	}
	if _, err := testSvc.CancelOrder(ctx, closed.ID, "buyer1"); err != nil {
		t.Fatalf("cancel failed: %v", err)
	}
	if _, err := testSvc.ShipOrder(ctx, shipCmd(closed.ID)); !errors.Is(err, entity.ErrOrderClosed) {
		t.Fatalf("closed order must return ErrOrderClosed, got %v", err)
	}

	refunded, err := testSvc.PlaceOrder(ctx, placeCmd("buyer2", "P1", 1))
	if err != nil {
		t.Fatalf("place order failed: %v", err)
	}
	if _, err := testSvc.PayNotify(ctx, notifyCmd("CH-NAMED", refunded.ID, refunded.AmountCents)); err != nil {
		t.Fatalf("notify failed: %v", err)
	}
	if _, err := testSvc.ApplyRefund(ctx, refunded.ID, "buyer2"); err != nil {
		t.Fatalf("apply refund failed: %v", err)
	}
	if _, err := testSvc.AuditRefund(ctx, auditCmd(refunded.ID, true, "")); err == nil {
		t.Fatalf("first refund must fail in fail-first mode")
	}
	if _, err := testSvc.AuditRefund(ctx, auditCmd(refunded.ID, true, "")); err != nil {
		t.Fatalf("retry refund failed: %v", err)
	}
	if _, err := testSvc.ShipOrder(ctx, shipCmd(refunded.ID)); !errors.Is(err, entity.ErrOrderRefunded) {
		t.Fatalf("refunded order must return ErrOrderRefunded, got %v", err)
	}
}

// 越权用例：他人订单取消必须被拒绝。
func TestCancelOthersOrderDenied(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	placed, err := testSvc.PlaceOrder(ctx, placeCmd("buyer1", "P1", 1))
	if err != nil {
		t.Fatalf("place order failed: %v", err)
	}
	if _, err := testSvc.CancelOrder(ctx, placed.ID, "buyer2"); err == nil {
		t.Fatalf("cancel others order must be denied")
	}
	if got := order(t, placed.ID).Status; got != entity.OrderStatusCreated {
		t.Fatalf("order must remain CREATED, got %s", got)
	}
}
