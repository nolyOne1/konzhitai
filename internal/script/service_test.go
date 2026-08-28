package script_test

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"yunling.local/platform/internal/script"
	"yunling.local/platform/internal/testpostgres"
)

func TestPublishCreatesImmutableContentAddressedVersions(t *testing.T) {
	db := testpostgres.Start(t)
	testpostgres.ApplyInitialMigration(t, db)
	ctx := context.Background()
	userID := insertUser(t, db)
	scriptID := insertScript(t, db, userID)
	objects := newMemoryStore()
	service := script.NewService(db, objects, fixedClock)

	v1, err := service.Publish(ctx, script.PublishInput{
		ScriptID:     scriptID,
		Content:      []byte("echo 1\n"),
		Runtime:      "bash",
		Entrypoint:   "main.sh",
		ReleaseNotes: "首次发布脚本",
		Distribution: script.DistributionRule{Mode: script.DistributionAllCompatible},
		AuthorID:     userID,
	})
	if err != nil {
		t.Fatalf("发布第一个版本：%v", err)
	}
	firstArtifact := append([]byte(nil), objects.bytes(t, v1.ArtifactURI)...)
	v2, err := service.Publish(ctx, script.PublishInput{
		ScriptID:     scriptID,
		Content:      []byte("echo 2\n"),
		Runtime:      "bash",
		Entrypoint:   "main.sh",
		ReleaseNotes: "更新执行内容",
		Distribution: script.DistributionRule{Mode: script.DistributionOnDemand},
		AuthorID:     userID,
	})
	if err != nil {
		t.Fatalf("发布第二个版本：%v", err)
	}

	if v1.Number != 1 || v2.Number != 2 {
		t.Fatalf("版本号必须从 1 递增，实际为 %d、%d", v1.Number, v2.Number)
	}
	if v1.ArtifactSHA256 == v2.ArtifactSHA256 {
		t.Fatal("不同内容必须生成不同校验值")
	}
	wantPrefix := fmt.Sprintf("scripts/%s/", scriptID)
	if !strings.HasPrefix(v1.ArtifactURI, wantPrefix) || !strings.HasSuffix(v1.ArtifactURI, ".tar.gz") {
		t.Fatalf("对象键不符合内容寻址规则：%s", v1.ArtifactURI)
	}
	if got := readArchiveFile(t, firstArtifact, "main.sh"); got != "echo 1\n" {
		t.Fatalf("第一个版本内容被改变：%q", got)
	}
	if got := objects.bytes(t, v1.ArtifactURI); string(got) != string(firstArtifact) {
		t.Fatal("发布新版本不得覆盖旧版本对象")
	}
	listed, err := service.List(ctx)
	if err != nil {
		t.Fatalf("读取脚本列表：%v", err)
	}
	if len(listed) != 1 || listed[0].CurrentVersion != 2 || listed[0].Category != "未分类" || listed[0].Tags == nil {
		t.Fatalf("脚本列表必须包含当前版本和草稿元数据：%+v", listed)
	}
	detail, err := service.Get(ctx, scriptID)
	if err != nil {
		t.Fatalf("读取脚本详情：%v", err)
	}
	if detail.Draft.Content != "echo 2\n" || len(detail.Versions) != 2 {
		t.Fatalf("脚本详情应返回最新草稿和完整版本：%+v", detail)
	}
	content, err := service.VersionContent(ctx, scriptID, v1.ID)
	if err != nil || content != "echo 1\n" {
		t.Fatalf("历史版本内容应在校验后返回：content=%q err=%v", content, err)
	}
}

func TestPublishingIdenticalContentReusesArtifactAndRollbackAppendsVersion(t *testing.T) {
	db := testpostgres.Start(t)
	testpostgres.ApplyInitialMigration(t, db)
	ctx := context.Background()
	userID := insertUser(t, db)
	scriptID := insertScript(t, db, userID)
	objects := newMemoryStore()
	service := script.NewService(db, objects, fixedClock)
	input := script.PublishInput{
		ScriptID:     scriptID,
		Content:      []byte("print('云令')\n"),
		Runtime:      "python3",
		Entrypoint:   "main.py",
		ReleaseNotes: "发布中文示例",
		Distribution: script.DistributionRule{Mode: script.DistributionLabels, Labels: map[string]string{"用途": "批处理"}},
		AuthorID:     userID,
	}
	v1, err := service.Publish(ctx, input)
	if err != nil {
		t.Fatalf("发布第一个版本：%v", err)
	}
	v2, err := service.Publish(ctx, input)
	if err != nil {
		t.Fatalf("再次发布相同内容：%v", err)
	}
	if v1.ArtifactURI != v2.ArtifactURI || objects.count() != 1 {
		t.Fatalf("相同脚本包应复用一个对象：v1=%s v2=%s objects=%d", v1.ArtifactURI, v2.ArtifactURI, objects.count())
	}

	v3, err := service.Rollback(ctx, script.RollbackInput{
		ScriptID:     scriptID,
		VersionID:    v1.ID,
		ReleaseNotes: "回滚到稳定版本",
		AuthorID:     userID,
	})
	if err != nil {
		t.Fatalf("回滚历史版本：%v", err)
	}
	if v3.Number != 3 || v3.ID == v1.ID {
		t.Fatalf("回滚必须追加新版本，实际为 %+v", v3)
	}
	if v3.ArtifactURI != v1.ArtifactURI {
		t.Fatalf("回滚相同内容应复用对象：v1=%s v3=%s", v1.ArtifactURI, v3.ArtifactURI)
	}

	versions, err := service.ListVersions(ctx, scriptID)
	if err != nil {
		t.Fatalf("读取版本历史：%v", err)
	}
	if len(versions) != 3 || versions[0].Number != 3 || versions[2].Number != 1 {
		t.Fatalf("版本历史必须完整且倒序返回：%+v", versions)
	}
}

func TestPublishRejectsIncompleteChineseReleaseInformation(t *testing.T) {
	db := testpostgres.Start(t)
	testpostgres.ApplyInitialMigration(t, db)
	ctx := context.Background()
	userID := insertUser(t, db)
	scriptID := insertScript(t, db, userID)
	service := script.NewService(db, newMemoryStore(), fixedClock)

	tests := []struct {
		name  string
		input script.PublishInput
	}{
		{name: "空发布说明", input: script.PublishInput{ScriptID: scriptID, Content: []byte("echo 1"), Runtime: "bash", Entrypoint: "main.sh", Distribution: script.DistributionRule{Mode: script.DistributionOnDemand}, AuthorID: userID}},
		{name: "非中文发布说明", input: script.PublishInput{ScriptID: scriptID, Content: []byte("echo 1"), Runtime: "bash", Entrypoint: "main.sh", ReleaseNotes: "release one", Distribution: script.DistributionRule{Mode: script.DistributionOnDemand}, AuthorID: userID}},
		{name: "未选择发布目标", input: script.PublishInput{ScriptID: scriptID, Content: []byte("echo 1"), Runtime: "bash", Entrypoint: "main.sh", ReleaseNotes: "发布说明", AuthorID: userID}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := service.Publish(ctx, test.input); err == nil {
				t.Fatal("无效发布信息必须被拒绝")
			}
		})
	}
}

func fixedClock() time.Time {
	return time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC)
}

func insertUser(t *testing.T, db *pgxpool.Pool) string {
	t.Helper()
	var id string
	if err := db.QueryRow(context.Background(), `
		INSERT INTO users (email, display_name, password_hash)
		VALUES ($1, '脚本开发者', 'test-hash')
		RETURNING id
	`, fmt.Sprintf("developer-%d@example.com", time.Now().UnixNano())).Scan(&id); err != nil {
		t.Fatalf("写入测试用户：%v", err)
	}
	return id
}

func insertScript(t *testing.T, db *pgxpool.Pool, userID string) string {
	t.Helper()
	var id string
	if err := db.QueryRow(context.Background(), `
		INSERT INTO scripts (name, description, runtime, created_by)
		VALUES ($1, '测试脚本', 'bash', $2)
		RETURNING id
	`, fmt.Sprintf("数据归档-%d", time.Now().UnixNano()), userID).Scan(&id); err != nil {
		t.Fatalf("写入测试脚本：%v", err)
	}
	return id
}

type memoryStore struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func newMemoryStore() *memoryStore {
	return &memoryStore{objects: map[string][]byte{}}
}

func (s *memoryStore) Put(_ context.Context, key string, body io.Reader, size int64, sha256 string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.objects[key]; exists {
		return nil
	}
	contents, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	if int64(len(contents)) != size || sha256 == "" {
		return fmt.Errorf("对象大小或校验值无效")
	}
	s.objects[key] = contents
	return nil
}

func (s *memoryStore) Open(_ context.Context, key string) (io.ReadCloser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	contents, exists := s.objects[key]
	if !exists {
		return nil, fmt.Errorf("对象不存在")
	}
	return io.NopCloser(strings.NewReader(string(contents))), nil
}

func (s *memoryStore) bytes(t *testing.T, key string) []byte {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	contents, exists := s.objects[key]
	if !exists {
		t.Fatalf("对象不存在：%s", key)
	}
	return append([]byte(nil), contents...)
}

func (s *memoryStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.objects)
}

func readArchiveFile(t *testing.T, archive []byte, name string) string {
	t.Helper()
	compressed, err := gzip.NewReader(strings.NewReader(string(archive)))
	if err != nil {
		t.Fatalf("读取 gzip：%v", err)
	}
	defer compressed.Close()
	reader := tar.NewReader(compressed)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("读取 tar：%v", err)
		}
		if header.Name == name {
			contents, err := io.ReadAll(reader)
			if err != nil {
				t.Fatalf("读取归档文件：%v", err)
			}
			return string(contents)
		}
	}
	t.Fatalf("归档中没有文件 %s", name)
	return ""
}
