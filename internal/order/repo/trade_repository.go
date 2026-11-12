package repo

import (
	"context"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/BwCloudWeGo/bw-cli/internal/order/entity"
	dbmodel "github.com/BwCloudWeGo/bw-cli/internal/order/model"
)

// UpdateAutoMigrate 扩展迁移：新增交易域五张表。
// 由 AutoMigrate 内部调用，保持 order 服务只需一个迁移入口。
func updateTradeAutoMigrate(db *gorm.DB) error {
	return db.AutoMigrate(
		&dbmodel.ProductModel{},
		&dbmodel.TradeOrderModel{},
		&dbmodel.TradeLedgerModel{},
		&dbmodel.PayCallbackModel{},
		&dbmodel.ExceptionPayModel{},
	)
}

var _ entity.TradeRepository = (*GormRepository)(nil)

// ListProducts 返回全部商品（含下架）。
func (r *GormRepository) ListProducts(ctx context.Context) ([]*entity.Product, error) {
	var records []dbmodel.ProductModel
	if err := r.db.WithContext(ctx).Order("id asc").Find(&records).Error; err != nil {
		return nil, err
	}
	items := make([]*entity.Product, 0, len(records))
	for i := range records {
		items = append(items, toProductDomain(&records[i]))
	}
	return items, nil
}

// FindProduct 按 ID 查询商品。
func (r *GormRepository) FindProduct(ctx context.Context, id string) (*entity.Product, error) {
	var record dbmodel.ProductModel
	err := r.db.WithContext(ctx).Where("id = ?", id).First(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, entity.ErrProductNotFound
	}
	if err != nil {
		return nil, err
	}
	return toProductDomain(&record), nil
}

// SaveProduct 新增或更新商品；显式维护时间字段，避免零值时间写入严格模式 MySQL。
func (r *GormRepository) SaveProduct(ctx context.Context, product *entity.Product) error {
	now := time.Now()
	updates := map[string]any{
		"name":        product.Name,
		"price_cents": product.PriceCents,
		"available":   product.Available,
		"on_sale":     product.OnSale,
		"total":       product.Total,
		"updated_at":  now,
	}
	res := r.db.WithContext(ctx).Model(&dbmodel.ProductModel{}).
		Where("id = ?", product.ID).Updates(updates)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected > 0 {
		return nil
	}
	return r.db.WithContext(ctx).Create(&dbmodel.ProductModel{
		ID:         product.ID,
		Name:       product.Name,
		PriceCents: product.PriceCents,
		Total:      product.Total,
		Available:  product.Available,
		OnSale:     product.OnSale,
		CreatedAt:  now,
		UpdatedAt:  now,
	}).Error
}

// HasAnyProduct 判断商品表是否为空（用于首次启动播种）。
func (r *GormRepository) HasAnyProduct(ctx context.Context) (bool, error) {
	var count int64
	if err := r.db.WithContext(ctx).Model(&dbmodel.ProductModel{}).Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// PlaceOrderTx 在同一事务内完成：校验商品 → 条件扣减可售数量 → 创建订单 → 写库存预占流水。
// 条件扣减失败（可售不足）直接返回 ErrInsufficientStock，不产生订单，保证并发下不超卖。
func (r *GormRepository) PlaceOrderTx(ctx context.Context, buyerID string, buyerName string, productID string, qty int32, keep time.Duration) (*entity.TradeOrder, error) {
	var order *entity.TradeOrder
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var product dbmodel.ProductModel
		if err := tx.Where("id = ?", productID).First(&product).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return entity.ErrProductNotFound
			}
			return err
		}
		if qty < 1 || qty > 2 {
			return entity.ErrInvalidQty
		}
		if !product.OnSale {
			return entity.ErrProductOffSale
		}
		now := time.Now()
		res := tx.Model(&dbmodel.ProductModel{}).
			Where("id = ? AND available >= ?", productID, qty).
			Updates(map[string]any{"available": gorm.Expr("available - ?", qty), "updated_at": now})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return entity.ErrInsufficientStock
		}
		order = &entity.TradeOrder{
			ID:          entity.NewTradeOrderID(),
			BuyerID:     buyerID,
			BuyerName:   buyerName,
			ProductID:   productID,
			ProductName: product.Name,
			Qty:         qty,
			PriceCents:  product.PriceCents,
			Status:      entity.OrderStatusCreated,
			CreatedAt:   now,
			Deadline:    now.Add(keep),
			Origin:      entity.OrderOriginSale,
			UpdatedAt:   now,
		}
		if err := tx.Create(toTradeOrderModel(order)).Error; err != nil {
			return err
		}
		return tx.Create(&dbmodel.TradeLedgerModel{
			OrderID:   order.ID,
			Kind:      entity.LedgerStock,
			Text:      "下单预占库存 " + itoa(qty) + " 件",
			Status:    entity.OrderStatusCreated,
			CreatedAt: now,
		}).Error
	})
	if err != nil {
		return nil, err
	}
	return order, nil
}

// FindTradeOrder 按 ID 查询订单。
func (r *GormRepository) FindTradeOrder(ctx context.Context, id string) (*entity.TradeOrder, error) {
	var record dbmodel.TradeOrderModel
	err := r.db.WithContext(ctx).Where("id = ?", id).First(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, entity.ErrTradeOrderNotFound
	}
	if err != nil {
		return nil, err
	}
	return toTradeOrderDomain(&record), nil
}

// ListTradeOrders 按买家与状态过滤订单（status/buyerID 为空表示不过滤）。
func (r *GormRepository) ListTradeOrders(ctx context.Context, buyerID string, status string) ([]*entity.TradeOrder, int64, error) {
	query := r.db.WithContext(ctx).Model(&dbmodel.TradeOrderModel{})
	if buyerID != "" {
		query = query.Where("buyer_id = ?", buyerID)
	}
	if status != "" {
		query = query.Where("status = ?", status)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var records []dbmodel.TradeOrderModel
	if err := query.Order("created_at desc").Find(&records).Error; err != nil {
		return nil, 0, err
	}
	items := make([]*entity.TradeOrder, 0, len(records))
	for i := range records {
		items = append(items, toTradeOrderDomain(&records[i]))
	}
	return items, total, nil
}

// TransitionOrder 条件更新实现唯一状态迁移入口：
// UPDATE ... SET status=to WHERE id=? AND status=from；rows=0 即迁移失败（幂等拒绝）。
// stockDelta>0 时在同一事务内回补可售数量，mutate 返回的台账条目同样随事务写入。
func (r *GormRepository) TransitionOrder(ctx context.Context, orderID string, from string, to string, stockDelta int32, mutate func(order *entity.TradeOrder) ([]entity.Ledger, error)) (bool, error) {
	applied := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now()
		res := tx.Model(&dbmodel.TradeOrderModel{}).
			Where("id = ? AND status = ?", orderID, from).
			Updates(map[string]any{"status": to, "updated_at": now})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return nil // 条件不满足：已被并发处理或状态不符
		}
		var record dbmodel.TradeOrderModel
		if err := tx.Where("id = ?", orderID).First(&record).Error; err != nil {
			return err
		}
		order := toTradeOrderDomain(&record)
		order.Status = to
		order.UpdatedAt = now
		var ledgers []entity.Ledger
		if mutate != nil {
			var err error
			ledgers, err = mutate(order)
			if err != nil {
				return err
			}
		}
		updates := map[string]any{
			"status":        to,
			"paid_at":       nullableTime(order.PaidAt),
			"closed_reason": order.ClosedReason,
			"carrier":       order.Carrier,
			"tracking_no":   order.TrackingNo,
			"reject_reason": order.RejectReason,
			"refund_fail":   order.RefundFail,
			"origin":        order.Origin,
			"updated_at":    now,
		}
		if err := tx.Model(&dbmodel.TradeOrderModel{}).Where("id = ?", orderID).Updates(updates).Error; err != nil {
			return err
		}
		if stockDelta > 0 {
			if err := tx.Model(&dbmodel.ProductModel{}).
				Where("id = ?", order.ProductID).
				Updates(map[string]any{
					"available":  gorm.Expr("available + ?", stockDelta),
					"updated_at": now,
				}).Error; err != nil {
				return err
			}
		}
		for i := range ledgers {
			entry := ledgers[i]
			if entry.Time.IsZero() {
				entry.Time = now
			}
			if entry.OrderID == "" {
				entry.OrderID = orderID
			}
			if err := tx.Create(&dbmodel.TradeLedgerModel{
				OrderID:     entry.OrderID,
				Kind:        entry.Kind,
				Text:        entry.Text,
				AmountCents: entry.AmountCents,
				TradeNo:     entry.TradeNo,
				Status:      entry.Status,
				CreatedAt:   entry.Time,
			}).Error; err != nil {
				return err
			}
		}
		applied = true
		return nil
	})
	return applied, err
}

// ListExpiredCreated 返回已超保留期的待支付订单（服务端时间判定）。
func (r *GormRepository) ListExpiredCreated(ctx context.Context, now time.Time) ([]*entity.TradeOrder, error) {
	var records []dbmodel.TradeOrderModel
	err := r.db.WithContext(ctx).
		Where("status = ? AND deadline <= ?", entity.OrderStatusCreated, now).
		Order("created_at asc").Limit(200).Find(&records).Error
	if err != nil {
		return nil, err
	}
	items := make([]*entity.TradeOrder, 0, len(records))
	for i := range records {
		items = append(items, toTradeOrderDomain(&records[i]))
	}
	return items, nil
}

// FindCallback 按渠道交易号查询回调记录。
func (r *GormRepository) FindCallback(ctx context.Context, tradeNo string) (*entity.PayCallback, error) {
	var record dbmodel.PayCallbackModel
	err := r.db.WithContext(ctx).Where("trade_no = ?", tradeNo).First(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &entity.PayCallback{
		TradeNo:     record.TradeNo,
		OrderID:     record.OrderID,
		AmountCents: record.AmountCents,
		Status:      record.Status,
		Hits:        record.Hits,
		CreatedAt:   record.CreatedAt,
		UpdatedAt:   record.UpdatedAt,
	}, nil
}

// SaveCallback 写入回调记录；主键冲突（重复通知）返回 ErrDuplicateTradeNo。
func (r *GormRepository) SaveCallback(ctx context.Context, cb *entity.PayCallback) error {
	record := &dbmodel.PayCallbackModel{
		TradeNo:     cb.TradeNo,
		OrderID:     cb.OrderID,
		AmountCents: cb.AmountCents,
		Status:      cb.Status,
		Hits:        1,
	}
	res := r.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(record)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return entity.ErrDuplicateTradeNo
	}
	return nil
}

// UpdateCallbackStatus 更新回调处理状态。
func (r *GormRepository) UpdateCallbackStatus(ctx context.Context, tradeNo string, status string) error {
	updates := map[string]any{"status": status, "updated_at": time.Now()}
	if status == entity.CallbackReceived {
		updates["hits"] = gorm.Expr("hits + 1")
	}
	return r.db.WithContext(ctx).Model(&dbmodel.PayCallbackModel{}).
		Where("trade_no = ?", tradeNo).Updates(updates).Error
}

// IncrementCallbackHits 仅累加重复命中次数，不改变处理状态
// （避免重复通知把已完成记录 DONE 降级回 RECEIVED，导致反查结果失真）。
func (r *GormRepository) IncrementCallbackHits(ctx context.Context, tradeNo string) error {
	return r.db.WithContext(ctx).Model(&dbmodel.PayCallbackModel{}).
		Where("trade_no = ?", tradeNo).
		Updates(map[string]any{"hits": gorm.Expr("hits + 1"), "updated_at": time.Now()}).Error
}

// ListCallbacks 按关键词（渠道交易号/订单号模糊匹配）与处理状态查询回调记录。
func (r *GormRepository) ListCallbacks(ctx context.Context, keyword string, status string) ([]*entity.PayCallback, error) {
	query := r.db.WithContext(ctx).Model(&dbmodel.PayCallbackModel{})
	if keyword = strings.TrimSpace(keyword); keyword != "" {
		like := "%" + keyword + "%"
		query = query.Where("trade_no LIKE ? OR order_id LIKE ?", like, like)
	}
	if status = strings.TrimSpace(status); status != "" {
		query = query.Where("status = ?", status)
	}
	var records []dbmodel.PayCallbackModel
	if err := query.Order("created_at desc").Limit(200).Find(&records).Error; err != nil {
		return nil, err
	}
	items := make([]*entity.PayCallback, 0, len(records))
	for i := range records {
		items = append(items, &entity.PayCallback{
			TradeNo:     records[i].TradeNo,
			OrderID:     records[i].OrderID,
			AmountCents: records[i].AmountCents,
			Status:      records[i].Status,
			Hits:        records[i].Hits,
			CreatedAt:   records[i].CreatedAt,
			UpdatedAt:   records[i].UpdatedAt,
		})
	}
	return items, nil
}

// SaveException 写入支付异常记录。
func (r *GormRepository) SaveException(ctx context.Context, ex *entity.ExceptionPay) error {
	now := time.Now()
	if ex.CreatedAt.IsZero() {
		ex.CreatedAt = now
	}
	return r.db.WithContext(ctx).Create(&dbmodel.ExceptionPayModel{
		ID:                 ex.ID,
		Type:               ex.Type,
		OrderID:            ex.OrderID,
		TradeNo:            ex.TradeNo,
		ChannelAmountCents: ex.ChannelAmountCents,
		SystemAmountCents:  ex.SystemAmountCents,
		Status:             ex.Status,
		Handler:            ex.Handler,
		Result:             ex.Result,
		CreatedAt:          ex.CreatedAt,
		UpdatedAt:          now,
	}).Error
}

// FindException 按 ID 查询异常记录。
func (r *GormRepository) FindException(ctx context.Context, id string) (*entity.ExceptionPay, error) {
	var record dbmodel.ExceptionPayModel
	err := r.db.WithContext(ctx).Where("id = ?", id).First(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, entity.ErrExceptionNotFound
	}
	if err != nil {
		return nil, err
	}
	return toExceptionDomain(&record), nil
}

// ListExceptions 列出异常记录，onlyPending 为真时仅返回待处理。
func (r *GormRepository) ListExceptions(ctx context.Context, onlyPending bool) ([]*entity.ExceptionPay, error) {
	query := r.db.WithContext(ctx).Model(&dbmodel.ExceptionPayModel{})
	if onlyPending {
		query = query.Where("status = ?", entity.ExcStatusPending)
	}
	var records []dbmodel.ExceptionPayModel
	if err := query.Order("created_at desc").Find(&records).Error; err != nil {
		return nil, err
	}
	items := make([]*entity.ExceptionPay, 0, len(records))
	for i := range records {
		items = append(items, toExceptionDomain(&records[i]))
	}
	return items, nil
}

// FindExceptionByTradeNo 用渠道交易号反查异常记录；不存在返回 (nil, nil)。
func (r *GormRepository) FindExceptionByTradeNo(ctx context.Context, tradeNo string) (*entity.ExceptionPay, error) {
	var record dbmodel.ExceptionPayModel
	err := r.db.WithContext(ctx).Where("trade_no = ?", tradeNo).
		Order("created_at desc").First(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return toExceptionDomain(&record), nil
}

// CloseException 闭环异常记录（条件更新：仅待处理可闭环）。
func (r *GormRepository) CloseException(ctx context.Context, id string, handler string, result string) error {
	res := r.db.WithContext(ctx).Model(&dbmodel.ExceptionPayModel{}).
		Where("id = ? AND status = ?", id, entity.ExcStatusPending).
		Updates(map[string]any{
			"status":     entity.ExcStatusClosed,
			"handler":    handler,
			"result":     result,
			"updated_at": time.Now(),
		})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return entity.ErrExceptionClosed
	}
	return nil
}

// MarkRefundFailed 记录渠道退款明确失败：订单保持 REFUNDING，写失败流水并置失败标记。
// 使用条件更新，仅当订单仍在退款审核中才生效。
func (r *GormRepository) MarkRefundFailed(ctx context.Context, orderID string) (bool, error) {
	applied := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now()
		res := tx.Model(&dbmodel.TradeOrderModel{}).
			Where("id = ? AND status = ?", orderID, entity.OrderStatusRefunding).
			Updates(map[string]any{"refund_fail": true, "updated_at": now})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return nil
		}
		if err := tx.Create(&dbmodel.TradeLedgerModel{
			OrderID:   orderID,
			Kind:      entity.LedgerRefund,
			Text:      "渠道退款失败（明确失败），保留待处理，客服可重试",
			Status:    "failed",
			CreatedAt: now,
		}).Error; err != nil {
			return err
		}
		applied = true
		return nil
	})
	return applied, err
}

// AppendLedger 仅追加一条台账记录（不改变订单状态、不调整库存）。
func (r *GormRepository) AppendLedger(ctx context.Context, ledger *entity.Ledger) error {
	if ledger.Time.IsZero() {
		ledger.Time = time.Now()
	}
	return r.db.WithContext(ctx).Create(&dbmodel.TradeLedgerModel{
		OrderID:     ledger.OrderID,
		Kind:        ledger.Kind,
		Text:        ledger.Text,
		AmountCents: ledger.AmountCents,
		TradeNo:     ledger.TradeNo,
		Status:      ledger.Status,
		CreatedAt:   ledger.Time,
	}).Error
}

// ListLedgers 返回订单的全部台账记录（时间正序）。
func (r *GormRepository) ListLedgers(ctx context.Context, orderID string) ([]entity.Ledger, error) {
	var records []dbmodel.TradeLedgerModel
	if err := r.db.WithContext(ctx).Where("order_id = ?", orderID).
		Order("created_at asc, id asc").Find(&records).Error; err != nil {
		return nil, err
	}
	items := make([]entity.Ledger, 0, len(records))
	for i := range records {
		items = append(items, entity.Ledger{
			Kind:        records[i].Kind,
			OrderID:     records[i].OrderID,
			Text:        records[i].Text,
			AmountCents: records[i].AmountCents,
			TradeNo:     records[i].TradeNo,
			Status:      records[i].Status,
			Time:        records[i].CreatedAt,
		})
	}
	return items, nil
}

// TradeSummary 汇总已收款、已退款、可售总量、异常待处理与各状态订单数。
func (r *GormRepository) TradeSummary(ctx context.Context) (*entity.Summary, error) {
	summary := &entity.Summary{OrdersByStatus: map[string]int32{}}
	var paid, refunded int64
	if err := r.db.WithContext(ctx).Model(&dbmodel.TradeLedgerModel{}).
		Where("kind = ?", entity.LedgerPay).
		Select("COALESCE(SUM(amount_cents),0)").Scan(&paid).Error; err != nil {
		return nil, err
	}
	if err := r.db.WithContext(ctx).Model(&dbmodel.TradeLedgerModel{}).
		Where("kind = ? AND status = ?", entity.LedgerRefund, "success").
		Select("COALESCE(SUM(amount_cents),0)").Scan(&refunded).Error; err != nil {
		return nil, err
	}
	summary.PaidTotalCents = paid
	summary.RefundedTotalCents = refunded

	var available int64
	if err := r.db.WithContext(ctx).Model(&dbmodel.ProductModel{}).
		Select("COALESCE(SUM(available),0)").Scan(&available).Error; err != nil {
		return nil, err
	}
	summary.AvailableTotal = int32(available)

	var reserved int64
	if err := r.db.WithContext(ctx).Model(&dbmodel.TradeOrderModel{}).
		Where("status = ?", entity.OrderStatusCreated).
		Select("COALESCE(SUM(qty),0)").Scan(&reserved).Error; err != nil {
		return nil, err
	}
	summary.ReservedTotal = int32(reserved)

	var pending int64
	if err := r.db.WithContext(ctx).Model(&dbmodel.ExceptionPayModel{}).
		Where("status = ?", entity.ExcStatusPending).Count(&pending).Error; err != nil {
		return nil, err
	}
	summary.ExceptionPending = int32(pending)

	type statusRow struct {
		K string `gorm:"column:k"`
		N int32  `gorm:"column:n"`
	}
	var rows []statusRow
	if err := r.db.WithContext(ctx).Model(&dbmodel.TradeOrderModel{}).
		Select("status as k, count(*) as n").Group("status").Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		summary.OrdersByStatus[row.K] = row.N
	}
	return summary, nil
}

func toProductDomain(record *dbmodel.ProductModel) *entity.Product {
	return &entity.Product{
		ID:         record.ID,
		Name:       record.Name,
		PriceCents: record.PriceCents,
		Total:      record.Total,
		Available:  record.Available,
		OnSale:     record.OnSale,
	}
}

func toTradeOrderModel(order *entity.TradeOrder) *dbmodel.TradeOrderModel {
	return &dbmodel.TradeOrderModel{
		ID:           order.ID,
		BuyerID:      order.BuyerID,
		BuyerName:    order.BuyerName,
		ProductID:    order.ProductID,
		ProductName:  order.ProductName,
		Qty:          order.Qty,
		PriceCents:   order.PriceCents,
		Status:       order.Status,
		Deadline:     order.Deadline,
		PaidAt:       nullableTime(order.PaidAt),
		ClosedReason: order.ClosedReason,
		Carrier:      order.Carrier,
		TrackingNo:   order.TrackingNo,
		RejectReason: order.RejectReason,
		RefundFail:   order.RefundFail,
		Origin:       order.Origin,
		CreatedAt:    order.CreatedAt,
		UpdatedAt:    order.UpdatedAt,
	}
}

func toTradeOrderDomain(record *dbmodel.TradeOrderModel) *entity.TradeOrder {
	order := &entity.TradeOrder{
		ID:           record.ID,
		BuyerID:      record.BuyerID,
		BuyerName:    record.BuyerName,
		ProductID:    record.ProductID,
		ProductName:  record.ProductName,
		Qty:          record.Qty,
		PriceCents:   record.PriceCents,
		Status:       record.Status,
		CreatedAt:    record.CreatedAt,
		Deadline:     record.Deadline,
		ClosedReason: record.ClosedReason,
		Carrier:      record.Carrier,
		TrackingNo:   record.TrackingNo,
		RejectReason: record.RejectReason,
		RefundFail:   record.RefundFail,
		Origin:       record.Origin,
		UpdatedAt:    record.UpdatedAt,
	}
	if record.PaidAt != nil {
		order.PaidAt = *record.PaidAt
	}
	return order
}

func toExceptionDomain(record *dbmodel.ExceptionPayModel) *entity.ExceptionPay {
	return &entity.ExceptionPay{
		ID:                 record.ID,
		Type:               record.Type,
		OrderID:            record.OrderID,
		TradeNo:            record.TradeNo,
		ChannelAmountCents: record.ChannelAmountCents,
		SystemAmountCents:  record.SystemAmountCents,
		Status:             record.Status,
		Handler:            record.Handler,
		Result:             record.Result,
		CreatedAt:          record.CreatedAt,
		UpdatedAt:          record.UpdatedAt,
	}
}

func nullableTime(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	return &value
}

func itoa(v int32) string {
	if v == 0 {
		return "0"
	}
	negative := v < 0
	if negative {
		v = -v
	}
	buf := make([]byte, 0, 8)
	for v > 0 {
		buf = append([]byte{byte('0' + v%10)}, buf...)
		v /= 10
	}
	if negative {
		buf = append([]byte{'-'}, buf...)
	}
	return string(buf)
}
