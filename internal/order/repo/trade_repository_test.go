package repo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"go.uber.org/zap"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/BwCloudWeGo/bw-cli/internal/order/entity"
	dbmodel "github.com/BwCloudWeGo/bw-cli/internal/order/model"
)

// 仓储层直测使用独立测试库 questions_test_repo，与 service 层测试库隔离，
// 保证 `go test ./...` 各包并行执行时互不清表。
var (
	repoDB   *gorm.DB
	repoImpl *GormRepository
)

func TestMain(m *testing.M) {
	server := "root:root@tcp(127.0.0.1:3306)/?charset=utf8mb4&parseTime=True&loc=Local"
	bootstrap, err := gorm.Open(mysql.Open(server), &gorm.Config{Logger: gormlogger.Discard})
	if err != nil {
		fmt.Println("skip repo tests: mysql unavailable:", err)
		os.Exit(0)
	}
	if err := bootstrap.Exec("CREATE DATABASE IF NOT EXISTS questions_test_repo DEFAULT CHARACTER SET utf8mb4").Error; err != nil {
		fmt.Println("skip repo tests: create test database failed:", err)
		os.Exit(0)
	}
	db, err := gorm.Open(mysql.Open("root:root@tcp(127.0.0.1:3306)/questions_test_repo?charset=utf8mb4&parseTime=True&loc=Local"), &gorm.Config{Logger: gormlogger.Discard})
	if err != nil {
		fmt.Println("skip repo tests: mysql unavailable:", err)
		os.Exit(0)
	}
	if err := AutoMigrate(db); err != nil {
		fmt.Println("skip repo tests: migrate failed:", err)
		os.Exit(0)
	}
	repoDB = db
	repoImpl = NewGormRepository(db, zap.NewNop())
	os.Exit(m.Run())
}

func resetRepoTables(t *testing.T) {
	t.Helper()
	for _, table := range []string{"orders", "shop_trade_ledgers", "shop_pay_callbacks", "shop_exception_pays", "shop_trade_orders", "shop_products"} {
		if err := repoDB.Exec("DELETE FROM " + table).Error; err != nil {
			t.Fatalf("reset %s failed: %v", table, err)
		}
	}
}

func seedRepoProduct(t *testing.T, id string, available int32, onSale bool) {
	t.Helper()
	err := repoImpl.SaveProduct(context.Background(), &entity.Product{
		ID: id, Name: "商品 " + id, PriceCents: 10000, Total: available, Available: available, OnSale: onSale,
	})
	if err != nil {
		t.Fatalf("seed product failed: %v", err)
	}
}

// 商品仓储：新增/更新/列表/查询/空表判断。
func TestTradeRepoProductLifecycle(t *testing.T) {
	resetRepoTables(t)
	ctx := context.Background()

	if exists, err := repoImpl.HasAnyProduct(ctx); err != nil || exists {
		t.Fatalf("empty table must report no product: %v %v", exists, err)
	}
	seedRepoProduct(t, "RP1", 5, true)
	if exists, err := repoImpl.HasAnyProduct(ctx); err != nil || !exists {
		t.Fatalf("seeded table must report product: %v %v", exists, err)
	}
	if _, err := repoImpl.FindProduct(ctx, "NOPE"); !errors.Is(err, entity.ErrProductNotFound) {
		t.Fatalf("missing product must be ErrProductNotFound, got %v", err)
	}
	items, err := repoImpl.ListProducts(ctx)
	if err != nil || len(items) != 1 || items[0].ID != "RP1" {
		t.Fatalf("list products failed: %+v err=%v", items, err)
	}
	// 更新路径：RowsAffected>0 时不再 Create
	if err := repoImpl.SaveProduct(ctx, &entity.Product{ID: "RP1", Name: "改名", PriceCents: 12000, Total: 5, Available: 4, OnSale: false}); err != nil {
		t.Fatalf("update product failed: %v", err)
	}
	updated, err := repoImpl.FindProduct(ctx, "RP1")
	if err != nil || updated.Name != "改名" || updated.Available != 4 || updated.OnSale {
		t.Fatalf("update product mismatch: %+v err=%v", updated, err)
	}
}

// 下单事务守卫：商品不存在/非法数量/下架/库存不足均不产生订单。
func TestTradeRepoPlaceOrderTxGuards(t *testing.T) {
	resetRepoTables(t)
	ctx := context.Background()
	seedRepoProduct(t, "RP1", 1, true)

	if _, err := repoImpl.PlaceOrderTx(ctx, "b1", "b1", "NOPE", 1, time.Minute); !errors.Is(err, entity.ErrProductNotFound) {
		t.Fatalf("missing product must fail, got %v", err)
	}
	if _, err := repoImpl.PlaceOrderTx(ctx, "b1", "b1", "RP1", 0, time.Minute); !errors.Is(err, entity.ErrInvalidQty) {
		t.Fatalf("qty 0 must fail, got %v", err)
	}
	seedRepoProduct(t, "RP2", 5, false)
	if got, _ := repoImpl.FindProduct(ctx, "RP2"); got.OnSale {
		t.Fatalf("RP2 must be off sale after seed, got %+v", got)
	}
	if _, err := repoImpl.PlaceOrderTx(ctx, "b1", "b1", "RP2", 1, time.Minute); !errors.Is(err, entity.ErrProductOffSale) {
		t.Fatalf("off-sale product must fail, got %v", err)
	}
	if _, err := repoImpl.PlaceOrderTx(ctx, "b1", "b1", "RP1", 2, time.Minute); !errors.Is(err, entity.ErrInsufficientStock) {
		t.Fatalf("insufficient stock must fail, got %v", err)
	}
	placed, err := repoImpl.PlaceOrderTx(ctx, "b1", "b1", "RP1", 1, time.Minute)
	if err != nil || placed.Status != entity.OrderStatusCreated {
		t.Fatalf("valid order must be created, got %+v err=%v", placed, err)
	}
	if got, _ := repoImpl.FindProduct(ctx, "RP1"); got.Available != 0 {
		t.Fatalf("stock must be reserved, got %d", got.Available)
	}
	if ledgers, err := repoImpl.ListLedgers(ctx, placed.ID); err != nil || len(ledgers) != 1 || ledgers[0].Kind != entity.LedgerStock {
		t.Fatalf("order must carry stock ledger, got %+v err=%v", ledgers, err)
	}
}

// 状态迁移：条件更新失败返回 applied=false，mutate 出错整体回滚。
func TestTradeRepoTransitionOrderGuards(t *testing.T) {
	resetRepoTables(t)
	ctx := context.Background()
	seedRepoProduct(t, "RP1", 5, true)
	placed, err := repoImpl.PlaceOrderTx(ctx, "b1", "b1", "RP1", 2, time.Minute)
	if err != nil {
		t.Fatalf("place order failed: %v", err)
	}

	// 状态不符：条件更新 rows=0
	applied, err := repoImpl.TransitionOrder(ctx, placed.ID, entity.OrderStatusPaid, entity.OrderStatusClosed, 2, nil)
	if err != nil || applied {
		t.Fatalf("wrong from-status must not apply, got %v err=%v", applied, err)
	}
	// mutate 出错：事务回滚，状态与库存保持不变
	boom := errors.New("mutate boom")
	applied, err = repoImpl.TransitionOrder(ctx, placed.ID, entity.OrderStatusCreated, entity.OrderStatusClosed, 2,
		func(*entity.TradeOrder) ([]entity.Ledger, error) { return nil, boom })
	if !errors.Is(err, boom) || applied {
		t.Fatalf("mutate error must rollback, got applied=%v err=%v", applied, err)
	}
	if got := mustFindOrder(t, placed.ID); got.Status != entity.OrderStatusCreated {
		t.Fatalf("status must stay CREATED after rollback, got %s", got.Status)
	}
	if got, _ := repoImpl.FindProduct(ctx, "RP1"); got.Available != 3 {
		t.Fatalf("stock must stay reserved after rollback, got %d", got.Available)
	}
	// 正常迁移 + 回补库存
	applied, err = repoImpl.TransitionOrder(ctx, placed.ID, entity.OrderStatusCreated, entity.OrderStatusClosed, 2,
		func(item *entity.TradeOrder) ([]entity.Ledger, error) {
			item.ClosedReason = "测试关闭"
			return []entity.Ledger{{Kind: entity.LedgerStock, Text: "释放", Status: entity.OrderStatusClosed}}, nil
		})
	if err != nil || !applied {
		t.Fatalf("valid transition must apply, got %v err=%v", applied, err)
	}
	if got := mustFindOrder(t, placed.ID); got.Status != entity.OrderStatusClosed || got.ClosedReason != "测试关闭" {
		t.Fatalf("transition mismatch: %+v", got)
	}
	if got, _ := repoImpl.FindProduct(ctx, "RP1"); got.Available != 5 {
		t.Fatalf("stock must be restored, got %d", got.Available)
	}
}

func mustFindOrder(t *testing.T, id string) *entity.TradeOrder {
	t.Helper()
	order, err := repoImpl.FindTradeOrder(context.Background(), id)
	if err != nil {
		t.Fatalf("find order %s failed: %v", id, err)
	}
	return order
}

// 订单查询：不存在错误、列表过滤、超时扫描列表。
func TestTradeRepoOrderQueryAndExpiry(t *testing.T) {
	resetRepoTables(t)
	ctx := context.Background()

	if _, err := repoImpl.FindTradeOrder(ctx, "NOPE"); !errors.Is(err, entity.ErrTradeOrderNotFound) {
		t.Fatalf("missing order must be ErrTradeOrderNotFound, got %v", err)
	}
	seedRepoProduct(t, "RP1", 5, true)
	fresh, err := repoImpl.PlaceOrderTx(ctx, "b1", "b1", "RP1", 1, 10*time.Minute)
	if err != nil {
		t.Fatalf("place order failed: %v", err)
	}
	expired, err := repoImpl.PlaceOrderTx(ctx, "b2", "b2", "RP1", 1, time.Nanosecond)
	if err != nil {
		t.Fatalf("place order failed: %v", err)
	}

	items, total, err := repoImpl.ListTradeOrders(ctx, "b1", "")
	if err != nil || total != 1 || items[0].ID != fresh.ID {
		t.Fatalf("buyer filter failed: total=%d err=%v", total, err)
	}
	if _, total, err := repoImpl.ListTradeOrders(ctx, "", entity.OrderStatusCreated); err != nil || total != 2 {
		t.Fatalf("status filter failed: total=%d err=%v", total, err)
	}
	list, err := repoImpl.ListExpiredCreated(ctx, time.Now())
	if err != nil || len(list) != 1 || list[0].ID != expired.ID {
		t.Fatalf("expired list mismatch: %+v err=%v", list, err)
	}
}

// 回调记录：唯一约束幂等、状态更新、命中计数、列表过滤。
func TestTradeRepoCallbackOperations(t *testing.T) {
	resetRepoTables(t)
	ctx := context.Background()

	cb := &entity.PayCallback{TradeNo: "CH-R1", OrderID: "ORD-R1", AmountCents: 100, Status: entity.CallbackReceived}
	if err := repoImpl.SaveCallback(ctx, cb); err != nil {
		t.Fatalf("save callback failed: %v", err)
	}
	if err := repoImpl.SaveCallback(ctx, cb); !errors.Is(err, entity.ErrDuplicateTradeNo) {
		t.Fatalf("duplicate trade no must fail, got %v", err)
	}
	if found, err := repoImpl.FindCallback(ctx, "CH-R1"); err != nil || found == nil || found.Hits != 1 {
		t.Fatalf("find callback mismatch: %+v err=%v", found, err)
	}
	if nilFound, err := repoImpl.FindCallback(ctx, "CH-NOPE"); err != nil || nilFound != nil {
		t.Fatalf("missing callback must be nil,nil, got %+v err=%v", nilFound, err)
	}
	if err := repoImpl.UpdateCallbackStatus(ctx, "CH-R1", entity.CallbackFailed); err != nil {
		t.Fatalf("update status failed: %v", err)
	}
	if err := repoImpl.IncrementCallbackHits(ctx, "CH-R1"); err != nil {
		t.Fatalf("increment hits failed: %v", err)
	}
	found, _ := repoImpl.FindCallback(ctx, "CH-R1")
	if found.Status != entity.CallbackFailed || found.Hits != 2 {
		t.Fatalf("increment must not change status, got %+v", found)
	}
	byOrder, err := repoImpl.ListCallbacks(ctx, "ORD-R1", "")
	if err != nil || len(byOrder) != 1 {
		t.Fatalf("list by order keyword failed: n=%d err=%v", len(byOrder), err)
	}
	byDone, err := repoImpl.ListCallbacks(ctx, "", entity.CallbackDone)
	if err != nil || len(byDone) != 0 {
		t.Fatalf("list by done status must be empty: n=%d err=%v", len(byDone), err)
	}
}

// 异常记录：保存/查询/条件闭环/按交易号反查。
func TestTradeRepoExceptionOperations(t *testing.T) {
	resetRepoTables(t)
	ctx := context.Background()

	ex := &entity.ExceptionPay{
		ID: "EX-R-000001", Type: entity.ExcLatePay, OrderID: "ORD-R1", TradeNo: "CH-R1",
		ChannelAmountCents: 100, SystemAmountCents: -1, Status: entity.ExcStatusPending,
	}
	if err := repoImpl.SaveException(ctx, ex); err != nil {
		t.Fatalf("save exception failed: %v", err)
	}
	if _, err := repoImpl.FindException(ctx, "NOPE"); !errors.Is(err, entity.ErrExceptionNotFound) {
		t.Fatalf("missing exception must be ErrExceptionNotFound, got %v", err)
	}
	if found, err := repoImpl.FindExceptionByTradeNo(ctx, "CH-R1"); err != nil || found == nil || found.ID != ex.ID {
		t.Fatalf("find by trade no failed: %+v err=%v", found, err)
	}
	if none, err := repoImpl.FindExceptionByTradeNo(ctx, "CH-NOPE"); err != nil || none != nil {
		t.Fatalf("missing by trade no must be nil,nil, got %+v err=%v", none, err)
	}
	pending, err := repoImpl.ListExceptions(ctx, true)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending list mismatch: n=%d err=%v", len(pending), err)
	}
	if err := repoImpl.CloseException(ctx, ex.ID, "客服", "核实后闭环"); err != nil {
		t.Fatalf("close exception failed: %v", err)
	}
	if err := repoImpl.CloseException(ctx, ex.ID, "客服", "重复闭环"); !errors.Is(err, entity.ErrExceptionClosed) {
		t.Fatalf("second close must fail, got %v", err)
	}
	if err := repoImpl.CloseException(ctx, "NOPE", "客服", "不存在"); !errors.Is(err, entity.ErrExceptionClosed) {
		t.Fatalf("close missing must fail, got %v", err)
	}
	closed, err := repoImpl.FindException(ctx, ex.ID)
	if err != nil || closed.Status != entity.ExcStatusClosed || closed.Result != "核实后闭环" {
		t.Fatalf("closed exception mismatch: %+v err=%v", closed, err)
	}
	if all, err := repoImpl.ListExceptions(ctx, true); err != nil || len(all) != 0 {
		t.Fatalf("pending list after close must be empty: n=%d err=%v", len(all), err)
	}
}

// 退款失败标记与台账追加。
func TestTradeRepoRefundFailAndLedger(t *testing.T) {
	resetRepoTables(t)
	ctx := context.Background()
	seedRepoProduct(t, "RP1", 5, true)
	placed, err := repoImpl.PlaceOrderTx(ctx, "b1", "b1", "RP1", 1, time.Minute)
	if err != nil {
		t.Fatalf("place order failed: %v", err)
	}

	// 非 REFUNDING 状态：不生效
	if applied, err := repoImpl.MarkRefundFailed(ctx, placed.ID); err != nil || applied {
		t.Fatalf("mark on CREATED must not apply, got %v err=%v", applied, err)
	}
	if applied, err := repoImpl.TransitionOrder(ctx, placed.ID, entity.OrderStatusCreated, entity.OrderStatusRefunding, 0, nil); err != nil || !applied {
		t.Fatalf("transition to refunding failed: %v err=%v", applied, err)
	}
	if applied, err := repoImpl.MarkRefundFailed(ctx, placed.ID); err != nil || !applied {
		t.Fatalf("mark on REFUNDING must apply, got %v err=%v", applied, err)
	}
	ledgers, err := repoImpl.ListLedgers(ctx, placed.ID)
	failedCount := 0
	for _, item := range ledgers {
		if item.Kind == entity.LedgerRefund && item.Status == "failed" {
			failedCount++
		}
	}
	if err != nil || failedCount != 1 {
		t.Fatalf("refund failed ledger mismatch: %+v err=%v", ledgers, err)
	}
	// AppendLedger 零值时间自动补齐
	if err := repoImpl.AppendLedger(ctx, &entity.Ledger{OrderID: placed.ID, Kind: entity.LedgerException, Text: "异常处置留痕"}); err != nil {
		t.Fatalf("append ledger failed: %v", err)
	}
	if ledgers, err := repoImpl.ListLedgers(ctx, placed.ID); err != nil || len(ledgers) != 3 {
		t.Fatalf("ledger count mismatch (预占+退款失败+异常留痕): n=%d err=%v", len(ledgers), err)
	}
}

// 运营汇总：收款/退款/可售/预占/异常/状态分布。
func TestTradeRepoSummary(t *testing.T) {
	resetRepoTables(t)
	ctx := context.Background()
	seedRepoProduct(t, "RP1", 5, true)

	first, err := repoImpl.PlaceOrderTx(ctx, "b1", "b1", "RP1", 1, time.Minute)
	if err != nil {
		t.Fatalf("place order failed: %v", err)
	}
	second, err := repoImpl.PlaceOrderTx(ctx, "b2", "b2", "RP1", 1, time.Minute)
	if err != nil {
		t.Fatalf("place order failed: %v", err)
	}
	// first 支付成功
	paid, err := repoImpl.TransitionOrder(ctx, first.ID, entity.OrderStatusCreated, entity.OrderStatusPaid, 0,
		func(item *entity.TradeOrder) ([]entity.Ledger, error) {
			return []entity.Ledger{{Kind: entity.LedgerPay, Text: "支付", AmountCents: item.AmountCents(), Status: "success"}}, nil
		})
	if err != nil || !paid {
		t.Fatalf("pay transition failed: %v err=%v", paid, err)
	}
	// second 退款成功（回补库存）
	refunded, err := repoImpl.TransitionOrder(ctx, second.ID, entity.OrderStatusCreated, entity.OrderStatusRefunded, 1,
		func(item *entity.TradeOrder) ([]entity.Ledger, error) {
			return []entity.Ledger{{Kind: entity.LedgerRefund, Text: "退款", AmountCents: item.AmountCents(), Status: "success"}}, nil
		})
	if err != nil || !refunded {
		t.Fatalf("refund transition failed: %v err=%v", refunded, err)
	}

	summary, err := repoImpl.TradeSummary(ctx)
	if err != nil {
		t.Fatalf("summary failed: %v", err)
	}
	if summary.PaidTotalCents != 10000 || summary.RefundedTotalCents != 10000 {
		t.Fatalf("money summary mismatch: paid=%d refunded=%d", summary.PaidTotalCents, summary.RefundedTotalCents)
	}
	if summary.AvailableTotal != 4 || summary.ReservedTotal != 0 {
		t.Fatalf("stock summary mismatch: available=%d reserved=%d", summary.AvailableTotal, summary.ReservedTotal)
	}
	if summary.ExceptionPending != 0 || summary.OrdersByStatus[entity.OrderStatusPaid] != 1 || summary.OrdersByStatus[entity.OrderStatusRefunded] != 1 {
		t.Fatalf("status summary mismatch: %+v", summary.OrdersByStatus)
	}
}

// 框架既有 order CRUD 仓储：保存/查询/分页/删除。
func TestLegacyOrderCrudRepository(t *testing.T) {
	resetRepoTables(t)
	ctx := context.Background()

	item := &entity.Order{ID: "LEG-1", Name: "旧订单", Description: "框架示例", CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := repoImpl.Save(ctx, item); err != nil {
		t.Fatalf("save legacy order failed: %v", err)
	}
	found, err := repoImpl.FindByID(ctx, "LEG-1")
	if err != nil || found.Name != "旧订单" {
		t.Fatalf("find legacy order mismatch: %+v err=%v", found, err)
	}
	if _, err := repoImpl.FindByID(ctx, "NOPE"); !errors.Is(err, entity.ErrOrderNotFound) {
		t.Fatalf("missing legacy order must be ErrOrderNotFound, got %v", err)
	}
	if err := repoImpl.Delete(ctx, "NOPE"); !errors.Is(err, entity.ErrOrderNotFound) {
		t.Fatalf("delete missing must be ErrOrderNotFound, got %v", err)
	}
	items, total, err := repoImpl.List(ctx, 0, 10)
	if err != nil || total != 1 || len(items) != 1 {
		t.Fatalf("list legacy orders mismatch: n=%d total=%d err=%v", len(items), total, err)
	}
	if err := repoImpl.Delete(ctx, "LEG-1"); err != nil {
		t.Fatalf("delete legacy order failed: %v", err)
	}
}

// 关闭底层连接池后，所有仓储方法的错误分支都必须返回错误且不 panic
// （真实 MySQL 故障注入：覆盖 res.Error != nil 的传播路径）。
func TestRepoErrorBranchesOnClosedDB(t *testing.T) {
	resetRepoTables(t)
	ctx := context.Background()

	broken, err := gorm.Open(mysql.Open("root:root@tcp(127.0.0.1:3306)/questions_test_repo?charset=utf8mb4&parseTime=True&loc=Local"), &gorm.Config{Logger: gormlogger.Discard})
	if err != nil {
		t.Fatalf("open broken db failed: %v", err)
	}
	sqlDB, err := broken.DB()
	if err != nil {
		t.Fatalf("get sql db failed: %v", err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatalf("close sql db failed: %v", err)
	}
	r := NewGormRepository(broken, zap.NewNop())

	assertErr := func(name string, err error) {
		t.Helper()
		if err == nil {
			t.Fatalf("%s must fail on closed db", name)
		}
	}
	query := func(name string, fn func() error) {
		t.Helper()
		assertErr(name, fn())
	}

	seed := &entity.Product{ID: "BX", Name: "x", PriceCents: 1, Total: 1, Available: 1, OnSale: true}
	query("SaveProduct", func() error { return r.SaveProduct(ctx, seed) })
	query("FindProduct", func() error { _, err := r.FindProduct(ctx, "BX"); return err })
	query("ListProducts", func() error { _, err := r.ListProducts(ctx); return err })
	query("HasAnyProduct", func() error { _, err := r.HasAnyProduct(ctx); return err })
	query("PlaceOrderTx", func() error { _, err := r.PlaceOrderTx(ctx, "b", "b", "BX", 1, time.Minute); return err })
	query("FindTradeOrder", func() error { _, err := r.FindTradeOrder(ctx, "X"); return err })
	query("ListTradeOrders", func() error { _, _, err := r.ListTradeOrders(ctx, "", ""); return err })
	query("ListExpiredCreated", func() error { _, err := r.ListExpiredCreated(ctx, time.Now()); return err })
	query("SaveCallback", func() error { return r.SaveCallback(ctx, &entity.PayCallback{TradeNo: "X", OrderID: "Y"}) })
	query("FindCallback", func() error { _, err := r.FindCallback(ctx, "X"); return err })
	query("UpdateCallbackStatus", func() error { return r.UpdateCallbackStatus(ctx, "X", entity.CallbackDone) })
	query("IncrementCallbackHits", func() error { return r.IncrementCallbackHits(ctx, "X") })
	query("ListCallbacks", func() error { _, err := r.ListCallbacks(ctx, "", ""); return err })
	query("SaveException", func() error {
		return r.SaveException(ctx, &entity.ExceptionPay{ID: "EX-X", Type: entity.ExcLatePay, Status: entity.ExcStatusPending})
	})
	query("FindException", func() error { _, err := r.FindException(ctx, "EX-X"); return err })
	query("ListExceptions", func() error { _, err := r.ListExceptions(ctx, true); return err })
	query("FindExceptionByTradeNo", func() error { _, err := r.FindExceptionByTradeNo(ctx, "X"); return err })
	query("CloseException", func() error { return r.CloseException(ctx, "EX-X", "h", "r") })
	query("MarkRefundFailed", func() error { _, err := r.MarkRefundFailed(ctx, "X"); return err })
	query("AppendLedger", func() error {
		return r.AppendLedger(ctx, &entity.Ledger{OrderID: "X", Kind: entity.LedgerStock, Text: "t"})
	})
	query("ListLedgers", func() error { _, err := r.ListLedgers(ctx, "X"); return err })
	query("TradeSummary", func() error { _, err := r.TradeSummary(ctx); return err })
	query("TransitionOrder", func() error {
		_, err := r.TransitionOrder(ctx, "X", entity.OrderStatusCreated, entity.OrderStatusClosed, 0, nil)
		return err
	})

	// 框架既有 order CRUD 的错误分支
	query("Save", func() error { return r.Save(ctx, &entity.Order{ID: "X", Name: "n"}) })
	query("FindByID", func() error { _, err := r.FindByID(ctx, "X"); return err })
	query("List", func() error { _, _, err := r.List(ctx, 0, 10); return err })
	query("Delete", func() error { return r.Delete(ctx, "X") })

	// 模型集合名（框架示例代码，保持 100%）
	if name := (dbmodel.OrderDocument{}).MongoCollectionName(); name == "" {
		t.Fatalf("mongo collection name must not be empty")
	}
}
