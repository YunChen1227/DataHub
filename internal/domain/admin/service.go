// Package admin implements the admin console business logic (DESIGN §16):
// operator login (JWT), 普通用户 (license) CRUD, MD5 凭证生成与轮换,
// 审计查询, 以及全局/每用户 IP 白名单。v0.6 起无额度配置。
package admin

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/datahub/relay/internal/common/jwt"
	"github.com/datahub/relay/internal/domain/model"
	"github.com/datahub/relay/internal/domain/port"
)

var (
	ErrInvalidCredentials = errors.New("用户名或密码错误")
	ErrUserNotFound       = errors.New("用户不存在")
	ErrValidation         = errors.New("参数校验失败")
)

// Config holds admin session knobs.
type Config struct {
	JWTSecret string
	TokenTTL  time.Duration
}

// Service coordinates the admin repositories.
type Service struct {
	admins port.AdminUserRepository
	users  port.UserAdminRepository
	audits port.AuditRepository
	ips    port.GlobalIPRepository
	cfg    Config
}

func New(admins port.AdminUserRepository, users port.UserAdminRepository, audits port.AuditRepository, ips port.GlobalIPRepository, cfg Config) *Service {
	if cfg.TokenTTL <= 0 {
		cfg.TokenTTL = 8 * time.Hour
	}
	return &Service{admins: admins, users: users, audits: audits, ips: ips, cfg: cfg}
}

// --- §16.1 auth ---

// BootstrapAdmin creates the initial admin from config if it does not exist.
func (s *Service) BootstrapAdmin(ctx context.Context, username, password string) error {
	if username == "" || password == "" {
		return nil
	}
	existing, err := s.admins.FindAdmin(ctx, username)
	if err != nil {
		return err
	}
	if existing != nil {
		return nil
	}
	return s.admins.PutAdmin(ctx, &model.AdminUser{
		Username:     username,
		PasswordHash: HashPassword(password),
		Role:         "ADMIN",
		CreatedAt:    time.Now(),
	})
}

// Login verifies credentials and returns a signed JWT + expiry (unix seconds).
func (s *Service) Login(ctx context.Context, username, password string) (string, int64, error) {
	a, err := s.admins.FindAdmin(ctx, username)
	if err != nil {
		return "", 0, err
	}
	if a == nil || !VerifyPassword(password, a.PasswordHash) {
		return "", 0, ErrInvalidCredentials
	}
	return jwt.Sign(s.cfg.JWTSecret, a.Username, s.cfg.TokenTTL)
}

// VerifyToken validates a bearer token and returns the subject (username).
func (s *Service) VerifyToken(token string) (string, error) {
	c, err := jwt.Verify(s.cfg.JWTSecret, token)
	if err != nil {
		return "", err
	}
	return c.Sub, nil
}

// --- §16.2 user management ---

// CreateUserInput is the new-user payload from the admin UI.
type CreateUserInput struct {
	Name        string
	IPWhitelist []string
}

// CreateUserResult returns the created user plus the one-time plaintext secret.
type CreateUserResult struct {
	User   *model.UserDetail `json:"user"`
	Secret string            `json:"secret"` // 仅本次返回, 之后不可读
}

func (s *Service) ListUsers(ctx context.Context) ([]*model.UserDetail, error) {
	return s.users.ListUsers(ctx)
}

func (s *Service) GetUser(ctx context.Context, licenseID string) (*model.UserDetail, error) {
	u, err := s.users.GetUser(ctx, licenseID)
	if err != nil {
		return nil, err
	}
	if u == nil {
		return nil, ErrUserNotFound
	}
	return u, nil
}

func (s *Service) CreateUser(ctx context.Context, in CreateUserInput) (*CreateUserResult, error) {
	secret := GenerateSecret()
	detail := &model.UserDetail{
		LicenseID:   "LIC-" + strings.ToUpper(randAlpha(10)),
		AppKey:      GenerateAppKey(),
		Name:        strings.TrimSpace(in.Name),
		Status:      "ACTIVE",
		ClientUUID:  randAlpha(24),
		IPWhitelist: normalizeCIDRs(in.IPWhitelist),
		CreatedAt:   time.Now(),
	}
	if err := s.users.CreateUser(ctx, detail, secret); err != nil {
		return nil, err
	}
	return &CreateUserResult{User: detail, Secret: secret}, nil
}

// UpdateUserInput carries the editable fields (empty status = leave unchanged).
type UpdateUserInput struct {
	Status      string
	IPWhitelist []string
}

func (s *Service) UpdateUser(ctx context.Context, licenseID string, in UpdateUserInput) (*model.UserDetail, error) {
	cur, err := s.users.GetUser(ctx, licenseID)
	if err != nil {
		return nil, err
	}
	if cur == nil {
		return nil, ErrUserNotFound
	}
	status := in.Status
	if status == "" {
		status = cur.Status
	}
	if err := s.users.UpdateUser(ctx, licenseID, status, normalizeCIDRs(in.IPWhitelist)); err != nil {
		return nil, err
	}
	return s.users.GetUser(ctx, licenseID)
}

func (s *Service) DeleteUser(ctx context.Context, licenseID string) error {
	cur, err := s.users.GetUser(ctx, licenseID)
	if err != nil {
		return err
	}
	if cur == nil {
		return ErrUserNotFound
	}
	return s.users.DeleteUser(ctx, licenseID)
}

// RotateSecret regenerates the user's secret and returns the new plaintext once.
func (s *Service) RotateSecret(ctx context.Context, licenseID string) (string, error) {
	cur, err := s.users.GetUser(ctx, licenseID)
	if err != nil {
		return "", err
	}
	if cur == nil {
		return "", ErrUserNotFound
	}
	secret := GenerateSecret()
	if err := s.users.RotateSecret(ctx, licenseID, secret); err != nil {
		return "", err
	}
	return secret, nil
}

// --- §16.3 audits ---

func (s *Service) ListAudits(ctx context.Context, f model.AuditFilter) ([]*model.AuditRecord, error) {
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 100
	}
	return s.audits.ListAudits(ctx, f)
}

// --- §16.4 global IP whitelist ---

func (s *Service) GetGlobalIP(ctx context.Context) ([]string, error) {
	return s.ips.GetGlobalIP(ctx)
}

func (s *Service) SetGlobalIP(ctx context.Context, cidrs []string) error {
	return s.ips.SetGlobalIP(ctx, normalizeCIDRs(cidrs))
}

func normalizeCIDRs(in []string) []string {
	out := make([]string, 0, len(in))
	for _, c := range in {
		c = strings.TrimSpace(c)
		if c != "" {
			out = append(out, c)
		}
	}
	return out
}
