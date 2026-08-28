package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

const DefaultCredentialsPath = "/etc/yunling-agent/credentials.json"

var ErrInvalidCredentialsFile = errors.New("代理凭据文件内容无效")

type Credentials struct {
	ServerID   string `json:"server_id"`
	Credential string `json:"credential"`
	ControlURL string `json:"control_url"`
}

func ParseAllowedScriptRoots(value string) []string {
	seen := map[string]bool{}
	roots := make([]string, 0)
	for _, root := range filepath.SplitList(value) {
		root = strings.TrimSpace(root)
		if root != "" && !seen[root] {
			seen[root] = true
			roots = append(roots, root)
		}
	}
	return roots
}

func Enroll(ctx context.Context, controlURL, token string) (Credentials, error) {
	endpoint, err := url.Parse(controlURL)
	if err != nil || endpoint.Host == "" || (endpoint.Scheme != "https" && !(endpoint.Scheme == "http" && isLoopbackHostname(endpoint.Hostname()))) {
		return Credentials{}, fmt.Errorf("中央服务地址无效")
	}
	endpoint.Path = strings.TrimRight(endpoint.Path, "/") + "/api/agent/enroll"
	endpoint.RawQuery = ""
	endpoint.Fragment = ""
	body, err := json.Marshal(map[string]string{"token": token})
	if err != nil {
		return Credentials{}, fmt.Errorf("编码代理注册请求：%w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return Credentials{}, fmt.Errorf("创建代理注册请求：%w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return Credentials{}, fmt.Errorf("请求代理注册：%w", err)
	}
	defer response.Body.Close()
	limitedBody := io.LimitReader(response.Body, 1<<20)
	if response.StatusCode != http.StatusCreated {
		var failure struct {
			Message string `json:"message"`
		}
		_ = json.NewDecoder(limitedBody).Decode(&failure)
		if failure.Message == "" {
			failure.Message = "中央服务拒绝代理注册"
		}
		return Credentials{}, errors.New(failure.Message)
	}
	var credentials Credentials
	if err := json.NewDecoder(limitedBody).Decode(&credentials); err != nil {
		return Credentials{}, fmt.Errorf("解析代理注册响应：%w", err)
	}
	credentials.ControlURL = strings.TrimRight(controlURL, "/")
	if credentials.ServerID == "" || credentials.Credential == "" {
		return Credentials{}, ErrInvalidCredentialsFile
	}
	return credentials, nil
}

func SaveCredentials(path string, credentials Credentials) error {
	if credentials.ServerID == "" || credentials.Credential == "" || credentials.ControlURL == "" {
		return ErrInvalidCredentialsFile
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("创建代理配置目录：%w", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("创建代理凭据文件：%w", err)
	}
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(credentials); err != nil {
		_ = file.Close()
		return fmt.Errorf("写入代理凭据：%w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("同步代理凭据：%w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("关闭代理凭据文件：%w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("限制代理凭据权限：%w", err)
	}
	return nil
}

func LoadCredentials(path string) (Credentials, error) {
	file, err := os.Open(path)
	if err != nil {
		return Credentials{}, fmt.Errorf("打开代理凭据文件：%w", err)
	}
	defer file.Close()
	var credentials Credentials
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&credentials); err != nil {
		return Credentials{}, fmt.Errorf("解析代理凭据文件：%w", err)
	}
	if credentials.ServerID == "" || credentials.Credential == "" || credentials.ControlURL == "" {
		return Credentials{}, ErrInvalidCredentialsFile
	}
	return credentials, nil
}
