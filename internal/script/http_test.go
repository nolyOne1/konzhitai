package script

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"yunling.local/platform/internal/auth"
)

func TestPublishEndpointUsesAuthenticatedDeveloperAsAuthor(t *testing.T) {
	manager := &fakeScriptManager{published: Version{ID: "version-1", ScriptID: "script-1", Number: 1}}
	handler := Handler(manager)
	request := httptest.NewRequest(http.MethodPost, "/api/scripts/script-1/publish", bytes.NewBufferString(`{
		"content":"echo 1\n",
		"runtime":"bash",
		"entrypoint":"main.sh",
		"releaseNotes":"首次发布脚本",
		"distribution":{"mode":"all_compatible"}
	}`))
	request = request.WithContext(auth.WithPrincipal(request.Context(), auth.Principal{
		UserID: "123e4567-e89b-42d3-a456-426614174800",
		Roles:  []auth.RoleName{auth.RoleDeveloper},
	}))
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusCreated {
		t.Fatalf("开发者发布脚本应返回 201，实际为 %d：%s", recorder.Code, recorder.Body.String())
	}
	if manager.publishInput.ScriptID != "script-1" || manager.publishInput.AuthorID != "123e4567-e89b-42d3-a456-426614174800" ||
		manager.publishInput.ReleaseNotes != "首次发布脚本" || manager.publishInput.Runtime != "bash" {
		t.Fatalf("发布接口必须使用路径脚本和当前用户：%+v", manager.publishInput)
	}
	var version Version
	if err := json.NewDecoder(recorder.Body).Decode(&version); err != nil || version.Number != 1 {
		t.Fatalf("发布响应不正确：version=%+v err=%v", version, err)
	}
}

func TestPublishEndpointRejectsOperator(t *testing.T) {
	handler := Handler(&fakeScriptManager{})
	request := httptest.NewRequest(http.MethodPost, "/api/scripts/script-1/publish", bytes.NewBufferString(`{}`))
	request = request.WithContext(auth.WithPrincipal(request.Context(), auth.Principal{
		UserID: "operator-1",
		Roles:  []auth.RoleName{auth.RoleOperator},
	}))
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("运维角色发布脚本应返回 403，实际为 %d", recorder.Code)
	}
}

func TestImportEndpointRejectsUnsupportedFileExtension(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("name", "危险文件")
	file, err := writer.CreateFormFile("file", "payload.exe")
	if err != nil {
		t.Fatalf("创建文件字段：%v", err)
	}
	_, _ = file.Write([]byte("not a script"))
	_ = writer.Close()
	manager := &fakeScriptManager{}
	handler := Handler(manager)
	request := httptest.NewRequest(http.MethodPost, "/api/scripts/import", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request = request.WithContext(auth.WithPrincipal(request.Context(), auth.Principal{
		UserID: "developer-1",
		Roles:  []auth.RoleName{auth.RoleDeveloper},
	}))
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("不支持的文件扩展名应返回 400，实际为 %d：%s", recorder.Code, recorder.Body.String())
	}
	if manager.created {
		t.Fatal("不支持的文件不得创建脚本")
	}
}

type fakeScriptManager struct {
	published    Version
	publishInput PublishInput
	created      bool
}

func (m *fakeScriptManager) List(context.Context) ([]Script, error) {
	return []Script{}, nil
}

func (m *fakeScriptManager) Get(context.Context, string) (Detail, error) {
	return Detail{}, nil
}

func (m *fakeScriptManager) Create(context.Context, CreateInput) (Script, error) {
	m.created = true
	return Script{}, nil
}

func (m *fakeScriptManager) SaveDraft(context.Context, DraftInput) (Draft, error) {
	return Draft{}, nil
}

func (m *fakeScriptManager) Publish(_ context.Context, input PublishInput) (Version, error) {
	m.publishInput = input
	return m.published, nil
}

func (m *fakeScriptManager) Rollback(context.Context, RollbackInput) (Version, error) {
	return Version{}, nil
}

func (m *fakeScriptManager) ListVersions(context.Context, string) ([]Version, error) {
	return []Version{}, nil
}

func (m *fakeScriptManager) VersionContent(context.Context, string, string) (string, error) {
	return "", nil
}
