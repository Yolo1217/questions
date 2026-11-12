package request

// UpsertProductRequest 是 POST /api/v1/products 使用的 JSON 载荷（运营维护商品）。
type UpsertProductRequest struct {
	ID         string `json:"id" binding:"required"`
	Name       string `json:"name" binding:"required"`
	PriceCents int64  `json:"price_cents" binding:"required,gt=0"`
	Available  int32  `json:"available" binding:"gte=0"`
	OnSale     bool   `json:"on_sale"`
}

// PlaceTradeOrderRequest 是 POST /api/v1/trade/orders 使用的 JSON 载荷。
// 买家身份取自 JWT，请求体不允许指定他人。
type PlaceTradeOrderRequest struct {
	ProductID string `json:"product_id" binding:"required"`
	Qty       int32  `json:"qty" binding:"required,gte=1,lte=2"`
}

// AuditRefundRequest 是 POST /api/v1/cs/refunds/:id/audit 使用的 JSON 载荷。
type AuditRefundRequest struct {
	Approve bool   `json:"approve"`
	Reason  string `json:"reason"`
}

// ShipOrderRequest 是 POST /api/v1/warehouse/orders/:id/ship 使用的 JSON 载荷。
type ShipOrderRequest struct {
	Carrier    string `json:"carrier" binding:"required"`
	TrackingNo string `json:"tracking_no" binding:"required"`
}

// HandleExceptionRequest 是 POST /api/v1/cs/exceptions/:id/handle 使用的 JSON 载荷。
type HandleExceptionRequest struct {
	Action string `json:"action" binding:"required,oneof=refund verify ignore"`
	Reason string `json:"reason"`
}

// PayNotifyRequest 是 POST /api/v1/pay/notify 使用的 JSON 载荷（模拟渠道异步通知）。
// Signature 由渠道用共享密钥计算，商城服务端重算比对；缺失或不匹配一律判验签失败。
type PayNotifyRequest struct {
	ChannelTradeNo string `json:"channel_trade_no" binding:"required"`
	OrderID        string `json:"order_id" binding:"required"`
	AmountCents    int64  `json:"amount_cents" binding:"required,gt=0"`
	Signature      string `json:"signature"`
}

// MockPayRequest 是 POST /api/v1/pay/mock 的载荷：页面触发“模拟渠道支付”。
// 签名由网关用共享密钥在服务端计算，浏览器不接触密钥。
type MockPayRequest struct {
	OrderID        string `json:"order_id" binding:"required"`
	ChannelTradeNo string `json:"channel_trade_no"`
}

// ListTradeOrdersRequest 是订单列表查询参数。
type ListTradeOrdersRequest struct {
	Status  string `form:"status"`
	BuyerID string `form:"buyer_id"`
}
