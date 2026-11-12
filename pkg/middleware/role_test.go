package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// newRoleRouter 构造带已签发身份的测试路由，校验 RequireRole 的放行与拦截。
func newRoleRouter(userID string, role string, allowed []string) (*gin.Engine, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		if userID != "" {
			c.Set(claimsContextKey, JWTClaims{UserID: userID, Role: role})
		}
		c.Next()
	})
	router.GET("/ops/summary", RequireRole(allowed...), func(c *gin.Context) {
		c.Status(http.StatusOK)
	})
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ops/summary", nil)
	router.ServeHTTP(recorder, req)
	return router, recorder
}

func TestRequireRoleAllowsMatchingRole(t *testing.T) {
	_, recorder := newRoleRouter("u-ops", "ops", []string{"ops", "cs"})
	if recorder.Code != http.StatusOK {
		t.Fatalf("allowed role must pass, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestRequireRoleRejectsWrongRole(t *testing.T) {
	_, recorder := newRoleRouter("u-buyer", "buyer", []string{"ops"})
	if recorder.Code == http.StatusOK {
		t.Fatalf("buyer must not access ops endpoint")
	}
	if !strings.Contains(recorder.Body.String(), "permission_denied") {
		t.Fatalf("expected permission_denied in body, got %s", recorder.Body.String())
	}
}

func TestRequireRoleRejectsMissingToken(t *testing.T) {
	_, recorder := newRoleRouter("", "", []string{"ops"})
	if recorder.Code == http.StatusOK {
		t.Fatalf("missing token must be rejected")
	}
	if !strings.Contains(recorder.Body.String(), "missing_token") {
		t.Fatalf("expected missing_token in body, got %s", recorder.Body.String())
	}
}
