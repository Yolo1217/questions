package service

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/BwCloudWeGo/bw-cli/internal/order/dto"
	"github.com/BwCloudWeGo/bw-cli/internal/order/entity"
)

// DefaultChannelSecret 是模拟支付渠道与商城共享的验签密钥默认值（生产必须由配置注入）。
const DefaultChannelSecret = "shop-mock-channel-secret"

// TradeConfig 是交易域策略配置。
type TradeConfig struct {
	// Keep 是待支付订单保留期（服务端时间判定，默认 10 分钟）。
	Keep time.Duration
	// RefundFailFirst 开启后客服第一次退款会「渠道明确失败」，重试才成功（演示 S3）。
	RefundFailFirst bool
	// ChannelSecret 是支付回调 HMAC 验签密钥，渠道与商城共享。
	ChannelSecret string
}

// SignPayNotify 计算渠道回调签名：HMAC-SHA256(secret, "trade_no|order_id|amount_cents") 十六进制小写。
// 商城按同一算法自行计算比对，因此伪造/篡改金额的请求无法通过验签。
func SignPayNotify(secret string, tradeNo string, orderID string, amountCents int64) string {
	if secret == "" {
		secret = DefaultChannelSecret
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = fmt.Fprintf(mac, "%s|%s|%d", tradeNo, orderID, amountCents)
	return hex.EncodeToString(mac.Sum(nil))
}

// TradeService 编排题目01 的交易用例：商品、订单状态机、支付回调、退款、发货、异常闭环、汇总。
type TradeService struct {
	repo entity.TradeRepository
	cfg  TradeConfig
	log  *zap.Logger
}

// NewTradeService 创建交易用例服务。
func NewTradeService(repo entity.TradeRepository, cfg TradeConfig, log *zap.Logger) *TradeService {
	if log == nil {
		log = zap.NewNop()
	}
	if cfg.Keep <= 0 {
		cfg.Keep = 10 * time.Minute
	}
	if strings.TrimSpace(cfg.ChannelSecret) == "" {
		cfg.ChannelSecret = DefaultChannelSecret
	}
	return &TradeService{repo: repo, cfg: cfg, log: log}
}

// SeedProducts 在商品表为空时播种预置商品（P1 单价 10000 分 / 可售 5 件，P2 单价 6800 分 / 可售 8 件）。
func (s *TradeService) SeedProducts(ctx context.Context) error {
	exists, err := s.repo.HasAnyProduct(ctx)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	seeds := []*entity.Product{
		{ID: "P1", Name: "羊绒围巾 · 燕麦色", PriceCents: 10000, Total: 5, Available: 5, OnSale: true},
		{ID: "P2", Name: "手作陶瓷马克杯", PriceCents: 6800, Total: 8, Available: 8, OnSale: true},
	}
	for _, item := range seeds {
		if err := s.repo.SaveProduct(ctx, item); err != nil {
			return err
		}
	}
	s.log.Info("trade products seeded", zap.Int("count", len(seeds)))
	return nil
}

// ListProducts 返回全部商品。
func (s *TradeService) ListProducts(ctx context.Context) ([]*dto.ProductDTO, error) {
	items, err := s.repo.ListProducts(ctx)
	if err != nil {
		return nil, err
	}
	output := make([]*dto.ProductDTO, 0, len(items))
	for _, item := range items {
		output = append(output, dto.FromProduct(item))
	}
	return output, nil
}

// UpsertProduct 运营维护商品：价格与可售数量可调整，已有订单按下单快照不受影响。
func (s *TradeService) UpsertProduct(ctx context.Context, cmd dto.UpsertProductCommand) (*dto.ProductDTO, error) {
	if strings.TrimSpace(cmd.ID) == "" || strings.TrimSpace(cmd.Name) == "" {
		return nil, entity.ErrInvalidQty
	}
	if cmd.PriceCents <= 0 || cmd.Available < 0 {
		return nil, entity.ErrInvalidQty
	}
	product, err := s.repo.FindProduct(ctx, cmd.ID)
	if err != nil && !errors.Is(err, entity.ErrProductNotFound) {
		return nil, err
	}
	if errors.Is(err, entity.ErrProductNotFound) {
		product = &entity.Product{ID: cmd.ID, Total: cmd.Available}
	}
	product.Name = cmd.Name
	product.PriceCents = cmd.PriceCents
	product.Available = cmd.Available
	product.OnSale = cmd.OnSale
	if product.Total < product.Available {
		product.Total = product.Available
	}
	if err := s.repo.SaveProduct(ctx, product); err != nil {
		return nil, err
	}
	s.log.Info("product upserted", zap.String("product_id", product.ID), zap.Int64("price_cents", product.PriceCents), zap.Int32("available", product.Available))
	return dto.FromProduct(product), nil
}

// PlaceOrder 买家下单：校验在售与数量，事务内预占库存并创建待支付订单。
func (s *TradeService) PlaceOrder(ctx context.Context, cmd dto.PlaceOrderCommand) (*dto.TradeOrderDTO, error) {
	if strings.TrimSpace(cmd.BuyerID) == "" {
		return nil, entity.ErrPermissionDenied
	}
	order, err := s.repo.PlaceOrderTx(ctx, cmd.BuyerID, cmd.BuyerName, cmd.ProductID, cmd.Qty, s.cfg.Keep)
	if err != nil {
		return nil, err
	}
	s.log.Info("trade order placed", zap.String("order_id", order.ID), zap.String("buyer_id", order.BuyerID), zap.Int32("qty", order.Qty))
	return s.GetOrder(ctx, order.ID)
}

// CancelOrder 买家取消待支付订单：条件更新释放预占，重复取消不多释放。
func (s *TradeService) CancelOrder(ctx context.Context, orderID string, buyerID string) (*dto.TradeOrderDTO, error) {
	order, err := s.repo.FindTradeOrder(ctx, orderID)
	if err != nil {
		return nil, err
	}
	if order.BuyerID != buyerID {
		return nil, entity.ErrPermissionDenied
	}
	applied, err := s.repo.TransitionOrder(ctx, order.ID, entity.OrderStatusCreated, entity.OrderStatusClosed, order.Qty,
		func(item *entity.TradeOrder) ([]entity.Ledger, error) {
			item.ClosedReason = "买家主动取消"
			return []entity.Ledger{{
				Kind:   entity.LedgerStock,
				Text:   "取消订单，释放预占 " + int32String(item.Qty) + " 件",
				Status: entity.OrderStatusClosed,
			}}, nil
		})
	if err != nil {
		return nil, err
	}
	if !applied {
		return nil, entity.ErrInvalidTransition
	}
	s.log.Info("trade order cancelled", zap.String("order_id", order.ID))
	return s.GetOrder(ctx, order.ID)
}

// ApplyRefund 买家对已付款未发货订单申请整单退款：进入退款审核，冻结发货。
func (s *TradeService) ApplyRefund(ctx context.Context, orderID string, buyerID string) (*dto.TradeOrderDTO, error) {
	order, err := s.repo.FindTradeOrder(ctx, orderID)
	if err != nil {
		return nil, err
	}
	if order.BuyerID != buyerID {
		return nil, entity.ErrPermissionDenied
	}
	if order.Status == entity.OrderStatusShipped {
		return nil, entity.ErrRefundNotAllowed
	}
	applied, err := s.repo.TransitionOrder(ctx, order.ID, entity.OrderStatusPaid, entity.OrderStatusRefunding, 0,
		func(item *entity.TradeOrder) ([]entity.Ledger, error) {
			return []entity.Ledger{{
				Kind:        entity.LedgerRefund,
				Text:        "买家申请整单退款，已冻结发货",
				AmountCents: item.AmountCents(),
				Status:      "processing",
			}}, nil
		})
	if err != nil {
		return nil, err
	}
	if !applied {
		return nil, entity.ErrInvalidTransition
	}
	s.log.Info("refund applied", zap.String("order_id", order.ID))
	return s.GetOrder(ctx, order.ID)
}

// AuditRefund 客服退款审核：同意走渠道退款（成功才回补库存），拒绝须写原因并恢复待发货。
// 已退款订单重复审核/渠道重复退款通知按幂等返回成功，不重复打款、不回补库存。
func (s *TradeService) AuditRefund(ctx context.Context, cmd dto.AuditRefundCommand) (*dto.AuditRefundResult, error) {
	order, err := s.repo.FindTradeOrder(ctx, cmd.OrderID)
	if err != nil {
		return nil, err
	}
	if order.Status == entity.OrderStatusRefunded {
		detail, err := s.GetOrder(ctx, order.ID)
		if err != nil {
			return nil, err
		}
		return &dto.AuditRefundResult{
			Order:     detail,
			Duplicate: true,
			Message:   "退款已成功：本次为重复提交或重复退款通知，按幂等返回成功（不重复打款、不回补库存）",
		}, nil
	}
	if order.Status != entity.OrderStatusRefunding {
		return nil, entity.ErrInvalidTransition
	}
	if !cmd.Approve {
		if strings.TrimSpace(cmd.Reason) == "" {
			return nil, entity.ErrReasonRequired
		}
		applied, err := s.repo.TransitionOrder(ctx, order.ID, entity.OrderStatusRefunding, entity.OrderStatusPaid, 0,
			func(item *entity.TradeOrder) ([]entity.Ledger, error) {
				item.RejectReason = cmd.Reason
				return []entity.Ledger{{
					Kind:   entity.LedgerRefund,
					Text:   "客服拒绝退款（" + cmd.Operator + "）：" + cmd.Reason + "（订单恢复待发货）",
					Status: "rejected",
				}}, nil
			})
		if err != nil {
			return nil, err
		}
		if !applied {
			return nil, entity.ErrInvalidTransition
		}
		detail, err := s.GetOrder(ctx, order.ID)
		if err != nil {
			return nil, err
		}
		s.log.Info("refund rejected", zap.String("order_id", order.ID), zap.String("operator", cmd.Operator))
		return &dto.AuditRefundResult{Order: detail, Message: "已拒绝退款，订单恢复待发货"}, nil
	}

	// 渠道明确失败：保持 REFUNDING，保留待处理记录，等待客服重试。
	if s.cfg.RefundFailFirst && !order.RefundFail {
		applied, err := s.repo.MarkRefundFailed(ctx, order.ID)
		if err != nil {
			return nil, err
		}
		if !applied {
			return nil, entity.ErrInvalidTransition
		}
		s.log.Warn("channel refund failed", zap.String("order_id", order.ID))
		return nil, entity.ErrChannelRefundFailed
	}

	// 退款成功：仅正常售前退款回补库存；迟到支付退款不回补（此前已释放，回补即超卖）。
	restock := int32(0)
	if order.Origin == entity.OrderOriginSale {
		restock = order.Qty
	}
	applied, err := s.repo.TransitionOrder(ctx, order.ID, entity.OrderStatusRefunding, entity.OrderStatusRefunded, restock,
		func(item *entity.TradeOrder) ([]entity.Ledger, error) {
			ledgers := []entity.Ledger{{
				Kind:        entity.LedgerRefund,
				Text:        "渠道退款成功，原路退回",
				AmountCents: item.AmountCents(),
				TradeNo:     "RF" + item.ID,
				Status:      "success",
			}}
			if item.Origin == entity.OrderOriginSale {
				ledgers = append(ledgers, entity.Ledger{
					Kind:   entity.LedgerStock,
					Text:   "售前退款成功，回补可售 " + int32String(item.Qty) + " 件",
					Status: entity.OrderStatusRefunded,
				})
			}
			return ledgers, nil
		})
	if err != nil {
		return nil, err
	}
	if !applied {
		return nil, entity.ErrInvalidTransition
	}
	detail, err := s.GetOrder(ctx, order.ID)
	if err != nil {
		return nil, err
	}
	s.log.Info("refund succeeded", zap.String("order_id", order.ID), zap.String("operator", cmd.Operator), zap.Int32("restock", restock))
	message := "渠道退款成功，已回补可售数量"
	if restock == 0 {
		message = "渠道退款成功，迟到支付退款不回补库存"
	}
	return &dto.AuditRefundResult{Order: detail, Message: message}, nil
}

// ShipOrder 仓库为合法待发货订单登记一次发货。
func (s *TradeService) ShipOrder(ctx context.Context, cmd dto.ShipOrderCommand) (*dto.TradeOrderDTO, error) {
	if strings.TrimSpace(cmd.Carrier) == "" || strings.TrimSpace(cmd.TrackingNo) == "" {
		return nil, entity.ErrCarrierRequired
	}
	order, err := s.repo.FindTradeOrder(ctx, cmd.OrderID)
	if err != nil {
		return nil, err
	}
	switch order.Status {
	case entity.OrderStatusRefunding:
		return nil, entity.ErrOrderRefunding
	case entity.OrderStatusShipped:
		return nil, entity.ErrOrderAlreadyShipped
	case entity.OrderStatusClosed:
		return nil, entity.ErrOrderClosed
	case entity.OrderStatusRefunded:
		return nil, entity.ErrOrderRefunded
	case entity.OrderStatusPaid:
	default:
		return nil, entity.ErrInvalidTransition
	}
	applied, err := s.repo.TransitionOrder(ctx, order.ID, entity.OrderStatusPaid, entity.OrderStatusShipped, 0,
		func(item *entity.TradeOrder) ([]entity.Ledger, error) {
			item.Carrier = cmd.Carrier
			item.TrackingNo = cmd.TrackingNo
			return []entity.Ledger{{
				Kind:   entity.LedgerShip,
				Text:   "仓库发货登记：" + cmd.Carrier + " 运单 " + cmd.TrackingNo + "（操作人 " + cmd.Operator + "）",
				Status: entity.OrderStatusShipped,
			}}, nil
		})
	if err != nil {
		return nil, err
	}
	if !applied {
		return nil, entity.ErrInvalidTransition
	}
	s.log.Info("order shipped", zap.String("order_id", order.ID), zap.String("tracking_no", cmd.TrackingNo))
	return s.GetOrder(ctx, order.ID)
}

// PayNotify 处理渠道支付通知，固定六步 + 幂等三道防线。
func (s *TradeService) PayNotify(ctx context.Context, cmd dto.PayNotifyCommand) (*dto.PayNotifyResult, error) {
	if strings.TrimSpace(cmd.TradeNo) == "" || strings.TrimSpace(cmd.OrderID) == "" {
		return &dto.PayNotifyResult{Success: false, Code: "invalid_argument", Message: "缺少渠道交易号或订单号"}, nil
	}
	// 第 1 步：验签（先于任何落库）——伪造流量不产生回调记录，不会占用渠道交易号，
	// 真实回调重试不会被入口幂等误判为“重复”返回成功而不入账（评分整改：原实现先落库后验签存在该缺陷）。
	// 验签失败同样落异常表留痕（防重放证据），渠道流水号可在异常表反查；同流水号的合法重试可直接通过。
	expected := SignPayNotify(s.cfg.ChannelSecret, cmd.TradeNo, cmd.OrderID, cmd.AmountCents)
	provided := strings.ToLower(strings.TrimSpace(cmd.Signature))
	if subtle.ConstantTimeCompare([]byte(expected), []byte(provided)) != 1 {
		if err := s.recordException(ctx, entity.ExcBadSignature, cmd, nil); err != nil {
			return nil, err
		}
		s.log.Warn("callback signature verify failed", zap.String("trade_no", cmd.TradeNo))
		return &dto.PayNotifyResult{Success: false, Code: "verify_failed", Message: "验签失败：签名缺失或不匹配，不入账，已落异常表留痕（渠道用原流水号重试可通过）"}, nil
	}

	// 第 2 步：持久化回调记录（入口幂等：channel_trade_no 主键冲突即重复通知；仅验签通过的回调才会写入）
	if err := s.repo.SaveCallback(ctx, &entity.PayCallback{
		TradeNo:     cmd.TradeNo,
		OrderID:     cmd.OrderID,
		AmountCents: cmd.AmountCents,
		Status:      entity.CallbackReceived,
	}); err != nil {
		if errors.Is(err, entity.ErrDuplicateTradeNo) {
			_ = s.repo.IncrementCallbackHits(ctx, cmd.TradeNo)
			s.log.Info("duplicate callback rejected", zap.String("trade_no", cmd.TradeNo))
			return &dto.PayNotifyResult{Success: true, Duplicate: true, Code: "duplicate", Message: "重复通知：已处理过，返回成功（入口幂等，不重复入账）"}, nil
		}
		return nil, err
	}

	// 第 3 步：查订单，不存在落「无主支付」
	order, err := s.repo.FindTradeOrder(ctx, cmd.OrderID)
	if errors.Is(err, entity.ErrTradeOrderNotFound) {
		if err := s.recordException(ctx, entity.ExcNoOwner, cmd, nil); err != nil {
			return nil, err
		}
		_ = s.repo.UpdateCallbackStatus(ctx, cmd.TradeNo, entity.CallbackFailed)
		return &dto.PayNotifyResult{Success: false, Code: "order_not_found", Message: "无主支付：订单不存在，已落异常表（可用渠道交易号反查），返回失败码"}, nil
	}
	if err != nil {
		return nil, err
	}

	// 第 4 步：金额比对，不一致绝不入账
	if cmd.AmountCents != order.AmountCents() {
		if err := s.recordException(ctx, entity.ExcAmountMismatch, cmd, order); err != nil {
			return nil, err
		}
		_ = s.repo.UpdateCallbackStatus(ctx, cmd.TradeNo, entity.CallbackFailed)
		return &dto.PayNotifyResult{Success: false, Code: "amount_mismatch", Message: "金额不符：不入账、不发货，已落异常表，返回失败码"}, nil
	}

	// 第 5 步：按订单状态分流
	switch order.Status {
	case entity.OrderStatusCreated:
		applied, err := s.repo.TransitionOrder(ctx, order.ID, entity.OrderStatusCreated, entity.OrderStatusPaid, 0,
			func(item *entity.TradeOrder) ([]entity.Ledger, error) {
				now := time.Now()
				item.PaidAt = now
				return []entity.Ledger{
					{Kind: entity.LedgerPay, Text: "支付成功入账（预占转实扣）", AmountCents: item.AmountCents(), TradeNo: cmd.TradeNo, Status: "success", Time: now},
					{Kind: entity.LedgerStock, Text: "预占转实扣 " + int32String(item.Qty) + " 件", Status: entity.OrderStatusPaid, Time: now},
				}, nil
			})
		if err != nil {
			return nil, err
		}
		if !applied {
			// 缺陷整改（独立测试 D-2）：迁移失败不能一律当"重复"返回成功——
			// 若订单已被超时关单/退款收敛到终态，本笔真实支付将既无流水也无异常，
			// 资金悬置且渠道不再重推。终态场景必须落「关单后到账」异常闭环。
			latest, rerr := s.repo.FindTradeOrder(ctx, order.ID)
			if rerr != nil {
				return nil, rerr
			}
			if latest.Status == entity.OrderStatusClosed || latest.Status == entity.OrderStatusRefunded {
				if err := s.recordException(ctx, entity.ExcLatePay, cmd, latest); err != nil {
					return nil, err
				}
				_ = s.repo.UpdateCallbackStatus(ctx, cmd.TradeNo, entity.CallbackDone)
				s.log.Warn("late pay during close race", zap.String("order_id", order.ID), zap.String("trade_no", cmd.TradeNo))
				return &dto.PayNotifyResult{Success: true, Code: "success", Message: "支付确认前订单已关闭：落「关单后到账」异常，由客服核实退款"}, nil
			}
			// 结果幂等：并发通知中已被其他请求处理
			_ = s.repo.UpdateCallbackStatus(ctx, cmd.TradeNo, entity.CallbackDone)
			return &dto.PayNotifyResult{Success: true, Duplicate: true, Code: "duplicate", Message: "并发通知：订单已由其他请求处理，返回成功（状态仅迁移一次）"}, nil
		}
		_ = s.repo.UpdateCallbackStatus(ctx, cmd.TradeNo, entity.CallbackDone)
		s.log.Info("payment accepted", zap.String("order_id", order.ID), zap.String("trade_no", cmd.TradeNo))
		return &dto.PayNotifyResult{Success: true, Code: "success", Message: "支付成功：订单 " + order.ID + " → 待发货"}, nil

	case entity.OrderStatusPaid, entity.OrderStatusShipped, entity.OrderStatusRefunding:
		if err := s.recordException(ctx, entity.ExcDoubleCharge, cmd, order); err != nil {
			return nil, err
		}
		_ = s.repo.UpdateCallbackStatus(ctx, cmd.TradeNo, entity.CallbackDone)
		return &dto.PayNotifyResult{Success: true, Code: "success", Message: "订单已收款：判定重复扣款，落异常表，返回成功（不重复记款）"}, nil

	default:
		// CLOSED / REFUNDED：不恢复订单、不重新占用库存，落「关单后到账」由客服核实退款
		if err := s.recordException(ctx, entity.ExcLatePay, cmd, order); err != nil {
			return nil, err
		}
		_ = s.repo.UpdateCallbackStatus(ctx, cmd.TradeNo, entity.CallbackDone)
		return &dto.PayNotifyResult{Success: true, Code: "success", Message: "订单已关闭/已退款：落异常表「关单后到账」，返回成功（订单状态保持不变）"}, nil
	}
}

// ListExceptions 查询支付异常记录。
func (s *TradeService) ListExceptions(ctx context.Context, onlyPending bool) ([]*dto.ExceptionPayDTO, error) {
	items, err := s.repo.ListExceptions(ctx, onlyPending)
	if err != nil {
		return nil, err
	}
	output := make([]*dto.ExceptionPayDTO, 0, len(items))
	for _, item := range items {
		output = append(output, dto.FromExceptionPay(item))
	}
	return output, nil
}

// HandleException 客服处置支付异常：原路退款 / 联系渠道核实 / 标记忽略（须留原因）。
// 迟到支付退款：订单保持 CLOSED，仅写退款流水，不回补库存。
func (s *TradeService) HandleException(ctx context.Context, cmd dto.HandleExceptionCommand) (*dto.ExceptionPayDTO, error) {
	exception, err := s.repo.FindException(ctx, cmd.ExceptionID)
	if err != nil {
		return nil, err
	}
	if exception.Status != entity.ExcStatusPending {
		return nil, entity.ErrExceptionClosed
	}
	operator := strings.TrimSpace(cmd.Operator)
	if operator == "" {
		operator = "客服"
	}
	reason := strings.TrimSpace(cmd.Reason)
	switch cmd.Action {
	case "refund":
		if exception.Type == entity.ExcNoOwner || exception.OrderID == "" {
			return nil, entity.ErrExceptionNoRefund
		}
		if reason == "" {
			reason = "核实后原路退款"
		}
		order, err := s.repo.FindTradeOrder(ctx, exception.OrderID)
		if err != nil {
			return nil, err
		}
		if err := s.repo.CloseException(ctx, exception.ID, operator, "核实后原路退款成功（"+order.ID+"）；原因："+reason); err != nil {
			return nil, err
		}
		// 订单保持原状态：仅记录退款结果，不恢复订单、不动库存
		if err := s.repo.AppendLedger(ctx, &entity.Ledger{
			OrderID:     order.ID,
			Kind:        entity.LedgerRefund,
			Text:        "迟到支付退款：原路退回（不回补库存，订单保持 " + order.Status + "）",
			AmountCents: exception.ChannelAmountCents,
			TradeNo:     "RF" + exception.TradeNo,
			Status:      "success",
			Time:        time.Now(),
		}); err != nil {
			return nil, err
		}
		if err := s.appendExceptionLedger(ctx, exception, "客服从异常表发起退款（"+operator+"）："+reason); err != nil {
			return nil, err
		}
		s.log.Info("late payment refunded", zap.String("exception_id", exception.ID), zap.String("order_id", order.ID))
	case "ignore":
		if reason == "" {
			return nil, entity.ErrReasonRequired
		}
		if err := s.repo.CloseException(ctx, exception.ID, operator, "标记忽略："+reason); err != nil {
			return nil, err
		}
		if err := s.appendExceptionLedger(ctx, exception, "客服标记忽略（"+operator+"）："+reason); err != nil {
			return nil, err
		}
	default: // verify
		if reason == "" {
			return nil, entity.ErrReasonRequired
		}
		if err := s.repo.CloseException(ctx, exception.ID, operator, "已联系渠道核实："+reason); err != nil {
			return nil, err
		}
		if err := s.appendExceptionLedger(ctx, exception, "客服联系渠道核实（"+operator+"）："+reason); err != nil {
			return nil, err
		}
	}
	closed, err := s.repo.FindException(ctx, exception.ID)
	if err != nil {
		return nil, err
	}
	return dto.FromExceptionPay(closed), nil
}

// appendExceptionLedger 把异常处置结果写入订单台账，保证运营按订单能查到异常处理记录。
// 无主支付没有绑定订单，直接跳过（异常表本身已留痕）。
func (s *TradeService) appendExceptionLedger(ctx context.Context, exception *entity.ExceptionPay, text string) error {
	if strings.TrimSpace(exception.OrderID) == "" {
		return nil
	}
	return s.repo.AppendLedger(ctx, &entity.Ledger{
		OrderID:     exception.OrderID,
		Kind:        entity.LedgerException,
		Text:        text,
		AmountCents: exception.ChannelAmountCents,
		TradeNo:     exception.TradeNo,
		Status:      entity.ExcStatusClosed,
		Time:        time.Now(),
	})
}

// GetCallback 用渠道交易号反查回调处理结果：验签失败、金额不符、无主支付均可查询。
func (s *TradeService) GetCallback(ctx context.Context, tradeNo string) (*dto.PayCallbackDTO, error) {
	tradeNo = strings.TrimSpace(tradeNo)
	if tradeNo == "" {
		return nil, entity.ErrCallbackNotFound
	}
	callback, err := s.repo.FindCallback(ctx, tradeNo)
	if err != nil {
		return nil, err
	}
	if callback == nil {
		return nil, entity.ErrCallbackNotFound
	}
	exception, err := s.repo.FindExceptionByTradeNo(ctx, tradeNo)
	if err != nil {
		return nil, err
	}
	return dto.FromPayCallback(callback, exception), nil
}

// ListCallbacks 查询回调记录（客服/运营），keyword 支持渠道交易号或订单号。
func (s *TradeService) ListCallbacks(ctx context.Context, keyword string, status string) ([]*dto.PayCallbackDTO, error) {
	items, err := s.repo.ListCallbacks(ctx, keyword, status)
	if err != nil {
		return nil, err
	}
	output := make([]*dto.PayCallbackDTO, 0, len(items))
	for _, item := range items {
		exception, err := s.repo.FindExceptionByTradeNo(ctx, item.TradeNo)
		if err != nil {
			return nil, err
		}
		output = append(output, dto.FromPayCallback(item, exception))
	}
	return output, nil
}

// GetOrder 返回订单详情（含台账时间线）。
func (s *TradeService) GetOrder(ctx context.Context, orderID string) (*dto.TradeOrderDTO, error) {
	order, err := s.repo.FindTradeOrder(ctx, orderID)
	if err != nil {
		return nil, err
	}
	ledgers, err := s.repo.ListLedgers(ctx, orderID)
	if err != nil {
		return nil, err
	}
	return dto.FromTradeOrder(order, ledgers), nil
}

// ListOrders 按买家/状态过滤订单列表（对角色视图统一服务端过滤）。
func (s *TradeService) ListOrders(ctx context.Context, buyerID string, status string) (*dto.ListTradeOrderDTO, error) {
	items, total, err := s.repo.ListTradeOrders(ctx, buyerID, status)
	if err != nil {
		return nil, err
	}
	output := &dto.ListTradeOrderDTO{Items: make([]*dto.TradeOrderDTO, 0, len(items)), Total: total}
	for _, item := range items {
		ledgers, err := s.repo.ListLedgers(ctx, item.ID)
		if err != nil {
			return nil, err
		}
		output.Items = append(output.Items, dto.FromTradeOrder(item, ledgers))
	}
	return output, nil
}

// Summary 返回运营核查汇总。
func (s *TradeService) Summary(ctx context.Context) (*dto.TradeSummaryDTO, error) {
	summary, err := s.repo.TradeSummary(ctx)
	if err != nil {
		return nil, err
	}
	return dto.FromSummary(summary), nil
}

// SweepExpired 关闭已超保留期的待支付订单并释放预占（服务端时间判定，重复执行不多释放）。
func (s *TradeService) SweepExpired(ctx context.Context) (int, error) {
	expired, err := s.repo.ListExpiredCreated(ctx, time.Now())
	if err != nil {
		return 0, err
	}
	closed := 0
	for _, order := range expired {
		applied, err := s.repo.TransitionOrder(ctx, order.ID, entity.OrderStatusCreated, entity.OrderStatusClosed, order.Qty,
			func(item *entity.TradeOrder) ([]entity.Ledger, error) {
				item.ClosedReason = "超时关闭（服务端时间判定）"
				return []entity.Ledger{{
					Kind:   entity.LedgerStock,
					Text:   "超时关闭，释放预占 " + int32String(item.Qty) + " 件",
					Status: entity.OrderStatusClosed,
				}}, nil
			})
		if err != nil {
			return closed, err
		}
		if applied {
			closed++
			s.log.Info("order closed by timeout", zap.String("order_id", order.ID))
		}
	}
	return closed, nil
}

func (s *TradeService) recordException(ctx context.Context, excType string, cmd dto.PayNotifyCommand, order *entity.TradeOrder) error {
	// 缺陷整改（独立测试 D-1）：同一渠道流水号、同类型且仍待处理的异常只保留一条。
	// 验签失败的伪造流量可反复重推（回调记录不落库、入口幂等拦不住），若每次都新增
	// 工单会刷爆客服工作台；此处去重，后续重推仅记日志（首个工单仍可反查与闭环）。
	if existing, err := s.repo.FindExceptionByTradeNo(ctx, cmd.TradeNo); err != nil {
		return err
	} else if existing != nil && existing.Type == excType && existing.Status == entity.ExcStatusPending {
		s.log.Warn("duplicate pending exception suppressed",
			zap.String("trade_no", cmd.TradeNo), zap.String("type", excType), zap.String("existing_id", existing.ID))
		return nil
	}
	systemAmount := int64(-1)
	orderID := ""
	if order != nil {
		systemAmount = order.AmountCents()
		orderID = order.ID
	}
	exception := &entity.ExceptionPay{
		ID:                 entity.NewExceptionPayID(),
		Type:               excType,
		OrderID:            orderID,
		TradeNo:            cmd.TradeNo,
		ChannelAmountCents: cmd.AmountCents,
		SystemAmountCents:  systemAmount,
		Status:             entity.ExcStatusPending,
	}
	if err := s.repo.SaveException(ctx, exception); err != nil {
		return err
	}
	s.log.Warn("payment exception recorded", zap.String("exception_id", exception.ID), zap.String("type", excType), zap.String("trade_no", cmd.TradeNo))
	return nil
}

func int32String(v int32) string {
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
