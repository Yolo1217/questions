# AGENTS.md —— AI 协作规范

本文件约束任何 AI Agent（CodeBuddy / Claude / Codex 等）在本仓库内的行为。人类工程师同样适用。

## 1. 项目速览

- **框架**：bw-cli 脚手架（Go 1.25，gin + gRPC + GORM），module `github.com/BwCloudWeGo/bw-cli`
- **题目01 交付**：小型电商订单交易（商品库存 / 订单状态机 / 支付回调幂等 / 售前退款 / 发货 / 支付异常闭环 / 运营核查），落点在 `internal/order/`，网关层在 `internal/gateway/`（含内嵌页面 `internal/gateway/web/`）
- **详细设计**：[docs/题目01-业务实现.md](docs/题目01-业务实现.md)；自验收：[docs/验收报告-题目01.md](docs/验收报告-题目01.md)

## 2. 常用命令

```bash
make test                                   # 全量测试（需本地 MySQL root:root@127.0.0.1:3306 与 Mongo mongo:7）
go test -count=1 ./internal/order/... ./pkg/middleware/...   # 题目01 相关模块
make run-user && make run-order && make run-gateway          # 本地三进程（顺序启动）
GATEWAY_PORT=18080 docker compose -f docker-compose.trade.yml up -d --build   # 一键容器化
BASE_URL=http://127.0.0.1:18080 bash scripts/smoke-http.sh  # 部署冒烟（25 项断言）
powershell scripts\smoke-pages.ps1                           # 五页面端到端（Windows）
powershell scripts\seed-demo.ps1                             # 演示数据播种（六状态+三异常）
go test -coverpkg=./internal/order/... -coverprofile=reports/coverage.out ./internal/order/... ./pkg/middleware/...
```

## 3. 修改红线（违反即验收不通过）

1. **不得绕过状态机**：订单状态迁移唯一入口是 `internal/order/service` 的显式方法；禁止在 repo/handler 层直接 UPDATE status。
2. **不得引入删除类接口**：订单、台账、回调、异常表一律只增不改不删；异常闭环用条件更新（`WHERE status='待处理'`）。
3. **不得弱化幂等与验签，且验签必须先于落库**：支付回调先重算 HMAC 验签，通过后才写回调记录（伪造流量只落异常表，不得占用渠道流水号）；退款审核重复提交必须返回 `duplicate=true`。
4. **不得让关单后到账恢复订单或回补库存**：迟到支付只能落异常表。
5. **金额必须来自服务端快照**：任何金额计算不得采信前端传参；库存扣减必须是条件更新（`available >= qty`）。
6. **角色鉴权不得绕过，不允许任何匿名可写接口**：新增接口必须挂 `RequireRole`；买家数据接口必须校验归属（buyer_id）；脚手架遗留 CRUD 也必须登录后访问。
7. **覆盖率门禁 ≥85%**（order 模块口径）：改业务代码必须同步补测试；`pkg/middleware` 同样在门禁内。

## 4. 测试约定

- repo 层测试连真实 MySQL（库 `questions_test*`）与真实 Mongo（`questions_mongo_repo`，整集合 Drop 隔离）；错误注入用 stub 嵌入（`stubRepo{GormRepository: ...}`）。
- handler 测试用 `httptest` + gin TestMode，断言业务结果（状态/库存/流水条数/错误码），禁止只断言 HTTP 200。
- 已知坑：GORM `default:` 标签会让 Create 跳过零值字段（见 `model/trade.go` 注释）；`mysqladmin ping` 在 MySQL 建库期误报存活（compose 探活用 `mysql -e 'SELECT 1'`）。

## 5. 提交规范

格式：`[type] [版本] 摘要` + 说明体（缺陷根因、实测证据）。type 取 feat/fix/test/docs/chore/refactor。

- feat/fix 须附飞书项目工作项编号（project-key `r17te9`，如 `#6974499249`）；题目01 提交时编号暂缺，在说明体如实标注"工作项编号暂缺，未编造"。
- 分支：`dev/<序号>`；推送前 `gofmt` / `go vet` / `go test` / 覆盖率门禁必须全绿。
- 证据类产物（冒烟输出、覆盖率、截图）放 `reports/`，随提交入库。

## 6. 文档同步义务

改契约（接口/错误码/页面/部署方式）必须同步：`docs/题目01-业务实现.md` → `README.md` → `docs/验收报告-题目01.md`（复测结论与证据编号）。

## 7. Skill 使用要求

涉及交易业务实现或验收时，必须先加载 `order-trade-business` / `order-trade-acceptance` 技能（历史使用记录见 [docs/skill-usage.md](docs/skill-usage.md)），并按其口径修正实现；Skill 揭示的问题要形成"发现 → 修正 → 复检"闭环并留痕。
