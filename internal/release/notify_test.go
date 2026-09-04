package release

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestReleaseNotifierUsesChineseCardAndGitHubRunLink(t *testing.T) {
	var received []byte
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatal(err)
		}
		received = append([]byte(nil), body...)
		for _, required := range []string{
			"生产发布失败", "自动回滚成功", "nolyOne1", "github.com/nolyOne1/konzhitai/actions/runs/123",
			"2026-09-03 20:00:00", "diag-123",
		} {
			if !bytes.Contains(body, []byte(required)) {
				t.Errorf("飞书卡片缺少 %q：%s", required, body)
			}
		}
		_, _ = io.WriteString(response, `{"code":0}`)
	}))
	defer server.Close()

	target, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	client := server.Client()
	client.Transport = rewriteFeishuTransport{target: target, base: client.Transport}
	notifier := NewNotifier(client, func() time.Time {
		return time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	})
	result := Result{
		Operation: OperationDeploy, TargetID: "101", SourceSHA: strings.Repeat("d", 40),
		Actor: "nolyOne1", WorkflowRunID: 123,
		WorkflowURL: "https://github.com/nolyOne1/konzhitai/actions/runs/123",
		Status:      "failed", RollbackStatus: "succeeded", DiagnosticID: "diag-123",
		StartedAt:  time.Date(2026, 9, 3, 11, 59, 0, 0, time.UTC),
		FinishedAt: time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC),
	}
	if err := notifier.Send(context.Background(),
		"https://open.feishu.cn/open-apis/bot/v2/hook/00000000-0000-4000-8000-000000000000",
		"test-signing-secret", result); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(received, []byte("test-signing-secret")) {
		t.Fatal("签名密钥不得进入飞书请求正文的明文")
	}
}

func TestReleaseNotifierRejectsUntrustedWorkflowLinkBeforeHTTP(t *testing.T) {
	calls := 0
	notifier := NewNotifier(&http.Client{Transport: notifyRoundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, nil
	})}, time.Now)
	result := Result{
		Operation: OperationRollback, TargetID: "bootstrap", Actor: "nolyOne1", WorkflowRunID: 123,
		WorkflowURL: "https://evil.example/nolyOne1/konzhitai/actions/runs/123",
		Status:      "failed", RollbackStatus: "failed", DiagnosticID: "diag-123",
		StartedAt: time.Now().UTC(), FinishedAt: time.Now().UTC(),
	}
	err := notifier.Send(context.Background(),
		"https://open.feishu.cn/open-apis/bot/v2/hook/00000000-0000-4000-8000-000000000000",
		"test-signing-secret", result)
	if err == nil || calls != 0 {
		t.Fatalf("不可信跳转必须在 HTTP 前拒绝：calls=%d err=%v", calls, err)
	}
}

type rewriteFeishuTransport struct {
	target *url.URL
	base   http.RoundTripper
}

func (transport rewriteFeishuTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	clone.URL.Scheme = transport.target.Scheme
	clone.URL.Host = transport.target.Host
	return transport.base.RoundTrip(clone)
}

type notifyRoundTripFunc func(*http.Request) (*http.Response, error)

func (function notifyRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}
