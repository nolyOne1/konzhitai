package release

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxHealthResponseBytes = int64(64 << 10)

var ErrHealthCheckFailed = errors.New("应用健康检查失败")

type HealthChecker interface {
	CheckOnce(context.Context) error
	Wait(context.Context, time.Duration, time.Duration) error
}

type DockerHealthChecker struct {
	Config HostConfig
	Runner CommandRunner
	Client *http.Client
}

func NewDockerHealthChecker(config HostConfig, runner CommandRunner, client *http.Client) (*DockerHealthChecker, error) {
	if runner == nil {
		return nil, errors.New("健康检查命令执行器为空")
	}
	secureClient, err := secureHealthClient(client)
	if err != nil {
		return nil, err
	}
	config = normalizeHostConfig(config)
	if config.PublicBaseURL != "" {
		parsed, err := url.Parse(config.PublicBaseURL)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			return nil, errors.New("公网健康检查地址必须是无凭据、查询或片段的 HTTPS 地址")
		}
	}
	return &DockerHealthChecker{Config: config, Runner: runner, Client: secureClient}, nil
}

func (checker *DockerHealthChecker) CheckOnce(ctx context.Context) error {
	if checker == nil || ctx == nil || checker.Runner == nil || checker.Client == nil {
		return ErrHealthCheckFailed
	}
	for _, container := range []string{
		"yunling-api-1", "yunling-scheduler-1", "yunling-web-1", "yunling-ops-1",
	} {
		result, err := runSuccessful(ctx, checker.Runner, "docker", []string{
			"inspect", "--format", "{{json .State.Health.Status}}", container,
		})
		if err != nil {
			return fmt.Errorf("%w：容器 %s：%v", ErrHealthCheckFailed, container, err)
		}
		var status string
		if err := json.Unmarshal([]byte(strings.TrimSpace(string(result.Stdout))), &status); err != nil || status != "healthy" {
			return fmt.Errorf("%w：容器 %s health=%q", ErrHealthCheckFailed, container, status)
		}
	}
	for _, check := range []struct {
		container string
		url       string
	}{
		{container: "yunling-api-1", url: "http://127.0.0.1:8080/api/health"},
		{container: "yunling-web-1", url: "http://127.0.0.1:8080/healthz"},
		{container: "yunling-ops-1", url: "http://127.0.0.1:8081/healthz"},
	} {
		if _, err := runSuccessful(ctx, checker.Runner, "docker", []string{
			"exec", check.container, "wget", "-qO-", "--timeout=5", check.url,
		}); err != nil {
			return fmt.Errorf("%w：容器内探测 %s：%v", ErrHealthCheckFailed, check.container, err)
		}
	}
	if checker.Config.PublicBaseURL == "" {
		return nil
	}
	for _, path := range []string{"/healthz", "/api/health"} {
		if err := checker.checkPublic(ctx, path); err != nil {
			return err
		}
	}
	return nil
}

func (checker *DockerHealthChecker) Wait(ctx context.Context, timeout, interval time.Duration) error {
	if checker == nil || ctx == nil || timeout <= 0 || interval <= 0 {
		return ErrHealthCheckFailed
	}
	waitContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var lastErr error
	for {
		if err := checker.CheckOnce(waitContext); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if waitContext.Err() != nil {
			return fmt.Errorf("%w：等待超时：%v", ErrHealthCheckFailed, lastErr)
		}
		timer := time.NewTimer(interval)
		select {
		case <-waitContext.Done():
			timer.Stop()
			return fmt.Errorf("%w：等待超时：%v", ErrHealthCheckFailed, lastErr)
		case <-timer.C:
		}
	}
}

func (checker *DockerHealthChecker) checkPublic(ctx context.Context, path string) error {
	base, err := url.Parse(checker.Config.PublicBaseURL)
	if err != nil {
		return fmt.Errorf("%w：公网地址无效", ErrHealthCheckFailed)
	}
	base.Path = path
	base.RawPath = ""
	base.RawQuery = ""
	base.Fragment = ""
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, base.String(), nil)
	if err != nil {
		return fmt.Errorf("%w：创建公网探测请求：%v", ErrHealthCheckFailed, err)
	}
	request.Header.Set("Accept", "application/json")
	response, err := checker.Client.Do(request)
	if err != nil {
		return fmt.Errorf("%w：公网路径 %s：%v", ErrHealthCheckFailed, path, err)
	}
	if response.Body == nil {
		return fmt.Errorf("%w：公网路径 %s 响应正文为空", ErrHealthCheckFailed, path)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxHealthResponseBytes+1))
	if err != nil {
		return fmt.Errorf("%w：读取公网路径 %s：%v", ErrHealthCheckFailed, path, err)
	}
	if int64(len(body)) > maxHealthResponseBytes {
		return fmt.Errorf("%w：公网路径 %s 响应超过 64 KiB", ErrHealthCheckFailed, path)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("%w：公网路径 %s HTTP %d", ErrHealthCheckFailed, path, response.StatusCode)
	}
	return nil
}

func secureHealthClient(client *http.Client) (*http.Client, error) {
	if client == nil {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
		client = &http.Client{Transport: transport}
	} else {
		clone := *client
		client = &clone
		if transport, ok := client.Transport.(*http.Transport); ok {
			transport = transport.Clone()
			if transport.TLSClientConfig == nil {
				transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
			} else {
				transport.TLSClientConfig = transport.TLSClientConfig.Clone()
				if transport.TLSClientConfig.InsecureSkipVerify {
					return nil, errors.New("公网健康检查禁止关闭 TLS 证书校验")
				}
				if transport.TLSClientConfig.MinVersion < tls.VersionTLS12 {
					transport.TLSClientConfig.MinVersion = tls.VersionTLS12
				}
			}
			client.Transport = transport
		}
	}
	client.Timeout = 5 * time.Second
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return client, nil
}
