package agent

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestCredentialDownloaderRejectsPlainHTTPOutsideLoopback(t *testing.T) {
	called := false
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		called = true
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/gzip"}}, Body: io.NopCloser(strings.NewReader("archive"))}, nil
	})}
	body, err := NewCredentialDownloader("agent-secret", client).Download(context.Background(), "http://control.example/artifact")
	if body != nil {
		_ = body.Close()
	}
	if err == nil || called {
		t.Fatalf("非回环地址的脚本下载必须强制 HTTPS：called=%v err=%v", called, err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}
