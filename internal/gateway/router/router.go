// router 包负责 Gin 引擎构建和路由注册。
package router

import (
	"io/fs"
	"net/http"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/BwCloudWeGo/bw-cli/internal/gateway/client"
	"github.com/BwCloudWeGo/bw-cli/internal/gateway/web"
	"github.com/BwCloudWeGo/bw-cli/pkg/config"
	"github.com/BwCloudWeGo/bw-cli/pkg/middleware"
)

// New 使用配置好的中间件和版本化 API 路由构建网关 Gin 引擎。
func New(clients *client.Clients, log *zap.Logger, middlewareCfg config.MiddlewareConfig) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(
		middleware.CORS(middlewareCfg.CORS),
		middleware.RequestID(),
		middleware.RequestLogger(log),
		gin.Recovery(),
	)
	r.OPTIONS("/*path", func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	registerHealthRoutes(r)
	registerAPIRoutes(r, clients, log, middlewareCfg)
	registerPages(r)
	return r
}

// registerPages 托管题目01 的可操作页面（go:embed 内嵌，随二进制发布）。
// 页面静态资源为公开资源，业务数据仍由 /api/v1 的 JWT 鉴权保护。
func registerPages(r *gin.Engine) {
	pages := web.FS()
	// 根路径直接回写首页内容：FileFromFS 对 "/" 会触发 index.html 重定向，导致循环。
	indexHTML, err := fs.ReadFile(pages, "index.html")
	if err != nil {
		panic("gateway: embed index.html missing: " + err.Error())
	}
	r.GET("/", func(c *gin.Context) { c.Data(http.StatusOK, "text/html; charset=utf-8", indexHTML) })
	for _, page := range []string{"buyer.html", "warehouse.html", "refund.html", "exception.html", "ops.html"} {
		name := page
		r.GET("/"+name, func(c *gin.Context) { c.FileFromFS(name, http.FS(pages)) })
	}
	assets, err := fs.Sub(pages, "assets")
	if err != nil {
		panic("gateway: embed assets missing: " + err.Error())
	}
	r.StaticFS("/assets", http.FS(assets))
}
