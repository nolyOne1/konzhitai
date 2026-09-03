package release

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

func TestPublicHealthDoesNotDisableTLSVerification(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/healthz" && request.URL.Path != "/api/health" {
			http.NotFound(response, request)
			return
		}
		response.Header().Set("Content-Type", "application/json")
		response.WriteHeader(http.StatusOK)
		_, _ = response.Write([]byte(`{"status":"ok"}`))
	}))
	defer server.Close()

	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
		MinVersion: tls.VersionTLS12,
		RootCAs:    roots,
	}}}
	runner := healthyApplicationRunner()
	checker, err := NewDockerHealthChecker(HostConfig{PublicBaseURL: server.URL}, runner, client)
	if err != nil {
		t.Fatal(err)
	}
	if err := checker.CheckOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	transport := checker.Client.Transport.(*http.Transport)
	if transport.TLSClientConfig.InsecureSkipVerify {
		t.Fatal("公网检查不得关闭 TLS 校验")
	}
	if checker.Client.Timeout != 5*time.Second {
		t.Fatalf("公网检查超时=%s", checker.Client.Timeout)
	}
	if checker.Client.CheckRedirect == nil {
		t.Fatal("公网检查必须禁用重定向")
	}
	if err := checker.Client.CheckRedirect(nil, nil); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatalf("公网检查重定向策略无效：%v", err)
	}
}

func TestHealthCheckUsesFixedContainerAndInternalCommands(t *testing.T) {
	runner := healthyApplicationRunner()
	checker, err := NewDockerHealthChecker(HostConfig{}, runner, &http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := checker.CheckOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []commandCall{
		{name: "docker", args: []string{"inspect", "--format", "{{json .State.Health.Status}}", "yunling-api-1"}},
		{name: "docker", args: []string{"inspect", "--format", "{{json .State.Health.Status}}", "yunling-scheduler-1"}},
		{name: "docker", args: []string{"inspect", "--format", "{{json .State.Health.Status}}", "yunling-web-1"}},
		{name: "docker", args: []string{"inspect", "--format", "{{json .State.Health.Status}}", "yunling-ops-1"}},
		{name: "docker", args: []string{"exec", "yunling-api-1", "wget", "-qO-", "--timeout=5", "http://127.0.0.1:8080/api/health"}},
		{name: "docker", args: []string{"exec", "yunling-web-1", "wget", "-qO-", "--timeout=5", "http://127.0.0.1:8080/healthz"}},
		{name: "docker", args: []string{"exec", "yunling-ops-1", "wget", "-qO-", "--timeout=5", "http://127.0.0.1:8081/healthz"}},
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("健康检查命令不匹配：\ngot=%#v\nwant=%#v", runner.calls, want)
	}
}

func TestHealthRejectsOversizedPublicResponse(t *testing.T) {
	checker, err := NewDockerHealthChecker(HostConfig{PublicBaseURL: "https://example.invalid"}, healthyApplicationRunner(), &http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       &repeatingBody{remaining: 64<<10 + 1},
			}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := checker.CheckOnce(context.Background()); !errors.Is(err, ErrHealthCheckFailed) {
		t.Fatalf("超大响应必须失败：%v", err)
	}
}

func TestHealthWaitRetriesUntilAllLayersRecover(t *testing.T) {
	results := make([]CommandResult, 8)
	results[0].Stdout = []byte(`"starting"` + "\n")
	for index := 1; index < len(results); index++ {
		if index < 5 {
			results[index].Stdout = []byte(`"healthy"` + "\n")
		}
	}
	runner := &recordingRunner{results: results}
	checker, err := NewDockerHealthChecker(HostConfig{}, runner, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := checker.Wait(context.Background(), 200*time.Millisecond, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 8 {
		t.Fatalf("恢复后命令数=%d，期望第一次失败 1 条加第二次完整 7 条", len(runner.calls))
	}
}

func TestHealthWaitDeadlineCancelsAnInFlightCheck(t *testing.T) {
	checker, err := NewDockerHealthChecker(HostConfig{}, blockingCommandRunner{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	err = checker.Wait(context.Background(), 25*time.Millisecond, time.Millisecond)
	if !errors.Is(err, ErrHealthCheckFailed) {
		t.Fatalf("实际错误：%v", err)
	}
	if elapsed := time.Since(started); elapsed >= 100*time.Millisecond {
		t.Fatalf("进行中的探测未被总超时取消：%s", elapsed)
	}
}

func healthyApplicationRunner() *recordingRunner {
	results := make([]CommandResult, 7)
	for index := 0; index < 4; index++ {
		results[index].Stdout = []byte(`"healthy"` + "\n")
	}
	return &recordingRunner{results: results}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

type repeatingBody struct {
	remaining int
}

func (body *repeatingBody) Read(buffer []byte) (int, error) {
	if body.remaining == 0 {
		return 0, io.EOF
	}
	if len(buffer) > body.remaining {
		buffer = buffer[:body.remaining]
	}
	for index := range buffer {
		buffer[index] = 'x'
	}
	body.remaining -= len(buffer)
	return len(buffer), nil
}

func (*repeatingBody) Close() error { return nil }

type blockingCommandRunner struct{}

func (blockingCommandRunner) Run(ctx context.Context, _ string, _ []string, _ []byte) (CommandResult, error) {
	select {
	case <-ctx.Done():
		return CommandResult{}, ctx.Err()
	case <-time.After(150 * time.Millisecond):
		return CommandResult{}, errors.New("测试阻塞结束")
	}
}
