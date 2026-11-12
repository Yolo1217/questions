package handler

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"go.uber.org/zap"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	orderv1 "github.com/BwCloudWeGo/bw-cli/api/gen/order/v1"
	"github.com/BwCloudWeGo/bw-cli/internal/order/entity"
	"github.com/BwCloudWeGo/bw-cli/internal/order/repo"
	"github.com/BwCloudWeGo/bw-cli/internal/order/service"
	apperrors "github.com/BwCloudWeGo/bw-cli/pkg/errors"
)

// handler 直测使用独立测试库 questions_test_handler，与其他包测试并行互不干扰。
var (
	handlerDB *gorm.DB
	server    *Server
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
	bootstrap, err := gorm.Open(mysql.Open("root:root@tcp(127.0.0.1:3306)/?charset=utf8mb4&parseTime=True&loc=Local"), &gorm.Config{Logger: gormlogger.Discard})
	if err != nil {
		exitNoDB("handler tests: mysql unavailable", err)
	}
	if err := bootstrap.Exec("CREATE DATABASE IF NOT EXISTS questions_test_handler DEFAULT CHARACTER SET utf8mb4").Error; err != nil {
		exitNoDB("handler tests: create test database failed", err)
	}
	db, err := gorm.Open(mysql.Open("root:root@tcp(127.0.0.1:3306)/questions_test_handler?charset=utf8mb4&parseTime=True&loc=Local"), &gorm.Config{Logger: gormlogger.Discard})
	if err != nil {
		exitNoDB("handler tests: mysql unavailable", err)
	}
	if err := repo.AutoMigrate(db); err != nil {
		exitNoDB("handler tests: migrate failed", err)
	}
	handlerDB = db
	orderRepo := repo.NewGormRepository(db, zap.NewNop())
	tradeSvc := service.NewTradeService(orderRepo, service.TradeConfig{Keep: 10 * time.Minute, RefundFailFirst: false}, nil)
	server = NewServer(service.NewService(orderRepo, zap.NewNop()), tradeSvc, nil)
	os.Exit(m.Run())
}

func resetHandlerTables(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	if _, err := server.TradeSummary(ctx, &orderv1.TradeSummaryRequest{}); err != nil {
		t.Fatalf("service ready check failed: %v", err)
	}
	for _, table := range []string{"orders", "shop_trade_ledgers", "shop_pay_callbacks", "shop_exception_pays", "shop_trade_orders", "shop_products"} {
		if err := handlerDB.Exec("DELETE FROM " + table).Error; err != nil {
			t.Fatalf("reset %s failed: %v", table, err)
		}
	}
}

// TestTradeRPCFlow 走通全部交易 RPC：列表/维护/下单/取消/支付/发货/退款/反查/异常/汇总。
func TestTradeRPCFlow(t *testing.T) {
	resetHandlerTables(t)
	ctx := context.Background()

	products, err := server.ListProducts(ctx, &orderv1.ListProductsRequest{})
	if err != nil || len(products.GetItems()) != 0 {
		t.Fatalf("empty catalog expected, got n=%d err=%v", len(products.GetItems()), err)
	}
	seeded, err := server.UpsertProduct(ctx, &orderv1.UpsertProductRequest{Id: "P1", Name: "羊绒围巾", PriceCents: 10000, Available: 5, OnSale: true})
	if err != nil || seeded.GetOnSale() != true {
		t.Fatalf("upsert product failed: %+v err=%v", seeded, err)
	}

	placed, err := server.PlaceOrder(ctx, &orderv1.PlaceOrderRequest{BuyerId: "buyer1", BuyerName: "阿禾", ProductId: "P1", Qty: 1})
	if err != nil || placed.GetStatus() != entity.OrderStatusCreated {
		t.Fatalf("place order failed: %+v err=%v", placed, err)
	}
	if len(placed.GetLedgers()) != 1 {
		t.Fatalf("order detail must carry stock ledger, got %d", len(placed.GetLedgers()))
	}

	forgotten, err := server.PlaceOrder(ctx, &orderv1.PlaceOrderRequest{BuyerId: "buyer2", BuyerName: "北舟", ProductId: "P1", Qty: 1})
	if err != nil {
		t.Fatalf("second order failed: %v", err)
	}
	if _, err := server.CancelOrder(ctx, &orderv1.CancelOrderRequest{OrderId: forgotten.GetId(), BuyerId: "buyer2"}); err != nil {
		t.Fatalf("cancel failed: %v", err)
	}
	if _, err := server.CancelOrder(ctx, &orderv1.CancelOrderRequest{OrderId: forgotten.GetId(), BuyerId: "buyer2"}); err == nil {
		t.Fatalf("second cancel must be rejected")
	}

	// 支付（携带正确签名）
	sign := func(tradeNo string, orderID string, amount int64) string {
		return service.SignPayNotify(service.DefaultChannelSecret, tradeNo, orderID, amount)
	}
	notify, err := server.PayNotify(ctx, &orderv1.PayNotifyRequest{
		ChannelTradeNo: "CH-H1", OrderId: placed.GetId(), AmountCents: placed.GetAmountCents(),
		Signature: sign("CH-H1", placed.GetId(), placed.GetAmountCents()),
	})
	if err != nil || !notify.GetSuccess() || notify.GetCode() != "success" {
		t.Fatalf("pay notify failed: %+v err=%v", notify, err)
	}
	if dup, err := server.PayNotify(ctx, &orderv1.PayNotifyRequest{
		ChannelTradeNo: "CH-H1", OrderId: placed.GetId(), AmountCents: placed.GetAmountCents(),
		Signature: sign("CH-H1", placed.GetId(), placed.GetAmountCents()),
	}); err != nil || !dup.GetDuplicate() {
		t.Fatalf("duplicate notify must be idempotent, got %+v err=%v", dup, err)
	}

	mine, err := server.ListMyOrders(ctx, &orderv1.ListMyOrdersRequest{BuyerId: "buyer1"})
	if err != nil || mine.GetTotal() != 1 {
		t.Fatalf("list my orders failed: total=%d err=%v", mine.GetTotal(), err)
	}
	byStatus, err := server.ListOrdersByStatus(ctx, &orderv1.ListOrdersByStatusRequest{Status: entity.OrderStatusPaid})
	if err != nil || len(byStatus.GetItems()) != 1 {
		t.Fatalf("list by status failed: n=%d err=%v", len(byStatus.GetItems()), err)
	}

	shipped, err := server.ShipOrder(ctx, &orderv1.ShipOrderRequest{OrderId: placed.GetId(), Carrier: "顺丰速运", TrackingNo: "SF001", Operator: "仓管小周"})
	if err != nil || shipped.GetStatus() != entity.OrderStatusShipped {
		t.Fatalf("ship failed: %+v err=%v", shipped, err)
	}

	// 另一单退款：申请 → 审核通过 → 幂等重复审核
	refundedOrder, err := server.PlaceOrder(ctx, &orderv1.PlaceOrderRequest{BuyerId: "buyer1", BuyerName: "阿禾", ProductId: "P1", Qty: 1})
	if err != nil {
		t.Fatalf("refund order place failed: %v", err)
	}
	if _, err := server.PayNotify(ctx, &orderv1.PayNotifyRequest{
		ChannelTradeNo: "CH-H2", OrderId: refundedOrder.GetId(), AmountCents: refundedOrder.GetAmountCents(),
		Signature: sign("CH-H2", refundedOrder.GetId(), refundedOrder.GetAmountCents()),
	}); err != nil {
		t.Fatalf("pay notify failed: %v", err)
	}
	if _, err := server.ApplyRefund(ctx, &orderv1.ApplyRefundRequest{OrderId: refundedOrder.GetId(), BuyerId: "buyer1"}); err != nil {
		t.Fatalf("apply refund failed: %v", err)
	}
	audited, err := server.AuditRefund(ctx, &orderv1.AuditRefundRequest{OrderId: refundedOrder.GetId(), Approve: true, Reason: "同意", Operator: "客服小时"})
	if err != nil || audited.GetOrder().GetStatus() != entity.OrderStatusRefunded {
		t.Fatalf("audit refund failed: %+v err=%v", audited, err)
	}
	dupAudit, err := server.AuditRefund(ctx, &orderv1.AuditRefundRequest{OrderId: refundedOrder.GetId(), Approve: true, Reason: "再次", Operator: "客服小时"})
	if err != nil || !dupAudit.GetDuplicate() {
		t.Fatalf("duplicate audit must be idempotent, got %+v err=%v", dupAudit, err)
	}

	// 异常：伪造签名通知（验签前置：不落回调记录，落异常表）→ 异常列表 → 处置 → 合法回调反查
	if _, err := server.PayNotify(ctx, &orderv1.PayNotifyRequest{ChannelTradeNo: "CH-H3", OrderId: "ORD-MISSING", AmountCents: 1, Signature: "bad"}); err != nil {
		t.Fatalf("forged notify should not error: %v", err)
	}
	exs, err := server.ListExceptionPays(ctx, &orderv1.ListExceptionPaysRequest{OnlyPending: true})
	if err != nil || len(exs.GetItems()) != 1 {
		t.Fatalf("pending exception expected, got n=%d err=%v", len(exs.GetItems()), err)
	}
	if exs.GetItems()[0].GetType() != entity.ExcBadSignature {
		t.Fatalf("expected verify_failed exception, got %s", exs.GetItems()[0].GetType())
	}
	closed, err := server.HandleExceptionPay(ctx, &orderv1.HandleExceptionPayRequest{
		ExceptionId: exs.GetItems()[0].GetId(), Action: "ignore", Reason: "渠道确认误报", Operator: "客服小时",
	})
	if err != nil || closed.GetStatus() != entity.ExcStatusClosed {
		t.Fatalf("handle exception failed: %+v err=%v", closed, err)
	}
	// 伪造流水号不得出现在回调表；反查走合法支付流水 CH-H1
	callback, err := server.GetPayCallback(ctx, &orderv1.GetPayCallbackRequest{TradeNo: "CH-H1"})
	if err != nil || callback.GetStatus() != entity.CallbackDone {
		t.Fatalf("callback lookup failed: %+v err=%v", callback, err)
	}
	if _, err := server.GetPayCallback(ctx, &orderv1.GetPayCallbackRequest{TradeNo: "CH-H3"}); err == nil {
		t.Fatalf("forged trade no must not persist callback")
	}
	callbacks, err := server.ListPayCallbacks(ctx, &orderv1.ListPayCallbacksRequest{Keyword: "CH-H1"})
	if err != nil || len(callbacks.GetItems()) != 1 {
		t.Fatalf("callback list failed: n=%d err=%v", len(callbacks.GetItems()), err)
	}
	if _, err := server.GetPayCallback(ctx, &orderv1.GetPayCallbackRequest{TradeNo: "CH-NOPE"}); err == nil {
		t.Fatalf("unknown trade no must fail")
	}

	detail, err := server.GetTradeOrder(ctx, &orderv1.GetTradeOrderRequest{Id: placed.GetId()})
	if err != nil || detail.GetStatus() != entity.OrderStatusShipped {
		t.Fatalf("get trade order failed: %+v err=%v", detail, err)
	}

	summary, err := server.TradeSummary(ctx, &orderv1.TradeSummaryRequest{})
	if err != nil {
		t.Fatalf("summary failed: %v", err)
	}
	if summary.GetPaidTotalCents() != placed.GetAmountCents()+refundedOrder.GetAmountCents() {
		t.Fatalf("paid total mismatch: %d", summary.GetPaidTotalCents())
	}
	if summary.GetRefundedTotalCents() != refundedOrder.GetAmountCents() {
		t.Fatalf("refunded total mismatch: %d", summary.GetRefundedTotalCents())
	}
	if _, ok := summary.GetOrdersByStatus()[entity.OrderStatusShipped]; !ok {
		t.Fatalf("status distribution must contain SHIPPED: %v", summary.GetOrdersByStatus())
	}
}

// 错误映射表：每个业务错误都映射为稳定的应用错误。
func TestMapTradeErrorCoversAllSentinels(t *testing.T) {
	cases := []struct {
		err  error
		kind apperrors.Kind
		code string
	}{
		{entity.ErrPermissionDenied, apperrors.KindUnauthorized, "permission_denied"},
		{entity.ErrInsufficientStock, apperrors.KindConflict, "insufficient_stock"},
		{entity.ErrProductOffSale, apperrors.KindConflict, "product_off_sale"},
		{entity.ErrProductNotFound, apperrors.KindNotFound, "product_not_found"},
		{entity.ErrInvalidQty, apperrors.KindInvalidArgument, "invalid_quantity"},
		{entity.ErrTradeOrderNotFound, apperrors.KindNotFound, "order_not_found"},
		{entity.ErrInvalidTransition, apperrors.KindConflict, "invalid_status_transition"},
		{entity.ErrOrderRefunding, apperrors.KindConflict, "order_refunding"},
		{entity.ErrOrderAlreadyShipped, apperrors.KindConflict, "order_already_shipped"},
		{entity.ErrRefundNotAllowed, apperrors.KindConflict, "refund_not_allowed"},
		{entity.ErrCarrierRequired, apperrors.KindInvalidArgument, "carrier_required"},
		{entity.ErrReasonRequired, apperrors.KindInvalidArgument, "reason_required"},
		{entity.ErrChannelRefundFailed, apperrors.KindConflict, "channel_refund_failed"},
		{entity.ErrExceptionNotFound, apperrors.KindNotFound, "exception_not_found"},
		{entity.ErrExceptionClosed, apperrors.KindConflict, "exception_closed"},
		{entity.ErrExceptionNoRefund, apperrors.KindInvalidArgument, "exception_no_refund"},
		{entity.ErrCallbackNotFound, apperrors.KindNotFound, "callback_not_found"},
		{entity.ErrOrderClosed, apperrors.KindConflict, "order_closed"},
		{entity.ErrOrderRefunded, apperrors.KindConflict, "order_refunded"},
		{errors.New("boom"), apperrors.KindInternal, "trade_service_error"},
	}
	for _, tc := range cases {
		mapped := mapTradeError(tc.err)
		appErr, ok := apperrors.As(mapped)
		if !ok {
			t.Fatalf("%v must map to AppError, got %v", tc.err, mapped)
		}
		if appErr.Kind != tc.kind || appErr.Code != tc.code {
			t.Fatalf("%v must map to %s/%s, got %s/%s", tc.err, tc.kind, tc.code, appErr.Kind, appErr.Code)
		}
	}
}
