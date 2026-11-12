package entity

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

// NewTradeOrderID 生成订单号：SO + yyyyMMddHHmmss + 4 位随机十六进制。
func NewTradeOrderID() string {
	return "SO" + time.Now().Format("20060102150405") + randomHex(2)
}

// NewExceptionPayID 生成支付异常记录号。
func NewExceptionPayID() string {
	return "EX" + time.Now().Format("20060102150405") + randomHex(2)
}

func randomHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "0000"[:n*2]
	}
	return hex.EncodeToString(buf)
}
