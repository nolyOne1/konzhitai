package agent

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

type CredentialDownloader struct {
	credential string
	client     *http.Client
}

func NewCredentialDownloader(credential string, client *http.Client) *CredentialDownloader {
	if client == nil {
		client = http.DefaultClient
	}
	return &CredentialDownloader{credential: credential, client: client}
}

func (d *CredentialDownloader) Download(ctx context.Context, artifactURL string) (io.ReadCloser, error) {
	parsed, err := url.Parse(artifactURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return nil, fmt.Errorf("脚本包地址无效")
	}
	if parsed.Scheme == "http" && !isLoopbackHostname(parsed.Hostname()) {
		return nil, fmt.Errorf("脚本包地址必须使用 HTTPS")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("创建脚本包下载请求：%w", err)
	}
	request.Header.Set("Authorization", "Bearer "+d.credential)
	response, err := d.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("请求脚本包：%w", err)
	}
	if response.StatusCode != http.StatusOK {
		_ = response.Body.Close()
		return nil, fmt.Errorf("中央服务拒绝脚本包下载（状态码 %d）", response.StatusCode)
	}
	if contentType := response.Header.Get("Content-Type"); contentType != "" && !strings.HasPrefix(contentType, "application/gzip") && !strings.HasPrefix(contentType, "application/octet-stream") {
		_ = response.Body.Close()
		return nil, fmt.Errorf("脚本包响应类型无效")
	}
	return response.Body, nil
}
