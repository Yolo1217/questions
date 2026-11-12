package entity

import (
	"context"
	"errors"
	"time"
)

// 题目01 交易域业务错误：handler 层统一映射为 AppError。
var (
	ErrProductNotFound     = errors.New("product not found")
	ErrProductOffSale      = errors.New("product off sale")
	ErrInsufficientStock   = errors.New("insufficient stock")
	ErrInvalidQty          = errors.New("invalid quantity")
	ErrTradeOrderNotFound  = errors.New("trade order not found")
	ErrPermissionDenied    = errors.New("permission denied")
	ErrInvalidTransition   = errors.New("invalid status transition")
	ErrCarrierRequired     = errors.New("carrier and tracking number are required")
	ErrReasonRequired      = errors.New("reason is required")
	ErrExceptionNotFound   = errors.New("exception not found")
	ErrExceptionClosed     = errors.New("exception already closed")
	ErrDuplicateTradeNo    = errors.New("duplicate channel trade no")
	ErrOrderAlreadyShipped = errors.New("order already shipped")
	ErrOrderRefunding      = errors.New("order is refunding, shipping blocked")
	ErrRefundNotAllowed    = errors.New("refund not allowed for shipped order")
	ErrChannelRefundFailed = errors.New("channel refund failed")
	ErrExceptionNoRefund   = errors.New("no order bound to this exception, cannot refund")
	ErrCallbackNotFound    = errors.New("pay callback not found")
	ErrSignatureMismatch   = errors.New("pay callback signature mismatch")
	ErrOrderClosed         = errors.New("order closed, cannot ship")
	ErrOrderRefunded       = errors.New("order refunded, cannot ship")
)

// 订单状态机（最小集）：迁移表是唯一合法入口。
const (
	OrderStatusCreated   = "CREATED"   // 待支付，库存预占
	OrderStatusPaid      = "PAID"      // 待发货，预占转实扣
	OrderStatusRefunding = "REFUNDING" // 退款审核中，冻结发货
	OrderStatusShipped   = "SHIPPED"   // 已发货
	OrderStatusClosed    = "CLOSED"    // 已关闭（取消/超时），释放预占
	OrderStatusRefunded  = "REFUNDED"  // 已退款
)

// 订单来源。
const (
	OrderOriginSale = "sale" // 正常售前退款：退款成功回补库存
	OrderOriginLate = "late" // 迟到支付退款：不回补库存（已释放，回补即超卖）
)

// 台账类别。
const (
	LedgerStock     = "stock"
	LedgerPay       = "pay"
	LedgerRefund    = "refund"
	LedgerShip      = "ship"
	LedgerException = "exception"
)

// 支付异常类型（至少覆盖五类）。
const (
	ExcLatePay        = "关单后到账"
	ExcNoOwner        = "无主支付"
	ExcAmountMismatch = "金额不符"
	ExcDoubleCharge   = "重复扣款"
	ExcBadSignature   = "验签失败"
)

// 异常状态。
const (
	ExcStatusPending = "待处理"
	ExcStatusClosed  = "已闭环"
)

// 回调记录状态（处理幂等：RECEIVED → PROCESSING → DONE/FAILED）。
const (
	CallbackReceived   = "RECEIVED"
	CallbackProcessing = "PROCESSING"
	CallbackDone       = "DONE"
	CallbackFailed     = "FAILED"
)

// Product 是商品聚合：运营维护售价与可售数量。
type Product struct {
	ID         string
	Name       string
	PriceCents int64
	Total      int32
	Available  int32
	OnSale     bool
}

// TradeOrder 是交易订单聚合根。
type TradeOrder struct {
	ID           string
	BuyerID      string
	BuyerName    string
	ProductID    string
	ProductName  string
	Qty          int32
	PriceCents   int64 // 下单时价格快照，后续调价不影响
	Status       string
	CreatedAt    time.Time
	Deadline     time.Time // 待支付保留截止时间（服务端时间判定）
	PaidAt       time.Time
	ClosedReason string
	Carrier      string
	TrackingNo   string
	RejectReason string
	RefundFail   bool
	Origin       string // sale | late
	UpdatedAt    time.Time
}

// AmountCents 返回订单应付金额。
func (o *TradeOrder) AmountCents() int64 { return o.PriceCents * int64(o.Qty) }

// Ledger 是统一台账条目（库存/收款/退款/发货/异常共用一张表）。
type Ledger struct {
	Kind        string
	OrderID     string
	Text        string
	AmountCents int64
	TradeNo     string
	Status      string
	Time        time.Time
}

// ExceptionPay 是支付异常表条目：必须有可查记录并能闭环。
type ExceptionPay struct {
	ID                 string
	Type               string
	OrderID            string // 无主支付时为空
	TradeNo            string
	ChannelAmountCents int64
	SystemAmountCents  int64 // 无主支付时为 -1
	Status             string
	Handler            string
	Result             string
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// PayCallback 是渠道回调记录表条目：入口幂等键 channel_trade_no。
type PayCallback struct {
	TradeNo     string
	OrderID     string
	AmountCents int64
	Status      string
	Hits        int32
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Summary 是运营核查汇总：可售 + 预占 + 已收 + 已退，用于活动结束后对账。
type Summary struct {
	PaidTotalCents     int64
	RefundedTotalCents int64
	AvailableTotal     int32
	ReservedTotal      int32 // 待支付订单已预占件数
	ExceptionPending   int32
	OrdersByStatus     map[string]int32
}

// TradeRepository 定义交易域所需的持久化行为。
// 全部状态迁移通过 TransitionOrder 的条件更新实现：受影响行数为 0 即迁移失败。
type TradeRepository interface {
	// 商品
	ListProducts(ctx context.Context) ([]*Product, error)
	FindProduct(ctx context.Context, id string) (*Product, error)
	SaveProduct(ctx context.Context, product *Product) error
	HasAnyProduct(ctx context.Context) (bool, error)

	// 订单
	PlaceOrderTx(ctx context.Context, buyerID string, buyerName string, productID string, qty int32, keep time.Duration) (*TradeOrder, error)
	FindTradeOrder(ctx context.Context, id string) (*TradeOrder, error)
	ListTradeOrders(ctx context.Context, buyerID string, status string) ([]*TradeOrder, int64, error)
	// TransitionOrder 条件更新状态：WHERE id=? AND status=from。rows=0 返回 (false, nil)。
	// stockDelta>0 时在同一事务内回补商品可售数量；mutate 在抢占成功后调用，
	// 可修改订单字段并返回随事务一起写入的台账条目。
	TransitionOrder(ctx context.Context, orderID string, from string, to string, stockDelta int32, mutate func(order *TradeOrder) ([]Ledger, error)) (bool, error)
	ListExpiredCreated(ctx context.Context, now time.Time) ([]*TradeOrder, error)
	// MarkRefundFailed 记录渠道退款明确失败：订单保持 REFUNDING，仅置失败标记并写失败流水。
	MarkRefundFailed(ctx context.Context, orderID string) (bool, error)
	// AppendLedger 仅追加台账（用于迟到支付退款：订单状态不变、不回补库存）。
	AppendLedger(ctx context.Context, ledger *Ledger) error

	// 回调记录（入口幂等）
	FindCallback(ctx context.Context, tradeNo string) (*PayCallback, error)
	// ListCallbacks 按渠道交易号/订单号关键词与处理状态查询回调记录。
	ListCallbacks(ctx context.Context, keyword string, status string) ([]*PayCallback, error)
	SaveCallback(ctx context.Context, cb *PayCallback) error // 唯一索引冲突返回 ErrDuplicateTradeNo
	UpdateCallbackStatus(ctx context.Context, tradeNo string, status string) error
	// IncrementCallbackHits 重复通知仅计数，不改变处理状态。
	IncrementCallbackHits(ctx context.Context, tradeNo string) error

	// 支付异常表
	SaveException(ctx context.Context, ex *ExceptionPay) error
	FindException(ctx context.Context, id string) (*ExceptionPay, error)
	// FindExceptionByTradeNo 用渠道交易号反查异常记录（无则返回 nil, nil）。
	FindExceptionByTradeNo(ctx context.Context, tradeNo string) (*ExceptionPay, error)
	ListExceptions(ctx context.Context, onlyPending bool) ([]*ExceptionPay, error)
	CloseException(ctx context.Context, id string, handler string, result string) error

	// 台账与汇总
	ListLedgers(ctx context.Context, orderID string) ([]Ledger, error)
	TradeSummary(ctx context.Context) (*Summary, error)
}

// NewExceptionID 生成异常记录 ID。
func NewExceptionID(seq int64) string {
	return "EX" + time.Now().UTC().Format("20060102150405") + "-" + intToPadded(seq)
}

func intToPadded(v int64) string {
	buf := [6]byte{}
	for i := 5; i >= 0; i-- {
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[:])
}
