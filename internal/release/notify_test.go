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

func TestReleaseNotifierRequiresExplicitSuccessCode(t *testing.T) {
	const webhook = "https://open.feishu.cn/open-apis/bot/v2/hook/00000000-0000-4000-8000-000000000000"
	const secretValue = "never-print-signing-secret"
	result := Result{
		Operation: OperationDeploy, TargetID: "101", SourceSHA: strings.Repeat("d", 40),
		Actor: "nolyOne1", WorkflowRunID: 123,
		WorkflowURL: "https://github.com/nolyOne1/konzhitai/actions/runs/123",
		Status:      "succeeded", RollbackStatus: "not-required",
		StartedAt:  time.Date(2026, 9, 3, 11, 59, 0, 0, time.UTC),
		FinishedAt: time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC),
	}
	for _, test := range []struct {
		name, body, wantError string
		status                int
	}{
		{name: "explicit zero", body: `{"code":0}`, status: http.StatusOK},
		{name: "empty object", body: `{}`, status: http.StatusOK, wantError: "响应格式无效"},
		{name: "null", body: `null`, status: http.StatusOK, wantError: "响应格式无效"},
		{name: "null code", body: `{"code":null}`, status: http.StatusOK, wantError: "响应格式无效"},
		{name: "missing code with message", body: `{"msg":"` + secretValue + `"}`, status: http.StatusOK, wantError: "响应格式无效"},
		{name: "string code", body: `{"code":"0"}`, status: http.StatusOK, wantError: "响应格式无效"},
		{name: "boolean code", body: `{"code":false}`, status: http.StatusOK, wantError: "响应格式无效"},
		{name: "array code", body: `{"code":[]}`, status: http.StatusOK, wantError: "响应格式无效"},
		{name: "object code", body: `{"code":{}}`, status: http.StatusOK, wantError: "响应格式无效"},
		{name: "fractional code", body: `{"code":0.5}`, status: http.StatusOK, wantError: "响应格式无效"},
		{name: "array response", body: `[]`, status: http.StatusOK, wantError: "响应格式无效"},
		{name: "trailing response", body: `{"code":0}{}`, status: http.StatusOK, wantError: "响应格式无效"},
		{name: "empty 204", body: ``, status: http.StatusNoContent, wantError: "响应格式无效"},
		{name: "business failure", body: `{"code":19001,"msg":"` + secretValue + `"}`, status: http.StatusOK, wantError: "业务错误 19001"},
		{name: "HTTP failure with zero", body: `{"code":0,"msg":"` + secretValue + `"}`, status: http.StatusBadGateway, wantError: "HTTP 502"},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			notifier := NewNotifier(&http.Client{Transport: notifyRoundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: test.status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(test.body))}, nil
			})}, time.Now)
			err := notifier.Send(context.Background(), webhook, secretValue, result)
			if calls != 1 {
				t.Fatalf("通知只能尝试发送一次，实际 %d 次", calls)
			}
			if test.wantError == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("期望错误 %q，实际 %v", test.wantError, err)
			}
			for _, forbidden := range []string{webhook, secretValue, test.body} {
				if forbidden != "" && strings.Contains(err.Error(), forbidden) {
					t.Fatal("通知错误泄露了敏感值或响应正文")
				}
			}
		})
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
