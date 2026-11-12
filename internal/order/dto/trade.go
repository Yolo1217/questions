package dto

import "github.com/BwCloudWeGo/bw-cli/internal/order/entity"

// UpsertProductCommand 是运营维护商品的入参。
type UpsertProductCommand struct {
	ID         string
	Name       string
	PriceCents int64
	Available  int32
	OnSale     bool
}

// PlaceOrderCommand 是买家下单入参。
type PlaceOrderCommand struct {
	BuyerID   string
	BuyerName string
	ProductID string
	Qty       int32
}

// PayNotifyCommand 是模拟渠道支付通知入参。
// Signature 为 HMAC-SHA256 十六进制值，服务端自行计算比对，不接受"已验签"声明。
type PayNotifyCommand struct {
	TradeNo     string
	OrderID     string
	AmountCents int64
	Signature   string
}

// AuditRefundCommand 是客服退款审核入参。
type AuditRefundCommand struct {
	OrderID  string
	Approve  bool
	Reason   string
	Operator string
}

// ShipOrderCommand 是仓库发货登记入参。
type ShipOrderCommand struct {
	OrderID    string
	Carrier    string
	TrackingNo string
	Operator   string
}

// HandleExceptionCommand 是客服从异常表处置入参。
type HandleExceptionCommand struct {
	ExceptionID string
	Action      string // refund | verify | ignore
	Reason      string
	Operator    string
}

// ProductDTO 是商品输出。
type ProductDTO struct {
	ID         string
	Name       string
	PriceCents int64
	Available  int32
	Total      int32
	OnSale     bool
}

// LedgerDTO 是台账输出。
type LedgerDTO struct {
	Kind        string
	Time        string
	Text        string
	AmountCents int64
	TradeNo     string
	Status      string
}

// TradeOrderDTO 是订单输出（含状态与四类记录）。
type TradeOrderDTO struct {
	ID           string
	BuyerID      string
	BuyerName    string
	ProductID    string
	ProductName  string
	Qty          int32
	PriceCents   int64
	AmountCents  int64
	Status       string
	CreatedAt    string
	Deadline     string
	PaidAt       string
	ClosedReason string
	Carrier      string
	TrackingNo   string
	RejectReason string
	RefundFail   bool
	Ledgers      []LedgerDTO
}

// ListTradeOrderDTO 是订单列表输出。
type ListTradeOrderDTO struct {
	Items []*TradeOrderDTO
	Total int64
}

// PayNotifyResult 是回调处理结果（响应约定：重复返回成功，验签失败/未完成返回失败码）。
type PayNotifyResult struct {
	Success   bool
	Code      string
	Message   string
	Duplicate bool
}

// AuditRefundResult 是退款审核结果：Duplicate=true 表示重复提交按幂等返回成功。
type AuditRefundResult struct {
	Order     *TradeOrderDTO
	Duplicate bool
	Message   string
}

// PayCallbackDTO 是渠道回调记录输出，供客服/运营用渠道流水号反查处理结果。
type PayCallbackDTO struct {
	TradeNo         string
	OrderID         string
	AmountCents     int64
	Status          string
	Hits            int32
	CreatedAt       string
	UpdatedAt       string
	ExceptionType   string
	ExceptionStatus string
}

// ExceptionPayDTO 是支付异常输出。
type ExceptionPayDTO struct {
	ID                 string
	Type               string
	OrderID            string
	TradeNo            string
	ChannelAmountCents int64
	SystemAmountCents  int64
	Status             string
	Handler            string
	Result             string
	CreatedAt          string
}

// TradeSummaryDTO 是运营核查汇总输出。
type TradeSummaryDTO struct {
	PaidTotalCents     int64
	RefundedTotalCents int64
	AvailableTotal     int32
	ReservedTotal      int32
	ExceptionPending   int32
	OrdersByStatus     map[string]int32
}

// FromProduct 将商品实体转为输出 DTO。
func FromProduct(item *entity.Product) *ProductDTO {
	return &ProductDTO{
		ID:         item.ID,
		Name:       item.Name,
		PriceCents: item.PriceCents,
		Available:  item.Available,
		Total:      item.Total,
		OnSale:     item.OnSale,
	}
}

// FromTradeOrder 将订单实体与台账转为输出 DTO。
func FromTradeOrder(item *entity.TradeOrder, ledgers []entity.Ledger) *TradeOrderDTO {
	output := &TradeOrderDTO{
		ID:           item.ID,
		BuyerID:      item.BuyerID,
		BuyerName:    item.BuyerName,
		ProductID:    item.ProductID,
		ProductName:  item.ProductName,
		Qty:          item.Qty,
		PriceCents:   item.PriceCents,
		AmountCents:  item.AmountCents(),
		Status:       item.Status,
		CreatedAt:    formatTime(item.CreatedAt),
		Deadline:     formatTime(item.Deadline),
		ClosedReason: item.ClosedReason,
		Carrier:      item.Carrier,
		TrackingNo:   item.TrackingNo,
		RejectReason: item.RejectReason,
		RefundFail:   item.RefundFail,
		Ledgers:      make([]LedgerDTO, 0, len(ledgers)),
	}
	if !item.PaidAt.IsZero() {
		output.PaidAt = formatTime(item.PaidAt)
	}
	for _, ledger := range ledgers {
		output.Ledgers = append(output.Ledgers, LedgerDTO{
			Kind:        ledger.Kind,
			Time:        formatTime(ledger.Time),
			Text:        ledger.Text,
			AmountCents: ledger.AmountCents,
			TradeNo:     ledger.TradeNo,
			Status:      ledger.Status,
		})
	}
	return output
}

// FromExceptionPay 将异常实体转为输出 DTO。
func FromExceptionPay(item *entity.ExceptionPay) *ExceptionPayDTO {
	return &ExceptionPayDTO{
		ID:                 item.ID,
		Type:               item.Type,
		OrderID:            item.OrderID,
		TradeNo:            item.TradeNo,
		ChannelAmountCents: item.ChannelAmountCents,
		SystemAmountCents:  item.SystemAmountCents,
		Status:             item.Status,
		Handler:            item.Handler,
		Result:             item.Result,
		CreatedAt:          formatTime(item.CreatedAt),
	}
}

// FromPayCallback 将回调记录（可关联异常）转为输出 DTO。
func FromPayCallback(item *entity.PayCallback, exception *entity.ExceptionPay) *PayCallbackDTO {
	output := &PayCallbackDTO{
		TradeNo:     item.TradeNo,
		OrderID:     item.OrderID,
		AmountCents: item.AmountCents,
		Status:      item.Status,
		Hits:        item.Hits,
		CreatedAt:   formatTime(item.CreatedAt),
		UpdatedAt:   formatTime(item.UpdatedAt),
	}
	if exception != nil {
		output.ExceptionType = exception.Type
		output.ExceptionStatus = exception.Status
	}
	return output
}

// FromSummary 将汇总实体转为输出 DTO。
func FromSummary(item *entity.Summary) *TradeSummaryDTO {
	return &TradeSummaryDTO{
		PaidTotalCents:     item.PaidTotalCents,
		RefundedTotalCents: item.RefundedTotalCents,
		AvailableTotal:     item.AvailableTotal,
		ReservedTotal:      item.ReservedTotal,
		ExceptionPending:   item.ExceptionPending,
		OrdersByStatus:     item.OrdersByStatus,
	}
}
