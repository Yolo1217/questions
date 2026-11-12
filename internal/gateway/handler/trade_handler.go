package handler

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	orderv1 "github.com/BwCloudWeGo/bw-cli/api/gen/order/v1"
	"github.com/BwCloudWeGo/bw-cli/internal/gateway/request"
	apperrors "github.com/BwCloudWeGo/bw-cli/pkg/errors"
	"github.com/BwCloudWeGo/bw-cli/pkg/httpx"
	"github.com/BwCloudWeGo/bw-cli/pkg/middleware"
)

// logFields 统一网关业务日志字段。
func logFields(c *gin.Context, aggregateID string) []zap.Field {
	return []zap.Field{
		zap.String("request_id", httpx.RequestID(c)),
		zap.String("aggregate_id", aggregateID),
		zap.String("role", middleware.ClaimsFromContext(c).Role),
	}
}

// ListProducts 将 GET /api/v1/products 代理到 ListProducts（登录即可访问）。
func (h *OrderHandler) ListProducts(c *gin.Context) {
	resp, err := h.client.ListProducts(outgoingContext(c), &orderv1.ListProductsRequest{})
	if err != nil {
		httpx.Error(c, apperrors.FromGRPC(err))
		return
	}
	httpx.OK(c, resp)
}

// UpsertProduct 将 POST /api/v1/products 代理到 UpsertProduct（仅运营）。
func (h *OrderHandler) UpsertProduct(c *gin.Context) {
	var req request.UpsertProductRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Error(c, apperrors.InvalidArgument("invalid_request", err.Error()))
		return
	}
	resp, err := h.client.UpsertProduct(outgoingContext(c), &orderv1.UpsertProductRequest{
		Id:         req.ID,
		Name:       req.Name,
		PriceCents: req.PriceCents,
		Available:  req.Available,
		OnSale:     req.OnSale,
	})
	if err != nil {
		httpx.Error(c, apperrors.FromGRPC(err))
		return
	}
	h.log.Info("gateway product upsert proxied", logFields(c, resp.GetId())...)
	httpx.OK(c, resp)
}

// PlaceTradeOrder 将 POST /api/v1/trade/orders 代理到 PlaceOrder（仅买家，身份取自 JWT）。
func (h *OrderHandler) PlaceTradeOrder(c *gin.Context) {
	var req request.PlaceTradeOrderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Error(c, apperrors.InvalidArgument("invalid_request", err.Error()))
		return
	}
	claims := middleware.ClaimsFromContext(c)
	resp, err := h.client.PlaceOrder(outgoingContext(c), &orderv1.PlaceOrderRequest{
		BuyerId:   claims.UserID,
		BuyerName: claims.Name,
		ProductId: req.ProductID,
		Qty:       req.Qty,
	})
	if err != nil {
		httpx.Error(c, apperrors.FromGRPC(err))
		return
	}
	h.log.Info("gateway trade order placed", logFields(c, resp.GetId())...)
	httpx.Created(c, resp)
}

// MyTradeOrders 将 GET /api/v1/trade/orders/mine 代理到 ListMyOrders。
func (h *OrderHandler) MyTradeOrders(c *gin.Context) {
	claims := middleware.ClaimsFromContext(c)
	resp, err := h.client.ListMyOrders(outgoingContext(c), &orderv1.ListMyOrdersRequest{BuyerId: claims.UserID})
	if err != nil {
		httpx.Error(c, apperrors.FromGRPC(err))
		return
	}
	httpx.OK(c, resp)
}

// TradeOrderDetail 将 GET /api/v1/trade/orders/:id 代理到 GetTradeOrder。
// 买家只能查看本人订单，员工按角色可查（服务端返回真实数据，越权在网关拦截）。
func (h *OrderHandler) TradeOrderDetail(c *gin.Context) {
	resp, err := h.client.GetTradeOrder(outgoingContext(c), &orderv1.GetTradeOrderRequest{Id: c.Param("id")})
	if err != nil {
		httpx.Error(c, apperrors.FromGRPC(err))
		return
	}
	claims := middleware.ClaimsFromContext(c)
	if claims.Role == "buyer" && resp.GetBuyerId() != claims.UserID {
		httpx.Error(c, apperrors.New(apperrors.KindUnauthorized, "permission_denied", "越权拒绝：只能查看本人订单"))
		return
	}
	httpx.OK(c, resp)
}

// CancelTradeOrder 将 POST /api/v1/trade/orders/:id/cancel 代理到 CancelOrder。
func (h *OrderHandler) CancelTradeOrder(c *gin.Context) {
	claims := middleware.ClaimsFromContext(c)
	resp, err := h.client.CancelOrder(outgoingContext(c), &orderv1.CancelOrderRequest{
		OrderId: c.Param("id"),
		BuyerId: claims.UserID,
	})
	if err != nil {
		httpx.Error(c, apperrors.FromGRPC(err))
		return
	}
	h.log.Info("gateway trade order cancelled", logFields(c, resp.GetId())...)
	httpx.OK(c, resp)
}

// ApplyTradeRefund 将 POST /api/v1/trade/orders/:id/refund 代理到 ApplyRefund。
func (h *OrderHandler) ApplyTradeRefund(c *gin.Context) {
	claims := middleware.ClaimsFromContext(c)
	resp, err := h.client.ApplyRefund(outgoingContext(c), &orderv1.ApplyRefundRequest{
		OrderId: c.Param("id"),
		BuyerId: claims.UserID,
	})
	if err != nil {
		httpx.Error(c, apperrors.FromGRPC(err))
		return
	}
	h.log.Info("gateway refund applied", logFields(c, resp.GetId())...)
	httpx.OK(c, resp)
}

// MockChannelSecret 是模拟渠道的默认共享密钥，需与订单服务 TRADE_CHANNEL_SECRET 一致。
// 生产环境必须通过环境变量注入，绝不进入前端。
const MockChannelSecret = "shop-mock-channel-secret"

// signMockPayNotify 计算渠道回调签名，与订单服务 SignPayNotify 保持一致：
// hex(HMAC-SHA256(secret, "trade_no|order_id|amount_cents"))。
func signMockPayNotify(secret string, tradeNo string, orderID string, amountCents int64) string {
	if secret == "" {
		secret = MockChannelSecret
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(fmt.Sprintf("%s|%s|%d", tradeNo, orderID, amountCents)))
	return hex.EncodeToString(mac.Sum(nil))
}

// MockPayNotify 处理 POST /api/v1/pay/mock：页面“模拟渠道支付”。
// 网关以服务端密钥签名后调用正式回调链路，因此仍会完整走验签、幂等与异常分支。
func (h *OrderHandler) MockPayNotify(c *gin.Context) {
	var req request.MockPayRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Error(c, apperrors.InvalidArgument("invalid_request", err.Error()))
		return
	}
	claims := middleware.ClaimsFromContext(c)
	order, err := h.client.GetTradeOrder(outgoingContext(c), &orderv1.GetTradeOrderRequest{Id: req.OrderID})
	if err != nil {
		httpx.Error(c, apperrors.FromGRPC(err))
		return
	}
	// 买家只能为自己的订单发起支付，防止用他人订单号试探金额
	if claims.Role == "buyer" && order.GetBuyerId() != claims.UserID {
		httpx.Error(c, apperrors.New(apperrors.KindUnauthorized, "permission_denied", "越权拒绝：只能支付本人订单"))
		return
	}
	tradeNo := strings.TrimSpace(req.ChannelTradeNo)
	if tradeNo == "" {
		tradeNo = fmt.Sprintf("CH-MOCK-%s-%d", order.GetId(), time.Now().UnixNano()%1_000_000)
	}
	secret := os.Getenv("TRADE_CHANNEL_SECRET")
	resp, err := h.client.PayNotify(outgoingContext(c), &orderv1.PayNotifyRequest{
		ChannelTradeNo: tradeNo,
		OrderId:        order.GetId(),
		AmountCents:    order.GetAmountCents(),
		Signature:      signMockPayNotify(secret, tradeNo, order.GetId(), order.GetAmountCents()),
	})
	if err != nil {
		httpx.Error(c, apperrors.FromGRPC(err))
		return
	}
	h.log.Info("gateway mock pay notify proxied",
		append(logFields(c, order.GetId()), zap.String("trade_no", tradeNo), zap.String("callback_code", resp.GetCode()))...)
	httpx.OK(c, resp)
}

// WarehouseOrders 将 GET /api/v1/warehouse/orders 代理到 ListOrdersByStatus（固定只取待发货）。
func (h *OrderHandler) WarehouseOrders(c *gin.Context) {
	resp, err := h.client.ListOrdersByStatus(outgoingContext(c), &orderv1.ListOrdersByStatusRequest{
		Status: "PAID",
	})
	if err != nil {
		httpx.Error(c, apperrors.FromGRPC(err))
		return
	}
	httpx.OK(c, resp)
}

// ShipTradeOrder 将 POST /api/v1/warehouse/orders/:id/ship 代理到 ShipOrder。
func (h *OrderHandler) ShipTradeOrder(c *gin.Context) {
	var req request.ShipOrderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Error(c, apperrors.InvalidArgument("invalid_request", err.Error()))
		return
	}
	claims := middleware.ClaimsFromContext(c)
	resp, err := h.client.ShipOrder(outgoingContext(c), &orderv1.ShipOrderRequest{
		OrderId:    c.Param("id"),
		Carrier:    req.Carrier,
		TrackingNo: req.TrackingNo,
		Operator:   claims.Name,
	})
	if err != nil {
		httpx.Error(c, apperrors.FromGRPC(err))
		return
	}
	h.log.Info("gateway order shipped", logFields(c, resp.GetId())...)
	httpx.OK(c, resp)
}

// RefundPendingOrders 将 GET /api/v1/cs/refunds 代理到 ListOrdersByStatus（退款审核中）。
func (h *OrderHandler) RefundPendingOrders(c *gin.Context) {
	resp, err := h.client.ListOrdersByStatus(outgoingContext(c), &orderv1.ListOrdersByStatusRequest{
		Status: "REFUNDING",
	})
	if err != nil {
		httpx.Error(c, apperrors.FromGRPC(err))
		return
	}
	httpx.OK(c, resp)
}

// AuditTradeRefund 将 POST /api/v1/cs/refunds/:id/audit 代理到 AuditRefund。
func (h *OrderHandler) AuditTradeRefund(c *gin.Context) {
	var req request.AuditRefundRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Error(c, apperrors.InvalidArgument("invalid_request", err.Error()))
		return
	}
	claims := middleware.ClaimsFromContext(c)
	resp, err := h.client.AuditRefund(outgoingContext(c), &orderv1.AuditRefundRequest{
		OrderId:  c.Param("id"),
		Approve:  req.Approve,
		Reason:   req.Reason,
		Operator: claims.Name,
	})
	if err != nil {
		httpx.Error(c, apperrors.FromGRPC(err))
		return
	}
	h.log.Info("gateway refund audited",
		append(logFields(c, resp.GetOrder().GetId()), zap.Bool("duplicate", resp.GetDuplicate()))...)
	httpx.OK(c, resp)
}

// ExceptionPays 将 GET /api/v1/cs/exceptions 代理到 ListExceptionPays。
func (h *OrderHandler) ExceptionPays(c *gin.Context) {
	onlyPending := c.Query("only_pending") != "false"
	resp, err := h.client.ListExceptionPays(outgoingContext(c), &orderv1.ListExceptionPaysRequest{OnlyPending: onlyPending})
	if err != nil {
		httpx.Error(c, apperrors.FromGRPC(err))
		return
	}
	httpx.OK(c, resp)
}

// HandleExceptionPay 将 POST /api/v1/cs/exceptions/:id/handle 代理到 HandleExceptionPay。
func (h *OrderHandler) HandleExceptionPay(c *gin.Context) {
	var req request.HandleExceptionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Error(c, apperrors.InvalidArgument("invalid_request", err.Error()))
		return
	}
	claims := middleware.ClaimsFromContext(c)
	resp, err := h.client.HandleExceptionPay(outgoingContext(c), &orderv1.HandleExceptionPayRequest{
		ExceptionId: c.Param("id"),
		Action:      req.Action,
		Reason:      req.Reason,
		Operator:    claims.Name,
	})
	if err != nil {
		httpx.Error(c, apperrors.FromGRPC(err))
		return
	}
	h.log.Info("gateway exception handled", logFields(c, resp.GetId())...)
	httpx.OK(c, resp)
}

// PayCallbacks 将 GET /api/v1/cs/callbacks 代理到 ListPayCallbacks（渠道流水号/订单号反查）。
func (h *OrderHandler) PayCallbacks(c *gin.Context) {
	resp, err := h.client.ListPayCallbacks(outgoingContext(c), &orderv1.ListPayCallbacksRequest{
		Keyword: c.Query("keyword"),
		Status:  c.Query("status"),
	})
	if err != nil {
		httpx.Error(c, apperrors.FromGRPC(err))
		return
	}
	httpx.OK(c, resp)
}

// PayCallbackDetail 将 GET /api/v1/cs/callbacks/:tradeNo 代理到 GetPayCallback。
func (h *OrderHandler) PayCallbackDetail(c *gin.Context) {
	resp, err := h.client.GetPayCallback(outgoingContext(c), &orderv1.GetPayCallbackRequest{TradeNo: c.Param("tradeNo")})
	if err != nil {
		httpx.Error(c, apperrors.FromGRPC(err))
		return
	}
	httpx.OK(c, resp)
}

// OpsOrders 将 GET /api/v1/ops/orders 代理到 ListOrdersByStatus（按状态/买家可选过滤）。
func (h *OrderHandler) OpsOrders(c *gin.Context) {
	var req request.ListTradeOrdersRequest
	if err := c.ShouldBindQuery(&req); err != nil {
		httpx.Error(c, apperrors.InvalidArgument("invalid_request", err.Error()))
		return
	}
	resp, err := h.client.ListOrdersByStatus(outgoingContext(c), &orderv1.ListOrdersByStatusRequest{
		Status:  req.Status,
		BuyerId: req.BuyerID,
	})
	if err != nil {
		httpx.Error(c, apperrors.FromGRPC(err))
		return
	}
	httpx.OK(c, resp)
}

// OpsSummary 将 GET /api/v1/ops/summary 代理到 TradeSummary。
func (h *OrderHandler) OpsSummary(c *gin.Context) {
	resp, err := h.client.TradeSummary(outgoingContext(c), &orderv1.TradeSummaryRequest{})
	if err != nil {
		httpx.Error(c, apperrors.FromGRPC(err))
		return
	}
	httpx.OK(c, resp)
}

// PayNotify 将 POST /api/v1/pay/notify 代理到 PayNotify（模拟渠道回调，不需要登录态）。
func (h *OrderHandler) PayNotify(c *gin.Context) {
	var req request.PayNotifyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Error(c, apperrors.InvalidArgument("invalid_request", err.Error()))
		return
	}
	resp, err := h.client.PayNotify(outgoingContext(c), &orderv1.PayNotifyRequest{
		ChannelTradeNo: req.ChannelTradeNo,
		OrderId:        req.OrderID,
		AmountCents:    req.AmountCents,
		Signature:      req.Signature,
	})
	if err != nil {
		httpx.Error(c, apperrors.FromGRPC(err))
		return
	}
	h.log.Info("gateway pay notify proxied", logFields(c, req.ChannelTradeNo)...)
	httpx.OK(c, resp)
}
