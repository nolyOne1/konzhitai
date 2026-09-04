package auth_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"yunling.local/platform/internal/auth"
)

func TestTeamServiceCreatesMemberWithNormalizedInputAndTemporaryPassword(t *testing.T) {
	repository := &memoryTeamRepository{}
	service := auth.NewTeamService(repository)

	result, err := service.Create(context.Background(), "admin-1", auth.CreateMemberInput{
		Email: " OPS@Example.COM ", DisplayName: " 值班运维 ",
		Roles: []auth.RoleName{auth.RoleViewer, auth.RoleOperator, auth.RoleViewer},
	})
	if err != nil { t.Fatal(err) }
	if repository.created.Email != "ops@example.com" || repository.created.DisplayName != "值班运维" { t.Fatalf("创建输入未规范化：%+v", repository.created) }
	wantRoles := []auth.RoleName{auth.RoleOperator, auth.RoleViewer}
	if !reflect.DeepEqual(repository.created.Roles, wantRoles) { t.Fatalf("创建角色未规范化：%v", repository.created.Roles) }
	if len(result.TemporaryPassword) < 12 { t.Fatal("临时密码长度不足") }
	valid, err := auth.VerifyPassword(repository.created.PasswordHash, result.TemporaryPassword)
	if err != nil || !valid { t.Fatal("临时密码没有以哈希形式传入仓储") }
}

func TestTeamServiceRejectsInvalidCreateMemberInput(t *testing.T) {
	service := auth.NewTeamService(&memoryTeamRepository{})
	tests := []struct { name string; input auth.CreateMemberInput }{
		{name: "无效邮箱", input: auth.CreateMemberInput{Email: "not-an-email", DisplayName: "运维", Roles: []auth.RoleName{auth.RoleViewer}}},
		{name: "空白显示名", input: auth.CreateMemberInput{Email: "ops@example.com", DisplayName: " \t", Roles: []auth.RoleName{auth.RoleViewer}}},
		{name: "空角色", input: auth.CreateMemberInput{Email: "ops@example.com", DisplayName: "运维"}},
		{name: "未知角色", input: auth.CreateMemberInput{Email: "ops@example.com", DisplayName: "运维", Roles: []auth.RoleName{"owner"}}},
	}
	for _, test := range tests { t.Run(test.name, func(t *testing.T) {
		if _, err := service.Create(context.Background(), "admin-1", test.input); !errors.Is(err, auth.ErrInvalidMember) { t.Fatalf("无效创建输入应被拒绝：%v", err) }
	}) }
}

func TestTeamServiceNormalizesAndUpdatesMemberRoles(t *testing.T) {
	repository := &memoryTeamRepository{}
	service := auth.NewTeamService(repository)
	member, err := service.UpdateRoles(context.Background(), "admin-1", "user-1", []auth.RoleName{auth.RoleViewer, auth.RoleAdmin, auth.RoleViewer})
	if err != nil { t.Fatal(err) }
	want := []auth.RoleName{auth.RoleAdmin, auth.RoleViewer}
	if repository.actorID != "admin-1" || repository.targetID != "user-1" || !reflect.DeepEqual(repository.roles, want) || !reflect.DeepEqual(member.Roles, want) { t.Fatalf("角色应校验、去重并排序：actor=%s target=%s stored=%v member=%v", repository.actorID, repository.targetID, repository.roles, member.Roles) }
}

func TestTeamServiceRejectsUnknownOrEmptyRoles(t *testing.T) {
	service := auth.NewTeamService(&memoryTeamRepository{})
	for _, roles := range [][]auth.RoleName{nil, {"owner"}} {
		if _, err := service.UpdateRoles(context.Background(), "admin-1", "user-1", roles); !errors.Is(err, auth.ErrInvalidRoles) { t.Fatalf("无效角色应被拒绝，roles=%v err=%v", roles, err) }
	}
}

func TestTeamServiceRejectsSelfMutation(t *testing.T) {
	service := auth.NewTeamService(&memoryTeamRepository{})
	ctx := context.Background()
	if _, err := service.UpdateRoles(ctx, "admin-1", "admin-1", []auth.RoleName{auth.RoleViewer}); !errors.Is(err, auth.ErrCannotModifySelf) { t.Fatalf("管理员必须不能修改自己的角色：%v", err) }
	if _, err := service.SetEnabled(ctx, "admin-1", "admin-1", false); !errors.Is(err, auth.ErrCannotModifySelf) { t.Fatalf("管理员必须不能停用自己：%v", err) }
	if _, err := service.Remove(ctx, "admin-1", "admin-1"); !errors.Is(err, auth.ErrCannotModifySelf) { t.Fatalf("管理员必须不能移除自己：%v", err) }
	if _, err := service.Restore(ctx, "admin-1", "admin-1"); !errors.Is(err, auth.ErrCannotModifySelf) { t.Fatalf("管理员必须不能恢复自己：%v", err) }
	if _, err := service.ResetPassword(ctx, "admin-1", "admin-1"); !errors.Is(err, auth.ErrCannotModifySelf) { t.Fatalf("管理员必须不能重置自己的密码：%v", err) }
}

func TestTeamServiceRejectsInvalidStatusFilter(t *testing.T) {
	service := auth.NewTeamService(&memoryTeamRepository{})
	if _, err := service.List(context.Background(), auth.MemberStatus("archived")); !errors.Is(err, auth.ErrInvalidMember) { t.Fatalf("无效状态筛选应被拒绝：%v", err) }
}

func TestTeamServiceListsAllUnremovedMembers(t *testing.T) {
	repository := &memoryTeamRepository{members: []auth.Member{}}
	service := auth.NewTeamService(repository)
	members, err := service.List(context.Background(), auth.MemberStatusAll)
	if err != nil { t.Fatal(err) }
	if repository.status != auth.MemberStatusAll || members == nil { t.Fatalf("全部成员筛选应委派并保留空切片：status=%q members=%#v", repository.status, members) }
}

func TestTeamServiceDelegatesMemberStateChanges(t *testing.T) {
	repository := &memoryTeamRepository{}
	service := auth.NewTeamService(repository)
	ctx := context.Background()
	for _, test := range []struct { name string; call func() error; want string }{
		{name: "停用", call: func() error { _, err := service.SetEnabled(ctx, "admin-1", "user-1", false); return err }, want: "set-enabled:false"},
		{name: "移除", call: func() error { _, err := service.Remove(ctx, "admin-1", "user-1"); return err }, want: "remove"},
		{name: "恢复", call: func() error { _, err := service.Restore(ctx, "admin-1", "user-1"); return err }, want: "restore"},
	} { t.Run(test.name, func(t *testing.T) {
		if err := test.call(); err != nil { t.Fatal(err) }
		if repository.call != test.want || repository.actorID != "admin-1" || repository.targetID != "user-1" { t.Fatalf("成员状态变更未正确委派：call=%s actor=%s target=%s", repository.call, repository.actorID, repository.targetID) }
	}) }
}

func TestTeamServiceResetsPasswordWithHash(t *testing.T) {
	repository := &memoryTeamRepository{}
	service := auth.NewTeamService(repository)
	result, err := service.ResetPassword(context.Background(), "admin-1", "user-1")
	if err != nil { t.Fatal(err) }
	if len(result.TemporaryPassword) < 12 { t.Fatal("临时密码长度不足") }
	valid, err := auth.VerifyPassword(repository.passwordHash, result.TemporaryPassword)
	if err != nil || !valid { t.Fatal("重置密码没有以哈希形式传入仓储") }
	if repository.actorID != "admin-1" || repository.targetID != "user-1" { t.Fatalf("重置密码未传递操作者和目标：actor=%s target=%s", repository.actorID, repository.targetID) }
}

type memoryTeamRepository struct {
	members []auth.Member
	status auth.MemberStatus
	created auth.CreateMemberRecord
	roles []auth.RoleName
	passwordHash string
	actorID string
	targetID string
	call string
}

func (r *memoryTeamRepository) ListMembers(_ context.Context, status auth.MemberStatus) ([]auth.Member, error) { r.status = status; return r.members, nil }
func (r *memoryTeamRepository) CreateMember(_ context.Context, actorID string, record auth.CreateMemberRecord) (auth.Member, error) { r.actorID, r.created = actorID, record; return auth.Member{ID: "user-1", Email: record.Email, DisplayName: record.DisplayName, Roles: append([]auth.RoleName(nil), record.Roles...), CreatedAt: time.Now()}, nil }
func (r *memoryTeamRepository) ReplaceMemberRoles(_ context.Context, actorID, targetID string, roles []auth.RoleName) (auth.Member, error) { r.actorID, r.targetID, r.roles = actorID, targetID, append([]auth.RoleName(nil), roles...); return auth.Member{ID: targetID, Roles: append([]auth.RoleName(nil), roles...), CreatedAt: time.Now()}, nil }
func (r *memoryTeamRepository) SetMemberEnabled(_ context.Context, actorID, targetID string, enabled bool) (auth.Member, error) { r.actorID, r.targetID, r.call = actorID, targetID, "set-enabled:"+map[bool]string{true: "true", false: "false"}[enabled]; return auth.Member{ID: targetID, Enabled: enabled, CreatedAt: time.Now()}, nil }
func (r *memoryTeamRepository) RemoveMember(_ context.Context, actorID, targetID string) (auth.Member, error) { r.actorID, r.targetID, r.call = actorID, targetID, "remove"; return auth.Member{ID: targetID, CreatedAt: time.Now()}, nil }
func (r *memoryTeamRepository) RestoreMember(_ context.Context, actorID, targetID string) (auth.Member, error) { r.actorID, r.targetID, r.call = actorID, targetID, "restore"; return auth.Member{ID: targetID, CreatedAt: time.Now()}, nil }
func (r *memoryTeamRepository) ResetMemberPassword(_ context.Context, actorID, targetID, passwordHash string) (auth.Member, error) { r.actorID, r.targetID, r.passwordHash = actorID, targetID, passwordHash; return auth.Member{ID: targetID, CreatedAt: time.Now()}, nil }
