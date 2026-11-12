package service

import (
	"context"
	"errors"
	"strings"

	"github.com/BwCloudWeGo/bw-cli/internal/user/dto"
	"github.com/BwCloudWeGo/bw-cli/internal/user/entity"
)

// PasswordHasher 对业务用例隐藏密码哈希实现细节。
type PasswordHasher interface {
	Hash(password string) (string, error)
	Verify(hash string, password string) bool
}

// SeedUserCommand 暴露预置账号入参，便于 cmd 层直接构造。
type SeedUserCommand = dto.SeedUserCommand

// Service 编排 user 用例。
type Service struct {
	repo   entity.Repository
	hasher PasswordHasher
}

// NewService 创建 user 用例服务。
func NewService(repo entity.Repository, hasher PasswordHasher) *Service {
	return &Service{repo: repo, hasher: hasher}
}

// Register 在检查账号唯一性后创建新用户。
func (s *Service) Register(ctx context.Context, cmd dto.RegisterCommand) (*dto.UserDTO, error) {
	if strings.TrimSpace(cmd.Password) == "" {
		return nil, entity.ErrInvalidUser
	}
	account := entity.NormalizeAccount(cmd.Account)
	if _, err := s.repo.FindByAccount(ctx, account); err == nil {
		return nil, entity.ErrAccountAlreadyExists
	} else if !errors.Is(err, entity.ErrUserNotFound) {
		return nil, err
	}

	hash, err := s.hasher.Hash(cmd.Password)
	if err != nil {
		return nil, err
	}
	user, err := entity.NewUserWithRole(account, cmd.DisplayName, hash, cmd.Role)
	if err != nil {
		return nil, err
	}
	if err := s.repo.Save(ctx, user); err != nil {
		return nil, err
	}
	return dto.FromUser(user), nil
}

// SeedUsers 幂等预置账号：已存在则跳过，返回本次新建数量。
func (s *Service) SeedUsers(ctx context.Context, seeds []dto.SeedUserCommand) (int, error) {
	created := 0
	for _, seed := range seeds {
		account := entity.NormalizeAccount(seed.Account)
		if _, err := s.repo.FindByAccount(ctx, account); err == nil {
			continue
		} else if !errors.Is(err, entity.ErrUserNotFound) {
			return created, err
		}
		hash, err := s.hasher.Hash(seed.Password)
		if err != nil {
			return created, err
		}
		user, err := entity.NewUserWithRole(account, seed.DisplayName, hash, seed.Role)
		if err != nil {
			return created, err
		}
		if err := s.repo.Save(ctx, user); err != nil {
			return created, err
		}
		created++
	}
	return created, nil
}

// Login 校验凭证并返回匹配用户。
func (s *Service) Login(ctx context.Context, cmd dto.LoginCommand) (*dto.UserDTO, error) {
	user, err := s.repo.FindByAccount(ctx, entity.NormalizeAccount(cmd.Account))
	if err != nil {
		if errors.Is(err, entity.ErrUserNotFound) {
			return nil, entity.ErrInvalidCredentials
		}
		return nil, err
	}
	if !s.hasher.Verify(user.PasswordHash, cmd.Password) {
		return nil, entity.ErrInvalidCredentials
	}
	return dto.FromUser(user), nil
}

// GetUser 根据 ID 返回一个用户。
func (s *Service) GetUser(ctx context.Context, id string) (*dto.UserDTO, error) {
	user, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	return dto.FromUser(user), nil
}
