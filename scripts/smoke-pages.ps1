# 五个工作台页面的端到端冒烟：按页面实际操作顺序调用其接口。
# 前置：user(9001) / order(9100) / gateway(8080) 三进程已启动，MySQL 可用。
param(
    [string]$BaseUrl = "http://127.0.0.1:8080"
)

$ErrorActionPreference = "Stop"
$script:failed = 0
# 每次运行使用唯一渠道流水号，避免与历史回调/异常记录幂等碰撞
$stamp = Get-Date -Format "MMddHHmmss"
$dupTradeNo = "CH-PAGE-DUP-$stamp"
$badTradeNo = "CH-PAGE-BAD-$stamp"
[Console]::OutputEncoding = [Text.Encoding]::UTF8

function Step($name) { Write-Host "`n[STEP] $name" -ForegroundColor Cyan }
function Ok($msg) { Write-Host "  PASS  $msg" -ForegroundColor Green }
function Bad($msg) { Write-Host "  FAIL  $msg" -ForegroundColor Red; $script:failed++ }

# Send-Json 以 UTF-8 字节发送 JSON 体，并按 UTF-8 解码响应，避免中文乱码。
function Send-Json($method, $path, $headers, $body) {
    $params = @{
        Uri             = "$BaseUrl$path"
        Method          = $method
        Headers         = $headers
        UseBasicParsing = $true
    }
    if ($null -ne $body) {
        $json = $body | ConvertTo-Json -Compress
        $params.ContentType = "application/json; charset=utf-8"
        $params.Body = [Text.Encoding]::UTF8.GetBytes($json)
    }
    try {
        $resp = Invoke-WebRequest @params
        $text = [Text.Encoding]::UTF8.GetString($resp.RawContentStream.ToArray())
        return ($text | ConvertFrom-Json)
    } catch {
        $raw = $_.ErrorDetails.Message
        if ($raw) {
            try { return ($raw | ConvertFrom-Json) }
            catch { return @{ error = @{ code = "unparsable"; message = $raw } } }
        }
        throw
    }
}

function Login($account, $role) {
    $resp = Send-Json "POST" "/api/v1/users/login" @{} @{ account = $account; password = "123456" }
    if ($resp.data.user.role -ne $role) { throw "账号 $account 角色不是 $role" }
    return @{ Authorization = "Bearer $($resp.data.token)" }
}

function Get-Api($path, $headers) {
    $resp = Send-Json "GET" $path $headers $null
    # 成功响应形如 { request_id, data }；失败响应形如 { error: { code, message } }
    if ($resp.error) { throw "GET $path -> $($resp.error.code): $($resp.error.message)" }
    return $resp.data
}

function Post-Api($path, $headers, $body) {
    return (Send-Json "POST" $path $headers $body)
}

Step "页面静态资源可访问"
foreach ($page in @("/", "/buyer.html", "/warehouse.html", "/refund.html", "/exception.html", "/ops.html", "/assets/app.css", "/assets/app.js")) {
    $r = Invoke-WebRequest -Uri "$BaseUrl$page" -UseBasicParsing -TimeoutSec 5
    if ($r.StatusCode -eq 200) { Ok "$page 200（$($r.RawContentLength) 字节）" } else { Bad "$page -> $($r.StatusCode)" }
}

Step "准备：运营重置演示库存"
$opsSetup = Login "ops01" "ops"
$null = Post-Api "/api/v1/products" $opsSetup @{ id = "P1"; name = "羊绒围巾 · 燕麦色"; price_cents = 10000; available = 5; on_sale = $true }
$null = Post-Api "/api/v1/products" $opsSetup @{ id = "P2"; name = "手作陶瓷马克杯"; price_cents = 6800; available = 6; on_sale = $true }
Ok "P1 可售 5 / P2 可售 6 已就绪"

Step "① 买家页：登录 / 商品 / 下单 / 模拟支付 / 详情"
$buyer = Login "buyer1" "buyer"
$products = Get-Api "/api/v1/products" $buyer
Ok "商品 $($products.items.Count) 个，首个 $($products.items[0].id) 可售 $($products.items[0].available)"

$placed = (Post-Api "/api/v1/trade/orders" $buyer @{ product_id = "P1"; qty = 1 }).data
Ok "下单 $($placed.id) 状态=$($placed.status) 金额=$($placed.amount_cents)"

$pay = (Post-Api "/api/v1/pay/mock" $buyer @{ order_id = $placed.id; channel_trade_no = $dupTradeNo }).data
if ($pay.success) { Ok "模拟渠道支付 success code=$($pay.code)" } else { Bad "模拟支付失败 code=$($pay.code)" }

$again = (Post-Api "/api/v1/pay/mock" $buyer @{ order_id = $placed.id; channel_trade_no = $dupTradeNo }).data
if ($again.duplicate) { Ok "重复支付幂等 duplicate=true，未重复记账" } else { Bad "重复支付未返回 duplicate" }

$detail = Get-Api "/api/v1/trade/orders/$($placed.id)" $buyer
Ok "详情状态=$($detail.status) 台账 $($detail.ledgers.Count) 条"

Step "② 仓库页：待发货列表 / 发货登记"
$warehouse = Login "warehouse01" "warehouse"
$pending = Get-Api "/api/v1/warehouse/orders" $warehouse
if ($pending.items.Count -ge 1) { Ok "待发货 $($pending.items.Count) 单" } else { Bad "待发货列表为空" }

$shipped = (Post-Api "/api/v1/warehouse/orders/$($placed.id)/ship" $warehouse @{ carrier = "顺丰速运"; tracking_no = "SF-PAGE-001" }).data
if ($shipped.status -eq "SHIPPED") { Ok "发货成功 $($shipped.carrier) / $($shipped.tracking_no)" } else { Bad "发货状态异常 $($shipped.status)" }

Step "③ 客服退款页：申请 / 审核（首次失败可重试）/ 幂等"
$order2 = (Post-Api "/api/v1/trade/orders" $buyer @{ product_id = "P1"; qty = 1 }).data
$null = Post-Api "/api/v1/pay/mock" $buyer @{ order_id = $order2.id }
$null = Post-Api "/api/v1/trade/orders/$($order2.id)/refund" $buyer @{}
$cs = Login "support01" "cs"
$refunds = Get-Api "/api/v1/cs/refunds" $cs
if ($refunds.items.Count -ge 1) { Ok "退款待审 $($refunds.items.Count) 单" } else { Bad "退款待审列表为空" }

$first = Post-Api "/api/v1/cs/refunds/$($order2.id)/audit" $cs @{ approve = $true; reason = "同意退款" }
if ($first.error) {
    if ($first.error.code -eq "channel_refund_failed") { Ok "首次渠道退款失败 code=channel_refund_failed（订单仍退款审核中）" }
    else { Bad "首次审核异常：$($first.error.code)" }
} else {
    $state = $first.data.order.status
    if ($state -eq "REFUNDED") { Ok "首次退款即成功 REFUNDED" } else { Bad "首次退款状态 $state" }
}

$retry = Post-Api "/api/v1/cs/refunds/$($order2.id)/audit" $cs @{ approve = $true; reason = "重试退款" }
if ($retry.error) {
    Bad "重试失败：$($retry.error.code)"
} elseif ($retry.data.order.status -eq "REFUNDED") {
    # 重试路径是新的渠道调用，不携带重复标记；重复性由下一步的同状态再提交验证
    Ok "重试后 REFUNDED（重试为全新渠道调用，无重复标记）"
} else {
    Bad "重试后状态 $($retry.data.order.status)"
}

$dup = Post-Api "/api/v1/cs/refunds/$($order2.id)/audit" $cs @{ approve = $true; reason = "再提交" }
if ($dup.data.duplicate) { Ok "重复审核幂等 duplicate=true，未重复打款" } else { Bad "重复审核未返回 duplicate" }

Step "④ 客服异常页：伪造签名 / 处置 / 回调反查"
$forged = (Post-Api "/api/v1/pay/notify" @{} @{ channel_trade_no = $badTradeNo; order_id = $order2.id; amount_cents = 1; signature = "bad" }).data
if (-not $forged.success -and $forged.code -eq "verify_failed") { Ok "验签失败 success=False code=verify_failed" } else { Bad "验签未拦截 $($forged.code)" }

$exceptions = Get-Api "/api/v1/cs/exceptions?only_pending=true" $cs
if ($exceptions.items.Count -ge 1) { Ok "待处理异常 $($exceptions.items.Count) 条（类型 $($exceptions.items[0].type)）" } else { Bad "异常列表为空" }
$target = $exceptions.items[0]

$blank = Post-Api "/api/v1/cs/exceptions/$($target.id)/handle" $cs @{ action = "ignore"; reason = " " }
if ($blank.error) { Ok "空原因被拒 code=$($blank.error.code)" } else { Bad "空原因未被拒" }

$closed = (Post-Api "/api/v1/cs/exceptions/$($target.id)/handle" $cs @{ action = "ignore"; reason = "渠道确认误报" }).data
if ($closed.status -eq "已闭环") { Ok "处置闭环：$($closed.result)" } else { Bad "处置状态 $($closed.status)" }

$callback = Get-Api "/api/v1/cs/callbacks/$badTradeNo" $cs
Ok "回调反查 status=$($callback.status) 命中=$($callback.hits) 异常=$($callback.exception_type)/$($callback.exception_status)"

Step "⑤ 运营页：汇总 / 商品维护 / 订单检索 / 回调列表"
$ops = Login "ops01" "ops"
$summary = Get-Api "/api/v1/ops/summary" $ops
# protobuf JSON 省略零值字段，缺失一律按 0 展示
function Nz($v) { if ($null -ne $v) { $v } else { 0 } }
Ok "已收款=$(Nz $summary.paid_total_cents) 已退款=$(Nz $summary.refunded_total_cents) 可售=$(Nz $summary.available_total) 预占=$(Nz $summary.reserved_total) 待处理异常=$(Nz $summary.exception_pending)"

$saved = (Post-Api "/api/v1/products" $ops @{ id = "P3"; name = "页面演示新品"; price_cents = 6600; available = 4; on_sale = $true }).data
Ok "商品维护 $($saved.id) 可售=$($saved.available) 在售=$($saved.on_sale)"

$offsale = (Post-Api "/api/v1/products" $ops @{ id = "P3"; name = "页面演示新品"; price_cents = 6600; available = 4; on_sale = $false }).data
if (-not $offsale.on_sale) { Ok "下架生效 on_sale=false" } else { Bad "下架未生效" }

$allOrders = Get-Api "/api/v1/ops/orders" $ops
Ok "运营订单检索 $($allOrders.total) 单"
$callbacks = Get-Api "/api/v1/ops/callbacks?keyword=CH-PAGE" $ops
Ok "运营回调检索 $($callbacks.items.Count) 条"

Step "越权校验"
$denied = Send-Json "GET" "/api/v1/ops/summary" $buyer $null
if ($denied.error) { Ok "买家访问 /ops/summary 被拒 code=$($denied.error.code)" } else { Bad "买家访问运营接口未被拦截" }

Write-Host ""
if ($script:failed -eq 0) {
    Write-Host "页面端到端冒烟：全部通过" -ForegroundColor Green
    exit 0
} else {
    Write-Host "页面端到端冒烟：$($script:failed) 项失败" -ForegroundColor Red
    exit 1
}
