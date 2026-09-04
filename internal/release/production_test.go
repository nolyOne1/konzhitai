package release

import (
	"errors"
	"path/filepath"
	"reflect"
	"testing"
)

func TestValidateProductionInputAllowsOnlyDeployOrHistoricalRollback(t *testing.T) {
	valid := []ProductionInput{
		{Operation: "deploy", TargetID: "123"},
		{Operation: "rollback", TargetID: "456"},
		{Operation: "rollback", TargetID: "bootstrap"},
	}
	for _, input := range valid {
		if err := ValidateProductionInput(input); err != nil {
			t.Fatalf("合法输入被拒绝：%v", err)
		}
	}
	for _, input := range []ProductionInput{
		{Operation: "deploy", TargetID: "bootstrap"},
		{Operation: "deploy", TargetID: "01"},
		{Operation: "shell", TargetID: "123"},
		{Operation: "rollback", TargetID: "../current"},
	} {
		if err := ValidateProductionInput(input); !errors.Is(err, ErrInvalidProductionInput) {
			t.Fatalf("危险输入被接受：%+v err=%v", input, err)
		}
	}
}

func TestSSHArgumentsAreFixedAndFailClosed(t *testing.T) {
	key := filepath.Join(t.TempDir(), "deploy-key")
	knownHosts := filepath.Join(t.TempDir(), "known-hosts")
	got, err := SSHArguments("134.175.131.19", key, knownHosts)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"-i", key,
		"-o", "BatchMode=yes",
		"-o", "IdentitiesOnly=yes",
		"-o", "StrictHostKeyChecking=yes",
		"-o", "UserKnownHostsFile=" + knownHosts,
		"-o", "PasswordAuthentication=no",
		"--", "yunling-deploy@134.175.131.19", "execute",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SSH 参数不匹配：got=%v want=%v", got, want)
	}
	for _, host := range []string{"", "-oProxyCommand=sh", "host name", "host\nother"} {
		if _, err := SSHArguments(host, key, knownHosts); !errors.Is(err, ErrInvalidSSHArguments) {
			t.Fatalf("危险 SSH 主机必须失败：%q err=%v", host, err)
		}
	}
}
