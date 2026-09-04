package release

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateCandidateRunRejectsUntrustedRuns(t *testing.T) {
	valid := RunMetadata{Workflow: "云令 CI", Conclusion: "success", Branch: "main", Event: "push", RepositoryID: 42}
	cases := []struct {
		name string
		edit func(*RunMetadata)
	}{
		{"错误工作流", func(run *RunMetadata) { run.Workflow = "其他" }},
		{"失败结论", func(run *RunMetadata) { run.Conclusion = "failure" }},
		{"非主分支", func(run *RunMetadata) { run.Branch = "feature" }},
		{"非推送事件", func(run *RunMetadata) { run.Event = "pull_request" }},
		{"外部仓库", func(run *RunMetadata) { run.RepositoryID = 99 }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			run := valid
			test.edit(&run)
			if err := ValidateCandidateRun(run, CandidatePolicy{RepositoryID: 42}); !errors.Is(err, ErrUntrustedCandidateRun) {
				t.Fatalf("不受信任的运行必须失败：%v", err)
			}
		})
	}
	if err := ValidateCandidateRun(valid, CandidatePolicy{RepositoryID: 42}); err != nil {
		t.Fatalf("可信运行被拒绝：%v", err)
	}
}

func TestDecodeRunMetadataAcceptsGitHubShapeAndRejectsTrailingJSON(t *testing.T) {
	body := `{"id":123,"name":"云令 CI","conclusion":"success","head_branch":"main","event":"push","repository":{"id":42,"full_name":"nolyOne1/konzhitai"},"head_commit":{"id":"abc"}}`
	run, err := DecodeRunMetadata(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if run.Workflow != "云令 CI" || run.Branch != "main" || run.RepositoryID != 42 {
		t.Fatalf("GitHub 运行字段映射错误：%+v", run)
	}
	if _, err := DecodeRunMetadata(strings.NewReader(body + `{}`)); err == nil {
		t.Fatal("尾随 JSON 必须失败")
	}
	duplicate := `{"name":"云令 CI","name":"伪造","conclusion":"success","head_branch":"main","event":"push","repository":{"id":42}}`
	if _, err := DecodeRunMetadata(strings.NewReader(duplicate)); err == nil {
		t.Fatal("重复 JSON 键必须失败")
	}
}
