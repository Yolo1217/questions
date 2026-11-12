package service

// 独立测试会话补充的边界用例（不改业务代码）。
// 覆盖题面状态机/红线中"现有测试未锁定的路径"：
//  1. 未支付订单不可发货（S1 发货前提 + 红线 3 正面）——TestShipRejectsClosedAndRefundedOrder
//     只测了 CLOSED/REFUNDED，CREATED 分支漏测；
//  2. 关单后申请售前退款必须拒绝——E18 只测了 SHIPPED，CLOSED 是用户真实可达路径
//     （超时关单/取消后点退款）；
//  3. 已付款订单不可走"取消"，只能走退款——题面状态机表取消迁移 from=CREATED，
//     PAID 分支漏测；
//  4. 空买家身份下单必须拒绝——网关从 JWT 注入，但 gRPC 层需自有防线；
//  5. 超时关单与支付回调并发只产生一种合法结果——E04/E07 分别测过，交集未测；
//  6. 同一伪造流水号重复重推不得刷爆异常表（怀疑缺陷路径，先写失败测试再下结论）。

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/BwCloudWeGo/bw-cli/internal/order/dto"
	"github.com/BwCloudWeGo/bw-cli/internal/order/entity"
)

// 边界 1：CREATED（未支付）订单发货必须拒绝，不产生发货台账。
func TestShipUnpaidOrderRejected(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	placed, err := testSvc.PlaceOrder(ctx, placeCmd("buyer1", "P1", 1))
	if err != nil {
		t.Fatalf("place order failed: %v", err)
	}
	if _, err := testSvc.ShipOrder(ctx, shipCmd(placed.ID)); !errors.Is(err, entity.ErrInvalidTransition) {
		t.Fatalf("shipping an unpaid (CREATED) order must be rejected, got %v", err)
	}
	if got := order(t, placed.ID).Status; got != entity.OrderStatusCreated {
		t.Fatalf("order must stay CREATED, got %s", got)
	}
	if ships := ledgersOf(t, placed.ID, entity.LedgerShip); len(ships) != 0 {
		t.Fatalf("unpaid ship must not write ship ledger, got %d", len(ships))
	}
}

// 边界 2：CLOSED / CREATED 订单申请售前退款必须拒绝，不写退款台账。
func TestApplyRefundOnClosedAndCreatedOrderRejected(t *testing.T) {
	resetTables(t)
	ctx := context.Background()

	closed, err := testSvc.PlaceOrder(ctx, placeCmd("buyer1", "P1", 1))
	if err != nil {
		t.Fatalf("place order failed: %v", err)
	}
	if _, err := testSvc.CancelOrder(ctx, closed.ID, "buyer1"); err != nil {
		t.Fatalf("cancel failed: %v", err)
	}
	if _, err := testSvc.ApplyRefund(ctx, closed.ID, "buyer1"); !errors.Is(err, entity.ErrInvalidTransition) {
		t.Fatalf("refund on CLOSED order must be rejected, got %v", err)
	}
	if got := order(t, closed.ID).Status; got != entity.OrderStatusClosed {
		t.Fatalf("closed order must stay CLOSED, got %s", got)
	}
	if refunds := ledgersOf(t, closed.ID, entity.LedgerRefund); len(refunds) != 0 {
		t.Fatalf("closed order must not gain refund ledger, got %d", len(refunds))
	}

	created, err := testSvc.PlaceOrder(ctx, placeCmd("buyer2", "P1", 1))
	if err != nil {
		t.Fatalf("place order failed: %v", err)
	}
	if _, err := testSvc.ApplyRefund(ctx, created.ID, "buyer2"); !errors.Is(err, entity.ErrInvalidTransition) {
		t.Fatalf("refund on CREATED (unpaid) order must be rejected, got %v", err)
	}
	if got := order(t, created.ID).Status; got != entity.OrderStatusCreated {
		t.Fatalf("created order must stay CREATED, got %s", got)
	}
}

// 边界 3：已付款订单不能取消（只能走退款），取消不得释放库存。
func TestCancelPaidOrderRejected(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	placed, err := testSvc.PlaceOrder(ctx, placeCmd("buyer1", "P1", 1))
	if err != nil {
		t.Fatalf("place order failed: %v", err)
	}
	if _, err := testSvc.PayNotify(ctx, notifyCmd("CH-CANCEL-PAID", placed.ID, placed.AmountCents)); err != nil {
		t.Fatalf("pay notify failed: %v", err)
	}
	if got := product(t, "P1").Available; got != 4 {
		t.Fatalf("precondition available must be 4, got %d", got)
	}
	if _, err := testSvc.CancelOrder(ctx, placed.ID, "buyer1"); !errors.Is(err, entity.ErrInvalidTransition) {
		t.Fatalf("cancel on PAID order must be rejected, got %v", err)
	}
	if got := order(t, placed.ID).Status; got != entity.OrderStatusPaid {
		t.Fatalf("order must stay PAID, got %s", got)
	}
	if got := product(t, "P1").Available; got != 4 {
		t.Fatalf("paid order cancel must not release stock: expected 4, got %d", got)
	}
	if pays := ledgersOf(t, placed.ID, entity.LedgerPay); len(pays) != 1 {
		t.Fatalf("pay ledger must stay 1, got %d", len(pays))
	}
}

// 边界 4：空买家身份下单必须拒绝，不产单、不扣库存。
func TestPlaceOrderRequiresBuyerIdentity(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	if _, err := testSvc.PlaceOrder(ctx, dto.PlaceOrderCommand{BuyerID: "  ", ProductID: "P1", Qty: 1}); !errors.Is(err, entity.ErrPermissionDenied) {
		t.Fatalf("empty buyer must be denied, got %v", err)
	}
	if got := product(t, "P1").Available; got != 5 {
		t.Fatalf("stock must be untouched, got %d", got)
	}
	list, err := testSvc.ListOrders(ctx, "", "")
	if err != nil || list.Total != 0 {
		t.Fatalf("no order must be created, got total=%d err=%v", list.Total, err)
	}
}

// 边界 5：超时关单与支付回调并发——只能收敛为一种合法结果：
// 要么 PAID（入账且不释放库存），要么 CLOSED（释放库存且不产生支付流水）。
//
// 独立测试记录：曾观察到第三种交错——PayNotify 先读到 CREATED，SweepExpired
// 抢先关单释放库存，PayNotify 的迁移 rows=0 被判"并发重复"返回成功，
// 结果 CLOSED + 回调 DONE + 无支付流水 + 无「关单后到账」异常（见独立测试
// 记录缺陷 D-2）。本用例只锁定所有交错下都必须成立的资金/库存不变式。
func TestSweepAndPayRaceSingleOutcome(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	sweepSvc := NewTradeService(testRepo, TradeConfig{Keep: time.Nanosecond, RefundFailFirst: false}, nil)
	placed, err := sweepSvc.PlaceOrder(ctx, placeCmd("buyer1", "P1", 1))
	if err != nil {
		t.Fatalf("place order failed: %v", err)
	}
	var (
		wg        sync.WaitGroup
		payResult *dto.PayNotifyResult
		payErr    error
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		payResult, payErr = testSvc.PayNotify(ctx, notifyCmd("CH-SWEEP-RACE", placed.ID, placed.AmountCents))
	}()
	go func() {
		defer wg.Done()
		if _, sweepErr := sweepSvc.SweepExpired(ctx); sweepErr != nil {
			t.Errorf("sweep failed: %v", sweepErr)
		}
	}()
	wg.Wait()
	if payErr != nil {
		t.Fatalf("pay notify failed: %v", payErr)
	}
	if payResult == nil || !payResult.Success {
		t.Fatalf("pay notify must return success to stop channel retry, got %+v", payResult)
	}
	switch got := order(t, placed.ID).Status; got {
	case entity.OrderStatusPaid:
		if pays := ledgersOf(t, placed.ID, entity.LedgerPay); len(pays) != 1 {
			t.Fatalf("paid outcome must have exactly 1 pay ledger, got %d", len(pays))
		}
		if avail := product(t, "P1").Available; avail != 4 {
			t.Fatalf("paid outcome must not release stock, got %d", avail)
		}
	case entity.OrderStatusClosed:
		if pays := ledgersOf(t, placed.ID, entity.LedgerPay); len(pays) != 0 {
			t.Fatalf("closed outcome must have no pay ledger, got %d", len(pays))
		}
		if avail := product(t, "P1").Available; avail != 5 {
			t.Fatalf("closed outcome must release stock once, got %d", avail)
		}
	default:
		t.Fatalf("race must end in PAID or CLOSED, got %s", got)
	}
}

// 怀疑缺陷路径：同一伪造流水号反复重推验签失败的回调，每次都会新增一条
// 待处理「验签失败」异常。若成立，伪造流量可无限刷异常工单，且
// FindExceptionByTradeNo 反查只取最新一条，历史工单堆积（低严重度）。
// 断言按合理口径：同一渠道流水号的重复伪造重推不应重复产生待处理工单。
func TestForgedSignatureRepeatDoesNotFloodExceptionDesk(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		result, err := testSvc.PayNotify(ctx, forgedNotifyCmd("CH-FLOOD", "ORD-ANY", 100))
		if err != nil {
			t.Fatalf("forged notify must not error: %v", err)
		}
		if result.Success || result.Code != "verify_failed" {
			t.Fatalf("forged notify must be verify_failed, got %+v", result)
		}
	}
	exceptions, err := testSvc.ListExceptions(ctx, true)
	if err != nil {
		t.Fatalf("list exceptions failed: %v", err)
	}
	if len(exceptions) > 1 {
		t.Fatalf("repeated forged retry with same trade no must not flood pending exceptions: got %d", len(exceptions))
	}
}
