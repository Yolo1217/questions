package entity

import (
	"errors"
	"strings"
	"time"
)

var (
	ErrUserNotFound         = errors.New("user not found")
	ErrAccountAlreadyExists = errors.New("account already exists")
	ErrInvalidCredentials   = errors.New("invalid credentials")
	ErrInvalidUser          = errors.New("invalid user")
)

// 角色常量：题目01 的四类使用方 + 模拟支付渠道。
const (
	RoleBuyer     = "buyer"
	RoleWarehouse = "warehouse"
	RoleCS        = "cs"
	RoleOps       = "ops"
)

// User 是 user 服务使用的用户聚合。
type User struct {
	ID           string
	Account      string
	DisplayName  string
	PasswordHash string
	PasswordSalt string
	Sex          bool
	Role         string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// NewUser 校验输入，并创建带规范化账号的用户聚合（默认买家角色）。
func NewUser(account string, displayName string, passwordHash string) (*User, error) {
	return NewUserWithRole(account, displayName, passwordHash, RoleBuyer)
}

// NewUserWithRole 创建带明确角色的用户聚合。
func NewUserWithRole(account string, displayName string, passwordHash string, role string) (*User, error) {
	account = NormalizeAccount(account)
	displayName = strings.TrimSpace(displayName)
	if account == "" || displayName == "" || passwordHash == "" {
		return nil, ErrInvalidUser
	}

	now := time.Now().UTC()
	return &User{
		Account:      account,
		DisplayName:  displayName,
		PasswordHash: passwordHash,
		Role:         NormalizeRole(role),
		CreatedAt:    now,
		UpdatedAt:    now,
	}, nil
}

// NormalizeRole 收敛角色取值，非法或空值一律降级为买家。
func NormalizeRole(role string) string {
	switch strings.TrimSpace(strings.ToLower(role)) {
	case RoleWarehouse:
		return RoleWarehouse
	case RoleCS:
		return RoleCS
	case RoleOps:
		return RoleOps
	default:
		return RoleBuyer
	}
}

// NormalizeAccount 去除账号首尾空白并转小写，用于唯一查询。
func NormalizeAccount(account string) string {
	return strings.TrimSpace(strings.ToLower(account))
}
