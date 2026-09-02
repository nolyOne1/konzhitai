package agentrelease

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestHTTPManifestAndArtifactResponses(t *testing.T) {
	root, stored, contents := writeValidCatalog(t)
	catalog, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	handler := Handler(catalog, WithLimits(100, 2, time.Minute, time.Now))

	manifestResponse := httptest.NewRecorder()
	handler.ServeHTTP(manifestResponse, httptest.NewRequest(http.MethodGet, "/api/releases/agent/latest", nil))
	if manifestResponse.Code != http.StatusOK {
		t.Fatalf("清单状态码：%d body=%s", manifestResponse.Code, manifestResponse.Body.String())
	}
	if got := manifestResponse.Header().Get("Cache-Control"); got != "no-cache, no-store, must-revalidate" {
		t.Fatalf("清单缓存头：%q", got)
	}
	if got := manifestResponse.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("清单 nosniff：%q", got)
	}
	if got := manifestResponse.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Fatalf("清单内容类型：%q", got)
	}
	var publicManifest Manifest
	if err := json.NewDecoder(manifestResponse.Body).Decode(&publicManifest); err != nil {
		t.Fatal(err)
	}
	if publicManifest.Version != stored.Version || len(publicManifest.Artifacts) != 2 {
		t.Fatalf("公共清单：%+v", publicManifest)
	}

	artifact := publicManifest.Artifacts[0]
	requestPath := artifact.DownloadURL
	artifactResponse := httptest.NewRecorder()
	handler.ServeHTTP(artifactResponse, httptest.NewRequest(http.MethodGet, requestPath, nil))
	if artifactResponse.Code != http.StatusOK {
		t.Fatalf("归档状态码：%d body=%s", artifactResponse.Code, artifactResponse.Body.String())
	}
	if got := artifactResponse.Body.Bytes(); !bytes.Equal(got, contents[artifact.FileName]) {
		t.Fatalf("归档内容：%q", got)
	}
	for header, want := range map[string]string{
		"Content-Type":           "application/gzip",
		"Content-Length":         strconv.FormatInt(artifact.ByteSize, 10),
		"Content-Disposition":    `attachment; filename="` + artifact.FileName + `"`,
		"ETag":                   `"` + artifact.SHA256 + `"`,
		"Cache-Control":          "public, max-age=31536000, immutable",
		"X-Content-Type-Options": "nosniff",
	} {
		if got := artifactResponse.Header().Get(header); got != want {
			t.Fatalf("归档响应头 %s：got=%q want=%q", header, got, want)
		}
	}

	notModifiedRequest := httptest.NewRequest(http.MethodGet, requestPath, nil)
	notModifiedRequest.Header.Set("If-None-Match", `"`+artifact.SHA256+`"`)
	notModifiedResponse := httptest.NewRecorder()
	handler.ServeHTTP(notModifiedResponse, notModifiedRequest)
	if notModifiedResponse.Code != http.StatusNotModified || notModifiedResponse.Body.Len() != 0 {
		t.Fatalf("条件请求：status=%d body=%q", notModifiedResponse.Code, notModifiedResponse.Body.String())
	}

	if err := os.Remove(filepath.Join(root, artifact.FileName)); err != nil {
		t.Fatal(err)
	}
	notModifiedWithoutFile := httptest.NewRecorder()
	handler.ServeHTTP(notModifiedWithoutFile, notModifiedRequest.Clone(notModifiedRequest.Context()))
	if notModifiedWithoutFile.Code != http.StatusNotModified {
		t.Fatalf("ETag 命中时不得重新打开归档：status=%d body=%q", notModifiedWithoutFile.Code, notModifiedWithoutFile.Body.String())
	}
}

func TestHandlerRejectsInvalidRequests(t *testing.T) {
	root, _, _ := writeValidCatalog(t)
	if err := os.WriteFile(filepath.Join(root, "unlisted.tar.gz"), []byte("not published"), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	manifest := catalog.Manifest()
	artifact := manifest.Artifacts[0]
	handler := Handler(catalog, WithLimits(100, 2, time.Minute, time.Now))

	tests := []struct {
		name       string
		method     string
		path       string
		wantStatus int
	}{
		{name: "清单拒绝 POST", method: http.MethodPost, path: "/api/releases/agent/latest", wantStatus: http.StatusMethodNotAllowed},
		{name: "错误版本", method: http.MethodGet, path: strings.Replace(artifact.DownloadURL, manifest.Version, "9.9.9", 1), wantStatus: http.StatusNotFound},
		{name: "错误摘要", method: http.MethodGet, path: "/api/releases/agent/" + manifest.Version + "/" + strings.Repeat("f", 64) + "/" + artifact.FileName, wantStatus: http.StatusNotFound},
		{name: "错误文件名", method: http.MethodGet, path: "/api/releases/agent/" + manifest.Version + "/" + artifact.SHA256 + "/unlisted.tar.gz", wantStatus: http.StatusNotFound},
		{name: "编码路径穿越", method: http.MethodGet, path: "/api/releases/agent/" + manifest.Version + "/" + artifact.SHA256 + "/%2e%2e%2fmanifest.json", wantStatus: http.StatusNotFound},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(test.method, test.path, nil))
			if response.Code != test.wantStatus {
				t.Fatalf("状态码：got=%d want=%d body=%q", response.Code, test.wantStatus, response.Body.String())
			}
		})
	}
}

func TestUnavailableHandlerReturnsFixedServiceUnavailable(t *testing.T) {
	response := httptest.NewRecorder()
	UnavailableHandler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/releases/agent/latest", nil))
	if response.Code != http.StatusServiceUnavailable || strings.TrimSpace(response.Body.String()) != "代理发布暂不可用" {
		t.Fatalf("不可用响应：status=%d body=%q", response.Code, response.Body.String())
	}
}

func TestHandlerRateLimitResetsAfterWindow(t *testing.T) {
	root, _, _ := writeValidCatalog(t)
	catalog, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	handler := Handler(catalog, WithLimits(2, 1, time.Minute, func() time.Time { return now }))
	for requestNumber := 1; requestNumber <= 3; requestNumber++ {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/releases/agent/latest", nil))
		if requestNumber < 3 && response.Code != http.StatusOK {
			t.Fatalf("第 %d 次请求：%d", requestNumber, response.Code)
		}
		if requestNumber == 3 && (response.Code != http.StatusTooManyRequests || response.Header().Get("Retry-After") != "5") {
			t.Fatalf("限流响应：status=%d retry=%q", response.Code, response.Header().Get("Retry-After"))
		}
	}
	now = now.Add(time.Minute)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/releases/agent/latest", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("新窗口必须恢复：%d", response.Code)
	}
}

func TestHandlerLimitsConcurrentDownloads(t *testing.T) {
	root, _, _ := writeValidCatalog(t)
	catalog, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	artifact := catalog.Manifest().Artifacts[0]
	handler := Handler(catalog, WithLimits(10, 1, time.Minute, time.Now))

	blocked := newBlockingResponseWriter()
	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.ServeHTTP(blocked, httptest.NewRequest(http.MethodGet, artifact.DownloadURL, nil))
	}()
	select {
	case <-blocked.started:
	case <-time.After(2 * time.Second):
		t.Fatal("第一个下载未开始")
	}

	second := httptest.NewRecorder()
	handler.ServeHTTP(second, httptest.NewRequest(http.MethodGet, artifact.DownloadURL, nil))
	if second.Code != http.StatusTooManyRequests || second.Header().Get("Retry-After") != "5" {
		t.Fatalf("并发限流：status=%d retry=%q", second.Code, second.Header().Get("Retry-After"))
	}
	close(blocked.release)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("第一个下载未结束")
	}
}

type blockingResponseWriter struct {
	header  http.Header
	status  int
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func newBlockingResponseWriter() *blockingResponseWriter {
	return &blockingResponseWriter{
		header: make(http.Header), started: make(chan struct{}), release: make(chan struct{}),
	}
}

func (w *blockingResponseWriter) Header() http.Header { return w.header }

func (w *blockingResponseWriter) WriteHeader(status int) { w.status = status }

func (w *blockingResponseWriter) Write(content []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	w.once.Do(func() { close(w.started) })
	<-w.release
	return len(content), nil
}
