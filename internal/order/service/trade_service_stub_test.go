package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/BwCloudWeGo/bw-cli/internal/order/dto"
	"github.com/BwCloudWeGo/bw-cli/internal/order/entity"
	"github.com/BwCloudWeGo/bw-cli/internal/order/repo"
)

// stubRepo 包装真实仓储，按需注入持久层故障，覆盖 service 层错误传播分支。
type stubRepo struct {
	*repo.GormRepository

	failHasAnyProduct          error
	failSaveProduct            error
	failFindProduct            error
	failListProducts           error
	failPlaceOrder             error
	failFindTradeOrder         error
	failListTradeOrders        error
	failListExpiredCreated     error
	failSaveCallback           error
	failFindCallback           error
	failListCallbacks          error
	failSaveException          error
	failFindException          error
	failListExceptions         error
	failFindExceptionByTradeNo error
	failCloseException         error
	failAppendLedger           error
	failListLedgers            error
	failTradeSummary           error
	failTransition             error
}

var _ entity.TradeRepository = (*stubRepo)(nil)

func (s *stubRepo) HasAnyProduct(ctx context.Context) (bool, error) {
	if s.failHasAnyProduct != nil {
		return false, s.failHasAnyProduct
	}
	return s.GormRepository.HasAnyProduct(ctx)
}

func (s *stubRepo) SaveProduct(ctx context.Context, product *entity.Product) error {
	if s.failSaveProduct != nil {
		return s.failSaveProduct
	}
	return s.GormRepository.SaveProduct(ctx, product)
}

func (s *stubRepo) FindProduct(ctx context.Context, id string) (*entity.Product, error) {
	if s.failFindProduct != nil {
		return nil, s.failFindProduct
	}
	return s.GormRepository.FindProduct(ctx, id)
}

func (s *stubRepo) ListProducts(ctx context.Context) ([]*entity.Product, error) {
	if s.failListProducts != nil {
		return nil, s.failListProducts
	}
	return s.GormRepository.ListProducts(ctx)
}

func (s *stubRepo) PlaceOrderTx(ctx context.Context, buyerID string, buyerName string, productID string, qty int32, keep time.Duration) (*entity.TradeOrder, error) {
	if s.failPlaceOrder != nil {
		return nil, s.failPlaceOrder
	}
	return s.GormRepository.PlaceOrderTx(ctx, buyerID, buyerName, productID, qty, keep)
}

func (s *stubRepo) FindTradeOrder(ctx context.Context, id string) (*entity.TradeOrder, error) {
	if s.failFindTradeOrder != nil {
		return nil, s.failFindTradeOrder
	}
	return s.GormRepository.FindTradeOrder(ctx, id)
}

func (s *stubRepo) ListTradeOrders(ctx context.Context, buyerID string, status string) ([]*entity.TradeOrder, int64, error) {
	if s.failListTradeOrders != nil {
		return nil, 0, s.failListTradeOrders
	}
	return s.GormRepository.ListTradeOrders(ctx, buyerID, status)
}

func (s *stubRepo) ListExpiredCreated(ctx context.Context, now time.Time) ([]*entity.TradeOrder, error) {
	if s.failListExpiredCreated != nil {
		return nil, s.failListExpiredCreated
	}
	return s.GormRepository.ListExpiredCreated(ctx, now)
}

func (s *stubRepo) TransitionOrder(ctx context.Context, orderID string, from string, to string, stockDelta int32, mutate func(order *entity.TradeOrder) ([]entity.Ledger, error)) (bool, error) {
	if s.failTransition != nil {
		return false, s.failTransition
	}
	return s.GormRepository.TransitionOrder(ctx, orderID, from, to, stockDelta, mutate)
}

func (s *stubRepo) SaveCallback(ctx context.Context, cb *entity.PayCallback) error {
	if s.failSaveCallback != nil {
		return s.failSaveCallback
	}
	return s.GormRepository.SaveCallback(ctx, cb)
}

func (s *stubRepo) FindCallback(ctx context.Context, tradeNo string) (*entity.PayCallback, error) {
	if s.failFindCallback != nil {
		return nil, s.failFindCallback
	}
	return s.GormRepository.FindCallback(ctx, tradeNo)
}

func (s *stubRepo) ListCallbacks(ctx context.Context, keyword string, status string) ([]*entity.PayCallback, error) {
	if s.failListCallbacks != nil {
		return nil, s.failListCallbacks
	}
	return s.GormRepository.ListCallbacks(ctx, keyword, status)
}

func (s *stubRepo) SaveException(ctx context.Context, ex *entity.ExceptionPay) error {
	if s.failSaveException != nil {
		return s.failSaveException
	}
	return s.GormRepository.SaveException(ctx, ex)
}

func (s *stubRepo) FindException(ctx context.Context, id string) (*entity.ExceptionPay, error) {
	if s.failFindException != nil {
		return nil, s.failFindException
	}
	return s.GormRepository.FindException(ctx, id)
}

func (s *stubRepo) ListExceptions(ctx context.Context, onlyPending bool) ([]*entity.ExceptionPay, error) {
	if s.failListExceptions != nil {
		return nil, s.failListExceptions
	}
	return s.GormRepository.ListExceptions(ctx, onlyPending)
}

func (s *stubRepo) FindExceptionByTradeNo(ctx context.Context, tradeNo string) (*entity.ExceptionPay, error) {
	if s.failFindExceptionByTradeNo != nil {
		return nil, s.failFindExceptionByTradeNo
	}
	return s.GormRepository.FindExceptionByTradeNo(ctx, tradeNo)
}

func (s *stubRepo) CloseException(ctx context.Context, id string, handler string, result string) error {
	if s.failCloseException != nil {
		return s.failCloseException
	}
	return s.GormRepository.CloseException(ctx, id, handler, result)
}

func (s *stubRepo) AppendLedger(ctx context.Context, ledger *entity.Ledger) error {
	if s.failAppendLedger != nil {
		return s.failAppendLedger
	}
	return s.GormRepository.AppendLedger(ctx, ledger)
}

func (s *stubRepo) ListLedgers(ctx context.Context, orderID string) ([]entity.Ledger, error) {
	if s.failListLedgers != nil {
		return nil, s.failListLedgers
	}
	return s.GormRepository.ListLedgers(ctx, orderID)
}

func (s *stubRepo) TradeSummary(ctx context.Context) (*entity.Summary, error) {
	if s.failTradeSummary != nil {
		return nil, s.failTradeSummary
	}
	return s.GormRepository.TradeSummary(ctx)
}

// 持久层故障必须原样向上传播，不得被 service 吞掉或当作业务结果。
func TestTradeServicePersistenceErrorsPropagate(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	boom := errors.New("db down")
	stub := &stubRepo{GormRepository: testRepo}
	svc := NewTradeService(stub, TradeConfig{Keep: time.Minute, RefundFailFirst: true}, nil)

	stub.failHasAnyProduct = boom
	if err := svc.SeedProducts(ctx); !errors.Is(err, boom) {
		t.Fatalf("SeedProducts/HasAnyProduct error must propagate, got %v", err)
	}
	stub.failHasAnyProduct = nil

	// 清空商品表，使 SeedProducts 真正走到 SaveProduct
	if err := testDB.Exec("DELETE FROM shop_products").Error; err != nil {
		t.Fatalf("clear products failed: %v", err)
	}
	stub.failSaveProduct = boom
	if err := svc.SeedProducts(ctx); !errors.Is(err, boom) {
		t.Fatalf("SeedProducts/SaveProduct error must propagate, got %v", err)
	}
	stub.failSaveProduct = nil
	if err := svc.SeedProducts(ctx); err != nil {
		t.Fatalf("reseed products failed: %v", err)
	}

	stub.failListProducts = boom
	if _, err := svc.ListProducts(ctx); !errors.Is(err, boom) {
		t.Fatalf("ListProducts error must propagate, got %v", err)
	}
	stub.failListProducts = nil

	stub.failFindProduct = boom
	if _, err := svc.UpsertProduct(ctx, upsertCmd("PX", 100, 1)); !errors.Is(err, boom) {
		t.Fatalf("UpsertProduct/FindProduct error must propagate, got %v", err)
	}
	stub.failFindProduct = nil

	stub.failPlaceOrder = boom
	if _, err := svc.PlaceOrder(ctx, placeCmd("buyer1", "P1", 1)); !errors.Is(err, boom) {
		t.Fatalf("PlaceOrder error must propagate, got %v", err)
	}
	stub.failPlaceOrder = nil

	// 先创建真实订单，供依赖存在性的错误注入使用
	placed, err := testSvc.PlaceOrder(ctx, placeCmd("buyer1", "P1", 1))
	if err != nil {
		t.Fatalf("place order failed: %v", err)
	}

	stub.failFindTradeOrder = boom
	if _, err := svc.GetOrder(ctx, "ORD-X"); !errors.Is(err, boom) {
		t.Fatalf("GetOrder/FindTradeOrder error must propagate, got %v", err)
	}
	if _, err := svc.CancelOrder(ctx, "ORD-X", "buyer1"); !errors.Is(err, boom) {
		t.Fatalf("CancelOrder/FindTradeOrder error must propagate, got %v", err)
	}
	if _, err := svc.ApplyRefund(ctx, "ORD-X", "buyer1"); !errors.Is(err, boom) {
		t.Fatalf("ApplyRefund/FindTradeOrder error must propagate, got %v", err)
	}
	if _, err := svc.AuditRefund(ctx, auditCmd("ORD-X", true, "r")); !errors.Is(err, boom) {
		t.Fatalf("AuditRefund/FindTradeOrder error must propagate, got %v", err)
	}
	if _, err := svc.ShipOrder(ctx, shipCmd("ORD-X")); !errors.Is(err, boom) {
		t.Fatalf("ShipOrder/FindTradeOrder error must propagate, got %v", err)
	}
	stub.failFindTradeOrder = nil

	stub.failListLedgers = boom
	if _, err := svc.GetOrder(ctx, placed.ID); !errors.Is(err, boom) {
		t.Fatalf("GetOrder/ListLedgers error must propagate, got %v", err)
	}
	stub.failListLedgers = nil

	stub.failListTradeOrders = boom
	if _, err := svc.ListOrders(ctx, "", ""); !errors.Is(err, boom) {
		t.Fatalf("ListOrders error must propagate, got %v", err)
	}
	stub.failListTradeOrders = nil

	stub.failTradeSummary = boom
	if _, err := svc.Summary(ctx); !errors.Is(err, boom) {
		t.Fatalf("Summary error must propagate, got %v", err)
	}
	stub.failTradeSummary = nil

	stub.failListExpiredCreated = boom
	if _, err := svc.SweepExpired(ctx); !errors.Is(err, boom) {
		t.Fatalf("SweepExpired/ListExpiredCreated error must propagate, got %v", err)
	}
	stub.failListExpiredCreated = nil

	stub.failSaveCallback = boom
	if _, err := svc.PayNotify(ctx, notifyCmd("CH-ERR-1", placed.ID, placed.AmountCents)); !errors.Is(err, boom) {
		t.Fatalf("PayNotify/SaveCallback error must propagate, got %v", err)
	}
	stub.failSaveCallback = nil

	// 真实落一条回调记录，供反查错误注入使用
	if _, err := testSvc.PayNotify(ctx, notifyCmd("CH-ERR-1", placed.ID, placed.AmountCents)); err != nil {
		t.Fatalf("pay notify failed: %v", err)
	}

	stub.failFindCallback = boom
	if _, err := svc.GetCallback(ctx, "CH-ERR-1"); !errors.Is(err, boom) {
		t.Fatalf("GetCallback/FindCallback error must propagate, got %v", err)
	}
	stub.failFindCallback = nil

	stub.failListCallbacks = boom
	if _, err := svc.ListCallbacks(ctx, "", ""); !errors.Is(err, boom) {
		t.Fatalf("ListCallbacks error must propagate, got %v", err)
	}
	stub.failListCallbacks = nil

	stub.failFindExceptionByTradeNo = boom
	if _, err := svc.GetCallback(ctx, "CH-ERR-1"); !errors.Is(err, boom) {
		t.Fatalf("GetCallback/FindExceptionByTradeNo error must propagate, got %v", err)
	}
	stub.failFindExceptionByTradeNo = nil

	stub.failSaveException = boom
	if _, err := svc.PayNotify(ctx, notifyCmd("CH-ERR-2", placed.ID, placed.AmountCents+1)); !errors.Is(err, boom) {
		t.Fatalf("PayNotify/recordException error must propagate, got %v", err)
	}
	stub.failSaveException = nil

	stub.failFindException = boom
	if _, err := svc.HandleException(ctx, handleCmd("EX-X", "verify", "r")); !errors.Is(err, boom) {
		t.Fatalf("HandleException/FindException error must propagate, got %v", err)
	}
	stub.failFindException = nil

	stub.failListExceptions = boom
	if _, err := svc.ListExceptions(ctx, true); !errors.Is(err, boom) {
		t.Fatalf("ListExceptions error must propagate, got %v", err)
	}
	stub.failListExceptions = nil
}

// 已有订单但仓储迁移/闭环失败时，service 必须原样返回错误。
func TestTradeServiceTransitionAndCloseErrorsPropagate(t *testing.T) {
	resetTables(t)
	ctx := context.Background()
	placed, err := testSvc.PlaceOrder(ctx, placeCmd("buyer1", "P1", 1))
	if err != nil {
		t.Fatalf("place order failed: %v", err)
	}
	boom := errors.New("db down")
	stub := &stubRepo{GormRepository: testRepo}
	svc := NewTradeService(stub, TradeConfig{Keep: time.Minute, RefundFailFirst: true}, nil)

	stub.failTransition = boom
	if _, err := svc.CancelOrder(ctx, placed.ID, "buyer1"); !errors.Is(err, boom) {
		t.Fatalf("CancelOrder/TransitionOrder error must propagate, got %v", err)
	}
	stub.failTransition = nil

	// 构造带订单的异常（金额不符，签名正确才走到金额比对），覆盖 ignore / refund 分支中的失败传播
	mismatch, err := svc.PayNotify(ctx, notifyCmd("CH-ERR-3", placed.ID, placed.AmountCents+1))
	if err != nil || mismatch.Success || mismatch.Code != "amount_mismatch" {
		t.Fatalf("mismatch notify must be rejected, got %+v err=%v", mismatch, err)
	}
	ex := pendingException(t, "CH-ERR-3")
	if fresh, err := stub.FindException(ctx, ex.ID); err != nil || fresh.OrderID == "" {
		t.Fatalf("amount mismatch exception must bind order, got %+v err=%v", fresh, err)
	}

	stub.failCloseException = boom
	if _, err := svc.HandleException(ctx, handleCmd(ex.ID, "ignore", "x")); !errors.Is(err, boom) {
		t.Fatalf("HandleException/CloseException error must propagate, got %v", err)
	}
	stub.failCloseException = nil

	stub.failAppendLedger = boom
	if _, err := svc.HandleException(ctx, handleCmd(ex.ID, "ignore", "x")); !errors.Is(err, boom) {
		t.Fatalf("HandleException/AppendLedger error must propagate, got %v", err)
	}
	stub.failAppendLedger = nil

	// 上面的 ignore 迭代已把异常闭环，另起一条带订单的异常覆盖 refund 分支
	if _, err := svc.PayNotify(ctx, notifyCmd("CH-ERR-4", placed.ID, placed.AmountCents+1)); err != nil {
		t.Fatalf("mismatch notify 2 failed: %v", err)
	}
	ex2 := pendingException(t, "CH-ERR-4")
	stub.failFindTradeOrder = boom
	if _, err := svc.HandleException(ctx, handleCmd(ex2.ID, "refund", "x")); !errors.Is(err, boom) {
		t.Fatalf("HandleException refund/FindTradeOrder error must propagate, got %v", err)
	}
	stub.failFindTradeOrder = nil
}

// NewTradeService 缺省配置收敛：保留期与渠道密钥。
func TestNewTradeServiceDefaults(t *testing.T) {
	svc := NewTradeService(testRepo, TradeConfig{Keep: 0, ChannelSecret: "  "}, nil)
	if svc.cfg.Keep != 10*time.Minute {
		t.Fatalf("keep must default to 10 minutes, got %v", svc.cfg.Keep)
	}
	if svc.cfg.ChannelSecret != DefaultChannelSecret {
		t.Fatalf("secret must default, got %q", svc.cfg.ChannelSecret)
	}
	if SignPayNotify("", "T", "O", 1) == "" {
		t.Fatalf("SignPayNotify with empty secret must still sign")
	}
}

func upsertCmd(id string, price int64, available int32) dto.UpsertProductCommand {
	return dto.UpsertProductCommand{ID: id, Name: "商品 " + id, PriceCents: price, Available: available, OnSale: true}
}
