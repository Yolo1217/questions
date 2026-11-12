# 迟到支付场景冒烟（S4）：需以 TRADE_KEEP_SECONDS=3 启动 order 服务，便于观察超时关单。
# 用法： powershell -ExecutionPolicy Bypass -File scripts\smoke-late-pay.ps1
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
function Sign($tradeNo, $orderId, $amount) {
  $secret = 'shop-mock-channel-secret'
  if ($env:TRADE_CHANNEL_SECRET) { $secret = $env:TRADE_CHANNEL_SECRET }
  $mac = New-Object System.Security.Cryptography.HMACSHA256
  $mac.Key = $utf8.GetBytes($secret)
  $hash = $mac.ComputeHash($utf8.GetBytes("$tradeNo|$orderId|$amount"))
  return ([System.BitConverter]::ToString($hash) -replace '-', '').ToLower()
}

$buyer = Login 'buyer1'
$warehouse = Login 'warehouse01'
$cs = Login 'support01'
$ops = Login 'ops01'

Write-Host '=== 1. 下单后等待超时关闭（服务端时间判定） ==='
$placed = Call 'Post' '/trade/orders' $buyer.token @{ product_id = 'P1'; qty = 1 }
Write-Host ("  订单={0} 状态={1}" -f $placed.id, $placed.status)
$availableAfterReserve = (Call 'Get' '/products' $buyer.token).items | Where-Object { $_.id -eq 'P1' } | Select-Object -ExpandProperty available
Start-Sleep -Seconds 8
$closed = Call 'Get' "/trade/orders/$($placed.id)" $buyer.token
$availableAfterClose = (Call 'Get' '/products' $buyer.token).items | Where-Object { $_.id -eq 'P1' } | Select-Object -ExpandProperty available
Write-Host ("  超时后状态={0} 关闭原因={1} 可售 {2} -> {3}" -f $closed.status, $closed.closed_reason, $availableAfterReserve, $availableAfterClose)

Write-Host '=== 2. 成功支付通知迟到送达（签名正确） ==='
$tradeNo = "CH-LATE-$($placed.id)"
$notify = Call 'Post' '/pay/notify' $null @{
  channel_trade_no = $tradeNo; order_id = $placed.id; amount_cents = $placed.amount_cents; signature = (Sign $tradeNo $placed.id $placed.amount_cents)
}
Write-Host ("  success={0} message={1}" -f $notify.success, $notify.message)

Write-Host '=== 3. 客服异常列表 + 渠道流水号反查 ==='
$exceptions = Call 'Get' '/cs/exceptions' $cs.token
$target = $exceptions.items | Where-Object { $_.order_id -eq $placed.id } | Select-Object -First 1
Write-Host ("  异常={0} 类型={1} 渠道金额={2} 系统金额={3} 状态={4}" -f $target.id, $target.type, $target.channel_amount_cents, $target.system_amount_cents, $target.status)
$callback = Call 'Get' "/cs/callbacks/$tradeNo" $cs.token
Write-Host ("  反查回调：状态={0} 关联异常={1}/{2}" -f $callback.status, $callback.exception_type, $callback.exception_status)

Write-Host '=== 4. 客服处置：空原因被拒 → 填写原因后原路退款 ==='
try {
  Call 'Post' "/cs/exceptions/$($target.id)/handle" $cs.token @{ action = 'verify'; reason = '' } | Out-Null
  Write-Host '  异常：空原因未被拒绝'
} catch {
  $err = $_.ErrorDetails.Message | ConvertFrom-Json
  Write-Host ("  空原因被拒绝：{0} / {1}" -f $err.error.code, $err.error.message)
}
$handled = Call 'Post' "/cs/exceptions/$($target.id)/handle" $cs.token @{ action = 'refund'; reason = '核实渠道已收款且订单已关闭' }
Write-Host ("  处置结果={0} 处理人={1} 说明={2}" -f $handled.status, $handled.handler, $handled.result)

Write-Host '=== 5. 订单仍为 CLOSED，库存不重复释放，异常留痕在订单台账 ==='
$after = Call 'Get' "/trade/orders/$($placed.id)" $buyer.token
$availableAfterRefund = (Call 'Get' '/products' $buyer.token).items | Where-Object { $_.id -eq 'P1' } | Select-Object -ExpandProperty available
Write-Host ("  订单状态={0} 可售={1}（退款前后应一致）" -f $after.status, $availableAfterRefund)
foreach ($l in $after.ledgers) { Write-Host ("  [{0}] {1} {2}" -f $l.kind, $l.time, $l.text) }

Write-Host '=== 6. 正常成交一笔并验证中文承运方落库 ==='
$fresh = Call 'Post' '/trade/orders' $buyer.token @{ product_id = 'P2'; qty = 1 }
$freshNo = "CH-OK-$($fresh.id)"
Call 'Post' '/pay/notify' $null @{
  channel_trade_no = $freshNo; order_id = $fresh.id; amount_cents = $fresh.amount_cents; signature = (Sign $freshNo $fresh.id $fresh.amount_cents)
} | Out-Null
$shipped = Call 'Post' "/warehouse/orders/$($fresh.id)/ship" $warehouse.token @{ carrier = '顺丰速运'; tracking_no = 'SF20261112' }
Write-Host ("  发货状态={0} 承运方={1} 运单={2}" -f $shipped.status, $shipped.carrier, $shipped.tracking_no)

Write-Host '=== 7. 运营汇总（可售 + 预占 对账） ==='
$summary = Call 'Get' '/ops/summary' $ops.token
Write-Host ("  已收款={0}分 已退款={1}分 可售={2} 预占中={3} 异常待处理={4}" -f `
    $summary.paid_total_cents, $summary.refunded_total_cents, $summary.available_total, [int]$summary.reserved_total, [int]$summary.exception_pending)

