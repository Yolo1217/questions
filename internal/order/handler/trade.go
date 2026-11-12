package handler

import (
	"context"
	stderrors "errors"

	orderv1 "github.com/BwCloudWeGo/bw-cli/api/gen/order/v1"
	"github.com/BwCloudWeGo/bw-cli/internal/order/dto"
	"github.com/BwCloudWeGo/bw-cli/internal/order/entity"
	apperrors "github.com/BwCloudWeGo/bw-cli/pkg/errors"
)

// ListProducts 返回商品列表。
func (s *Server) ListProducts(ctx context.Context, _ *orderv1.ListProductsRequest) (*orderv1.ListProductsResponse, error) {
	items, err := s.trade.ListProducts(ctx)
	if err != nil {
		return nil, mapTradeError(err)
	}
	resp := &orderv1.ListProductsResponse{Items: make([]*orderv1.ProductResponse, 0, len(items))}
	for _, item := range items {
		resp.Items = append(resp.Items, &orderv1.ProductResponse{
			Id:         item.ID,
			Name:       item.Name,
			PriceCents: item.PriceCents,
			Available:  item.Available,
			Total:      item.Total,
			OnSale:     item.OnSale,
		})
	}
	return resp, nil
}

// UpsertProduct 运营维护商品（上下架、价格、可售数量）。
func (s *Server) UpsertProduct(ctx context.Context, req *orderv1.UpsertProductRequest) (*orderv1.ProductResponse, error) {
	item, err := s.trade.UpsertProduct(ctx, dto.UpsertProductCommand{
		ID:         req.GetId(),
		Name:       req.GetName(),
		PriceCents: req.GetPriceCents(),
		Available:  req.GetAvailable(),
		OnSale:     req.GetOnSale(),
	})
	if err != nil {
		return nil, mapTradeError(err)
	}
	return &orderv1.ProductResponse{
		Id:         item.ID,
		Name:       item.Name,
		PriceCents: item.PriceCents,
		Available:  item.Available,
		Total:      item.Total,
		OnSale:     item.OnSale,
	}, nil
}

// PlaceOrder 买家下单。
func (s *Server) PlaceOrder(ctx context.Context, req *orderv1.PlaceOrderRequest) (*orderv1.OrderDetailResponse, error) {
	item, err := s.trade.PlaceOrder(ctx, dto.PlaceOrderCommand{
		BuyerID:   req.GetBuyerId(),
		BuyerName: req.GetBuyerName(),
		ProductID: req.GetProductId(),
		Qty:       req.GetQty(),
	})
	if err != nil {
		return nil, mapTradeError(err)
	}
	return toTradeProto(item), nil
}

// CancelOrder 买家取消待支付订单。
func (s *Server) CancelOrder(ctx context.Context, req *orderv1.CancelOrderRequest) (*orderv1.OrderDetailResponse, error) {
	item, err := s.trade.CancelOrder(ctx, req.GetOrderId(), req.GetBuyerId())
	if err != nil {
		return nil, mapTradeError(err)
	}
	return toTradeProto(item), nil
}

// ApplyRefund 买家申请售前整单退款。
func (s *Server) ApplyRefund(ctx context.Context, req *orderv1.ApplyRefundRequest) (*orderv1.OrderDetailResponse, error) {
	item, err := s.trade.ApplyRefund(ctx, req.GetOrderId(), req.GetBuyerId())
	if err != nil {
		return nil, mapTradeError(err)
	}
	return toTradeProto(item), nil
}

// ListMyOrders 返回当前买家订单。
func (s *Server) ListMyOrders(ctx context.Context, req *orderv1.ListMyOrdersRequest) (*orderv1.ListTradeOrdersResponse, error) {
	return s.listOrders(ctx, req.GetBuyerId(), "")
}

// ListOrdersByStatus 按状态过滤订单（仓库/客服/运营视图）。
func (s *Server) ListOrdersByStatus(ctx context.Context, req *orderv1.ListOrdersByStatusRequest) (*orderv1.ListTradeOrdersResponse, error) {
	return s.listOrders(ctx, req.GetBuyerId(), req.GetStatus())
}

// GetTradeOrder 返回订单详情（含四类记录）。
func (s *Server) GetTradeOrder(ctx context.Context, req *orderv1.GetTradeOrderRequest) (*orderv1.OrderDetailResponse, error) {
	item, err := s.trade.GetOrder(ctx, req.GetId())
	if err != nil {
		return nil, mapTradeError(err)
	}
	return toTradeProto(item), nil
}

// PayNotify 处理渠道支付通知。
func (s *Server) PayNotify(ctx context.Context, req *orderv1.PayNotifyRequest) (*orderv1.PayNotifyResponse, error) {
	result, err := s.trade.PayNotify(ctx, dto.PayNotifyCommand{
		TradeNo:     req.GetChannelTradeNo(),
		OrderID:     req.GetOrderId(),
		AmountCents: req.GetAmountCents(),
		Signature:   req.GetSignature(),
	})
	if err != nil {
		return nil, mapTradeError(err)
	}
	return &orderv1.PayNotifyResponse{
		Success:   result.Success,
		Code:      result.Code,
		Message:   result.Message,
		Duplicate: result.Duplicate,
	}, nil
}

// AuditRefund 客服审核退款（重复提交按幂等返回成功）。
func (s *Server) AuditRefund(ctx context.Context, req *orderv1.AuditRefundRequest) (*orderv1.AuditRefundResponse, error) {
	result, err := s.trade.AuditRefund(ctx, dto.AuditRefundCommand{
		OrderID:  req.GetOrderId(),
		Approve:  req.GetApprove(),
		Reason:   req.GetReason(),
		Operator: req.GetOperator(),
	})
	if err != nil {
		return nil, mapTradeError(err)
	}
	return &orderv1.AuditRefundResponse{
		Order:     toTradeProto(result.Order),
		Duplicate: result.Duplicate,
		Message:   result.Message,
	}, nil
}

// GetPayCallback 用渠道交易号反查回调处理结果。
func (s *Server) GetPayCallback(ctx context.Context, req *orderv1.GetPayCallbackRequest) (*orderv1.PayCallbackResponse, error) {
	item, err := s.trade.GetCallback(ctx, req.GetTradeNo())
	if err != nil {
		return nil, mapTradeError(err)
	}
	return toCallbackProto(item), nil
}

// ListPayCallbacks 按关键词/状态查询回调记录。
func (s *Server) ListPayCallbacks(ctx context.Context, req *orderv1.ListPayCallbacksRequest) (*orderv1.ListPayCallbacksResponse, error) {
	items, err := s.trade.ListCallbacks(ctx, req.GetKeyword(), req.GetStatus())
	if err != nil {
		return nil, mapTradeError(err)
	}
	resp := &orderv1.ListPayCallbacksResponse{Items: make([]*orderv1.PayCallbackResponse, 0, len(items))}
	for _, item := range items {
		resp.Items = append(resp.Items, toCallbackProto(item))
	}
	return resp, nil
}

func toCallbackProto(item *dto.PayCallbackDTO) *orderv1.PayCallbackResponse {
	return &orderv1.PayCallbackResponse{
		TradeNo:         item.TradeNo,
		OrderId:         item.OrderID,
		AmountCents:     item.AmountCents,
		Status:          item.Status,
		Hits:            item.Hits,
		CreatedAt:       item.CreatedAt,
		UpdatedAt:       item.UpdatedAt,
		ExceptionType:   item.ExceptionType,
		ExceptionStatus: item.ExceptionStatus,
	}
}

// ShipOrder 仓库发货登记。
func (s *Server) ShipOrder(ctx context.Context, req *orderv1.ShipOrderRequest) (*orderv1.OrderDetailResponse, error) {
	item, err := s.trade.ShipOrder(ctx, dto.ShipOrderCommand{
		OrderID:    req.GetOrderId(),
		Carrier:    req.GetCarrier(),
		TrackingNo: req.GetTrackingNo(),
		Operator:   req.GetOperator(),
	})
	if err != nil {
		return nil, mapTradeError(err)
	}
	return toTradeProto(item), nil
}

// ListExceptionPays 查询支付异常。
func (s *Server) ListExceptionPays(ctx context.Context, req *orderv1.ListExceptionPaysRequest) (*orderv1.ListExceptionPaysResponse, error) {
	items, err := s.trade.ListExceptions(ctx, req.GetOnlyPending())
	if err != nil {
		return nil, mapTradeError(err)
	}
	resp := &orderv1.ListExceptionPaysResponse{Items: make([]*orderv1.ExceptionPayResponse, 0, len(items))}
	for _, item := range items {
		resp.Items = append(resp.Items, &orderv1.ExceptionPayResponse{
			Id:                 item.ID,
			Type:               item.Type,
			OrderId:            item.OrderID,
			TradeNo:            item.TradeNo,
			ChannelAmountCents: item.ChannelAmountCents,
			SystemAmountCents:  item.SystemAmountCents,
			Status:             item.Status,
			Handler:            item.Handler,
			Result:             item.Result,
			CreatedAt:          item.CreatedAt,
		})
	}
	return resp, nil
}

// HandleExceptionPay 客服从异常表处置。
func (s *Server) HandleExceptionPay(ctx context.Context, req *orderv1.HandleExceptionPayRequest) (*orderv1.ExceptionPayResponse, error) {
	item, err := s.trade.HandleException(ctx, dto.HandleExceptionCommand{
		ExceptionID: req.GetExceptionId(),
		Action:      req.GetAction(),
		Reason:      req.GetReason(),
		Operator:    req.GetOperator(),
	})
	if err != nil {
		return nil, mapTradeError(err)
	}
	return &orderv1.ExceptionPayResponse{
		Id:                 item.ID,
		Type:               item.Type,
		OrderId:            item.OrderID,
		TradeNo:            item.TradeNo,
		ChannelAmountCents: item.ChannelAmountCents,
		SystemAmountCents:  item.SystemAmountCents,
		Status:             item.Status,
		Handler:            item.Handler,
		Result:             item.Result,
		CreatedAt:          item.CreatedAt,
	}, nil
}

// TradeSummary 返回运营核查汇总。
func (s *Server) TradeSummary(ctx context.Context, _ *orderv1.TradeSummaryRequest) (*orderv1.TradeSummaryResponse, error) {
	summary, err := s.trade.Summary(ctx)
	if err != nil {
		return nil, mapTradeError(err)
	}
	return &orderv1.TradeSummaryResponse{
		PaidTotalCents:     summary.PaidTotalCents,
		RefundedTotalCents: summary.RefundedTotalCents,
		AvailableTotal:     summary.AvailableTotal,
		ReservedTotal:      summary.ReservedTotal,
		ExceptionPending:   summary.ExceptionPending,
		OrdersByStatus:     summary.OrdersByStatus,
	}, nil
}

func (s *Server) listOrders(ctx context.Context, buyerID string, status string) (*orderv1.ListTradeOrdersResponse, error) {
	list, err := s.trade.ListOrders(ctx, buyerID, status)
	if err != nil {
		return nil, mapTradeError(err)
	}
	resp := &orderv1.ListTradeOrdersResponse{Items: make([]*orderv1.OrderDetailResponse, 0, len(list.Items)), Total: list.Total}
	for _, item := range list.Items {
		resp.Items = append(resp.Items, toTradeProto(item))
	}
	return resp, nil
}

func toTradeProto(item *dto.TradeOrderDTO) *orderv1.OrderDetailResponse {
	resp := &orderv1.OrderDetailResponse{
		Id:           item.ID,
		BuyerId:      item.BuyerID,
		BuyerName:    item.BuyerName,
		ProductId:    item.ProductID,
		ProductName:  item.ProductName,
		Qty:          item.Qty,
		PriceCents:   item.PriceCents,
		AmountCents:  item.AmountCents,
		Status:       item.Status,
		CreatedAt:    item.CreatedAt,
		Deadline:     item.Deadline,
		PaidAt:       item.PaidAt,
		ClosedReason: item.ClosedReason,
		Carrier:      item.Carrier,
		TrackingNo:   item.TrackingNo,
		RejectReason: item.RejectReason,
		RefundFail:   item.RefundFail,
		Ledgers:      make([]*orderv1.LedgerEntry, 0, len(item.Ledgers)),
	}
	for _, ledger := range item.Ledgers {
		resp.Ledgers = append(resp.Ledgers, &orderv1.LedgerEntry{
			Kind:        ledger.Kind,
			Time:        ledger.Time,
			Text:        ledger.Text,
			AmountCents: ledger.AmountCents,
			TradeNo:     ledger.TradeNo,
			Status:      ledger.Status,
		})
	}
	return resp
}

// mapTradeError 将交易域业务错误映射为稳定的应用错误（HTTP 4xx / gRPC 状态码）。
func mapTradeError(err error) error {
	switch {
	case stderrors.Is(err, entity.ErrPermissionDenied):
		return apperrors.New(apperrors.KindUnauthorized, "permission_denied", "越权拒绝：只能操作本人订单")
	case stderrors.Is(err, entity.ErrInsufficientStock):
		return apperrors.Conflict("insufficient_stock", "库存不足：条件扣减失败，不产生订单")
	case stderrors.Is(err, entity.ErrProductOffSale):
		return apperrors.Conflict("product_off_sale", "商品已下架，拒绝下单")
	case stderrors.Is(err, entity.ErrProductNotFound):
		return apperrors.NotFound("product_not_found", "商品不存在")
	case stderrors.Is(err, entity.ErrInvalidQty):
		return apperrors.InvalidArgument("invalid_quantity", "每单数量仅 1~2 件")
	case stderrors.Is(err, entity.ErrTradeOrderNotFound):
		return apperrors.NotFound("order_not_found", "订单不存在")
	case stderrors.Is(err, entity.ErrInvalidTransition):
		return apperrors.Conflict("invalid_status_transition", "当前订单状态不允许该操作")
	case stderrors.Is(err, entity.ErrOrderRefunding):
		return apperrors.Conflict("order_refunding", "订单退款审核中，已暂停发货")
	case stderrors.Is(err, entity.ErrOrderAlreadyShipped):
		return apperrors.Conflict("order_already_shipped", "订单已发货，每单仅允许登记一次")
	case stderrors.Is(err, entity.ErrRefundNotAllowed):
		return apperrors.Conflict("refund_not_allowed", "订单已发货，不受理售前退款")
	case stderrors.Is(err, entity.ErrCarrierRequired):
		return apperrors.InvalidArgument("carrier_required", "承运方与运单号均为必填")
	case stderrors.Is(err, entity.ErrReasonRequired):
		return apperrors.InvalidArgument("reason_required", "必须填写原因")
	case stderrors.Is(err, entity.ErrChannelRefundFailed):
		return apperrors.Conflict("channel_refund_failed", "渠道退款失败：订单保持退款中，待处理记录已保留，可重试")
	case stderrors.Is(err, entity.ErrExceptionNotFound):
		return apperrors.NotFound("exception_not_found", "异常记录不存在")
	case stderrors.Is(err, entity.ErrExceptionClosed):
		return apperrors.Conflict("exception_closed", "异常记录已闭环")
	case stderrors.Is(err, entity.ErrExceptionNoRefund):
		return apperrors.InvalidArgument("exception_no_refund", "无主支付无对应订单，请选择联系渠道核实或标记忽略")
	case stderrors.Is(err, entity.ErrCallbackNotFound):
		return apperrors.NotFound("callback_not_found", "该渠道交易号没有回调记录")
	case stderrors.Is(err, entity.ErrOrderClosed):
		return apperrors.Conflict("order_closed", "订单已关闭（取消/超时），不能发货")
	case stderrors.Is(err, entity.ErrOrderRefunded):
		return apperrors.Conflict("order_refunded", "订单已退款，不能发货")
	default:
		return apperrors.Wrap(apperrors.KindInternal, "trade_service_error", "trade service error", err)
	}
}
