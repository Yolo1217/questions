package middleware

import (
	"github.com/gin-gonic/gin"

	apperrors "github.com/BwCloudWeGo/bw-cli/pkg/errors"
	"github.com/BwCloudWeGo/bw-cli/pkg/httpx"
)

// RequireRole 在 JWTAuth 之后校验角色，越权直接 401/403 中止，不透传到下游服务。
// 角色与账号在 JWT 中签发，网关据此做唯一入口鉴权。
func RequireRole(roles ...string) gin.HandlerFunc {
	allowed := make(map[string]struct{}, len(roles))
	for _, role := range roles {
		allowed[role] = struct{}{}
	}
	return func(c *gin.Context) {
		claims := ClaimsFromContext(c)
		if claims.UserID == "" {
			httpx.Error(c, apperrors.Unauthorized("missing_token", "missing bearer token"))
			c.Abort()
			return
		}
		if _, ok := allowed[claims.Role]; !ok {
			httpx.Error(c, apperrors.New(apperrors.KindUnauthorized, "permission_denied",
				"越权拒绝：角色 "+claims.Role+" 无权访问该接口"))
			c.Abort()
			return
		}
		c.Next()
	}
}
