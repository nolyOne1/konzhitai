package auth

import (
	"context"
	"errors"
	"net/mail"
	"sort"
	"strings"
	"time"
)

var (
	ErrInvalidRoles        = errors.New("成员角色配置无效")
	ErrMemberNotFound      = errors.New("成员不存在")
	ErrInvalidMember       = errors.New("成员信息无效")
	ErrDuplicateEmail      = errors.New("成员邮箱已存在")
	ErrMemberStateConflict = errors.New("成员状态冲突")
	ErrCannotModifySelf    = errors.New("不能修改当前成员")
	ErrLastAdmin           = errors.New("不能修改最后一名管理员")
)

type Member struct {
	ID                 string     `json:"id"`
	Email              string     `json:"email"`
	DisplayName        string     `json:"displayName"`
	Enabled            bool       `json:"enabled"`
	Roles              []RoleName `json:"roles"`
	MustChangePassword bool       `json:"mustChangePassword"`
	RemovedAt          *time.Time `json:"removedAt"`
	CreatedAt          time.Time  `json:"createdAt"`
}

type CreateMemberInput struct { Email, DisplayName string; Roles []RoleName }
type CreateMemberResult struct { Member Member `json:"member"`; TemporaryPassword string `json:"temporaryPassword"` }
type ResetPasswordResult struct { Member Member `json:"member"`; TemporaryPassword string `json:"temporaryPassword"` }
type CreateMemberRecord struct { Email, DisplayName, PasswordHash string; Roles []RoleName }

type TeamRepository interface {
	ListMembers(context.Context, MemberStatus) ([]Member, error)
	CreateMember(context.Context, string, CreateMemberRecord) (Member, error)
	ReplaceMemberRoles(context.Context, string, string, []RoleName) (Member, error)
	SetMemberEnabled(context.Context, string, string, bool) (Member, error)
	RemoveMember(context.Context, string, string) (Member, error)
	RestoreMember(context.Context, string, string) (Member, error)
	ResetMemberPassword(context.Context, string, string, string) (Member, error)
}

type TeamService struct{ repository TeamRepository }

func NewTeamService(repository TeamRepository) *TeamService { return &TeamService{repository: repository} }

func (s *TeamService) List(ctx context.Context, status MemberStatus) ([]Member, error) {
	if s == nil || s.repository == nil || !validMemberStatus(status) { return nil, ErrInvalidMember }
	members, err := s.repository.ListMembers(ctx, status)
	if members == nil && err == nil { members = []Member{} }
	return members, err
}

func (s *TeamService) Create(ctx context.Context, actorID string, input CreateMemberInput) (CreateMemberResult, error) {
	actorID = strings.TrimSpace(actorID)
	if s == nil || s.repository == nil || actorID == "" { return CreateMemberResult{}, ErrInvalidMember }
	input.Email, input.DisplayName = strings.ToLower(strings.TrimSpace(input.Email)), strings.TrimSpace(input.DisplayName)
	if !validEmail(input.Email) || input.DisplayName == "" { return CreateMemberResult{}, ErrInvalidMember }
	roles, err := normalizeRoles(input.Roles)
	if err != nil { return CreateMemberResult{}, ErrInvalidMember }
	temporaryPassword, err := randomToken(18)
	if err != nil { return CreateMemberResult{}, err }
	passwordHash, err := HashPassword(temporaryPassword)
	if err != nil { return CreateMemberResult{}, err }
	member, err := s.repository.CreateMember(ctx, actorID, CreateMemberRecord{Email: input.Email, DisplayName: input.DisplayName, PasswordHash: passwordHash, Roles: roles})
	if err != nil { return CreateMemberResult{}, err }
	return CreateMemberResult{Member: member, TemporaryPassword: temporaryPassword}, nil
}

func (s *TeamService) UpdateRoles(ctx context.Context, actorID, userID string, roles []RoleName) (Member, error) {
	actorID, userID = strings.TrimSpace(actorID), strings.TrimSpace(userID)
	if s == nil || s.repository == nil || actorID == "" || userID == "" { return Member{}, ErrInvalidMember }
	if actorID == userID { return Member{}, ErrCannotModifySelf }
	normalized, err := normalizeRoles(roles)
	if err != nil { return Member{}, err }
	return s.repository.ReplaceMemberRoles(ctx, actorID, userID, normalized)
}

func (s *TeamService) SetEnabled(ctx context.Context, actorID, userID string, enabled bool) (Member, error) {
	if err := s.validateMutation(actorID, userID); err != nil { return Member{}, err }
	return s.repository.SetMemberEnabled(ctx, strings.TrimSpace(actorID), strings.TrimSpace(userID), enabled)
}

func (s *TeamService) Remove(ctx context.Context, actorID, userID string) (Member, error) {
	if err := s.validateMutation(actorID, userID); err != nil { return Member{}, err }
	return s.repository.RemoveMember(ctx, strings.TrimSpace(actorID), strings.TrimSpace(userID))
}

func (s *TeamService) Restore(ctx context.Context, actorID, userID string) (Member, error) {
	if err := s.validateMutation(actorID, userID); err != nil { return Member{}, err }
	return s.repository.RestoreMember(ctx, strings.TrimSpace(actorID), strings.TrimSpace(userID))
}

func (s *TeamService) ResetPassword(ctx context.Context, actorID, userID string) (ResetPasswordResult, error) {
	if err := s.validateMutation(actorID, userID); err != nil { return ResetPasswordResult{}, err }
	temporaryPassword, err := randomToken(18)
	if err != nil { return ResetPasswordResult{}, err }
	passwordHash, err := HashPassword(temporaryPassword)
	if err != nil { return ResetPasswordResult{}, err }
	member, err := s.repository.ResetMemberPassword(ctx, strings.TrimSpace(actorID), strings.TrimSpace(userID), passwordHash)
	if err != nil { return ResetPasswordResult{}, err }
	return ResetPasswordResult{Member: member, TemporaryPassword: temporaryPassword}, nil
}

func (s *TeamService) validateMutation(actorID, userID string) error {
	actorID, userID = strings.TrimSpace(actorID), strings.TrimSpace(userID)
	if s == nil || s.repository == nil || actorID == "" || userID == "" { return ErrInvalidMember }
	if actorID == userID { return ErrCannotModifySelf }
	return nil
}

func normalizeRoles(roles []RoleName) ([]RoleName, error) {
	if len(roles) == 0 { return nil, ErrInvalidRoles }
	seen := make(map[RoleName]bool, len(roles))
	normalized := make([]RoleName, 0, len(roles))
	for _, role := range roles {
		if role != RoleAdmin && role != RoleOperator && role != RoleDeveloper && role != RoleViewer { return nil, ErrInvalidRoles }
		if !seen[role] { seen[role] = true; normalized = append(normalized, role) }
	}
	sort.Slice(normalized, func(i, j int) bool { return normalized[i] < normalized[j] })
	return normalized, nil
}

func validEmail(email string) bool { parsed, err := mail.ParseAddress(email); return err == nil && parsed.Address == email }
func validMemberStatus(status MemberStatus) bool {
	switch status { case MemberStatusActive, MemberStatusDisabled, MemberStatusRemoved, MemberStatusAll: return true; default: return false }
}
