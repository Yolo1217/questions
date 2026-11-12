package handler

import (
	"context"
	"testing"

	orderv1 "github.com/BwCloudWeGo/bw-cli/api/gen/order/v1"
	apperrors "github.com/BwCloudWeGo/bw-cli/pkg/errors"
)

// 期望错误码断言：统一校验 AppError 的 kind/code 稳定映射。
func mustAppError(t *testing.T, err error, wantKind apperrors.Kind, wantCode string) {
	t.Helper()
	appErr, ok := apperrors.As(err)
	if !ok {
		t.Fatalf("expected AppError, got %v", err)
	}
	if appErr.Kind != wantKind || appErr.Code != wantCode {
		t.Fatalf("expected %s/%s, got %s/%s", wantKind, wantCode, appErr.Kind, appErr.Code)
	}
}

// 框架既有订单 CRUD RPC：创建/查询/列表/更新/删除及错误路径。
func TestLegacyOrderRPCs(t *testing.T) {
	resetHandlerTables(t)
	ctx := context.Background()

	if _, err := server.CreateOrder(ctx, &orderv1.CreateOrderRequest{Name: "  "}); err == nil {
		t.Fatalf("empty name must be rejected")
	} else {
		mustAppError(t, err, apperrors.KindInvalidArgument, "invalid_order")
	}

	created, err := server.CreateOrder(ctx, &orderv1.CreateOrderRequest{Name: "示例订单", Description: "框架 CRUD"})
	if err != nil || created.GetName() != "示例订单" {
		t.Fatalf("create order failed: %+v err=%v", created, err)
	}
	found, err := server.GetOrder(ctx, &orderv1.GetOrderRequest{Id: created.GetId()})
	if err != nil || found.GetDescription() != "框架 CRUD" {
		t.Fatalf("get order failed: %+v err=%v", found, err)
	}
	if _, err := server.GetOrder(ctx, &orderv1.GetOrderRequest{Id: "NOPE"}); err == nil {
		t.Fatalf("missing order must fail")
	} else {
		mustAppError(t, err, apperrors.KindNotFound, "order_not_found")
	}

	list, err := server.ListOrders(ctx, &orderv1.ListOrdersRequest{Page: 1, PageSize: 10})
	if err != nil || list.GetTotal() != 1 || len(list.GetItems()) != 1 {
		t.Fatalf("list orders mismatch: total=%d n=%d err=%v", list.GetTotal(), len(list.GetItems()), err)
	}

	updated, err := server.UpdateOrder(ctx, &orderv1.UpdateOrderRequest{Id: created.GetId(), Name: "改名", Description: "更新后"})
	if err != nil || updated.GetName() != "改名" {
		t.Fatalf("update order failed: %+v err=%v", updated, err)
	}
	if _, err := server.UpdateOrder(ctx, &orderv1.UpdateOrderRequest{Id: "NOPE", Name: "x"}); err == nil {
		t.Fatalf("update missing order must fail")
	} else {
		mustAppError(t, err, apperrors.KindNotFound, "order_not_found")
	}

	deleted, err := server.DeleteOrder(ctx, &orderv1.DeleteOrderRequest{Id: created.GetId()})
	if err != nil || !deleted.GetSuccess() {
		t.Fatalf("delete order failed: %+v err=%v", deleted, err)
	}
	if _, err := server.DeleteOrder(ctx, &orderv1.DeleteOrderRequest{Id: created.GetId()}); err == nil {
		t.Fatalf("delete missing order must fail")
	} else {
		mustAppError(t, err, apperrors.KindNotFound, "order_not_found")
	}
}

// 交易 RPC 负路径：入参/状态/不存在订单均返回稳定错误码。
func TestTradeRPCNegativePaths(t *testing.T) {
	resetHandlerTables(t)
	ctx := context.Background()

	// 先播种商品，保证数量校验在商品存在性之后仍可单独触发
	if _, err := server.UpsertProduct(ctx, &orderv1.UpsertProductRequest{Id: "P1", Name: "羊绒围巾", PriceCents: 10000, Available: 5, OnSale: true}); err != nil {
		t.Fatalf("seed product failed: %v", err)
	}

	if _, err := server.PlaceOrder(ctx, &orderv1.PlaceOrderRequest{BuyerId: "buyer1", ProductId: "P1", Qty: 3}); err == nil {
		t.Fatalf("qty 3 must be rejected")
	} else {
		mustAppError(t, err, apperrors.KindInvalidArgument, "invalid_quantity")
	}
	if _, err := server.PlaceOrder(ctx, &orderv1.PlaceOrderRequest{BuyerId: "buyer1", ProductId: "NOPE", Qty: 1}); err == nil {
		t.Fatalf("unknown product must fail")
	} else {
		mustAppError(t, err, apperrors.KindNotFound, "product_not_found")
	}
	if _, err := server.CancelOrder(ctx, &orderv1.CancelOrderRequest{OrderId: "NOPE", BuyerId: "buyer1"}); err == nil {
		t.Fatalf("cancel unknown order must fail")
	} else {
		mustAppError(t, err, apperrors.KindNotFound, "order_not_found")
	}
	if _, err := server.ApplyRefund(ctx, &orderv1.ApplyRefundRequest{OrderId: "NOPE", BuyerId: "buyer1"}); err == nil {
		t.Fatalf("refund unknown order must fail")
	} else {
		mustAppError(t, err, apperrors.KindNotFound, "order_not_found")
	}
	if _, err := server.AuditRefund(ctx, &orderv1.AuditRefundRequest{OrderId: "NOPE", Approve: true}); err == nil {
		t.Fatalf("audit unknown order must fail")
	} else {
		mustAppError(t, err, apperrors.KindNotFound, "order_not_found")
	}
	if _, err := server.ShipOrder(ctx, &orderv1.ShipOrderRequest{OrderId: "NOPE", Carrier: "顺丰", TrackingNo: "SF"}); err == nil {
		t.Fatalf("ship unknown order must fail")
	} else {
		mustAppError(t, err, apperrors.KindNotFound, "order_not_found")
	}
	if _, err := server.GetTradeOrder(ctx, &orderv1.GetTradeOrderRequest{Id: "NOPE"}); err == nil {
		t.Fatalf("get unknown order must fail")
	} else {
		mustAppError(t, err, apperrors.KindNotFound, "order_not_found")
	}
	if _, err := server.HandleExceptionPay(ctx, &orderv1.HandleExceptionPayRequest{ExceptionId: "NOPE", Action: "verify", Reason: "x"}); err == nil {
		t.Fatalf("handle unknown exception must fail")
	} else {
		mustAppError(t, err, apperrors.KindNotFound, "exception_not_found")
	}
	if _, err := server.UpsertProduct(ctx, &orderv1.UpsertProductRequest{Id: "P1", Name: "x", PriceCents: 0}); err == nil {
		t.Fatalf("invalid price must fail")
	} else {
		mustAppError(t, err, apperrors.KindInvalidArgument, "invalid_quantity")
	}
	if _, err := server.ListExceptionPays(ctx, &orderv1.ListExceptionPaysRequest{OnlyPending: true}); err != nil {
		t.Fatalf("list exceptions failed: %v", err)
	}
	if _, err := server.ListPayCallbacks(ctx, &orderv1.ListPayCallbacksRequest{Keyword: "nothing"}); err != nil {
		t.Fatalf("list callbacks failed: %v", err)
	}
}
