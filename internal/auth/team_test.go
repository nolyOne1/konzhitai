package auth_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"yunling.local/platform/internal/auth"
)

func TestTeamServiceNormalizesAndUpdatesMemberRoles(t *testing.T) {
	repository := &memoryTeamRepository{}
	service := auth.NewTeamService(repository)

	member, err := service.UpdateRoles(context.Background(), "user-1", []auth.RoleName{
		auth.RoleViewer, auth.RoleAdmin, auth.RoleViewer,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []auth.RoleName{auth.RoleAdmin, auth.RoleViewer}
	if !reflect.DeepEqual(repository.roles, want) || !reflect.DeepEqual(member.Roles, want) {
		t.Fatalf("角色应校验、去重并排序：stored=%v member=%v", repository.roles, member.Roles)
	}
}

func TestTeamServiceRejectsUnknownOrEmptyRoles(t *testing.T) {
	service := auth.NewTeamService(&memoryTeamRepository{})
	for _, roles := range [][]auth.RoleName{nil, {"owner"}} {
		if _, err := service.UpdateRoles(context.Background(), "user-1", roles); !errors.Is(err, auth.ErrInvalidRoles) {
			t.Fatalf("无效角色应被拒绝，roles=%v err=%v", roles, err)
		}
	}
}

type memoryTeamRepository struct{ roles []auth.RoleName }

func (r *memoryTeamRepository) ListMembers(context.Context) ([]auth.Member, error) {
	return []auth.Member{}, nil
}

func (r *memoryTeamRepository) ReplaceMemberRoles(_ context.Context, userID string, roles []auth.RoleName) (auth.Member, error) {
	r.roles = append([]auth.RoleName(nil), roles...)
	return auth.Member{ID: userID, Roles: append([]auth.RoleName(nil), roles...), CreatedAt: time.Now()}, nil
}
