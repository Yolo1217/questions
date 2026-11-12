package router_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	orderv1 "github.com/BwCloudWeGo/bw-cli/api/gen/order/v1"
	"github.com/BwCloudWeGo/bw-cli/internal/gateway/client"
	"github.com/BwCloudWeGo/bw-cli/internal/gateway/router"
	"github.com/BwCloudWeGo/bw-cli/pkg/config"
	"github.com/BwCloudWeGo/bw-cli/pkg/middleware"
)

// fakeOrderClient 实现生成的 OrderServiceClient 接口，用于在网关层回放
// 交易链路的正常与失败分支；通过 captures 记录下游收到的请求以便断言。
type fakeOrderClient struct {
	err error // 非 nil 时所有方法返回该 gRPC 错误

	detail *orderv1.OrderDetailResponse

	placeOrder     *orderv1.PlaceOrderRequest
	payNotify      *orderv1.PayNotifyRequest
	shipOrder      *orderv1.ShipOrderRequest
	auditRefund    *orderv1.AuditRefundRequest
	listByStatus   *orderv1.ListOrdersByStatusRequest
	handleExcep    *orderv1.HandleExceptionPayRequest
	upsertProduct  *orderv1.UpsertProductRequest
	cancelOrder    *orderv1.CancelOrderRequest
	applyRefund    *orderv1.ApplyRefundRequest
	handleExID     string
	listTradeOrder string
}

func (f *fakeOrderClient) fail() error {
	return status.Error(codes.Internal, "internal_error|fake downstream failure")
}

func (f *fakeOrderClient) CreateOrder(context.Context, *orderv1.CreateOrderRequest, ...grpc.CallOption) (*orderv1.OrderResponse, error) {
	if f.err != nil {
		return nil, f.fail()
	}
	return &orderv1.OrderResponse{Id: "o-1"}, nil
}

func (f *fakeOrderClient) GetOrder(context.Context, *orderv1.GetOrderRequest, ...grpc.CallOption) (*orderv1.OrderResponse, error) {
	if f.err != nil {
		return nil, f.fail()
	}
	return &orderv1.OrderResponse{Id: "o-1"}, nil
}

func (f *fakeOrderClient) ListOrders(context.Context, *orderv1.ListOrdersRequest, ...grpc.CallOption) (*orderv1.ListOrdersResponse, error) {
	if f.err != nil {
		return nil, f.fail()
	}
	return &orderv1.ListOrdersResponse{}, nil
}

func (f *fakeOrderClient) UpdateOrder(context.Context, *orderv1.UpdateOrderRequest, ...grpc.CallOption) (*orderv1.OrderResponse, error) {
	if f.err != nil {
		return nil, f.fail()
	}
	return &orderv1.OrderResponse{Id: "o-1"}, nil
}

func (f *fakeOrderClient) DeleteOrder(context.Context, *orderv1.DeleteOrderRequest, ...grpc.CallOption) (*orderv1.DeleteOrderResponse, error) {
	if f.err != nil {
		return nil, f.fail()
	}
	return &orderv1.DeleteOrderResponse{}, nil
}

func (f *fakeOrderClient) ListProducts(context.Context, *orderv1.ListProductsRequest, ...grpc.CallOption) (*orderv1.ListProductsResponse, error) {
	if f.err != nil {
		return nil, f.fail()
	}
	return &orderv1.ListProductsResponse{}, nil
}

func (f *fakeOrderClient) UpsertProduct(_ context.Context, in *orderv1.UpsertProductRequest, _ ...grpc.CallOption) (*orderv1.ProductResponse, error) {
	if f.err != nil {
		return nil, f.fail()
	}
	f.upsertProduct = in
	return &orderv1.ProductResponse{Id: in.GetId()}, nil
}

func (f *fakeOrderClient) PlaceOrder(_ context.Context, in *orderv1.PlaceOrderRequest, _ ...grpc.CallOption) (*orderv1.OrderDetailResponse, error) {
	if f.err != nil {
		return nil, f.fail()
	}
	f.placeOrder = in
	return f.detailOf("SO-NEW"), nil
}

func (f *fakeOrderClient) CancelOrder(_ context.Context, in *orderv1.CancelOrderRequest, _ ...grpc.CallOption) (*orderv1.OrderDetailResponse, error) {
	if f.err != nil {
		return nil, f.fail()
	}
	f.cancelOrder = in
	return f.detailOf(in.GetOrderId()), nil
}

func (f *fakeOrderClient) ListMyOrders(context.Context, *orderv1.ListMyOrdersRequest, ...grpc.CallOption) (*orderv1.ListTradeOrdersResponse, error) {
	if f.err != nil {
		return nil, f.fail()
	}
	return &orderv1.ListTradeOrdersResponse{}, nil
}

func (f *fakeOrderClient) ApplyRefund(_ context.Context, in *orderv1.ApplyRefundRequest, _ ...grpc.CallOption) (*orderv1.OrderDetailResponse, error) {
	if f.err != nil {
		return nil, f.fail()
	}
	f.applyRefund = in
	return f.detailOf(in.GetOrderId()), nil
}

func (f *fakeOrderClient) GetTradeOrder(_ context.Context, in *orderv1.GetTradeOrderRequest, _ ...grpc.CallOption) (*orderv1.OrderDetailResponse, error) {
	if f.err != nil {
		return nil, f.fail()
	}
	f.listTradeOrder = in.GetId()
	return f.detail, nil
}

func (f *fakeOrderClient) ListOrdersByStatus(_ context.Context, in *orderv1.ListOrdersByStatusRequest, _ ...grpc.CallOption) (*orderv1.ListTradeOrdersResponse, error) {
	if f.err != nil {
		return nil, f.fail()
	}
	f.listByStatus = in
	return &orderv1.ListTradeOrdersResponse{}, nil
}

func (f *fakeOrderClient) PayNotify(_ context.Context, in *orderv1.PayNotifyRequest, _ ...grpc.CallOption) (*orderv1.PayNotifyResponse, error) {
	if f.err != nil {
		return nil, f.fail()
	}
	f.payNotify = in
	return &orderv1.PayNotifyResponse{Success: true, Code: "paid"}, nil
}

func (f *fakeOrderClient) GetPayCallback(context.Context, *orderv1.GetPayCallbackRequest, ...grpc.CallOption) (*orderv1.PayCallbackResponse, error) {
	if f.err != nil {
		return nil, f.fail()
	}
	return &orderv1.PayCallbackResponse{}, nil
}

func (f *fakeOrderClient) ListPayCallbacks(context.Context, *orderv1.ListPayCallbacksRequest, ...grpc.CallOption) (*orderv1.ListPayCallbacksResponse, error) {
	if f.err != nil {
		return nil, f.fail()
	}
	return &orderv1.ListPayCallbacksResponse{}, nil
}

func (f *fakeOrderClient) AuditRefund(_ context.Context, in *orderv1.AuditRefundRequest, _ ...grpc.CallOption) (*orderv1.AuditRefundResponse, error) {
	if f.err != nil {
		return nil, f.fail()
	}
	f.auditRefund = in
	return &orderv1.AuditRefundResponse{Order: f.detailOf(in.GetOrderId()), Duplicate: true}, nil
}

func (f *fakeOrderClient) ListExceptionPays(context.Context, *orderv1.ListExceptionPaysRequest, ...grpc.CallOption) (*orderv1.ListExceptionPaysResponse, error) {
	if f.err != nil {
		return nil, f.fail()
	}
	return &orderv1.ListExceptionPaysResponse{}, nil
}

func (f *fakeOrderClient) HandleExceptionPay(_ context.Context, in *orderv1.HandleExceptionPayRequest, _ ...grpc.CallOption) (*orderv1.ExceptionPayResponse, error) {
	if f.err != nil {
		return nil, f.fail()
	}
	f.handleExcep = in
	f.handleExID = in.GetExceptionId()
	return &orderv1.ExceptionPayResponse{Id: in.GetExceptionId()}, nil
}

func (f *fakeOrderClient) ShipOrder(_ context.Context, in *orderv1.ShipOrderRequest, _ ...grpc.CallOption) (*orderv1.OrderDetailResponse, error) {
	if f.err != nil {
		return nil, f.fail()
	}
	f.shipOrder = in
	return f.detailOf(in.GetOrderId()), nil
}

func (f *fakeOrderClient) TradeSummary(context.Context, *orderv1.TradeSummaryRequest, ...grpc.CallOption) (*orderv1.TradeSummaryResponse, error) {
	if f.err != nil {
		return nil, f.fail()
	}
	return &orderv1.TradeSummaryResponse{}, nil
}

func (f *fakeOrderClient) detailOf(id string) *orderv1.OrderDetailResponse {
	if f.detail != nil {
		return f.detail
	}
	return &orderv1.OrderDetailResponse{Id: id}
}

func newTradeEngine(order orderv1.OrderServiceClient) *gin.Engine {
	gin.SetMode(gin.TestMode)
	return router.New(&client.Clients{
		User:  fakeUserClient{},
		Note:  fakeNoteClient{},
		Order: order,
	}, zap.NewNop(), config.MiddlewareConfig{
		JWT: middleware.JWTConfig{
			Secret:        "test-secret",
			Issuer:        "xiaolanshu",
			ExpireSeconds: 7200,
		},
	})
}

func tokenFor(t *testing.T, role string, userID string, name string) string {
	t.Helper()
	token, err := middleware.GenerateToken(middleware.JWTConfig{
		Secret:        "test-secret",
		Issuer:        "xiaolanshu",
		ExpireSeconds: 7200,
	}, middleware.JWTClaims{UserID: userID, Role: role, Name: name})
	require.NoError(t, err)
	return token
}

func doJSON(t *testing.T, engine *gin.Engine, method string, path string, token string, body string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	return rec
}

func bodyOf(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var parsed map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &parsed))
	return parsed
}

func expectOK(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
	return bodyOf(t, rec)
}

func expectError(t *testing.T, rec *httptest.ResponseRecorder, wantStatus int, wantCode string) {
	t.Helper()
	require.Equal(t, wantStatus, rec.Code, "body=%s", rec.Body.String())
	body := bodyOf(t, rec)
	errBody, ok := body["error"].(map[string]any)
	require.True(t, ok, "missing error body: %s", rec.Body.String())
	require.Equal(t, wantCode, errBody["code"])
}

const buyerOrderJSON = `{"product_id":"P1","qty":1}`

// 下单：买家身份取自 JWT，请求体不能指定他人。
func TestTradePlaceOrderUsesJWTIdentity(t *testing.T) {
	fake := &fakeOrderClient{detail: &orderv1.OrderDetailResponse{Id: "SO-NEW", BuyerId: "b-1", AmountCents: 10000}}
	engine := newTradeEngine(fake)

	rec := doJSON(t, engine, http.MethodPost, "/api/v1/trade/orders",
		tokenFor(t, "buyer", "b-1", "阿禾"), buyerOrderJSON)

	require.Equal(t, http.StatusCreated, rec.Code, "body=%s", rec.Body.String())
	require.NotNil(t, fake.placeOrder)
	require.Equal(t, "b-1", fake.placeOrder.GetBuyerId())
	require.Equal(t, "阿禾", fake.placeOrder.GetBuyerName())
	require.Equal(t, "P1", fake.placeOrder.GetProductId())
	require.Equal(t, int32(1), fake.placeOrder.GetQty())
}

func TestTradePlaceOrderRejectsInvalidPayload(t *testing.T) {
	engine := newTradeEngine(&fakeOrderClient{})
	rec := doJSON(t, engine, http.MethodPost, "/api/v1/trade/orders",
		tokenFor(t, "buyer", "b-1", "阿禾"), `{"product_id":"","qty":9}`)
	expectError(t, rec, http.StatusBadRequest, "invalid_request")
}

func TestTradePlaceOrderRejectsWrongRole(t *testing.T) {
	engine := newTradeEngine(&fakeOrderClient{})
	rec := doJSON(t, engine, http.MethodPost, "/api/v1/trade/orders",
		tokenFor(t, "warehouse", "w-1", "仓管小周"), buyerOrderJSON)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

// 详情：买家只能看本人订单，越权在网关拦截。
func TestTradeOrderDetailBuyerOwnership(t *testing.T) {
	fake := &fakeOrderClient{detail: &orderv1.OrderDetailResponse{Id: "SO-1", BuyerId: "b-1", AmountCents: 10000}}
	engine := newTradeEngine(fake)

	rec := doJSON(t, engine, http.MethodGet, "/api/v1/trade/orders/SO-1",
		tokenFor(t, "buyer", "b-1", "阿禾"), "")
	expectOK(t, rec)
	require.Equal(t, "SO-1", fake.listTradeOrder)

	rec = doJSON(t, engine, http.MethodGet, "/api/v1/trade/orders/SO-1",
		tokenFor(t, "buyer", "b-2", "路人"), "")
	expectError(t, rec, http.StatusUnauthorized, "permission_denied")
}

func TestTradeOrderDetailAccessibleToStaffRoles(t *testing.T) {
	fake := &fakeOrderClient{detail: &orderv1.OrderDetailResponse{Id: "SO-1", BuyerId: "b-1"}}
	engine := newTradeEngine(fake)
	for _, role := range []string{"warehouse", "cs", "ops"} {
		rec := doJSON(t, engine, http.MethodGet, "/api/v1/trade/orders/SO-1",
			tokenFor(t, role, role+"-1", role), "")
		expectOK(t, rec)
	}
}

func TestTradeOrderDetailPropagatesDownstreamError(t *testing.T) {
	engine := newTradeEngine(&fakeOrderClient{err: fmt.Errorf("boom")})
	rec := doJSON(t, engine, http.MethodGet, "/api/v1/trade/orders/SO-1",
		tokenFor(t, "buyer", "b-1", "阿禾"), "")
	require.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestTradeCancelAndRefundUseJWTBuyer(t *testing.T) {
	fake := &fakeOrderClient{}
	engine := newTradeEngine(fake)
	token := tokenFor(t, "buyer", "b-1", "阿禾")

	rec := doJSON(t, engine, http.MethodPost, "/api/v1/trade/orders/SO-1/cancel", token, "")
	expectOK(t, rec)
	require.Equal(t, "SO-1", fake.cancelOrder.GetOrderId())
	require.Equal(t, "b-1", fake.cancelOrder.GetBuyerId())

	rec = doJSON(t, engine, http.MethodPost, "/api/v1/trade/orders/SO-1/refund", token, "")
	expectOK(t, rec)
	require.Equal(t, "SO-1", fake.applyRefund.GetOrderId())
	require.Equal(t, "b-1", fake.applyRefund.GetBuyerId())
}

// 模拟渠道支付：网关用服务端密钥签名，买家只能支付本人订单。
func mockSignature(secret string, tradeNo string, orderID string, amountCents int64) string {
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%s|%s|%d", tradeNo, orderID, amountCents)
	return hex.EncodeToString(mac.Sum(nil))
}

func TestMockPayNotifySignsWithServerSecret(t *testing.T) {
	t.Setenv("TRADE_CHANNEL_SECRET", "")
	fake := &fakeOrderClient{detail: &orderv1.OrderDetailResponse{Id: "SO-1", BuyerId: "b-1", AmountCents: 25000}}
	engine := newTradeEngine(fake)

	rec := doJSON(t, engine, http.MethodPost, "/api/v1/pay/mock",
		tokenFor(t, "buyer", "b-1", "阿禾"), `{"order_id":"SO-1","channel_trade_no":"CH-T-1"}`)

	expectOK(t, rec)
	require.NotNil(t, fake.payNotify)
	require.Equal(t, "CH-T-1", fake.payNotify.GetChannelTradeNo())
	require.Equal(t, int64(25000), fake.payNotify.GetAmountCents())
	require.Equal(t, mockSignature("shop-mock-channel-secret", "CH-T-1", "SO-1", 25000),
		fake.payNotify.GetSignature())
}

func TestMockPayNotifyUsesEnvSecretOverride(t *testing.T) {
	t.Setenv("TRADE_CHANNEL_SECRET", "env-channel-secret")
	fake := &fakeOrderClient{detail: &orderv1.OrderDetailResponse{Id: "SO-1", BuyerId: "b-1", AmountCents: 25000}}
	engine := newTradeEngine(fake)

	rec := doJSON(t, engine, http.MethodPost, "/api/v1/pay/mock",
		tokenFor(t, "buyer", "b-1", "阿禾"), `{"order_id":"SO-1"}`)

	expectOK(t, rec)
	require.Equal(t, mockSignature("env-channel-secret", fake.payNotify.GetChannelTradeNo(), "SO-1", 25000),
		fake.payNotify.GetSignature())
}

func TestMockPayNotifyBlocksForeignOrder(t *testing.T) {
	fake := &fakeOrderClient{detail: &orderv1.OrderDetailResponse{Id: "SO-1", BuyerId: "b-other", AmountCents: 25000}}
	engine := newTradeEngine(fake)
	rec := doJSON(t, engine, http.MethodPost, "/api/v1/pay/mock",
		tokenFor(t, "buyer", "b-1", "阿禾"), `{"order_id":"SO-1"}`)
	expectError(t, rec, http.StatusUnauthorized, "permission_denied")
	require.Nil(t, fake.payNotify)
}

func TestMockPayNotifyRejectsInvalidBodyAndMissingOrder(t *testing.T) {
	engine := newTradeEngine(&fakeOrderClient{})
	rec := doJSON(t, engine, http.MethodPost, "/api/v1/pay/mock",
		tokenFor(t, "buyer", "b-1", "阿禾"), `{}`)
	expectError(t, rec, http.StatusBadRequest, "invalid_request")

	rec = doJSON(t, engine, http.MethodPost, "/api/v1/pay/mock",
		tokenFor(t, "buyer", "b-1", "阿禾"), `{"order_id":"SO-MISSING"}`)
	// 下游返回空详情：买家身份与订单归属不符，网关直接拦截。
	expectError(t, rec, http.StatusUnauthorized, "permission_denied")
}

// 渠道异步回调入口：无需登录态，载荷校验后透传。
func TestPayNotifyPublicEndpointProxiesRequest(t *testing.T) {
	fake := &fakeOrderClient{}
	engine := newTradeEngine(fake)

	rec := doJSON(t, engine, http.MethodPost, "/api/v1/pay/notify", "",
		`{"channel_trade_no":"CH-1","order_id":"SO-1","amount_cents":10000,"signature":"abc"}`)

	expectOK(t, rec)
	require.NotNil(t, fake.payNotify)
	require.Equal(t, "CH-1", fake.payNotify.GetChannelTradeNo())
	require.Equal(t, "abc", fake.payNotify.GetSignature())
}

func TestPayNotifyPublicEndpointRejectsInvalidBody(t *testing.T) {
	engine := newTradeEngine(&fakeOrderClient{})
	rec := doJSON(t, engine, http.MethodPost, "/api/v1/pay/notify", "", `{}`)
	expectError(t, rec, http.StatusBadRequest, "invalid_request")
}

// 仓库：待发货列表 + 发货登记（操作人取 JWT 姓名）。
func TestWarehouseListAndShip(t *testing.T) {
	fake := &fakeOrderClient{}
	engine := newTradeEngine(fake)
	token := tokenFor(t, "warehouse", "w-1", "仓管小周")

	rec := doJSON(t, engine, http.MethodGet, "/api/v1/warehouse/orders", token, "")
	expectOK(t, rec)
	require.Equal(t, "PAID", fake.listByStatus.GetStatus())

	rec = doJSON(t, engine, http.MethodPost, "/api/v1/warehouse/orders/SO-1/ship", token,
		`{"carrier":"顺丰速运","tracking_no":"SF-001"}`)
	expectOK(t, rec)
	require.Equal(t, "SO-1", fake.shipOrder.GetOrderId())
	require.Equal(t, "仓管小周", fake.shipOrder.GetOperator())

	rec = doJSON(t, engine, http.MethodPost, "/api/v1/warehouse/orders/SO-1/ship", token, `{}`)
	expectError(t, rec, http.StatusBadRequest, "invalid_request")
}

func TestWarehouseRoutesRejectOtherRoles(t *testing.T) {
	engine := newTradeEngine(&fakeOrderClient{})
	for _, role := range []string{"buyer", "cs", "ops"} {
		rec := doJSON(t, engine, http.MethodGet, "/api/v1/warehouse/orders",
			tokenFor(t, role, role+"-1", role), "")
		require.Equal(t, http.StatusUnauthorized, rec.Code, "role=%s", role)
	}
}

// 客服：退款审核、异常处置、回调反查。
func TestCustomerServiceEndpoints(t *testing.T) {
	fake := &fakeOrderClient{}
	engine := newTradeEngine(fake)
	token := tokenFor(t, "cs", "cs-1", "客服小美")

	rec := doJSON(t, engine, http.MethodGet, "/api/v1/cs/refunds", token, "")
	expectOK(t, rec)
	require.Equal(t, "REFUNDING", fake.listByStatus.GetStatus())

	rec = doJSON(t, engine, http.MethodPost, "/api/v1/cs/refunds/SO-1/audit", token,
		`{"approve":true,"reason":"凭证一致"}`)
	expectOK(t, rec)
	require.Equal(t, "SO-1", fake.auditRefund.GetOrderId())
	require.True(t, fake.auditRefund.GetApprove())
	require.Equal(t, "客服小美", fake.auditRefund.GetOperator())

	rec = doJSON(t, engine, http.MethodPost, "/api/v1/cs/refunds/SO-1/audit", token, `{}`)
	require.Equal(t, http.StatusOK, rec.Code) // audit 无必填字段，合法载荷

	rec = doJSON(t, engine, http.MethodGet, "/api/v1/cs/exceptions", token, "")
	expectOK(t, rec)

	rec = doJSON(t, engine, http.MethodGet, "/api/v1/cs/exceptions?only_pending=false", token, "")
	expectOK(t, rec)

	rec = doJSON(t, engine, http.MethodPost, "/api/v1/cs/exceptions/EX-1/handle", token,
		`{"action":"refund","reason":"迟到支付退款"}`)
	expectOK(t, rec)
	require.Equal(t, "EX-1", fake.handleExID)
	require.Equal(t, "refund", fake.handleExcep.GetAction())

	rec = doJSON(t, engine, http.MethodPost, "/api/v1/cs/exceptions/EX-1/handle", token,
		`{"action":"destroy"}`)
	expectError(t, rec, http.StatusBadRequest, "invalid_request")

	rec = doJSON(t, engine, http.MethodGet, "/api/v1/cs/callbacks?keyword=CH&status=success", token, "")
	expectOK(t, rec)

	rec = doJSON(t, engine, http.MethodGet, "/api/v1/cs/callbacks/CH-1", token, "")
	expectOK(t, rec)
}

// 运营：按状态/买家查单、交易核查汇总、回调反查。
func TestOpsEndpoints(t *testing.T) {
	fake := &fakeOrderClient{}
	engine := newTradeEngine(fake)
	token := tokenFor(t, "ops", "ops-1", "运营小王")

	rec := doJSON(t, engine, http.MethodGet, "/api/v1/ops/orders?status=PAID&buyer_id=b-1", token, "")
	expectOK(t, rec)
	require.Equal(t, "PAID", fake.listByStatus.GetStatus())
	require.Equal(t, "b-1", fake.listByStatus.GetBuyerId())

	rec = doJSON(t, engine, http.MethodGet, "/api/v1/ops/summary", token, "")
	expectOK(t, rec)

	rec = doJSON(t, engine, http.MethodGet, "/api/v1/ops/callbacks/CH-9", token, "")
	expectOK(t, rec)
}

// 商品：登录可见，运营可维护。
func TestProductRoutes(t *testing.T) {
	fake := &fakeOrderClient{}
	engine := newTradeEngine(fake)

	rec := doJSON(t, engine, http.MethodGet, "/api/v1/products",
		tokenFor(t, "buyer", "b-1", "阿禾"), "")
	expectOK(t, rec)

	ops := tokenFor(t, "ops", "ops-1", "运营小王")
	rec = doJSON(t, engine, http.MethodPost, "/api/v1/products", ops,
		`{"id":"P1","name":"羊绒围巾","price_cents":10000,"available":15,"on_sale":true}`)
	expectOK(t, rec)
	require.NotNil(t, fake.upsertProduct)
	require.Equal(t, "P1", fake.upsertProduct.GetId())

	rec = doJSON(t, engine, http.MethodPost, "/api/v1/products", ops, `{"id":"","name":""}`)
	expectError(t, rec, http.StatusBadRequest, "invalid_request")

	rec = doJSON(t, engine, http.MethodPost, "/api/v1/products",
		tokenFor(t, "buyer", "b-1", "阿禾"), `{"id":"P1","name":"x","price_cents":1}`)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

// 脚手架 CRUD：登录后可用，写操作仅运营。
func TestScaffoldOrderCRUDWithAuth(t *testing.T) {
	fake := &fakeOrderClient{}
	engine := newTradeEngine(fake)
	ops := tokenFor(t, "ops", "ops-1", "运营小王")

	rec := doJSON(t, engine, http.MethodPost, "/api/v1/orders", ops,
		`{"name":"脚手架订单","description":"兼容保留"}`)
	require.Equal(t, http.StatusCreated, rec.Code)

	rec = doJSON(t, engine, http.MethodGet, "/api/v1/orders?page=1&page_size=10", ops, "")
	expectOK(t, rec)

	rec = doJSON(t, engine, http.MethodGet, "/api/v1/orders/o-1", ops, "")
	expectOK(t, rec)

	rec = doJSON(t, engine, http.MethodPut, "/api/v1/orders/o-1", ops,
		`{"name":"改名"}`)
	expectOK(t, rec)

	rec = doJSON(t, engine, http.MethodDelete, "/api/v1/orders/o-1", ops, "")
	expectOK(t, rec)

	rec = doJSON(t, engine, http.MethodPost, "/api/v1/orders", ops, `{}`)
	expectError(t, rec, http.StatusBadRequest, "invalid_request")
}

// 买家“我的订单”列表。
func TestMyTradeOrdersUsesJWTBuyer(t *testing.T) {
	fake := &fakeOrderClient{}
	engine := newTradeEngine(fake)
	rec := doJSON(t, engine, http.MethodGet, "/api/v1/trade/orders/mine",
		tokenFor(t, "buyer", "b-1", "阿禾"), "")
	expectOK(t, rec)
}

// 下游 gRPC 错误统一映射为 HTTP 5xx/4xx，且带应用错误体。
func TestDownstreamErrorsPropagateAcrossHandlers(t *testing.T) {
	engine := newTradeEngine(&fakeOrderClient{err: fmt.Errorf("boom")})
	buyer := tokenFor(t, "buyer", "b-1", "阿禾")
	ops := tokenFor(t, "ops", "ops-1", "运营小王")
	cases := []struct{ method, path, token, body string }{
		{http.MethodGet, "/api/v1/products", buyer, ""},
		{http.MethodPost, "/api/v1/products", ops, `{"id":"P1","name":"x","price_cents":1}`},
		{http.MethodGet, "/api/v1/trade/orders/mine", buyer, ""},
		{http.MethodPost, "/api/v1/pay/mock", buyer, `{"order_id":"SO-1"}`},
		{http.MethodGet, "/api/v1/warehouse/orders", tokenFor(t, "warehouse", "w-1", "仓管"), ""},
		{http.MethodPost, "/api/v1/warehouse/orders/SO-1/ship", tokenFor(t, "warehouse", "w-1", "仓管"), `{"carrier":"顺丰","tracking_no":"SF-1"}`},
		{http.MethodGet, "/api/v1/cs/refunds", tokenFor(t, "cs", "cs-1", "客服"), ""},
		{http.MethodGet, "/api/v1/cs/exceptions", tokenFor(t, "cs", "cs-1", "客服"), ""},
		{http.MethodGet, "/api/v1/cs/callbacks", tokenFor(t, "cs", "cs-1", "客服"), ""},
		{http.MethodGet, "/api/v1/cs/callbacks/CH-1", tokenFor(t, "cs", "cs-1", "客服"), ""},
		{http.MethodGet, "/api/v1/ops/orders", ops, ""},
		{http.MethodGet, "/api/v1/ops/summary", ops, ""},
		{http.MethodGet, "/api/v1/ops/callbacks/CH-1", ops, ""},
		{http.MethodPost, "/api/v1/pay/notify", "", `{"channel_trade_no":"CH-1","order_id":"SO-1","amount_cents":1,"signature":"x"}`},
	}
	for _, tc := range cases {
		rec := doJSON(t, engine, tc.method, tc.path, tc.token, tc.body)
		require.Equal(t, http.StatusInternalServerError, rec.Code,
			"%s %s body=%s", tc.method, tc.path, rec.Body.String())
	}
}

// 脚手架 CRUD 的下游错误分支。
func TestScaffoldOrderCRUDDownstreamError(t *testing.T) {
	engine := newTradeEngine(&fakeOrderClient{err: fmt.Errorf("boom")})
	ops := tokenFor(t, "ops", "ops-1", "运营小王")
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/v1/orders", `{"name":"n"}`},
		{http.MethodGet, "/api/v1/orders", ""},
		{http.MethodGet, "/api/v1/orders/o-1", ""},
		{http.MethodPut, "/api/v1/orders/o-1", `{"name":"n"}`},
		{http.MethodDelete, "/api/v1/orders/o-1", ""},
	} {
		rec := doJSON(t, engine, tc.method, tc.path, ops, tc.body)
		require.Equal(t, http.StatusInternalServerError, rec.Code, "%s %s body=%s", tc.method, tc.path, rec.Body.String())
	}
}

// 笔记接口（脚手架保留）：创建/查询/发布成功与下游错误。
func TestNoteEndpoints(t *testing.T) {
	engine := newTradeEngine(&fakeOrderClient{})
	token := tokenFor(t, "buyer", "user-1", "阿禾")

	rec := doJSON(t, engine, http.MethodPost, "/api/v1/notes", token,
		`{"author_id":"user-1","title":"标题","content":"内容"}`)
	require.Equal(t, http.StatusCreated, rec.Code, "body=%s", rec.Body.String())

	rec = doJSON(t, engine, http.MethodGet, "/api/v1/notes/note-1", token, "")
	expectOK(t, rec)

	rec = doJSON(t, engine, http.MethodPost, "/api/v1/notes/publishNote", token,
		`{"author_id":"user-1","title":"标题","content":"内容"}`)
	expectOK(t, rec)

	rec = doJSON(t, engine, http.MethodPost, "/api/v1/notes", token, `{"author_id":""}`)
	expectError(t, rec, http.StatusBadRequest, "invalid_request")

	errEngine := router.New(&client.Clients{
		User: fakeUserClient{}, Note: fakeNoteClient{err: fmt.Errorf("boom")},
	}, zap.NewNop(), config.MiddlewareConfig{JWT: middleware.JWTConfig{Secret: "test-secret", Issuer: "xiaolanshu", ExpireSeconds: 7200}})
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/v1/notes", `{"author_id":"user-1","title":"t","content":"c"}`},
		{http.MethodGet, "/api/v1/notes/note-1", ""},
		{http.MethodPost, "/api/v1/notes/publishNote", `{"author_id":"user-1","title":"t","content":"c"}`},
	} {
		rec := doJSON(t, errEngine, tc.method, tc.path, token, tc.body)
		require.Equal(t, http.StatusInternalServerError, rec.Code, "%s %s body=%s", tc.method, tc.path, rec.Body.String())
	}
}

// 用户注册与查询的下游错误分支。
func TestUserRegisterAndDownstreamError(t *testing.T) {
	engine := newTradeEngine(&fakeOrderClient{})
	rec := doJSON(t, engine, http.MethodPost, "/api/v1/users/register", "",
		`{"account":"ada","display_name":"Ada","password":"secret123"}`)
	require.Equal(t, http.StatusCreated, rec.Code, "body=%s", rec.Body.String())

	rec = doJSON(t, engine, http.MethodPost, "/api/v1/users/register", "", `{}`)
	expectError(t, rec, http.StatusBadRequest, "invalid_request")

	errEngine := router.New(&client.Clients{
		User: fakeUserClient{err: fmt.Errorf("boom")}, Note: fakeNoteClient{},
	}, zap.NewNop(), config.MiddlewareConfig{JWT: middleware.JWTConfig{Secret: "test-secret", Issuer: "xiaolanshu", ExpireSeconds: 7200}})
	buyer := tokenFor(t, "buyer", "user-1", "阿禾")
	for _, tc := range []struct{ method, path, token, body string }{
		{http.MethodPost, "/api/v1/users/register", "", `{"account":"ada","display_name":"Ada","password":"secret123"}`},
		{http.MethodGet, "/api/v1/users/user-1", buyer, ""},
	} {
		rec := doJSON(t, errEngine, tc.method, tc.path, tc.token, tc.body)
		require.Equal(t, http.StatusInternalServerError, rec.Code, "%s %s body=%s", tc.method, tc.path, rec.Body.String())
	}
}
