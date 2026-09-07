package agentrelease

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"sort"
	"strings"
	"testing"
	"time"

	"yunling.local/platform/internal/artifact"
)

func TestServiceImportsTwoArchitecturesWithoutOverwrite(t *testing.T) {
	repository := newMemoryRepository()
	objects := newMemoryObjectStore()
	service := NewService(repository, objects, time.Now)
	release, err := service.Import(context.Background(), validImportInput("0.2.0"))
	if err != nil || release.Version != "0.2.0" || len(release.Artifacts) != 2 {
		t.Fatalf("导入代理版本失败：release=%+v err=%v", release, err)
	}
	if len(release.Capabilities) != 1 || release.Capabilities[0] != "self_upgrade_v1" {
		t.Fatalf("完整新包必须声明自升级能力：%v", release.Capabilities)
	}
	if objects.putCalls != 2 {
		t.Fatalf("两个架构应各写入一个不可变对象：%d", objects.putCalls)
	}
	_, err = service.Import(context.Background(), validImportInput("0.2.0"))
	if !errors.Is(err, ErrReleaseExists) {
		t.Fatalf("重复版本必须被拒绝：%v", err)
	}
}

func TestServiceAuditsCLIRecommendationWithImportActor(t *testing.T) {
	repository := newMemoryRepository()
	input := validImportInput("0.2.0")
	input.Recommend = true
	input.CreatedBy = "actor-1"
	release, err := NewService(repository, newMemoryObjectStore(), time.Now).Import(context.Background(), input)
	if err != nil || !release.Recommended || repository.recommendationActor != "actor-1" {
		t.Fatalf("CLI 推荐版本必须携带导入操作者：release=%+v actor=%s err=%v", release, repository.recommendationActor, err)
	}
}

func TestServiceValidatesReleaseArtifacts(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*ImportInput)
		want   error
	}{
		{"缺少 amd64", func(input *ImportInput) { input.Artifacts = input.Artifacts[1:] }, ErrReleaseInvalid},
		{"缺少 arm64", func(input *ImportInput) { input.Artifacts = input.Artifacts[:1] }, ErrReleaseInvalid},
		{"摘要不一致", func(input *ImportInput) { input.Artifacts[0].SHA256 = strings.Repeat("0", 64) }, ErrArtifactMismatch},
		{"对象冲突", func(input *ImportInput) {}, artifact.ErrObjectConflict},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := newMemoryRepository()
			objects := newMemoryObjectStore()
			service := NewService(repository, objects, time.Now)
			input := validImportInput("0.2.0")
			test.mutate(&input)
			if test.name == "对象冲突" {
				item := input.Artifacts[0]
				objects.values[ObjectKey(input.Version, Artifact{FileName: item.FileName, SHA256: item.SHA256})] = []byte("冲突内容")
			}
			_, err := service.Import(context.Background(), input)
			if !errors.Is(err, test.want) {
				t.Fatalf("错误=%v，期望=%v", err, test.want)
			}
		})
	}
}

func TestBootstrapFromDirectoryOnlyWhenRepositoryEmpty(t *testing.T) {
	root, _, _ := writeValidCatalog(t)
	repository := newMemoryRepository()
	objects := newMemoryObjectStore()
	service := NewService(repository, objects, time.Now)

	if err := service.BootstrapFromDirectory(context.Background(), root); err != nil {
		t.Fatalf("首次引导旧代理发布：%v", err)
	}
	release, err := service.Recommended(context.Background())
	if err != nil || release.Version != "0.1.0" || len(release.Capabilities) != 0 {
		t.Fatalf("旧包应导入为无升级能力的推荐版本：release=%+v err=%v", release, err)
	}
	writes := objects.putCalls
	if err := service.BootstrapFromDirectory(context.Background(), root); err != nil {
		t.Fatalf("重复引导必须幂等：%v", err)
	}
	if objects.putCalls != writes {
		t.Fatalf("仓库非空时不得再次写对象：before=%d after=%d", writes, objects.putCalls)
	}
}

func validImportInput(version string) ImportInput {
	input := ImportInput{Version: version, ReleaseNotes: "增加自升级器"}
	for _, arch := range []string{"amd64", "arm64"} {
		body := validAgentArchive(version)
		digest := sha256.Sum256(body)
		input.Artifacts = append(input.Artifacts, ImportArtifact{
			OS: "linux", Arch: arch, FileName: "yunling-agent-" + version + "-linux-" + arch + ".tar.gz",
			ByteSize: int64(len(body)), SHA256: hex.EncodeToString(digest[:]), Body: bytes.NewReader(body),
		})
	}
	return input
}

func validAgentArchive(version string) []byte {
	var output bytes.Buffer
	gzipWriter := gzip.NewWriter(&output)
	tarWriter := tar.NewWriter(gzipWriter)
	for name, contents := range map[string]string{
		"yunling-agent": "binary", "install.sh": "#!/bin/sh\n", "yunling-agent.service": "[Service]\n",
		"yunling-run@.service": "[Service]\n", "50-yunling-agent.rules": "rules\n",
		"agent-version": version + "\n", "yunling-agent-upgrade@.service": "[Service]\n",
	} {
		_ = tarWriter.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(contents))})
		_, _ = io.WriteString(tarWriter, contents)
	}
	_ = tarWriter.Close()
	_ = gzipWriter.Close()
	return output.Bytes()
}

type memoryObjectStore struct {
	values   map[string][]byte
	putCalls int
}

func newMemoryObjectStore() *memoryObjectStore {
	return &memoryObjectStore{values: map[string][]byte{}}
}

func (s *memoryObjectStore) Put(_ context.Context, key string, body io.Reader, size int64, digest string) error {
	data, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	if current, ok := s.values[key]; ok {
		currentDigest := sha256.Sum256(current)
		if int64(len(current)) != size || hex.EncodeToString(currentDigest[:]) != digest {
			return artifact.ErrObjectConflict
		}
		return nil
	}
	s.putCalls++
	s.values[key] = append([]byte(nil), data...)
	return nil
}

func (s *memoryObjectStore) Open(_ context.Context, key string) (io.ReadCloser, error) {
	value, ok := s.values[key]
	if !ok {
		return nil, artifact.ErrObjectMissing
	}
	return io.NopCloser(bytes.NewReader(value)), nil
}

type memoryRepository struct {
	releases            []Release
	recommendationActor string
}

func newMemoryRepository() *memoryRepository { return &memoryRepository{} }

func (r *memoryRepository) Create(_ context.Context, release Release) (Release, error) {
	for _, current := range r.releases {
		if current.Version == release.Version {
			return Release{}, ErrReleaseExists
		}
	}
	release.ID = "release-" + release.Version
	r.releases = append(r.releases, cloneRelease(release))
	return cloneRelease(release), nil
}
func (r *memoryRepository) List(context.Context) ([]Release, error) {
	result := make([]Release, len(r.releases))
	for i := range r.releases {
		result[i] = cloneRelease(r.releases[i])
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Version > result[j].Version })
	return result, nil
}
func (r *memoryRepository) Recommended(context.Context) (Release, error) {
	for _, release := range r.releases {
		if release.Recommended {
			return cloneRelease(release), nil
		}
	}
	return Release{}, ErrReleaseNotFound
}
func (r *memoryRepository) SetRecommended(_ context.Context, id string) (Release, error) {
	index := -1
	for i := range r.releases {
		if r.releases[i].ID == id {
			index = i
		}
	}
	if index < 0 {
		return Release{}, ErrReleaseNotFound
	}
	if r.releases[index].Status == ReleaseStatusWithdrawn {
		return Release{}, ErrReleaseWithdrawn
	}
	for i := range r.releases {
		r.releases[i].Recommended = i == index
	}
	return cloneRelease(r.releases[index]), nil
}
func (r *memoryRepository) SetRecommendedBy(ctx context.Context, id, actorID string) (Release, error) {
	r.recommendationActor = actorID
	return r.SetRecommended(ctx, id)
}
func (r *memoryRepository) Withdraw(_ context.Context, id string) (Release, error) {
	for i := range r.releases {
		if r.releases[i].ID != id {
			continue
		}
		if r.releases[i].Recommended {
			return Release{}, ErrRecommendedRelease
		}
		r.releases[i].Status = ReleaseStatusWithdrawn
		return cloneRelease(r.releases[i]), nil
	}
	return Release{}, ErrReleaseNotFound
}
func (r *memoryRepository) FindArtifact(_ context.Context, version, digest, fileName string) (Release, Artifact, error) {
	for _, release := range r.releases {
		if release.Version != version {
			continue
		}
		for _, item := range release.Artifacts {
			if item.SHA256 == digest && item.FileName == fileName {
				return cloneRelease(release), item, nil
			}
		}
	}
	return Release{}, Artifact{}, ErrArtifactNotFound
}

func cloneRelease(release Release) Release {
	release.Capabilities = append([]string(nil), release.Capabilities...)
	release.Artifacts = append([]Artifact(nil), release.Artifacts...)
	return release
}
