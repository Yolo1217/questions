# 演示数据播种：构造六种订单状态 + 三类支付异常的稳定样例，供五个页面演示与录屏。
# 依赖与 smoke-pages.ps1 相同（三进程 + MySQL），可重复执行（每次用新流水号）。
param(
    [string]$BaseUrl = "http://127.0.0.1:8080"
)

$ErrorActionPreference = "Stop"
[Console]::OutputEncoding = [Text.Encoding]::UTF8
$stamp = Get-Date -Format "MMddHHmmss"

function Send-Json($method, $path, $headers, $body) {
    $params = @{ Uri = "$BaseUrl$path"; Method = $method; Headers = $headers; UseBasicParsing = $true }
    if ($null -ne $body) {
        $params.ContentType = "application/json; charset=utf-8"
        $params.Body = [Text.Encoding]::UTF8.GetBytes(($body | ConvertTo-Json -Compress))
    }
    try {
        $resp = Invoke-WebRequest @params
        $text = [Text.Encoding]::UTF8.GetString($resp.RawContentStream.ToArray())
        return ($text | ConvertFrom-Json)
    } catch {
        $raw = $_.ErrorDetails.Message
        if ($raw) { try { return ($raw | ConvertFrom-Json) } catch { return @{ error = @{ code = "unparsable"; message = $raw } } } }
        throw
    }
}

function Login($account) {
    $resp = Send-Json "POST" "/api/v1/users/login" @{} @{ account = $account; password = "123456" }
    return @{ Authorization = "Bearer $($resp.data.token)" }
}

function Sign($tradeNo, $orderId, $amount) {
    # 与服务端一致：hex(HMAC-SHA256(secret, "trade_no|order_id|amount"))
    $secret = if ($env:TRADE_CHANNEL_SECRET) { $env:TRADE_CHANNEL_SECRET } else { "shop-mock-channel-secret" }
    $mac = New-Object System.Security.Cryptography.HMACSHA256
    $mac.Key = [Text.Encoding]::UTF8.GetBytes($secret)
    $payload = "$tradeNo|$orderId|$amount"
    $hash = $mac.ComputeHash([Text.Encoding]::UTF8.GetBytes($payload))
    return ($hash | ForEach-Object { $_.ToString("x2") }) -join ""
}

$ops = Login "ops01"
$buyer = Login "buyer1"
$buyer2 = Login "buyer2"
$wh = Login "warehouse01"
$cs = Login "support01"

Write-Host "播种商品..."
foreach ($p in @(
    @{ id = "P1"; name = "羊绒围巾 · 燕麦色"; price_cents = 10000; available = 12; on_sale = $true },
    @{ id = "P2"; name = "手作陶瓷马克杯"; price_cents = 6800; available = 8; on_sale = $true },
    @{ id = "P3"; name = "便携折叠伞 · 雾蓝"; price_cents = 5600; available = 6; on_sale = $true },
    @{ id = "P4"; name = "限量香薰礼盒（已下架）"; price_cents = 19900; available = 3; on_sale = $false }
)) {
    $null = Send-Json "POST" "/api/v1/products" $ops $p
}

function New-Order($headers, $productId, $qty) {
    $resp = Send-Json "POST" "/api/v1/trade/orders" $headers @{ product_id = $productId; qty = $qty }
    if ($resp.error) { throw "下单失败：$($resp.error.code)" }
    return $resp.data
}

function Pay($headers, $order, $suffix) {
    $tradeNo = "CH-DEMO-$suffix-$stamp"
    $null = Send-Json "POST" "/api/v1/pay/notify" @{} @{
        channel_trade_no = $tradeNo
        order_id         = $order.id
        amount_cents     = $order.amount_cents
        signature        = (Sign $tradeNo $order.id $order.amount_cents)
    }
    return $tradeNo
}

Write-Host "1) 待支付（买家随后可点取消/模拟支付）..."
$created = New-Order $buyer "P2" 1

Write-Host "2) 已支付待发货（仓库页可见）..."
$paid = New-Order $buyer2 "P1" 1
$null = Pay $buyer2 $paid "PAID"

Write-Host "3) 已发货（带物流信息）..."
$shipped = New-Order $buyer "P1" 1
$null = Pay $buyer $shipped "SHIPPED"
$null = Send-Json "POST" "/api/v1/warehouse/orders/$($shipped.id)/ship" $wh @{ carrier = "顺丰速运"; tracking_no = "SF-$stamp-01" }

Write-Host "4) 退款审核中（客服退款页可见）..."
$refunding = New-Order $buyer "P2" 2
$null = Pay $buyer $refunding "REFUNDING"
$null = Send-Json "POST" "/api/v1/trade/orders/$($refunding.id)/refund" $buyer @{}

Write-Host "5) 已退款（渠道首次失败后重试成功）..."
$refunded = New-Order $buyer "P1" 1
$null = Pay $buyer $refunded "REFUNDED"
$null = Send-Json "POST" "/api/v1/trade/orders/$($refunded.id)/refund" $buyer @{}
$null = Send-Json "POST" "/api/v1/cs/refunds/$($refunded.id)/audit" $cs @{ approve = $true; reason = "同意退款" }
$null = Send-Json "POST" "/api/v1/cs/refunds/$($refunded.id)/audit" $cs @{ approve = $true; reason = "渠道重试" }

Write-Host "6) 已关闭（买家取消）..."
$closed = New-Order $buyer2 "P2" 1
$null = Send-Json "POST" "/api/v1/trade/orders/$($closed.id)/cancel" $buyer2 @{}

Write-Host "异常 A：验签失败（伪造签名）..."
$null = Send-Json "POST" "/api/v1/pay/notify" @{} @{
    channel_trade_no = "CH-DEMO-BADSIG-$stamp"; order_id = $paid.id; amount_cents = 100; signature = "bad"
}

Write-Host "异常 B：金额不符（签名正确、金额少 1 元）..."
$misTradeNo = "CH-DEMO-MISMATCH-$stamp"
$null = Send-Json "POST" "/api/v1/pay/notify" @{} @{
    channel_trade_no = $misTradeNo
    order_id         = $paid.id
    amount_cents     = ($paid.amount_cents - 100)
    signature        = (Sign $misTradeNo $paid.id ($paid.amount_cents - 100))
}

Write-Host "异常 C：无主支付（订单号不存在）..."
$orphanTradeNo = "CH-DEMO-ORPHAN-$stamp"
$null = Send-Json "POST" "/api/v1/pay/notify" @{} @{
    channel_trade_no = $orphanTradeNo
    order_id         = "SO-DOES-NOT-EXIST"
    amount_cents     = 9900
    signature        = (Sign $orphanTradeNo "SO-DOES-NOT-EXIST" 9900)
}

Write-Host ""
Write-Host "演示数据就绪："
Write-Host "  待支付   = $($created.id)"
Write-Host "  待发货   = $($paid.id)"
Write-Host "  已发货   = $($shipped.id)（顺丰 SF-$stamp-01）"
Write-Host "  退款审核 = $($refunding.id)"
Write-Host "  已退款   = $($refunded.id)"
Write-Host "  已关闭   = $($closed.id)"
Write-Host "  异常 3 条：验签失败 / 金额不符($misTradeNo) / 无主支付($orphanTradeNo)"
