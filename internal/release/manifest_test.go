package release

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestDecodeManifestRejectsDuplicateKeysAtEveryDepth(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			name: "根对象重复",
			body: `{"schema_version":1,"candidate_run_id":7,"candidate_run_id":8}`,
		},
		{
			name: "嵌套对象重复",
			body: `{"schema_version":1,"images":{"services":"first","services":"second"}}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := DecodeManifest(strings.NewReader(test.body)); err == nil {
				t.Fatal("重复 JSON 键必须失败")
			}
		})
	}
}

func TestDecodeManifestRejectsUnknownFieldsAndTrailingJSON(t *testing.T) {
	for _, body := range []string{
		`{"schema_version":1,"unexpected":true}`,
		`{"schema_version":1} {"schema_version":1}`,
	} {
		if _, err := DecodeManifest(strings.NewReader(body)); err == nil {
			t.Fatalf("危险清单必须失败：%s", body)
		}
	}
}

func TestDecodeManifestAcceptsOneStrictManifest(t *testing.T) {
	want := validManifest()
	body, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeManifest(strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	if got.CandidateRunID != want.CandidateRunID || got.Images != want.Images || got.Compatibility != want.Compatibility {
		t.Fatalf("解码结果不匹配：got=%+v want=%+v", got, want)
	}
}

func TestValidateManifestAllowsOnlyPinnedYunlingImages(t *testing.T) {
	manifest := validManifest()
	policy := ManifestPolicy{RepositoryID: 42, Owner: "nolyone1"}
	if err := ValidateManifest(manifest, policy); err != nil {
		t.Fatalf("合法清单被拒绝：%v", err)
	}

	tests := []struct {
		name   string
		mutate func(*Manifest)
	}{
		{"错误仓库", func(value *Manifest) { value.RepositoryID = 99 }},
		{"错误所有者", func(value *Manifest) { value.Images.Web = imageRef("other", "yunling-web", 'b') }},
		{"错误镜像名", func(value *Manifest) { value.Images.Ops = imageRef("nolyone1", "yunling-api", 'c') }},
		{"可移动标签", func(value *Manifest) { value.Images.Services = "ghcr.io/nolyone1/yunling-services:latest" }},
		{"大写摘要", func(value *Manifest) { value.Images.Web = imageRef("nolyone1", "yunling-web", 'A') }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := manifest
			test.mutate(&candidate)
			if err := ValidateManifest(candidate, policy); err == nil {
				t.Fatal("越过镜像或仓库边界的清单必须失败")
			}
		})
	}
}

func TestValidateManifestRejectsInvalidMetadata(t *testing.T) {
	policy := ManifestPolicy{RepositoryID: 42, Owner: "nolyone1"}
	tests := []struct {
		name   string
		mutate func(*Manifest)
	}{
		{"格式版本", func(value *Manifest) { value.SchemaVersion = 2 }},
		{"候选运行编号", func(value *Manifest) { value.CandidateRunID = 0 }},
		{"源提交", func(value *Manifest) { value.SourceSHA = strings.Repeat("A", 40) }},
		{"非 UTC 时间", func(value *Manifest) { value.CreatedAt = value.CreatedAt.In(time.FixedZone("CST", 8*60*60)) }},
		{"迁移摘要", func(value *Manifest) { value.Compatibility.MigrationTreeSHA256 = "bad" }},
		{"部署摘要", func(value *Manifest) { value.Compatibility.DeploymentContractSHA256 = strings.Repeat("B", 64) }},
		{"代理版本", func(value *Manifest) { value.Compatibility.AgentVersion = "../0.1.0" }},
		{"代理清单摘要", func(value *Manifest) { value.Compatibility.AgentManifestSHA256 = strings.Repeat("f", 63) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifest := validManifest()
			test.mutate(&manifest)
			if err := ValidateManifest(manifest, policy); err == nil {
				t.Fatal("非法候选元数据必须失败")
			}
		})
	}
}

func validManifest() Manifest {
	return Manifest{
		SchemaVersion:  1,
		CandidateRunID: 123,
		RepositoryID:   42,
		SourceSHA:      strings.Repeat("d", 40),
		CreatedAt:      time.Date(2026, time.September, 3, 8, 0, 0, 0, time.UTC),
		Images: Images{
			Services: imageRef("nolyone1", "yunling-services", 'a'),
			Web:      imageRef("nolyone1", "yunling-web", 'b'),
			Ops:      imageRef("nolyone1", "yunling-ops", 'c'),
		},
		Compatibility: Compatibility{
			MigrationTreeSHA256:      strings.Repeat("1", 64),
			DeploymentContractSHA256: strings.Repeat("2", 64),
			AgentVersion:             "0.1.0",
			AgentManifestSHA256:      strings.Repeat("3", 64),
		},
	}
}

func imageRef(owner, name string, digestByte byte) string {
	return "ghcr.io/" + owner + "/" + name + "@sha256:" + strings.Repeat(string(digestByte), 64)
}
