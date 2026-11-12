# 题目01 交易主链路冒烟脚本：需先启动 user / order / gateway 三个服务。
# 用法： powershell -ExecutionPolicy Bypass -File scripts\smoke-trade.ps1
$ErrorActionPreference = 'Stop'
$base = 'http://127.0.0.1:8080/api/v1'
$utf8 = [System.Text.Encoding]::UTF8

function Call($method, $path, $token, $bodyObj) {
  $headers = @{}
  if ($token) { $headers['Authorization'] = "Bearer $token" }
  $params = @{ Method = $method; Uri = "$base$path"; Headers = $headers }
  if ($bodyObj) {
    $json = $bodyObj | ConvertTo-Json -Compress
    $params['ContentType'] = 'application/json; charset=utf-8'
    $params['Body'] = $utf8.GetBytes($json)
  }
  return (Invoke-RestMethod @params).data
}
function Login($account) { return Call 'Post' '/users/login' $null @{ account = $account; password = '123456' } }

# 模拟渠道侧签名：HMAC-SHA256(secret, "trade_no|order_id|amount") 十六进制小写
function Sign($tradeNo, $orderId, $amount) {
  $secret = 'shop-mock-channel-secret'
  if ($env:TRADE_CHANNEL_SECRET) { $secret = $env:TRADE_CHANNEL_SECRET }
  $mac = New-Object System.Security.Cryptography.HMACSHA256
  $mac.Key = $utf8.GetBytes($secret)
  $hash = $mac.ComputeHash($utf8.GetBytes("$tradeNo|$orderId|$amount"))
  return ([System.BitConverter]::ToString($hash) -replace '-', '').ToLower()
}
function Notify($tradeNo, $orderId, $amount, $signature) {
  $body = @{ channel_trade_no = $tradeNo; order_id = $orderId; amount_cents = $amount }
  if ($signature) { $body['signature'] = $signature }
  return Call 'Post' '/pay/notify' $null $body
}

Write-Host '=== 1. 登录四个角色 ==='
$buyer = Login 'buyer1'
$warehouse = Login 'warehouse01'
$cs = Login 'support01'
$ops = Login 'ops01'
Write-Host ("  买家={0}({1}) 仓库={2}({3}) 客服={4}({5}) 运营={6}({7})" -f `
    $buyer.user.display_name, $buyer.user.role, $warehouse.user.display_name, $warehouse.user.role, `
    $cs.user.display_name, $cs.user.role, $ops.user.display_name, $ops.user.role)

Write-Host '=== 2. 商品列表 ==='
$products = Call 'Get' '/products' $buyer.token
foreach ($p in $products.items) {
  Write-Host ("  {0} {1} 单价={2}分 可售={3}/{4} 在售={5}" -f $p.id, $p.name, $p.price_cents, $p.available, $p.total, $p.on_sale)
}

Write-Host '=== 3. 买家下单 ==='
$placed = Call 'Post' '/trade/orders' $buyer.token @{ product_id = 'P1'; qty = 1 }
$orderId = $placed.id
Write-Host ("  订单={0} 状态={1} 应付={2}分" -f $orderId, $placed.status, $placed.amount_cents)

Write-Host '=== 4. 支付通知：错误签名 / 正确签名重复推送 3 次 ==='
$forged = Notify "CH-FORGED-$orderId" $orderId $placed.amount_cents 'deadbeef'
Write-Host ("  伪造签名 -> success={0} code={1} {2}" -f [bool]$forged.success, $forged.code, $forged.message)
$tradeNo = "CH-$orderId"
$sig = Sign $tradeNo $orderId $placed.amount_cents
for ($i = 1; $i -le 3; $i++) {
  $notify = Notify $tradeNo $orderId $placed.amount_cents $sig
  Write-Host ("  第{0}次 -> success={1} code={2} {3}" -f $i, $notify.success, $notify.code, $notify.message)
}

Write-Host '=== 5. 用渠道流水号反查处理结果 ==='
$callback = Call 'Get' "/cs/callbacks/$tradeNo" $cs.token
Write-Host ("  状态={0} 命中={1} 关联异常={2}" -f $callback.status, $callback.hits, $callback.exception_type)
$forgedCallback = Call 'Get' "/cs/callbacks/CH-FORGED-$orderId" $cs.token
Write-Host ("  伪造签名的回调 -> 状态={0} 异常={1}" -f $forgedCallback.status, $forgedCallback.exception_type)

Write-Host '=== 6. 买家申请售前退款 ==='
$refunding = Call 'Post' "/trade/orders/$orderId/refund" $buyer.token $null
Write-Host ("  退款申请后状态={0}" -f $refunding.status)

Write-Host '=== 7. 仓库尝试发货（应被拒绝） ==='
try {
  Call 'Post' "/warehouse/orders/$orderId/ship" $warehouse.token @{ carrier = '顺丰速运'; tracking_no = 'SF999' } | Out-Null
  Write-Host '  异常：审核中订单竟可发货'
} catch {
  $err = $_.ErrorDetails.Message | ConvertFrom-Json
  Write-Host ("  发货被拒绝：{0} / {1}" -f $err.error.code, $err.error.message)
}

Write-Host '=== 8. 客服同意退款（渠道首次失败 → 重试成功 → 重复提交幂等） ==='
try {
  Call 'Post' "/cs/refunds/$orderId/audit" $cs.token @{ approve = $true } | Out-Null
  Write-Host '  首次退款意外成功（检查 TRADE_REFUND_FAIL_FIRST 配置）'
} catch {
  $err = $_.ErrorDetails.Message | ConvertFrom-Json
  Write-Host ("  首次退款失败：{0}" -f $err.error.message)
}
$refunded = Call 'Post' "/cs/refunds/$orderId/audit" $cs.token @{ approve = $true }
Write-Host ("  重试后状态={0} duplicate={1} {2}" -f $refunded.order.status, [bool]$refunded.duplicate, $refunded.message)
$again = Call 'Post' "/cs/refunds/$orderId/audit" $cs.token @{ approve = $true }
Write-Host ("  重复提交 -> 状态={0} duplicate={1} {2}" -f $again.order.status, [bool]$again.duplicate, $again.message)

Write-Host '=== 9. 运营核查 ==='
$summary = Call 'Get' '/ops/summary' $ops.token
Write-Host ("  已收款={0}分 已退款={1}分 可售={2} 预占中={3} 异常待处理={4}" -f `
    $summary.paid_total_cents, $summary.refunded_total_cents, $summary.available_total, [int]$summary.reserved_total, [int]$summary.exception_pending)

Write-Host '=== 10. 越权校验 ==='
try {
  Call 'Get' '/ops/summary' $warehouse.token | Out-Null
  Write-Host '  异常：仓库账号未被拒绝'
} catch {
  Write-Host ("  仓库访问运营接口被拒绝 HTTP {0}" -f [int]$_.Exception.Response.StatusCode)
}


