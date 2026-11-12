package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/BwCloudWeGo/bw-cli/internal/order/dto"
	"github.com/BwCloudWeGo/bw-cli/internal/order/entity"
)

// 运营商品维护：列表、校验拒绝、新增与更新路径、Total 收敛。
func TestListProductsAndUpsertValidation(t *testing.T) {
	resetTables(t)
	ctx := context.Background()

	items, err := testSvc.ListProducts(ctx)
	if err != nil {
		t.Fatalf("list products failed: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 seeded products, got %d", len(items))
	}

	// 校验拒绝：空 ID / 空名称 / 非法价格 / 负库存
	for name, cmd := range map[string]dto.UpsertProductCommand{
		"empty id":       {ID: " ", Name: "x", PriceCents: 100},
		"empty name":     {ID: "P9", Name: "  ", PriceCents: 100},
		"invalid price":  {ID: "P9", Name: "x", PriceCents: 0, Available: 1},
		"negative stock": {ID: "P9", Name: "x", PriceCents: 100, Available: -1},
	} {
		if _, err := testSvc.UpsertProduct(ctx, cmd); !errors.Is(err, entity.ErrInvalidQty) {
			t.Fatalf("%s must be rejected with ErrInvalidQty, got %v", name, err)
		}
	}

	// 新增商品：Total 随 Available 初始化
	created, err := testSvc.UpsertProduct(ctx, dto.UpsertProductCommand{ID: "P3", Name: "新品", PriceCents: 5000, Available: 3, OnSale: true})
	if err != nil {
		t.Fatalf("create product failed: %v", err)
	}
	if created.Available != 3 {
		t.Fatalf("new product available must be 3, got %d", created.Available)
	}

	// 更新已有商品：可售数量提升时 Total 同步收敛，原价可调
	updated, err := testSvc.UpsertProduct(ctx, dto.UpsertProductCommand{ID: "P1", Name: "羊绒围巾 · 燕麦色", PriceCents: 10000, Available: 6, OnSale: true})
	if err != nil {
		t.Fatalf("update product failed: %v", err)
	}
	if updated.Total != 6 {
		t.Fatalf("total must follow available when increased, got %d", updated.Total)
	}
	if got := product(t, "P1").PriceCents; got != 10000 {
		t.Fatalf("price must persist, got %d", got)
	}
}

// 订单列表：买家/状态过滤（角色视图服务端过滤）。
func TestListOrdersFilterByBuyerAndStatus(t *testing.T) {
	resetTables(t)
	ctx := context.Background()

	first, err := testSvc.PlaceOrder(ctx, placeCmd("buyer1", "P1", 1))
	if err != nil {
		t.Fatalf("place order failed: %v", err)
	}
	second, err := testSvc.PlaceOrder(ctx, placeCmd("buyer2", "P1", 1))
	if err != nil {
		t.Fatalf("place order failed: %v", err)
	}
	if _, err := testSvc.PayNotify(ctx, notifyCmd("CH-LIST-1", first.ID, first.AmountCents)); err != nil {
		t.Fatalf("pay notify failed: %v", err)
	}

	all, err := testSvc.ListOrders(ctx, "", "")
	if err != nil || all.Total != 2 {
		t.Fatalf("expected 2 orders, got total=%d err=%v", all.Total, err)
	}
	mine, err := testSvc.ListOrders(ctx, "buyer1", "")
	if err != nil || mine.Total != 1 || mine.Items[0].ID != first.ID {
		t.Fatalf("buyer filter failed: total=%d err=%v", mine.Total, err)
	}
	paid, err := testSvc.ListOrders(ctx, "", entity.OrderStatusPaid)
	if err != nil || len(paid.Items) != 1 || paid.Items[0].ID != first.ID {
		t.Fatalf("status filter failed: n=%d err=%v", len(paid.Items), err)
	}
	both, err := testSvc.ListOrders(ctx, "buyer1", entity.OrderStatusPaid)
	if err != nil || len(both.Items) != 1 {
		t.Fatalf("combined filter failed: n=%d err=%v", len(both.Items), err)
	}
	if second.ID == first.ID {
		t.Fatalf("order ids must differ")
	}
}

// 客服反查：异常列表、回调列表过滤、按交易号/空值查询。
func TestListExceptionsAndCallbackQuery(t *testing.T) {
	resetTables(t)
	ctx := context.Background()

	if _, err := testSvc.PayNotify(ctx, forgedNotifyCmd("CH-QRY-1", "ORD-MISSING", 100)); err != nil {
		t.Fatalf("notify should not error: %v", err)
	}

	pending, err := testSvc.ListExceptions(ctx, true)
	if err != nil || len(pending) != 1 {
		t.Fatalf("expected 1 pending exception, got %d err=%v", len(pending), err)
	}
	if pending[0].Type != entity.ExcBadSignature {
		t.Fatalf("expected verify_failed exception, got %s", pending[0].Type)
	}
	all, err := testSvc.ListExceptions(ctx, false)
	if err != nil || len(all) != 1 {
		t.Fatalf("list all exceptions failed: %d err=%v", len(all), err)
	}

	byKeyword, err := testSvc.ListCallbacks(ctx, "CH-QRY-1", "")
	if err != nil || len(byKeyword) != 1 {
		t.Fatalf("callback keyword query failed: n=%d err=%v", len(byKeyword), err)
	}
	byOrder, err := testSvc.ListCallbacks(ctx, "ORD-MISSING", entity.CallbackFailed)
	if err != nil || len(byOrder) != 1 || byOrder[0].Status != entity.CallbackFailed {
		t.Fatalf("callback order query failed: %+v err=%v", byOrder, err)
	}
	done, err := testSvc.ListCallbacks(ctx, "CH-QRY-1", entity.CallbackDone)
	if err != nil || len(done) != 0 {
		t.Fatalf("status filter mismatch: n=%d err=%v", len(done), err)
	}

	if _, err := testSvc.GetCallback(ctx, ""); !errors.Is(err, entity.ErrCallbackNotFound) {
		t.Fatalf("empty trade no must be ErrCallbackNotFound, got %v", err)
	}
	if _, err := testSvc.GetCallback(ctx, "CH-NOPE"); !errors.Is(err, entity.ErrCallbackNotFound) {
		t.Fatalf("unknown trade no must be ErrCallbackNotFound, got %v", err)
	}
}

// 超时关单扫描：到期订单关闭并释放预占，重复执行不多释放。
func TestSweepExpiredClosesAndReleasesOnce(t *testing.T) {
	resetTables(t)
	ctx := context.Background()

	// 保留期 1ns：下单即视为过期
	sweepSvc := NewTradeService(testRepo, TradeConfig{Keep: time.Nanosecond, RefundFailFirst: false}, nil)
	placed, err := sweepSvc.PlaceOrder(ctx, placeCmd("buyer1", "P1", 2))
	if err != nil {
		t.Fatalf("place order failed: %v", err)
	}

	closed, err := sweepSvc.SweepExpired(ctx)
	if err != nil || closed != 1 {
		t.Fatalf("expected 1 expired order closed, got %d err=%v", closed, err)
	}
	closedOrder := order(t, placed.ID)
	if closedOrder.Status != entity.OrderStatusClosed || closedOrder.ClosedReason == "" {
		t.Fatalf("order must be closed with reason, got %s / %q", closedOrder.Status, closedOrder.ClosedReason)
	}
	if got := product(t, "P1").Available; got != 5 {
		t.Fatalf("stock must be released exactly once, got %d", got)
	}
	if again, err := sweepSvc.SweepExpired(ctx); err != nil || again != 0 {
		t.Fatalf("second sweep must close nothing, got %d err=%v", again, err)
	}
}

// 回调入参校验 + 同订单不同交易号的重复扣款识别。
func TestPayNotifyInvalidArgumentAndDoubleCharge(t *testing.T) {
	resetTables(t)
	ctx := context.Background()

	placed, err := testSvc.PlaceOrder(ctx, placeCmd("buyer1", "P1", 1))
	if err != nil {
		t.Fatalf("place order failed: %v", err)
	}

	missing, err := testSvc.PayNotify(ctx, notifyCmd("", placed.ID, placed.AmountCents))
	if err != nil || missing.Success || missing.Code != "invalid_argument" {
		t.Fatalf("missing args must be invalid_argument, got %+v err=%v", missing, err)
	}

	if _, err := testSvc.PayNotify(ctx, notifyCmd("CH-PAY-OK", placed.ID, placed.AmountCents)); err != nil {
		t.Fatalf("first notify failed: %v", err)
	}
	double, err := testSvc.PayNotify(ctx, notifyCmd("CH-PAY-DUP", placed.ID, placed.AmountCents))
	if err != nil || !double.Success {
		t.Fatalf("double charge notify must succeed with exception, got %+v err=%v", double, err)
	}
	if pays := ledgersOf(t, placed.ID, entity.LedgerPay); len(pays) != 1 {
		t.Fatalf("pay ledger must stay 1, got %d", len(pays))
	}
	exs, err := testSvc.ListExceptions(ctx, true)
	if err != nil || len(exs) != 1 || exs[0].Type != entity.ExcDoubleCharge {
		t.Fatalf("expected double charge exception, got %+v err=%v", exs, err)
	}
}

// 退款审核守卫：非 REFUNDING 拒审、他人申请拒绝、重复申请拒绝、拒绝必填原因、拒绝后可发货。
func TestRefundAuditGuardsAndRejectPath(t *testing.T) {
	resetTables(t)
	ctx := context.Background()

	placed, err := testSvc.PlaceOrder(ctx, placeCmd("buyer1", "P1", 1))
	if err != nil {
		t.Fatalf("place order failed: %v", err)
	}
	if _, err := testSvc.PayNotify(ctx, notifyCmd("CH-GUARD", placed.ID, placed.AmountCents)); err != nil {
		t.Fatalf("pay notify failed: %v", err)
	}

	if _, err := testSvc.AuditRefund(ctx, auditCmd(placed.ID, true, "同意退款")); !errors.Is(err, entity.ErrInvalidTransition) {
		t.Fatalf("audit on PAID order must be rejected, got %v", err)
	}
	if _, err := testSvc.ApplyRefund(ctx, placed.ID, "buyer2"); !errors.Is(err, entity.ErrPermissionDenied) {
		t.Fatalf("other buyer apply must be denied, got %v", err)
	}
	if _, err := testSvc.ApplyRefund(ctx, placed.ID, "buyer1"); err != nil {
		t.Fatalf("apply refund failed: %v", err)
	}
	if _, err := testSvc.ApplyRefund(ctx, placed.ID, "buyer1"); !errors.Is(err, entity.ErrInvalidTransition) {
		t.Fatalf("second apply must be rejected, got %v", err)
	}
	if _, err := testSvc.AuditRefund(ctx, auditCmd(placed.ID, false, "  ")); !errors.Is(err, entity.ErrReasonRequired) {
		t.Fatalf("reject without reason must fail, got %v", err)
	}
	rejected, err := testSvc.AuditRefund(ctx, auditCmd(placed.ID, false, "凭证不足，暂不退款"))
	if err != nil || rejected.Duplicate {
		t.Fatalf("reject must succeed without duplicate, got %+v err=%v", rejected, err)
	}
	if got := order(t, placed.ID).Status; got != entity.OrderStatusPaid {
		t.Fatalf("order must return to PAID after reject, got %s", got)
	}
	if _, err := testSvc.ShipOrder(ctx, dto.ShipOrderCommand{OrderID: placed.ID, TrackingNo: "SF1"}); !errors.Is(err, entity.ErrCarrierRequired) {
		t.Fatalf("ship without carrier must fail, got %v", err)
	}
	shipped, err := testSvc.ShipOrder(ctx, shipCmd(placed.ID))
	if err != nil || shipped.Status != entity.OrderStatusShipped {
		t.Fatalf("ship after reject must succeed, got %+v err=%v", shipped, err)
	}
}

// 异常处置守卫：无主支付不可退款、ignore/verify 必填原因、闭环后不可重复处置。
func TestHandleExceptionActionsAndGuards(t *testing.T) {
	resetTables(t)
	ctx := context.Background()

	for _, tradeNo := range []string{"CH-EX-1", "CH-EX-2"} {
		if _, err := testSvc.PayNotify(ctx, forgedNotifyCmd(tradeNo, "ORD-MISSING", 100)); err != nil {
			t.Fatalf("notify should not error: %v", err)
		}
	}
	ex1 := pendingException(t, "CH-EX-1")
	ex2 := pendingException(t, "CH-EX-2")

	if _, err := testSvc.HandleException(ctx, handleCmd(ex1.ID, "refund", "r")); !errors.Is(err, entity.ErrExceptionNoRefund) {
		t.Fatalf("no-owner exception must not be refundable, got %v", err)
	}
	if _, err := testSvc.HandleException(ctx, handleCmd(ex1.ID, "ignore", " ")); !errors.Is(err, entity.ErrReasonRequired) {
		t.Fatalf("ignore without reason must fail, got %v", err)
	}
	ignored, err := testSvc.HandleException(ctx, handleCmd(ex1.ID, "ignore", "渠道确认误报"))
	if err != nil || ignored.Status != entity.ExcStatusClosed {
		t.Fatalf("ignore must close exception, got %+v err=%v", ignored, err)
	}
	if _, err := testSvc.HandleException(ctx, handleCmd(ex1.ID, "verify", "再核一次")); !errors.Is(err, entity.ErrExceptionClosed) {
		t.Fatalf("closed exception must reject further handling, got %v", err)
	}
	verified, err := testSvc.HandleException(ctx, handleCmd(ex2.ID, "verify", "已联系渠道核实为重试"))
	if err != nil || verified.Status != entity.ExcStatusClosed {
		t.Fatalf("verify must close exception, got %+v err=%v", verified, err)
	}
	// 无主支付没有订单：台账不追加（异常表自身留痕即可）
	if ledgerCount := len(ledgersOf(t, "", entity.LedgerException)); ledgerCount != 0 {
		t.Fatalf("no-owner handling must not append order ledgers, got %d", ledgerCount)
	}
}

func pendingException(t *testing.T, tradeNo string) *entity.ExceptionPay {
	t.Helper()
	items, err := testRepo.ListExceptions(context.Background(), true)
	if err != nil {
		t.Fatalf("list exceptions failed: %v", err)
	}
	for _, item := range items {
		if item.TradeNo == tradeNo {
			return item
		}
	}
	t.Fatalf("pending exception for %s not found", tradeNo)
	return nil
}
