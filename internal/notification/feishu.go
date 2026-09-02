package notification

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	feishuRequestTimeout = 10 * time.Second
	feishuResponseLimit  = 64 << 10
)

type FrozenMessage struct {
	Code            string    `json:"code"`
	Severity        string    `json:"severity"`
	Title           string    `json:"title"`
	SourceType      string    `json:"sourceType"`
	SourceID        string    `json:"sourceId"`
	OccurrenceCount int       `json:"occurrenceCount"`
	OccurredAt      time.Time `json:"occurredAt"`
}

type FeishuClient struct {
	httpClient *http.Client
	now        func() time.Time
}

func NewFeishuClient(client *http.Client, now func() time.Time) *FeishuClient {
	configured := &http.Client{}
	if client != nil {
		*configured = *client
	}
	configured.Timeout = feishuRequestTimeout
	configured.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	if now == nil {
		now = time.Now
	}
	return &FeishuClient{httpClient: configured, now: now}
}

func SignFeishu(timestamp int64, secretValue string) string {
	stringToSign := strconv.FormatInt(timestamp, 10) + "\n" + secretValue
	mac := hmac.New(sha256.New, []byte(stringToSign))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func (c *FeishuClient) Send(
	ctx context.Context,
	webhook, signingSecret string,
	payload FrozenMessage,
) (string, error) {
	if c == nil || c.httpClient == nil || ValidateFeishuWebhook(webhook) != nil || signingSecret == "" {
		return "", errors.New("飞书发送配置无效")
	}
	timestamp := c.now().Unix()
	requestBody := map[string]any{
		"timestamp": strconv.FormatInt(timestamp, 10),
		"sign":      SignFeishu(timestamp, signingSecret),
		"msg_type":  "interactive",
		"card":      feishuCard(payload),
	}
	encoded, err := json.Marshal(requestBody)
	if err != nil {
		return "", errors.New("生成飞书消息失败")
	}
	requestContext, cancel := context.WithTimeout(ctx, feishuRequestTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, http.MethodPost, webhook, bytes.NewReader(encoded))
	if err != nil {
		return "", errors.New("创建飞书请求失败")
	}
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	response, err := c.httpClient.Do(request)
	if err != nil {
		return "", errors.New("发送飞书请求失败")
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, feishuResponseLimit+1))
	if err != nil {
		return "", errors.New("读取飞书响应失败")
	}
	if len(body) > feishuResponseLimit {
		return "", errors.New("飞书响应超过安全限制")
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("飞书返回 HTTP %d", response.StatusCode)
	}
	var result struct {
		Code      int    `json:"code"`
		MessageID string `json:"message_id"`
		RequestID string `json:"request_id"`
		Data      struct {
			MessageID string `json:"message_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", errors.New("飞书响应格式无效")
	}
	if result.Code != 0 {
		return "", fmt.Errorf("飞书返回业务错误 %d", result.Code)
	}
	for _, id := range []string{result.Data.MessageID, result.MessageID, result.RequestID} {
		if strings.TrimSpace(id) != "" {
			return id, nil
		}
	}
	return "", nil
}

func feishuCard(payload FrozenMessage) map[string]any {
	template := "blue"
	switch payload.Severity {
	case "critical":
		template = "red"
	case "warning":
		template = "orange"
	case "info":
		template = "blue"
	}
	occurredAt := payload.OccurredAt.UTC().Format("2006-01-02 15:04:05 UTC")
	content := fmt.Sprintf(
		"**告警代码：** %s\n**来源：** %s / %s\n**累计次数：** %d\n**时间：** %s",
		payload.Code,
		payload.SourceType,
		payload.SourceID,
		payload.OccurrenceCount,
		occurredAt,
	)
	return map[string]any{
		"header": map[string]any{
			"template": template,
			"title":    map[string]string{"tag": "plain_text", "content": payload.Title},
		},
		"elements": []any{
			map[string]any{"tag": "markdown", "content": content},
		},
	}
}
