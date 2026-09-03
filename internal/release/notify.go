package release

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"yunling.local/platform/internal/notification"
)

const (
	notifierTimeout       = 10 * time.Second
	notifierResponseLimit = 64 << 10
)

type Notifier struct {
	client *http.Client
	now    func() time.Time
}

func NewNotifier(client *http.Client, now func() time.Time) *Notifier {
	configured := &http.Client{}
	if client != nil {
		*configured = *client
	}
	configured.Timeout = notifierTimeout
	configured.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	if now == nil {
		now = time.Now
	}
	return &Notifier{client: configured, now: now}
}

func (notifier *Notifier) Send(ctx context.Context, webhook, signingSecret string, result Result) error {
	if notifier == nil || notifier.client == nil || ctx == nil ||
		notification.ValidateFeishuWebhook(webhook) != nil || strings.TrimSpace(signingSecret) == "" {
		return errors.New("飞书发布通知配置无效")
	}
	if err := validateNotificationResult(result); err != nil {
		return err
	}
	timestamp := notifier.now().Unix()
	payload := map[string]any{
		"timestamp": strconv.FormatInt(timestamp, 10),
		"sign":      notification.SignFeishu(timestamp, signingSecret),
		"msg_type":  "interactive",
		"card":      releaseCard(result),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return errors.New("生成飞书发布通知失败")
	}
	requestContext, cancel := context.WithTimeout(ctx, notifierTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, http.MethodPost, webhook, bytes.NewReader(body))
	if err != nil {
		return errors.New("创建飞书发布通知请求失败")
	}
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	response, err := notifier.client.Do(request)
	if err != nil {
		return errors.New("发送飞书发布通知失败")
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, notifierResponseLimit+1))
	if err != nil {
		return errors.New("读取飞书发布通知响应失败")
	}
	if len(responseBody) > notifierResponseLimit {
		return errors.New("飞书发布通知响应超过安全限制")
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("飞书发布通知返回 HTTP %d", response.StatusCode)
	}
	var decoded struct {
		Code int `json:"code"`
	}
	if err := json.Unmarshal(responseBody, &decoded); err != nil {
		return errors.New("飞书发布通知响应格式无效")
	}
	if decoded.Code != 0 {
		return fmt.Errorf("飞书发布通知返回业务错误 %d", decoded.Code)
	}
	return nil
}

func validateNotificationResult(result Result) error {
	if result.Operation != OperationDeploy && result.Operation != OperationRollback {
		return errors.New("飞书发布结果操作无效")
	}
	if !validTargetID(result.TargetID) || !actorPattern.MatchString(result.Actor) ||
		!validWorkflowURL(result.WorkflowURL, result.WorkflowRunID, "nolyone1") {
		return errors.New("飞书发布结果来源无效")
	}
	if result.Status != "succeeded" && result.Status != "failed" {
		return errors.New("飞书发布结果状态无效")
	}
	switch result.RollbackStatus {
	case "", "not-required", "succeeded", "failed":
	default:
		return errors.New("飞书发布回滚状态无效")
	}
	if result.Status == "failed" && result.DiagnosticID == "" {
		return errors.New("失败的飞书发布结果缺少诊断编号")
	}
	if !isUTCNonZero(result.StartedAt) || !isUTCNonZero(result.FinishedAt) || result.FinishedAt.Before(result.StartedAt) {
		return errors.New("飞书发布结果时间无效")
	}
	if result.SourceSHA != "" && !lowerHex40Pattern.MatchString(result.SourceSHA) {
		return errors.New("飞书发布结果源提交无效")
	}
	return nil
}

func releaseCard(result Result) map[string]any {
	title := "生产发布成功"
	color := "green"
	if result.Operation == OperationRollback {
		title = "生产回滚成功"
	}
	if result.Status == "failed" {
		color = "red"
		if result.Operation == OperationRollback {
			title = "生产回滚失败"
		} else {
			title = "生产发布失败"
		}
	}
	rollback := map[string]string{
		"": "未记录", "not-required": "无需回滚", "succeeded": "自动回滚成功", "failed": "自动回滚失败",
	}[result.RollbackStatus]
	source := "本地基线"
	if result.SourceSHA != "" {
		source = result.SourceSHA[:12]
	}
	shanghai := time.FixedZone("Asia/Shanghai", 8*60*60)
	content := fmt.Sprintf(
		"**操作：** %s\n**操作者：** %s\n**目标：** %s\n**源提交：** %s\n**结果：** %s\n**回滚：** %s\n**诊断编号：** %s\n**完成时间：** %s",
		map[Operation]string{OperationDeploy: "生产发布", OperationRollback: "人工回滚"}[result.Operation],
		result.Actor, result.TargetID, source, map[string]string{"succeeded": "成功", "failed": "失败"}[result.Status],
		rollback, emptyAsDash(result.DiagnosticID), result.FinishedAt.In(shanghai).Format("2006-01-02 15:04:05 MST"),
	)
	return map[string]any{
		"header": map[string]any{
			"template": color,
			"title":    map[string]string{"tag": "plain_text", "content": title},
		},
		"elements": []any{
			map[string]string{"tag": "markdown", "content": content},
			map[string]any{
				"tag": "action",
				"actions": []any{map[string]any{
					"tag": "button", "type": "primary",
					"text": map[string]string{"tag": "plain_text", "content": "查看 GitHub 运行"},
					"url":  result.WorkflowURL,
				}},
			},
		},
	}
}

func emptyAsDash(value string) string {
	if value == "" {
		return "—"
	}
	return value
}
