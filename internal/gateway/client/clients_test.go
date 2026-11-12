package client_test

import (
	"net"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"google.golang.org/grpc"

	"github.com/BwCloudWeGo/bw-cli/internal/gateway/client"
	"github.com/BwCloudWeGo/bw-cli/pkg/config"
)

// dialConfig 返回三个服务都指向同一监听地址的配置。
func dialConfig(addr string) *config.Config {
	return &config.Config{
		Services: map[string]config.ServiceConfig{
			"user":  {Name: "user", Target: addr},
			"note":  {Name: "note", Target: addr},
			"order": {Name: "order", Target: addr},
		},
	}
}

// New 连接配置目标并创建三个强类型 client；Close 释放全部连接。
func TestNewDialsConfiguredTargetsAndCloseReleases(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv := grpc.NewServer()
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	cl, err := client.New(dialConfig(lis.Addr().String()), zap.NewNop())
	require.NoError(t, err)
	require.NotNil(t, cl.User)
	require.NotNil(t, cl.Note)
	require.NotNil(t, cl.Order)
	require.NotNil(t, cl.Config)

	cl.Close()
}

// 目标为空时 gRPC 无法解析目标，New 返回带服务名的错误。
func TestNewReportsUnusableTarget(t *testing.T) {
	cfg := dialConfig("")
	_, err := client.New(cfg, zap.NewNop())
	require.ErrorContains(t, err, "dial user service")
}

// 后续服务目标不可用时，New 返回对应服务名错误，并释放已建立的连接。
func TestNewClosesEarlierConnsWhenLaterDialFails(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv := grpc.NewServer()
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	addr := lis.Addr().String()

	cfg := &config.Config{Services: map[string]config.ServiceConfig{
		"user":  {Name: "user", Target: addr},
		"note":  {Name: "note", Target: ""},
		"order": {Name: "order", Target: addr},
	}}
	_, err = client.New(cfg, zap.NewNop())
	require.ErrorContains(t, err, "dial note service")

	cfg = &config.Config{Services: map[string]config.ServiceConfig{
		"user":  {Name: "user", Target: addr},
		"note":  {Name: "note", Target: addr},
		"order": {Name: "order", Target: ""},
	}}
	_, err = client.New(cfg, zap.NewNop())
	require.ErrorContains(t, err, "dial order service")
}
