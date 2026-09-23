package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"yunling.local/platform/internal/auth"
)

type groupMemory struct {
	group  Group
	writes int
}

func (m *groupMemory) ListGroups(context.Context) ([]Group, error) { return []Group{m.group}, nil }
func (m *groupMemory) CreateGroup(_ context.Context, name string) (Group, error) {
	m.writes++
	m.group = Group{ID: "group-1", Name: name}
	return m.group, nil
}
func (m *groupMemory) RenameGroup(_ context.Context, id, name string) (Group, error) {
	m.writes++
	m.group = Group{ID: id, Name: name}
	return m.group, nil
}

func TestServerGroupHTTPPermissionsAndValidation(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, body string
		role                     auth.RoleName
		status                   int
		writes                   int
	}{
		{"viewer can list", http.MethodGet, "/api/server-groups", "", auth.RoleViewer, 200, 0},
		{"viewer cannot create", http.MethodPost, "/api/server-groups", `{"name":"生产"}`, auth.RoleViewer, 403, 0},
		{"operator creates normalized group", http.MethodPost, "/api/server-groups", `{"name":" 生产 "}`, auth.RoleOperator, 201, 1},
		{"empty name", http.MethodPost, "/api/server-groups", `{"name":" "}`, auth.RoleAdmin, 400, 0},
		{"extra JSON", http.MethodPost, "/api/server-groups", `{"name":"生产"} {}`, auth.RoleAdmin, 400, 0},
		{"unknown field", http.MethodPost, "/api/server-groups", `{"name":"生产","id":"injected"}`, auth.RoleAdmin, 400, 0},
		{"rename", http.MethodPatch, "/api/server-groups/group-1", `{"name":"生产"}`, auth.RoleAdmin, 200, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager := &groupMemory{}
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			req = req.WithContext(auth.WithPrincipal(req.Context(), auth.Principal{UserID: "operator", Roles: []auth.RoleName{tc.role}}))
			response := httptest.NewRecorder()
			GroupsHandler(manager).ServeHTTP(response, req)
			if response.Code != tc.status || manager.writes != tc.writes {
				t.Fatalf("status=%d writes=%d body=%s", response.Code, manager.writes, response.Body.String())
			}
			if tc.writes > 0 && manager.group.Name != "生产" {
				t.Fatalf("group name was not normalized: %+v", manager.group)
			}
		})
	}
}

func TestServerGroupAssignmentIsValidUpdate(t *testing.T) {
	groupID := "group-1"
	if !validServerUpdate(UpdateServerInput{ServerGroupID: &groupID}) {
		t.Fatal("assigning a group must be allowed without unrelated fields")
	}
	groupID = ""
	if !validServerUpdate(UpdateServerInput{ServerGroupID: &groupID}) {
		t.Fatal("removing a group must be allowed")
	}
}
