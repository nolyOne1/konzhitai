package runartifact

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"yunling.local/platform/internal/auth"
)

const testRunID = "10000000-0000-4000-8000-000000000001"
const testServerID = "10000000-0000-4000-8000-000000000002"
const testArtifactID = "10000000-0000-4000-8000-000000000003"

type fakeManager struct {
	upload  UploadInput
	err     error
	calls   int
	created bool
}

func (f *fakeManager) Upload(_ context.Context, input UploadInput, _ io.Reader) (Record, bool, error) {
	f.calls++
	f.upload = input
	return Record{ID: testArtifactID, RunID: input.RunID, Name: input.Name, ObjectKey: "private-storage-key"}, f.created, f.err
}
func (f *fakeManager) List(context.Context, string) ([]Record, error) {
	f.calls++
	return []Record{{ID: testArtifactID, Name: "报表.csv", ByteSize: 4, SHA256: strings.Repeat("a", 64), ObjectKey: "private-storage-key"}}, f.err
}
func (f *fakeManager) Open(context.Context, string, string) (Record, io.ReadCloser, error) {
	f.calls++
	return Record{Name: "报表.csv", ByteSize: 4, SHA256: strings.Repeat("a", 64)}, io.NopCloser(strings.NewReader("data")), f.err
}

type fakeAgents struct{}

func (fakeAgents) Authenticate(_ context.Context, credential string) (string, error) {
	if credential != "valid-agent" {
		return "", errors.New("invalid")
	}
	return testServerID, nil
}

func TestUploadHandlerAuthenticatesAgentAndPreservesExecutionIdentity(t *testing.T) {
	for _, tc := range []struct {
		name       string
		mutate     func(*http.Request)
		serviceErr error
		created    bool
		want       int
		calls      int
	}{
		{name: "new", created: true, want: 201, calls: 1},
		{name: "replay", want: 200, calls: 1},
		{name: "missing credential", mutate: func(r *http.Request) { r.Header.Del("Authorization") }, want: 401},
		{name: "revoked credential", mutate: func(r *http.Request) { r.Header.Set("Authorization", "Bearer invalid") }, want: 401},
		{name: "no token", mutate: func(r *http.Request) { r.Header.Del("X-Execution-Token") }, want: 400},
		{name: "unknown body size", mutate: func(r *http.Request) { r.ContentLength = -1 }, want: 400},
		{name: "oversize", mutate: func(r *http.Request) { r.ContentLength = 101 << 20 }, want: 400},
		{name: "bad checksum", mutate: func(r *http.Request) { r.Header.Set("X-Content-SHA256", strings.Repeat("z", 64)) }, want: 400},
		{name: "wrong content type", mutate: func(r *http.Request) { r.Header.Set("Content-Type", "application/json") }, want: 415},
		{name: "other execution", serviceErr: ErrAccess, want: 403, calls: 1},
		{name: "changed file", serviceErr: ErrConflict, want: 409, calls: 1},
		{name: "quota", serviceErr: ErrLimit, want: 413, calls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager := &fakeManager{err: tc.serviceErr, created: tc.created}
			r := httptest.NewRequest(http.MethodPost, "/api/agent/runs/"+testRunID+"/artifacts/report.csv", strings.NewReader("data"))
			r.Header.Set("Authorization", "Bearer valid-agent")
			r.Header.Set("Content-Type", "application/octet-stream")
			r.Header.Set("X-Execution-Token", "execution-1")
			r.Header.Set("X-Content-SHA256", strings.Repeat("a", 64))
			if tc.mutate != nil {
				tc.mutate(r)
			}
			w := httptest.NewRecorder()
			UploadHandler(manager, fakeAgents{}).ServeHTTP(w, r)
			if w.Code != tc.want || manager.calls != tc.calls {
				t.Fatalf("status=%d calls=%d body=%s", w.Code, manager.calls, w.Body.String())
			}
			if manager.calls > 0 && (manager.upload.ServerID != testServerID || manager.upload.RunID != testRunID || manager.upload.ExecutionToken != "execution-1" || manager.upload.Name != "report.csv" || manager.upload.ByteSize != 4) {
				t.Fatalf("input=%+v", manager.upload)
			}
			if strings.Contains(w.Body.String(), "private-storage-key") {
				t.Fatal("internal object key leaked")
			}
		})
	}
}

func TestArtifactReadRequiresPermissionAndDownloadsChineseFilename(t *testing.T) {
	for _, endpoint := range []string{"/api/runs/" + testRunID + "/artifacts", "/api/runs/" + testRunID + "/artifacts/" + testArtifactID} {
		for _, authenticated := range []bool{false, true} {
			manager := &fakeManager{}
			r := httptest.NewRequest(http.MethodGet, endpoint, nil)
			if authenticated {
				r = r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{UserID: "viewer", Roles: []auth.RoleName{auth.RoleViewer}}))
			}
			w := httptest.NewRecorder()
			ReadHandler(manager).ServeHTTP(w, r)
			if !authenticated {
				if w.Code != 401 || manager.calls != 0 {
					t.Fatalf("anonymous status=%d calls=%d", w.Code, manager.calls)
				}
				continue
			}
			if w.Code != 200 || strings.Contains(w.Body.String(), "private-storage-key") {
				t.Fatalf("read=%d %s", w.Code, w.Body.String())
			}
			if strings.HasSuffix(endpoint, testArtifactID) {
				media, params, err := mime.ParseMediaType(w.Header().Get("Content-Disposition"))
				if err != nil || media != "attachment" || params["filename"] != "报表.csv" || w.Body.String() != "data" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
					t.Fatalf("download headers=%v body=%s", w.Header(), w.Body.String())
				}
			}
		}
	}
}
