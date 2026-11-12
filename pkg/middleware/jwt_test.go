package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

const testSecret = "unit-test-secret"

func newAuthRouter(cfg JWTConfig) (*gin.Engine, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/whoami", JWTAuth(cfg), func(c *gin.Context) {
		claims := ClaimsFromContext(c)
		c.JSON(http.StatusOK, gin.H{"user_id": claims.UserID, "role": claims.Role, "name": claims.Name})
	})
	recorder := httptest.NewRecorder()
	return router, recorder
}

func mustToken(t *testing.T, cfg JWTConfig, claims JWTClaims) string {
	t.Helper()
	token, err := GenerateToken(cfg, claims)
	if err != nil {
		t.Fatalf("generate token failed: %v", err)
	}
	return token
}

// 正常链路：签发 → Bearer 校验 → claims 写入上下文。
func TestJWTAuthAcceptsValidToken(t *testing.T) {
	cfg := JWTConfig{Secret: testSecret, Issuer: "unit", ExpireSeconds: 60}
	router, recorder := newAuthRouter(cfg)
	token := mustToken(t, cfg, JWTClaims{UserID: "u1", Role: "buyer", Name: "阿禾"})

	req := httptest.NewRequest(http.MethodGet, "/whoami", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("valid token must pass, got %d: %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	for _, want := range []string{`"user_id":"u1"`, `"role":"buyer"`, `"name":"阿禾"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("body must contain %s, got %s", want, body)
		}
	}
}

func TestJWTAuthRejectsMissingBearer(t *testing.T) {
	cfg := JWTConfig{Secret: testSecret, Issuer: "unit", ExpireSeconds: 60}
	router, recorder := newAuthRouter(cfg)
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/whoami", nil))
	if recorder.Code != http.StatusUnauthorized || !strings.Contains(recorder.Body.String(), "missing_token") {
		t.Fatalf("missing bearer must be 401/missing_token, got %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestJWTAuthRejectsTamperedToken(t *testing.T) {
	cfg := JWTConfig{Secret: testSecret, Issuer: "unit", ExpireSeconds: 60}
	router, recorder := newAuthRouter(cfg)
	token := mustToken(t, cfg, JWTClaims{UserID: "u1"})
	// 篡改最后一段签名
	tampered := token[:len(token)-2] + "xx"
	req := httptest.NewRequest(http.MethodGet, "/whoami", nil)
	req.Header.Set("Authorization", "Bearer "+tampered)
	router.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusUnauthorized || !strings.Contains(recorder.Body.String(), "invalid_token") {
		t.Fatalf("tampered token must be rejected, got %d %s", recorder.Code, recorder.Body.String())
	}
}

// 过期 token 必须被拒绝（无效声明）。
// 注：包级 GenerateToken 会把非正数过期时间收敛为默认值（NewJWT 预期行为），
// 因此这里直接构造未收敛的 JWT 实例签发已过期 token。
func TestJWTAuthRejectsExpiredToken(t *testing.T) {
	issuer := &JWT{cfg: JWTConfig{Secret: testSecret, Issuer: "unit", ExpireSeconds: -1}}
	verifier := JWTConfig{Secret: testSecret, Issuer: "unit", ExpireSeconds: 60}
	router, recorder := newAuthRouter(verifier)
	token, err := issuer.GenerateToken(JWTClaims{UserID: "u1"})
	if err != nil {
		t.Fatalf("issue expired token failed: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/whoami", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	router.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("expired token must be rejected, got %d", recorder.Code)
	}
}

// 错误密钥签发的 token 必须被拒绝。
func TestJWTAuthRejectsForeignSecret(t *testing.T) {
	issuer := JWTConfig{Secret: testSecret, Issuer: "unit", ExpireSeconds: 60}
	verifier := JWTConfig{Secret: "another-secret", Issuer: "unit", ExpireSeconds: 60}
	router, recorder := newAuthRouter(verifier)
	req := httptest.NewRequest(http.MethodGet, "/whoami", nil)
	req.Header.Set("Authorization", "Bearer "+mustToken(t, issuer, JWTClaims{UserID: "u1"}))
	router.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("foreign secret token must be rejected, got %d", recorder.Code)
	}
}

func TestJWTAuthRejectsMissingSecret(t *testing.T) {
	router, recorder := newAuthRouter(JWTConfig{})
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/whoami", nil))
	if recorder.Code != http.StatusUnauthorized || !strings.Contains(recorder.Body.String(), "jwt_secret_missing") {
		t.Fatalf("missing secret must abort, got %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestNewJWTAppliesDefaults(t *testing.T) {
	jwt := NewJWT(JWTConfig{Secret: testSecret})
	if jwt.cfg.Issuer != "xiaolanshu" || jwt.cfg.ExpireSeconds != 7200 {
		t.Fatalf("defaults must apply, got %+v", jwt.cfg)
	}
	if _, err := jwt.GenerateToken(JWTClaims{}); err != nil {
		t.Fatalf("token generation with defaults failed: %v", err)
	}
	if _, err := NewJWT(JWTConfig{}).GenerateToken(JWTClaims{}); err == nil {
		t.Fatalf("token generation without secret must fail")
	}
}

func TestBearerTokenParsing(t *testing.T) {
	cases := map[string]string{
		"Bearer abc":   "abc",
		"bearer abc":   "abc",
		"BEARER  abc ": "abc",
		"Bearer":       "",
		"Basic abc":    "",
		"Bearer a b":   "",
		"":             "",
		"Bearer ":      "",
	}
	for header, want := range cases {
		if got := bearerToken(header); got != want {
			t.Fatalf("bearerToken(%q) = %q, want %q", header, got, want)
		}
	}
}

func TestClaimsFromContextDefaults(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	if claims := ClaimsFromContext(c); claims.UserID != "" {
		t.Fatalf("empty context must return zero claims, got %+v", claims)
	}
}
