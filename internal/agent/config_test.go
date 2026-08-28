package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestParseAllowedScriptRootsUsesPlatformPathSeparator(t *testing.T) {
	value := strings.Join([]string{" /srv/jobs ", "/opt/team-jobs", "/srv/jobs"}, string(os.PathListSeparator))
	got := ParseAllowedScriptRoots(value)
	if len(got) != 2 || got[0] != "/srv/jobs" || got[1] != "/opt/team-jobs" {
		t.Fatalf("允许目录应去空白并去重：%+v", got)
	}
}

func TestCredentialsRoundTripWithoutBroadFilePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config", "credentials.json")
	want := Credentials{
		ServerID:   "server-1",
		Credential: "agent-secret",
		ControlURL: "https://control.example.com",
	}
	if err := SaveCredentials(path, want); err != nil {
		t.Fatalf("保存代理凭据：%v", err)
	}

	got, err := LoadCredentials(path)
	if err != nil {
		t.Fatalf("读取代理凭据：%v", err)
	}
	if got != want {
		t.Fatalf("读取的代理凭据不一致：got=%+v want=%+v", got, want)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("检查代理凭据权限：%v", err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("代理凭据权限必须为 0600，实际为 %o", info.Mode().Perm())
		}
	}
}

func TestEnrollExchangesOneTimeTokenForAgentCredentials(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/agent/enroll" {
			http.NotFound(w, r)
			return
		}
		var request struct {
			Token string `json:"token"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Token != "one-time-token" {
			http.Error(w, "注册令牌错误", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"server_id":"server-new","credential":"agent-new-secret"}`))
	}))
	defer server.Close()

	got, err := Enroll(context.Background(), server.URL, "one-time-token")
	if err != nil {
		t.Fatalf("使用一次性令牌注册代理：%v", err)
	}
	want := Credentials{
		ServerID:   "server-new",
		Credential: "agent-new-secret",
		ControlURL: server.URL,
	}
	if got != want {
		t.Fatalf("注册返回的代理凭据不完整：got=%+v want=%+v", got, want)
	}
}
