# Skill 使用留痕（题目01）

记录实现与验收过程中加载的 Agent Skill、其揭示的问题，以及"发现 → 修正 → 复检"闭环证据。

## 1. 加载记录

| Skill | 阶段 | 作用 |
|---|---|---|
| `order-trade-business` | 业务实现期 | 提供交易主链路裁决口径：下单预占、支付入账、售前整单退款、发货登记、库存回补的准入条件与唯一合法结果 |
| `order-trade-acceptance` | 自验收期 | 红线 7 条、六场景取证、十八项边界复现（E01–E18）、验收报告格式 |
| `student-project-audit` | 评分自检 | 交付物完整性、开发顺序、AI 全过程留痕的检查清单 |

## 2. Skill 口径驱动的修正（发现 → 修正 → 复检闭环）

| # | Skill 揭示的问题 | 修正 | 复检证据 |
|---|---|---|---|
| 1 | 支付入账不能只返回布尔：必须 HMAC 验签（恒定时间比较）+ `trade_no` 幂等键 + 条件更新三道防线，且**验签先于落库** | `service.PayNotify` 六步：验签 → 持久化幂等 → 订单存在 → 金额比对 → 状态分流 → 台账；异常分支只落异常表不改状态。初版实现误为先落库后验签，评分整改以 `TestForgedTradeNoDoesNotBlockGenuineRetry` 回归修正 | `TestPayNotifyBadSignatureRejected`、`TestPayNotifyRepeatedIsIdempotent`（5 次 → 1 条流水）、`TestConcurrentNotifyOnlyOneApplies`（3 路并发 → 1 条）；冒烟实测 `verify_failed` / `duplicate=true` |
| 2 | 异常处置原因必须必填且留痕，闭环后不可再改 | `HandleException` 校验 reason 非空，处置结果写入异常表与订单台账（`exception` 类）；`CloseException` 条件更新 | `TestHandleExceptionRequiresReason`；冒烟实测空原因 `reason_required`、闭环 `已闭环`、闭环后再处置被拒 |
| 3 | 退款审核重复提交必须幂等（不得重复打款/重复回补） | 审核先查订单状态：非 `REFUNDING` 直接返回 `duplicate=true` + 当前状态，不再触发渠道 | `TestAuditRefundDuplicateIsIdempotent`（退款流水条数不变）；冒烟实测第三次提交 `duplicate=true` |
| 4 | 发货必须对非法状态给出具名错误（已关闭/已退款/退款中），而非笼统失败 | `Ship` 显式分支：`order_closed` / `order_refunded` / `order_refunding`，并把拒绝原因写台账 | `TestShipRejectsClosedAndRefundedOrder`、`TestRefundAndShipRace`；页面实测 REFUNDING 订单发货返回 `order_refunding` |
| 5 | 渠道退款首次失败是设计内路径：订单保持退款审核中可重试 | `RefundFailFirst` 配置位；audit 成功路径区分 `REFUNDED` 与 `REFUNDING`（重试入口） | 冒烟实测首败 → 重试 `REFUNDED` → 重复 `duplicate=true`；`scripts/smoke-http.sh` 步骤 S3 |
| 6 | 验收必须换独立会话，本报告结论不具独立性 | 在验收报告中显著声明流程缺口，列入 D4 复测条件 | `docs/验收报告-题目01.md` 第 0 节流程声明 |

## 3. 未由 Skill 揭示、测试过程中发现的生产缺陷

| 缺陷 | 发现方式 | 修复 | 回归 |
|---|---|---|---|
| `ProductModel.OnSale` 带 `gorm:"default:true"`，Create 时零值 false 被 GORM 跳过，新建下架商品被存成在售 | repo 直测：seed 下架商品后断言 OnSale 失败 | 移除 default 标签并留注释（`internal/order/model/trade.go`） | `TestCreateProductPersistsOnSaleFalse`；页面"下架"徽章与下单拦截实测正常 |

## 4. 结论

Skill 的价值不在"生成代码"，而在**口径前置**：红线与唯一合法结果在动手前就确定，避免了事后返工。两次最容易走偏的地方（幂等返回布尔、异常处置可改状态）都在 Skill 口径里被直接否决。