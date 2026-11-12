package model

import "time"

// ProductModel 是 shop_products 表的持久化模型（运营维护售价与可售数量）。
type ProductModel struct {
	ID         string `gorm:"primaryKey;size:32"`
	Name       string `gorm:"size:128;not null"`
	PriceCents int64  `gorm:"not null"`
	Total      int32  `gorm:"not null"`
	Available  int32  `gorm:"not null"`
	// 注意不可加 default:true：GORM 会在零值 false 时跳过该列改用数据库默认值，
	// 导致新建下架商品被存成在售（真实缺陷，repo 直测发现）。
	OnSale    bool `gorm:"not null"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

// TableName 指定表名。
func (ProductModel) TableName() string { return "shop_products" }

// TradeOrderModel 是 shop_trade_orders 表的持久化模型。
// PriceCents 为下单时价格快照，后续调价不影响已有订单金额。
type TradeOrderModel struct {
	ID           string `gorm:"primaryKey;size:32"`
	BuyerID      string `gorm:"size:64;index:idx_buyer_status,priority:1"`
	BuyerName    string `gorm:"size:128"`
	ProductID    string `gorm:"size:32;index"`
	ProductName  string `gorm:"size:128"`
	Qty          int32  `gorm:"not null"`
	PriceCents   int64  `gorm:"not null"`
	Status       string `gorm:"size:16;not null;index:idx_buyer_status,priority:2"`
	Deadline     time.Time
	PaidAt       *time.Time
	ClosedReason string `gorm:"size:255"`
	Carrier      string `gorm:"size:64"`
	TrackingNo   string `gorm:"size:64"`
	RejectReason string `gorm:"size:255"`
	RefundFail   bool   `gorm:"not null;default:false"`
	Origin       string `gorm:"size:16;not null;default:'sale'"`
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// TableName 指定表名。
func (TradeOrderModel) TableName() string { return "shop_trade_orders" }

// TradeLedgerModel 是统一台账表：库存 / 收款 / 退款 / 发货 / 异常共用。
type TradeLedgerModel struct {
	ID          uint64 `gorm:"primaryKey;autoIncrement"`
	OrderID     string `gorm:"size:32;index"`
	Kind        string `gorm:"size:16;not null;index"`
	Text        string `gorm:"size:512"`
	AmountCents int64  `gorm:"not null;default:0"`
	TradeNo     string `gorm:"size:64;index"`
	Status      string `gorm:"size:32"`
	CreatedAt   time.Time
}

// TableName 指定表名。
func (TradeLedgerModel) TableName() string { return "shop_trade_ledgers" }

// PayCallbackModel 是渠道回调记录表：主键 channel_trade_no 即入口幂等键。
type PayCallbackModel struct {
	TradeNo     string `gorm:"primaryKey;size:64"`
	OrderID     string `gorm:"size:32;index"`
	AmountCents int64  `gorm:"not null"`
	Status      string `gorm:"size:16;not null;index"`
	Hits        int32  `gorm:"not null;default:1"`
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// TableName 指定表名。
func (PayCallbackModel) TableName() string { return "shop_pay_callbacks" }

// ExceptionPayModel 是支付异常表：不允许只打日志，必须有可查记录并能闭环。
type ExceptionPayModel struct {
	ID                 string `gorm:"primaryKey;size:48"`
	Type               string `gorm:"size:32;not null;index"`
	OrderID            string `gorm:"size:32;index"`
	TradeNo            string `gorm:"size:64;index"`
	ChannelAmountCents int64  `gorm:"not null"`
	SystemAmountCents  int64  `gorm:"not null"`
	Status             string `gorm:"size:16;not null;index"`
	Handler            string `gorm:"size:64"`
	Result             string `gorm:"size:255"`
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// TableName 指定表名。
func (ExceptionPayModel) TableName() string { return "shop_exception_pays" }
