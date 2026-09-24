package logstream

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"yunling.local/platform/internal/auth"
	"yunling.local/platform/internal/task"
)

const archiveTestRun = "10000000-0000-4000-8000-000000000001"

type archiveReaderStub struct{ info ArchiveInfo }

func (r archiveReaderStub) ArchiveStatus(context.Context, task.RunID) (ArchiveInfo, error) {
	return r.info, nil
}

func TestArchiveDownloadRequiresReadPermissionAndCurrentSnapshot(t *testing.T) {
	for _, tc := range []struct {
		name          string
		info          ArchiveInfo
		authenticated bool
		status        int
	}{
		{"unauthenticated", ArchiveInfo{Available: true, Current: true}, false, http.StatusUnauthorized},
		{"not archived", ArchiveInfo{}, true, http.StatusNotFound},
		{"late logs", ArchiveInfo{Available: true, Current: false}, true, http.StatusConflict},
		{"ready", ArchiveInfo{Available: true, Current: true, ObjectKey: "archive.gz", ByteSize: 4, SHA256: strings.Repeat("a", 64)}, true, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler := ArchiveHandler(archiveReaderStub{tc.info}, &memoryObjectStore{items: map[string][]byte{"archive.gz": []byte("gzip")}})
			request := httptest.NewRequest(http.MethodGet, "/api/runs/"+archiveTestRun+"/logs/archive", nil)
			if tc.authenticated {
				request = request.WithContext(auth.WithPrincipal(request.Context(), auth.Principal{UserID: "viewer", Roles: []auth.RoleName{auth.RoleViewer}}))
			}
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if recorder.Code != tc.status {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			if tc.status == http.StatusOK && (recorder.Body.String() != "gzip" || recorder.Header().Get("Content-Type") != "application/gzip") {
				t.Fatalf("download=%s headers=%v", recorder.Body.String(), recorder.Header())
			}
		})
	}
}

func TestArchiveInfoDoesNotExposeObjectKey(t *testing.T) {
	handler := ArchiveHandler(archiveReaderStub{ArchiveInfo{Available: true, Current: true, ObjectKey: "private-object", ChunkCount: 2}}, nil)
	request := httptest.NewRequest(http.MethodGet, "/api/runs/"+archiveTestRun+"/logs/archive/info", nil)
	request = request.WithContext(auth.WithPrincipal(request.Context(), auth.Principal{UserID: "viewer", Roles: []auth.RoleName{auth.RoleViewer}}))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || strings.Contains(recorder.Body.String(), "private-object") || !strings.Contains(recorder.Body.String(), `"chunkCount":2`) {
		t.Fatalf("info=%d %s", recorder.Code, recorder.Body.String())
	}
}
