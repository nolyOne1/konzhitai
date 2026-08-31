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
