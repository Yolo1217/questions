# AI 使用过程报告（题目01）

按题面"AI 提示词全过程留痕"要求整理。原始对话记录以开发工具的会话导出为准（CodeBuddy 会话历史），本报告是结构化归档。

## 1. 使用环境

- 工具：CodeBuddy（Auto 模型）+ Agent Skill（`order-trade-business` / `order-trade-acceptance` / `student-project-audit`）
- 会话方式：单一开发会话完成实现与自验收（独立验收会话缺口见验收报告 D4）

## 2. 工作阶段与关键提示词

| 阶段 | 关键指令（用户原话/意图） | AI 产出 |
|---|---|---|
| 覆盖率攻坚 | "把测试补齐，覆盖率要过 85%" | order 四层 69 条用例；repo 直测暴露 GORM `default:true` 真实缺陷并修复；模块口径 61.7% → 91.0% |
| Mongo 补测 | "不用整编号了，直接整，docker也修好了" | mongo:7 容器 + `mongo_repository_test.go`（真实 Mongo 直测），`questions_mongo_repo` 库隔离 |
| 提交规范 | "工作项编号这个在哪" → 用户提供 commit-convention 口径 | 语义化提交至 `dev/1`，feat/fix 编号暂缺在说明体如实标注 |
| 前端页面 | 用户附题面截图（要求可操作页面） | 网关内嵌 5 个工作台（`go:embed`）+ 服务端签名模拟支付端点 + 截图/冒烟脚本 |
| 部署与 CI | "你继续" | `docker-compose.trade.yml` 一键部署 + `scripts/smoke-http.sh`（25 项断言实测全过）+ GitHub Actions 双 job 门禁 |
| 规范留痕 | "你继续" | `AGENTS.md` + `docs/skill-usage.md`（6 项闭环）+ 本报告 |

## 3. 有效性分析

- **Skill 口径前置**避免了 6 处返工（见 [docs/skill-usage.md](docs/skill-usage.md) 第 2 节），其中"幂等不能返回布尔""异常处置不可改状态抹除"两处在实现早期即被否决。
- **AI 自测发现的真实缺陷** 1 个（GORM 零值跳过列），修复后由回归测试与页面实测双重验证——说明"测试驱动的 AI"比"AI 直接写实现"更可靠。
- **失败教训**：容器化首跑时 MySQL 健康探活用 `mysqladmin ping` 在建库期误报存活，user 服务过早启动 fatal；改为真实查询探活 + `restart: on-failure` 后稳定。已写入 AGENTS.md 已知坑清单。

## 4. 待人工完成

1. 原始会话记录导出（开发工具 UI 导出 JSON/Markdown，随提交归档）。
2. 飞书项目（project-key `r17te9`）任务规划回填与工作项编号补录（历史提交说明已预留真实标注，未编造）。
3. 独立验收会话复验（D4）。

## 5. 对应提交

- 测试与覆盖率：`c25652a`
- 页面交付：`207d857`
- 部署与 CI：`fde66de`
- 规范与留痕：`ad30ca0`
- 分支：`dev/1`（远端以推送后提交号为准）