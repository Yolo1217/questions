package middleware

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func newCORSPayload(origin string, method string) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(CORS(DefaultCORSConfig()))
	router.GET("/x", func(c *gin.Context) { c.Status(http.StatusOK) })
	router.OPTIONS("/x", func(c *gin.Context) { c.Status(http.StatusOK) })
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(method, "/x", nil)
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	router.ServeHTTP(recorder, req)
	return recorder
}

func TestCORSAllowsWildcardOrigin(t *testing.T) {
	recorder := newCORSPayload("https://shop.example.com", http.MethodGet)
	if recorder.Code != http.StatusOK {
		t.Fatalf("allowed origin must pass, got %d", recorder.Code)
	}
	if got := recorder.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("wildcard config must return *, got %q", got)
	}
	if recorder.Header().Get("Access-Control-Max-Age") == "" {
		t.Fatalf("max age header missing")
	}
}

func TestCORSPreflightAborts(t *testing.T) {
	recorder := newCORSPayload("https://shop.example.com", http.MethodOptions)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("preflight must return 204, got %d", recorder.Code)
	}
}

func TestCORSRejectsUnknownOrigin(t *testing.T) {
	cfg := DefaultCORSConfig()
	cfg.AllowOrigins = []string{"https://trusted.example.com"}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(CORS(cfg))
	router.GET("/x", func(c *gin.Context) { c.Status(http.StatusOK) })
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Origin", "https://evil.example.com")
	router.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("unknown origin must be forbidden, got %d", recorder.Code)
	}
}

func TestCORSWithoutOriginHeader(t *testing.T) {
	recorder := newCORSPayload("", http.MethodGet)
	if recorder.Code != http.StatusOK {
		t.Fatalf("request without origin must pass, got %d", recorder.Code)
	}
	if recorder.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("no CORS headers expected without Origin")
	}
}

func TestCORSPinnedOriginEchoesOrigin(t *testing.T) {
	cfg := DefaultCORSConfig()
	cfg.AllowOrigins = []string{"https://trusted.example.com"}
	cfg.AllowCredentials = true
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(CORS(cfg))
	router.GET("/x", func(c *gin.Context) { c.Status(http.StatusOK) })
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Origin", "https://trusted.example.com")
	router.ServeHTTP(recorder, req)
	if recorder.Header().Get("Access-Control-Allow-Origin") != "https://trusted.example.com" {
		t.Fatalf("pinned origin must echo origin, got %q", recorder.Header().Get("Access-Control-Allow-Origin"))
	}
	if recorder.Header().Get("Access-Control-Allow-Credentials") != "true" {
		t.Fatalf("credentials header missing")
	}
}

func TestRequestIDHeaderRoundTrip(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(RequestID())
	router.GET("/x", func(c *gin.Context) {
		if requestID(c) == "" {
			t.Fatalf("request id must be set in context")
		}
		c.Status(http.StatusOK)
	})
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/x", nil))
	if recorder.Header().Get(HeaderRequestID) == "" {
		t.Fatalf("generated request id must be echoed")
	}
}

func TestRequestIDReusesIncomingHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(RequestID())
	router.GET("/x", func(c *gin.Context) { c.Status(http.StatusOK) })
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set(HeaderRequestID, "trace-123")
	router.ServeHTTP(recorder, req)
	if got := recorder.Header().Get(HeaderRequestID); got != "trace-123" {
		t.Fatalf("incoming request id must be reused, got %q", got)
	}
}

func TestRequestLoggerRecordsFields(t *testing.T) {
	core, entries := observer.New(zap.InfoLevel)
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(RequestID())
	router.Use(RequestLogger(zap.New(core)))
	router.GET("/boom", func(c *gin.Context) {
		c.Set("error_code", "boom")
		c.AbortWithStatus(http.StatusInternalServerError)
	})
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/boom", nil))
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("handler must run, got %d", recorder.Code)
	}
	if requestID(&gin.Context{}) != "" || errorCode(&gin.Context{}) != "" {
		t.Fatalf("helpers must return empty on missing keys")
	}
	if len(entries.All()) != 1 {
		t.Fatalf("expected one access log entry, got %d", len(entries.All()))
	}
	entry := entries.All()[0]
	// zap.Int 底层存 int64，直接接口比较会失败，统一格式化后再断言
	if fmt.Sprint(entry.ContextMap()["error_code"]) != "boom" || fmt.Sprint(entry.ContextMap()["status"]) != "500" {
		t.Fatalf("log fields mismatch: %+v", entry.ContextMap())
	}
	if entry.ContextMap()["request_id"] == "" {
		t.Fatalf("log must carry request_id")
	}
}
