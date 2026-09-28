package notification_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"yunling.local/platform/internal/notification"
)

func TestSignFeishuUsesV2HMAC(t *testing.T) {
	const timestamp int64 = 1788123456
	const signingSecret = "signing-secret"
	mac := hmac.New(sha256.New, []byte(fmt.Sprintf("%d\n%s", timestamp, signingSecret)))
	want := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	if got := notification.SignFeishu(timestamp, signingSecret); got != want {
		t.Fatalf("签名错误：got=%q want=%q", got, want)
	}
}

func TestFeishuClientSendsSignedInteractiveCard(t *testing.T) {
	var captured map[string]any
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != validWebhook {
			t.Fatalf("Webhook 地址错误：%s", request.URL)
		}
		if err := json.NewDecoder(request.Body).Decode(&captured); err != nil {
			t.Fatal(err)
		}
		return response(http.StatusOK, `{"code":0,"data":{"message_id":"message-1"}}`), nil
	})
	client := notification.NewFeishuClient(&http.Client{Transport: transport}, func() time.Time {
		return time.Unix(1788123456, 0)
	})

	responseID, err := client.Send(context.Background(), validWebhook, "signing-secret", notification.FrozenMessage{
		Code: "agent_offline", Severity: "critical", Title: "服务器离线",
		SourceType: "server", SourceID: "server-1", OccurrenceCount: 1,
		OccurredAt: time.Date(2026, 8, 31, 10, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if responseID != "message-1" || captured["timestamp"] != "1788123456" || captured["msg_type"] != "interactive" {
		t.Fatalf("飞书请求不完整：response=%q body=%#v", responseID, captured)
	}
	if captured["sign"] != notification.SignFeishu(1788123456, "signing-secret") || captured["card"] == nil {
		t.Fatalf("飞书签名或卡片缺失：%#v", captured)
	}
}

func TestFeishuClientBoundsResponsesRejectsRedirectsAndRedactsErrors(t *testing.T) {
	secretValue := "never-print-signing-secret"
	tests := []struct {
		name      string
		response  *http.Response
		wantCalls int
		forbidden []string
	}{
		{name: "redirect", response: response(http.StatusFound, `redirect body`), wantCalls: 1},
		{name: "business error", response: response(http.StatusOK, `{"code":19001,"msg":"`+secretValue+`"}`), wantCalls: 1, forbidden: []string{secretValue}},
		{name: "oversized", response: response(http.StatusOK, strings.Repeat("x", 65<<10)), wantCalls: 1, forbidden: []string{strings.Repeat("x", 100)}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				return test.response, nil
			})
			client := notification.NewFeishuClient(&http.Client{Transport: transport}, time.Now)
			_, err := client.Send(context.Background(), validWebhook, secretValue, notification.FrozenMessage{Title: "测试消息"})
			if err == nil {
				t.Fatal("飞书错误响应必须失败")
			}
			if calls != test.wantCalls {
				t.Fatalf("不得跟随重定向，调用次数 %d", calls)
			}
			for _, forbidden := range append(test.forbidden, validWebhook, secretValue, notification.SignFeishu(time.Now().Unix(), secretValue)) {
				if forbidden != "" && strings.Contains(err.Error(), forbidden) {
					t.Fatalf("错误信息泄露敏感内容：%v", err)
				}
			}
		})
	}
}

func TestFeishuClientRequiresExplicitSuccessCode(t *testing.T) {
	const secretValue = "never-print-signing-secret"
	for _, test := range []struct {
		name, body, wantError string
	}{
		{name: "explicit zero", body: `{"code":0,"request_id":"request-1"}`},
		{name: "empty object", body: `{}`, wantError: "响应格式无效"},
		{name: "null", body: `null`, wantError: "响应格式无效"},
		{name: "null code", body: `{"code":null}`, wantError: "响应格式无效"},
		{name: "message without code", body: `{"message_id":"message-1","msg":"` + secretValue + `"}`, wantError: "响应格式无效"},
		{name: "string code", body: `{"code":"0"}`, wantError: "响应格式无效"},
		{name: "boolean code", body: `{"code":false}`, wantError: "响应格式无效"},
		{name: "array code", body: `{"code":[]}`, wantError: "响应格式无效"},
		{name: "object code", body: `{"code":{}}`, wantError: "响应格式无效"},
		{name: "fractional code", body: `{"code":0.5}`, wantError: "响应格式无效"},
		{name: "array response", body: `[]`, wantError: "响应格式无效"},
		{name: "trailing response", body: `{"code":0}{}`, wantError: "响应格式无效"},
		{name: "empty response", body: ``, wantError: "响应格式无效"},
		{name: "business failure", body: `{"code":19001,"msg":"` + secretValue + `"}`, wantError: "业务错误 19001"},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			client := notification.NewFeishuClient(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				return response(http.StatusOK, test.body), nil
			})}, time.Now)
			id, err := client.Send(context.Background(), validWebhook, secretValue, notification.FrozenMessage{Title: "测试消息"})
			if calls != 1 {
				t.Fatalf("通知只能尝试发送一次，实际 %d 次", calls)
			}
			if test.wantError == "" {
				if err != nil || id != "request-1" {
					t.Fatalf("有效成功响应未保留回执：id=%q err=%v", id, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantError) || id != "" {
				t.Fatalf("无效响应不得返回成功回执：id=%q err=%v", id, err)
			}
			for _, forbidden := range []string{validWebhook, secretValue, test.body} {
				if forbidden != "" && strings.Contains(err.Error(), forbidden) {
					t.Fatal("通知错误泄露了敏感值或响应正文")
				}
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func response(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
