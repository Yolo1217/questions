package router

import (
	"github.com/gin-gonic/gin"

	"github.com/BwCloudWeGo/bw-cli/internal/gateway/handler"
	"github.com/BwCloudWeGo/bw-cli/pkg/middleware"
)

// registerOrderRoutes 注册订单交易业务接口：
// 脚手架默认 CRUD（保留）+ 题目01 交易主链路（商品/下单/回调/退款/发货/异常/汇总）。
// 角色鉴权统一在网关完成：买家 / 仓库 / 客服 / 运营四类账号各走各自入口。
func registerOrderRoutes(v1 *gin.RouterGroup, orderHandler *handler.OrderHandler, jwtCfg middleware.JWTConfig) {
	auth := middleware.JWTAuth(jwtCfg)
	buyer := middleware.RequireRole("buyer")
	warehouse := middleware.RequireRole("warehouse")
	cs := middleware.RequireRole("cs")
	ops := middleware.RequireRole("ops")
	staff := middleware.RequireRole("buyer", "warehouse", "cs", "ops")

	// 模拟支付渠道回调：渠道侧无登录态，独立于鉴权路由。
	pay := v1.Group("/pay")
	pay.POST("/notify", orderHandler.PayNotify)

	// 商品：登录可见，运营可维护。
	products := v1.Group("/products")
	products.Use(auth)
	products.GET("", staff, orderHandler.ListProducts)
	products.POST("", ops, orderHandler.UpsertProduct)

	// 买家：下单 / 我的订单 / 详情 / 取消 / 申请退款。
	trade := v1.Group("/trade/orders")
	trade.Use(auth)
	trade.POST("", buyer, orderHandler.PlaceTradeOrder)
	trade.GET("/mine", buyer, orderHandler.MyTradeOrders)
	trade.GET("/:id", staff, orderHandler.TradeOrderDetail)
	trade.POST("/:id/cancel", buyer, orderHandler.CancelTradeOrder)
	trade.POST("/:id/refund", buyer, orderHandler.ApplyTradeRefund)

	// 仓库：可发货列表与发货登记。
	warehouseGroup := v1.Group("/warehouse")
	warehouseGroup.Use(auth, warehouse)
	warehouseGroup.GET("/orders", orderHandler.WarehouseOrders)
	warehouseGroup.POST("/orders/:id/ship", orderHandler.ShipTradeOrder)

	// 客服：退款审核、支付异常处置、渠道回调反查。
	csGroup := v1.Group("/cs")
	csGroup.Use(auth, cs)
	csGroup.GET("/refunds", orderHandler.RefundPendingOrders)
	csGroup.POST("/refunds/:id/audit", orderHandler.AuditTradeRefund)
	csGroup.GET("/exceptions", orderHandler.ExceptionPays)
	csGroup.POST("/exceptions/:id/handle", orderHandler.HandleExceptionPay)
	csGroup.GET("/callbacks", orderHandler.PayCallbacks)
	csGroup.GET("/callbacks/:tradeNo", orderHandler.PayCallbackDetail)

	// 运营：订单查询、交易核查汇总、回调反查（核对渠道收款去向）。
	opsGroup := v1.Group("/ops")
	opsGroup.Use(auth, ops)
	opsGroup.GET("/orders", orderHandler.OpsOrders)
	opsGroup.GET("/summary", orderHandler.OpsSummary)
	opsGroup.GET("/callbacks", orderHandler.PayCallbacks)
	opsGroup.GET("/callbacks/:tradeNo", orderHandler.PayCallbackDetail)

	// 脚手架默认 CRUD（保留兼容）。
	routes := v1.Group("/orders")
	routes.POST("", orderHandler.Create)
	routes.GET("", orderHandler.List)
	routes.GET("/:id", orderHandler.Get)
	routes.PUT("/:id", orderHandler.Update)
	routes.DELETE("/:id", orderHandler.Delete)
}
