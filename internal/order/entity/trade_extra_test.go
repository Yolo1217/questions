package entity

import (
	"strings"
	"testing"
)

// NewExceptionID 序列号必须补齐到 6 位，超出位数只保留末 6 位。
func TestNewExceptionIDPadsSequence(t *testing.T) {
	id := NewExceptionID(42)
	if !strings.HasPrefix(id, "EX") {
		t.Fatalf("exception id must start with EX, got %s", id)
	}
	if !strings.HasSuffix(id, "-000042") {
		t.Fatalf("sequence 42 must be padded to 000042, got %s", id)
	}
	wrapped := NewExceptionID(1234567)
	if !strings.HasSuffix(wrapped, "-234567") {
		t.Fatalf("overflow sequence must keep last 6 digits, got %s", wrapped)
	}
}
