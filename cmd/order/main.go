package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"go.uber.org/zap"
	"google.golang.org/grpc"

	orderv1 "github.com/BwCloudWeGo/bw-cli/api/gen/order/v1"
	orderhandler "github.com/BwCloudWeGo/bw-cli/internal/order/handler"
	orderrepo "github.com/BwCloudWeGo/bw-cli/internal/order/repo"
	orderservice "github.com/BwCloudWeGo/bw-cli/internal/order/service"
	"github.com/BwCloudWeGo/bw-cli/pkg/config"
	"github.com/BwCloudWeGo/bw-cli/pkg/database"
	"github.com/BwCloudWeGo/bw-cli/pkg/grpcx"
	"github.com/BwCloudWeGo/bw-cli/pkg/logger"
)

const serviceName = "order-service"
const defaultGRPCPort = 9100

func main() {
	if err := config.InitGlobal("configs/config.yaml"); err != nil {
		panic(err)
	}
	cfg := config.MustGlobal()
	cfg.Log.Service = cfg.ServiceName("order")
	cfg.Log = logger.WithDailyFileName(cfg.Log, time.Now())

	log, err := logger.New(cfg.Log)
	if err != nil {
		panic(err)
	}
	defer log.Sync()
	config.PrintSourceNotice(cfg, os.Stdout)

	db, err := database.Open(cfg.Database, cfg.MySQL, cfg.PostgreSQL, log)
	if err != nil {
		log.Fatal("open database failed", zap.Error(err))
	}
	if err := orderrepo.AutoMigrate(db); err != nil {
		log.Fatal("migrate order database failed", zap.Error(err))
	}

	repo := orderrepo.NewGormRepository(db, log)
	svc := orderservice.NewService(repo, log)
	trade := orderservice.NewTradeService(repo, tradeConfigFromEnv(), log)
	ctx, cancelSweep := context.WithCancel(context.Background())
	if err := trade.SeedProducts(ctx); err != nil {
		log.Fatal("seed trade products failed", zap.Error(err))
	}
	go sweepExpiredOrders(ctx, trade, log)

	server := grpc.NewServer(grpc.UnaryInterceptor(grpcx.UnaryServerInterceptor(log)))
	orderv1.RegisterOrderServiceServer(server, orderhandler.NewServer(svc, trade, log))

	port := cfg.ServicePort("order", defaultGRPCPort)
	addr := fmt.Sprintf("%s:%d", cfg.GRPC.Host, port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "\n[Service Start Failed]\n  service: %s\n  listen: %s\n  error: %v\n\n", serviceName, addr, err)
		log.Fatal("listen failed", zap.String("addr", addr), zap.Error(err))
	}

	printStartupSummary(cfg.App.Env, addr, port)
	go shutdownOnSignal(server, log)
	if err := server.Serve(listener); err != nil {
		cancelSweep()
		log.Fatal("service stopped unexpectedly", zap.Error(err))
	}
	cancelSweep()
}

// tradeConfigFromEnv 读取交易域策略：保留期秒数、渠道退款首次失败演示开关。
func tradeConfigFromEnv() orderservice.TradeConfig {
	keepSeconds := 600
	if raw := os.Getenv("TRADE_KEEP_SECONDS"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			keepSeconds = parsed
		}
	}
	refundFailFirst := os.Getenv("TRADE_REFUND_FAIL_FIRST") != "0"
	secret := os.Getenv("TRADE_CHANNEL_SECRET")
	if secret == "" {
		secret = orderservice.DefaultChannelSecret
	}
	return orderservice.TradeConfig{
		Keep:            time.Duration(keepSeconds) * time.Second,
		RefundFailFirst: refundFailFirst,
		ChannelSecret:   secret,
	}
}

// sweepExpiredOrders 每 5 秒关闭超保留期的待支付订单并释放预占（超时判断使用服务端时间）。
func sweepExpiredOrders(ctx context.Context, trade *orderservice.TradeService, log *zap.Logger) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			closed, err := trade.SweepExpired(ctx)
			if err != nil {
				log.Warn("sweep expired orders failed", zap.Error(err))
				continue
			}
			if closed > 0 {
				log.Info("expired orders closed", zap.Int("count", closed))
			}
		}
	}
}

func printStartupSummary(env string, addr string, port int) {
	host := strings.Split(addr, ":")[0]
	if host == "" || host == "0.0.0.0" {
		host = "127.0.0.1"
	}
	fmt.Fprintf(os.Stdout, "\n[Service Started]\n")
	fmt.Fprintf(os.Stdout, "  service: %s\n", serviceName)
	fmt.Fprintf(os.Stdout, "  env: %s\n", env)
	fmt.Fprintf(os.Stdout, "  listen: %s\n", addr)
	fmt.Fprintf(os.Stdout, "  grpc: %s:%d\n", host, port)
	fmt.Fprintf(os.Stdout, "  config: services.order.port\n\n")
}

func shutdownOnSignal(server *grpc.Server, log *zap.Logger) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	<-ch
	log.Info("service shutting down", zap.String("service", serviceName))
	server.GracefulStop()
}
