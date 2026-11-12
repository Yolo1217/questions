package router_test

// 独立测试会话补充的网关 HTTP 层用例（不改业务代码）。
// 既有 order_routes_test.go 覆盖了角色拦截与下游 Internal 错误（500），
// 本文件补齐四类缺口：
//  1. 匿名（无 Authorization）与伪造 token 的统一 401；
//  2. buyer/warehouse/cs/ops 跨角色访问矩阵（既有用例只覆盖 warehouse 单向）；
//  3. 下游 gRPC 非 Internal 状态码 → HTTP 4xx 的映射（NotFound/InvalidArgument/
//     AlreadyExists/Unauthenticated），既有用例只测了 Internal → 500；
//  4. /pay/notify 载荷边界：amount_cents=0 必须在网关被拒。

import (
	"context"
	"net/http"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	orderv1 "github.com/BwCloudWeGo/bw-cli/api/gen/order/v1"
)

// statusOrderClient 让指定 RPC 返回带 gRPC 状态码的错误，用于验证网关
// apperrors.FromGRPC → HTTP 状态码的映射（复用同包 fakeOrderClient 其余方法）。
type statusOrderClient struct {
	*fakeOrderClient
	code codes.Code
	msg  string
}

func (f *statusOrderClient) GetTradeOrder(_ context.Context, _ *orderv1.GetTradeOrderRequest, _ ...grpc.CallOption) (*orderv1.OrderDetailResponse, error) {
	return nil, status.Error(f.code, f.msg)
}

func (f *statusOrderClient) PlaceOrder(_ context.Context, _ *orderv1.PlaceOrderRequest, _ ...grpc.CallOption) (*orderv1.OrderDetailResponse, error) {
	return nil, status.Error(f.code, f.msg)
}

// 鉴权 1：无 token 访问所有受保护路由 → 401 missing_token，且不触达下游。
func TestAnonymousRequestsRejectedOnTradeRoutes(t *testing.T) {
	fake := &fakeOrderClient{}
	engine := newTradeEngine(fake)
	cases := []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/products", ""},
		{http.MethodPost, "/api/v1/trade/orders", buyerOrderJSON},
		{http.MethodGet, "/api/v1/trade/orders/mine", ""},
		{http.MethodGet, "/api/v1/trade/orders/SO-1", ""},
		{http.MethodPost, "/api/v1/trade/orders/SO-1/cancel", ""},
		{http.MethodPost, "/api/v1/trade/orders/SO-1/refund", ""},
		{http.MethodPost, "/api/v1/pay/mock", `{"order_id":"SO-1"}`},
		{http.MethodGet, "/api/v1/warehouse/orders", ""},
		{http.MethodPost, "/api/v1/warehouse/orders/SO-1/ship", `{"carrier":"顺丰","tracking_no":"SF-1"}`},
		{http.MethodGet, "/api/v1/cs/refunds", ""},
		{http.MethodGet, "/api/v1/cs/exceptions", ""},
		{http.MethodGet, "/api/v1/ops/summary", ""},
	}
	for _, tc := range cases {
		rec := doJSON(t, engine, tc.method, tc.path, "", tc.body)
		expectError(t, rec, http.StatusUnauthorized, "missing_token")
	}
	if fake.placeOrder != nil || fake.payNotify != nil || fake.shipOrder != nil {
		t.Fatalf("anonymous requests must never reach downstream")
	}
}

// 鉴权 2：伪造/篡改 token → 401 invalid_token。
func TestTamperedTokenRejected(t *testing.T) {
	engine := newTradeEngine(&fakeOrderClient{})
	rec := doJSON(t, engine, http.MethodGet, "/api/v1/products",
		"eyJhbGciOiJIUzI1NiJ9.tampered.payload", "")
	expectError(t, rec, http.StatusUnauthorized, "invalid_token")
}

// 越权：跨角色访问矩阵——buyer 不能进客服/运营/仓库，warehouse 不能进客服/运营，
// cs 不能进运营/仓库，ops 不能进客服/仓库。全部 401 且不触达下游。
func TestCrossRoleDenialMatrix(t *testing.T) {
	fake := &fakeOrderClient{}
	engine := newTradeEngine(fake)
	deny := []struct{ role, method, path, body string }{
		{"buyer", http.MethodGet, "/api/v1/cs/refunds", ""},
		{"buyer", http.MethodPost, "/api/v1/cs/refunds/SO-1/audit", `{"approve":true,"reason":"r"}`},
		{"buyer", http.MethodGet, "/api/v1/cs/exceptions", ""},
		{"buyer", http.MethodGet, "/api/v1/ops/summary", ""},
		{"buyer", http.MethodGet, "/api/v1/warehouse/orders", ""},
		{"warehouse", http.MethodGet, "/api/v1/cs/refunds", ""},
		{"warehouse", http.MethodGet, "/api/v1/ops/orders", ""},
		{"warehouse", http.MethodGet, "/api/v1/trade/orders/mine", ""},
		{"cs", http.MethodGet, "/api/v1/ops/summary", ""},
		{"cs", http.MethodGet, "/api/v1/warehouse/orders", ""},
		{"cs", http.MethodPost, "/api/v1/trade/orders", buyerOrderJSON},
		{"ops", http.MethodGet, "/api/v1/cs/callbacks/CH-1", ""},
		{"ops", http.MethodPost, "/api/v1/warehouse/orders/SO-1/ship", `{"carrier":"顺丰","tracking_no":"SF-1"}`},
		{"ops", http.MethodPost, "/api/v1/trade/orders", buyerOrderJSON},
	}
	for _, tc := range deny {
		rec := doJSON(t, engine, tc.method, tc.path, tokenFor(t, tc.role, tc.role+"-1", tc.role), tc.body)
		expectError(t, rec, http.StatusUnauthorized, "permission_denied")
	}
	if fake.placeOrder != nil || fake.auditRefund != nil || fake.shipOrder != nil {
		t.Fatalf("denied requests must never reach downstream")
	}
}

// 下游错误映射：gRPC NotFound/InvalidArgument/AlreadyExists/Unauthenticated/Internal
// 应还原为 HTTP 404/400/409/401/500，错误体保留业务 code（用 "code|message" 约定拆分）。
func TestDownstreamGRPCStatusMappedToHTTP(t *testing.T) {
	cases := []struct {
		code       codes.Code
		msg        string
		method     string
		path       string
		body       string
		wantStatus int
		wantCode   string
	}{
		{codes.NotFound, "order_not_found|订单不存在", http.MethodGet, "/api/v1/trade/orders/SO-404", "", http.StatusNotFound, "order_not_found"},
		{codes.InvalidArgument, "invalid_quantity|每单数量仅 1~2 件", http.MethodPost, "/api/v1/trade/orders", buyerOrderJSON, http.StatusBadRequest, "invalid_quantity"},
		{codes.AlreadyExists, "insufficient_stock|库存不足", http.MethodPost, "/api/v1/trade/orders", buyerOrderJSON, http.StatusConflict, "insufficient_stock"},
		{codes.Unauthenticated, "permission_denied|越权拒绝", http.MethodGet, "/api/v1/trade/orders/SO-1", "", http.StatusUnauthorized, "permission_denied"},
		{codes.Internal, "internal_error|boom", http.MethodGet, "/api/v1/trade/orders/SO-1", "", http.StatusInternalServerError, "internal_error"},
		{codes.Unavailable, "trade_service_error|down", http.MethodGet, "/api/v1/trade/orders/SO-1", "", http.StatusInternalServerError, "trade_service_error"},
	}
	for _, tc := range cases {
		client := &statusOrderClient{fakeOrderClient: &fakeOrderClient{}, code: tc.code, msg: tc.msg}
		engine := newTradeEngine(client)
		token := tokenFor(t, "buyer", "b-1", "阿禾")
		rec := doJSON(t, engine, tc.method, tc.path, token, tc.body)
		expectError(t, rec, tc.wantStatus, tc.wantCode)
	}
}

// 参数校验边界：/pay/notify 的 amount_cents=0（未下单金额）必须在网关被拒，
// 不得透传到订单服务。
func TestPayNotifyRejectsNonPositiveAmount(t *testing.T) {
	fake := &fakeOrderClient{}
	engine := newTradeEngine(fake)
	rec := doJSON(t, engine, http.MethodPost, "/api/v1/pay/notify", "",
		`{"channel_trade_no":"CH-1","order_id":"SO-1","amount_cents":0,"signature":"x"}`)
	expectError(t, rec, http.StatusBadRequest, "invalid_request")
	if fake.payNotify != nil {
		t.Fatalf("invalid notify payload must not reach downstream")
	}
}
