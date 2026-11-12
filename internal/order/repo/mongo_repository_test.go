package repo

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.uber.org/zap"

	"github.com/BwCloudWeGo/bw-cli/internal/order/entity"
)

// Mongo 仓储直测：连接本机 mongo:7 容器（docker run -d -p 27017:27017 mongo:7），
// 用独立集合 questions_mongo_repo，未连接时跳过。
func newMongoRepo(t *testing.T) (*MongoRepository, *mongo.Client) {
	t.Helper()
	client, err := mongo.Connect(options.Client().ApplyURI("mongodb://127.0.0.1:27017"))
	if err != nil {
		t.Skipf("mongo unavailable: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := client.Ping(ctx, nil); err != nil {
		t.Skipf("mongo ping failed: %v", err)
	}
	return NewMongoRepository(client.Database("questions_mongo_repo"), zap.NewNop()), client
}

func TestMongoRepositoryCRUD(t *testing.T) {
	repo, client := newMongoRepo(t)
	ctx := context.Background()

	// 独立集合隔离：直接整集合删除
	if err := client.Database("questions_mongo_repo").Collection("orders").Drop(ctx); err != nil {
		t.Fatalf("drop collection failed: %v", err)
	}

	now := time.Now()
	item := &entity.Order{ID: "MG-1", Name: "示例订单", Description: "mongo 直测", CreatedAt: now, UpdatedAt: now}
	if err := repo.Save(ctx, item); err != nil {
		t.Fatalf("save failed: %v", err)
	}
	// 幂等 upsert：同 ID 再存更新而非报错
	item.Name = "改名"
	if err := repo.Save(ctx, item); err != nil {
		t.Fatalf("upsert failed: %v", err)
	}

	found, err := repo.FindByID(ctx, "MG-1")
	if err != nil || found.Name != "改名" || found.Description != "mongo 直测" {
		t.Fatalf("find mismatch: %+v err=%v", found, err)
	}
	if _, err := repo.FindByID(ctx, "NOPE"); !errors.Is(err, entity.ErrOrderNotFound) {
		t.Fatalf("missing must be ErrOrderNotFound, got %v", err)
	}

	second := &entity.Order{ID: "MG-2", Name: "第二单", CreatedAt: now.Add(time.Second), UpdatedAt: now.Add(time.Second)}
	if err := repo.Save(ctx, second); err != nil {
		t.Fatalf("save second failed: %v", err)
	}
	items, total, err := repo.List(ctx, 0, 10)
	if err != nil || total != 2 || len(items) != 2 {
		t.Fatalf("list mismatch: total=%d n=%d err=%v", total, len(items), err)
	}
	// 按 created_at 倒序：新记录在前
	if items[0].ID != "MG-2" {
		t.Fatalf("list must sort by created_at desc, got first=%s", items[0].ID)
	}
	if _, _, err := repo.List(ctx, 1, 1); err != nil {
		t.Fatalf("paged list failed: %v", err)
	}

	if err := repo.Delete(ctx, "MG-1"); err != nil {
		t.Fatalf("delete failed: %v", err)
	}
	if err := repo.Delete(ctx, "MG-1"); !errors.Is(err, entity.ErrOrderNotFound) {
		t.Fatalf("delete missing must be ErrOrderNotFound, got %v", err)
	}
}

// 文档与领域对象互转（纯函数）。
func TestOrderDocumentMapping(t *testing.T) {
	now := time.Now()
	doc := toDocument(&entity.Order{ID: "M", Name: "n", Description: "d", CreatedAt: now, UpdatedAt: now})
	if doc.ID != "M" || doc.Name != "n" {
		t.Fatalf("toDocument mismatch: %+v", doc)
	}
	back := toDomainFromDocument(doc)
	if back.ID != "M" || back.Name != "n" || back.Description != "d" {
		t.Fatalf("toDomainFromDocument mismatch: %+v", back)
	}
}
