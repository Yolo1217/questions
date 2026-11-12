package entity

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

// NewTradeOrderID 生成订单号：SO + yyyyMMddHHmmss + 8 位随机十六进制。
// 随机位取 8 位（2^32）以避免同秒批量下单时生日碰撞导致主键冲突（偶发下单失败缺陷修复）。
func NewTradeOrderID() string {
	return "SO" + time.Now().Format("20060102150405") + randomHex(4)
}

// NewExceptionPayID 生成支付异常记录号（同样取 8 位随机十六进制防碰撞）。
func NewExceptionPayID() string {
	return "EX" + time.Now().Format("20060102150405") + randomHex(4)
}

func randomHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "0000"[:n*2]
	}
	return hex.EncodeToString(buf)
}
