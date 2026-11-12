#!/usr/bin/env bash
# 题目01 HTTP 冒烟（Linux/macOS/CI）：覆盖 S1 正常成交、S3 售前退款、S4 伪造签名异常、运营核查、越权。
# 用法：BASE_URL=http://127.0.0.1:18080 bash scripts/smoke-http.sh
# 依赖：curl、jq、openssl
set -euo pipefail

BASE_URL="${BASE_URL:-http://127.0.0.1:8080}"
SECRET="${TRADE_CHANNEL_SECRET:-shop-mock-channel-secret}"
STAMP="$(date +%m%d%H%M%S)"
PASS=0
FAIL=0

ok()   { echo "  PASS  $1"; PASS=$((PASS+1)); }
bad()  { echo "  FAIL  $1"; FAIL=$((FAIL+1)); }
step() { echo ""; echo "[STEP] $1"; }

# api METHOD PATH TOKEN JSON —— 输出完整响应 JSON（含 error 分支）
api() {
  local method="$1" path="$2" token="${3:-}" body="${4:-}"
  local args=(-sS -X "$method" -H 'Content-Type: application/json')
  [ -n "$token" ] && args+=(-H "Authorization: Bearer $token")
  [ -n "$body" ] && args+=(-d "$body")
  curl "${args[@]}" "$BASE_URL$path"
}

field() { echo "$1" | jq -r "$2"; }

# login ACCOUNT ROLE → 输出 token
login() {
  local resp
  resp="$(api POST /api/v1/users/login '' "{\"account\":\"$1\",\"password\":\"123456\"}")"
  local role; role="$(field "$resp" '.data.user.role')"
  if [ "$role" != "$2" ]; then echo "账号 $1 角色异常：$role" >&2; exit 1; fi
  field "$resp" '.data.token'
}

# signed_notify TRADE_NO ORDER_ID AMOUNT SIGNATURE
signed_notify() {
  api POST /api/v1/pay/notify '' "{\"channel_trade_no\":\"$1\",\"order_id\":\"$2\",\"amount_cents\":$3,\"signature\":\"$4\"}"
}

hmac() { printf '%s' "$1" | openssl dgst -sha256 -hmac "$SECRET" -hex | awk '{print $NF}'; }

step "等待网关就绪"
for i in $(seq 1 30); do
  if curl -sf "$BASE_URL/healthz" >/dev/null; then ok "healthz 200"; break; fi
  [ "$i" = 30 ] && { bad "网关未就绪"; exit 1; }
  sleep 2
done

step "页面静态资源"
for page in "/" "/buyer.html" "/warehouse.html" "/refund.html" "/exception.html" "/ops.html"; do
  code="$(curl -s -o /dev/null -w '%{http_code}' "$BASE_URL$page")"
  [ "$code" = "200" ] && ok "$page 200" || bad "$page -> $code"
done

step "登录四类角色"
BUYER="$(login buyer1 buyer)";  ok "buyer1"
WHOUSE="$(login warehouse01 warehouse)"; ok "warehouse01"
CS="$(login support01 cs)";     ok "support01"
OPS="$(login ops01 ops)";       ok "ops01"

step "S1 正常成交：下单 → 支付 → 发货"
ORDER1="$(field "$(api POST /api/v1/trade/orders "$BUYER" '{"product_id":"P1","qty":1}')" '.data.id')"
ok "下单 $ORDER1"
TRADE1="CH-SMOKE-1-$STAMP"
SIG1="$(hmac "$TRADE1|$ORDER1|10000")"
RESP="$(signed_notify "$TRADE1" "$ORDER1" 10000 "$SIG1")"
[ "$(field "$RESP" '.data.success')" = "true" ] && ok "支付入账 success" || bad "支付失败 $(field "$RESP" '.data.code')"
STATUS="$(field "$(api GET "/api/v1/trade/orders/$ORDER1" "$BUYER")" '.data.status')"
[ "$STATUS" = "PAID" ] && ok "订单 PAID" || bad "订单状态 $STATUS"
SHIP="$(api POST "/api/v1/warehouse/orders/$ORDER1/ship" "$WHOUSE" '{"carrier":"顺丰速运","tracking_no":"SF-CI-001"}')"
[ "$(field "$SHIP" '.data.status')" = "SHIPPED" ] && ok "发货 SHIPPED" || bad "发货异常 $(field "$SHIP" '.error.code')"

step "支付幂等：同流水号重推"
RESP="$(signed_notify "$TRADE1" "$ORDER1" 10000 "$SIG1")"
[ "$(field "$RESP" '.data.duplicate')" = "true" ] && ok "duplicate=true 未重复记账" || bad "重推未幂等"

step "S3 售前退款：申请 → 首次渠道失败 → 重试 → 幂等"
ORDER2="$(field "$(api POST /api/v1/trade/orders "$BUYER" '{"product_id":"P1","qty":1}')" '.data.id')"
TRADE2="CH-SMOKE-2-$STAMP"
SIG2="$(hmac "$TRADE2|$ORDER2|10000")"
signed_notify "$TRADE2" "$ORDER2" 10000 "$SIG2" >/dev/null
api POST "/api/v1/trade/orders/$ORDER2/refund" "$BUYER" '{}' >/dev/null
FIRST="$(api POST "/api/v1/cs/refunds/$ORDER2/audit" "$CS" '{"approve":true,"reason":"同意退款"}')"
if [ -n "$(field "$FIRST" '.error.code')" ]; then
  [ "$(field "$FIRST" '.error.code')" = "channel_refund_failed" ] && ok "首次渠道退款失败（可重试）" || bad "首次审核 $(field "$FIRST" '.error.code')"
fi
RETRY="$(api POST "/api/v1/cs/refunds/$ORDER2/audit" "$CS" '{"approve":true,"reason":"渠道重试"}')"
[ "$(field "$RETRY" '.data.order.status')" = "REFUNDED" ] && ok "重试后 REFUNDED" || bad "重试状态 $(field "$RETRY" '.data.order.status')"
DUP="$(api POST "/api/v1/cs/refunds/$ORDER2/audit" "$CS" '{"approve":true,"reason":"再提交"}')"
[ "$(field "$DUP" '.data.duplicate')" = "true" ] && ok "重复审核 duplicate=true" || bad "重复审核未幂等"

step "S4 伪造签名 → 异常闭环"
BAD="$(signed_notify "CH-SMOKE-BAD-$STAMP" "$ORDER1" 1 "bad")"
[ "$(field "$BAD" '.data.code')" = "verify_failed" ] && ok "验签失败被拦截" || bad "验签未拦截"
EX="$(api GET '/api/v1/cs/exceptions?only_pending=true' "$CS")"
EX_ID="$(field "$EX" '.data.items[0].id')"
[ -n "$EX_ID" ] && [ "$EX_ID" != "null" ] && ok "待处理异常 $EX_ID" || bad "异常列表为空"
BLANK="$(api POST "/api/v1/cs/exceptions/$EX_ID/handle" "$CS" '{"action":"ignore","reason":" "}')"
[ "$(field "$BLANK" '.error.code')" = "reason_required" ] && ok "空原因被拒" || bad "空原因未被拒"
CLOSED="$(field "$(api POST "/api/v1/cs/exceptions/$EX_ID/handle" "$CS" '{"action":"ignore","reason":"渠道确认误报"}')" '.data.status')"
[ "$CLOSED" = "已闭环" ] && ok "处置闭环" || bad "处置状态 $CLOSED"

step "运营核查"
SUMMARY="$(api GET /api/v1/ops/summary "$OPS")"
ok "已收款=$(field "$SUMMARY" '.data.paid_total_cents') 已退款=$(field "$SUMMARY" '.data.refunded_total_cents') 可售=$(field "$SUMMARY" '.data.available_total')"
DENIED="$(api GET /api/v1/ops/summary "$BUYER")"
[ -n "$(field "$DENIED" '.error.code')" ] && ok "买家访问运营接口被拒 $(field "$DENIED" '.error.code')" || bad "越权未拦截"

echo ""
if [ "$FAIL" -eq 0 ]; then
  echo "HTTP 冒烟：全部通过（$PASS 项）"
  exit 0
else
  echo "HTTP 冒烟：$FAIL 项失败"
  exit 1
fi
