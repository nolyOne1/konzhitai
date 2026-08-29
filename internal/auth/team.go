package auth

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"
)

var (
	ErrInvalidRoles   = errors.New("成员角色配置无效")
	ErrMemberNotFound = errors.New("成员不存在")
)

type Member struct {
	ID          string     `json:"id"`
	Email       string     `json:"email"`
	DisplayName string     `json:"displayName"`
	Enabled     bool       `json:"enabled"`
	Roles       []RoleName `json:"roles"`
	CreatedAt   time.Time  `json:"createdAt"`
}

type TeamRepository interface {
	ListMembers(context.Context) ([]Member, error)
	ReplaceMemberRoles(context.Context, string, []RoleName) (Member, error)
}

type TeamService struct{ repository TeamRepository }

func NewTeamService(repository TeamRepository) *TeamService {
	return &TeamService{repository: repository}
}

func (s *TeamService) List(ctx context.Context) ([]Member, error) {
	if s == nil || s.repository == nil {
		return nil, ErrInvalidRoles
	}
	members, err := s.repository.ListMembers(ctx)
	if members == nil && err == nil {
		members = []Member{}
	}
	return members, err
}

func (s *TeamService) UpdateRoles(ctx context.Context, userID string, roles []RoleName) (Member, error) {
	userID = strings.TrimSpace(userID)
	if s == nil || s.repository == nil || userID == "" || len(roles) == 0 {
		return Member{}, ErrInvalidRoles
	}
	seen := make(map[RoleName]bool, len(roles))
	normalized := make([]RoleName, 0, len(roles))
	for _, role := range roles {
		if role != RoleAdmin && role != RoleOperator && role != RoleDeveloper && role != RoleViewer {
			return Member{}, ErrInvalidRoles
		}
		if !seen[role] {
			seen[role] = true
			normalized = append(normalized, role)
		}
	}
	sort.Slice(normalized, func(i, j int) bool { return normalized[i] < normalized[j] })
	return s.repository.ReplaceMemberRoles(ctx, userID, normalized)
}
