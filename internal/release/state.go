package release

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"time"
)

const (
	currentStateFile     = "current.json"
	previousStateFile    = "previous.json"
	stateTransactionFile = "state-transaction.json"
	auditFile            = "audit.jsonl"
	releaseManifestFile  = "release-manifest.json"
	releaseSuccessFile   = "successful.json"
)

var (
	ErrInvalidRelease       = errors.New("发布版本无效")
	ErrReleaseExists        = errors.New("发布历史已存在")
	ErrReleaseNotFound      = errors.New("发布历史不存在")
	ErrReleaseNotSuccessful = errors.New("发布历史尚未成功")
	targetIDPattern         = regexp.MustCompile(`^[1-9][0-9]{0,18}$`)
	bootstrapImagePattern   = regexp.MustCompile(`^yunling-local-bootstrap/(api|scheduler|web|ops):[0-9a-f]{12}$`)
	ghcrStoredImagePattern  = regexp.MustCompile(`^ghcr\.io/([a-z0-9](?:[a-z0-9-]{0,38}))/yunling-(services|web|ops)@sha256:[0-9a-f]{64}$`)
)

type ReleaseOrigin string

const (
	OriginGHCR      ReleaseOrigin = "ghcr"
	OriginBootstrap ReleaseOrigin = "local-bootstrap"
)

type ServiceImages struct {
	API       string `json:"api"`
	Scheduler string `json:"scheduler"`
	Web       string `json:"web"`
	Ops       string `json:"ops"`
}

type StoredRelease struct {
	TargetID      string        `json:"target_id"`
	Origin        ReleaseOrigin `json:"origin"`
	SourceSHA     string        `json:"source_sha,omitempty"`
	Images        ServiceImages `json:"images"`
	Compatibility Compatibility `json:"compatibility"`
	SuccessfulAt  time.Time     `json:"successful_at,omitempty"`
	validated     bool
}

type AuditEvent struct {
	Operation      string    `json:"operation"`
	TargetID       string    `json:"target_id"`
	Status         string    `json:"status"`
	OccurredAt     time.Time `json:"occurred_at"`
	Actor          string    `json:"actor,omitempty"`
	WorkflowRunID  int64     `json:"workflow_run_id,omitempty"`
	WorkflowURL    string    `json:"workflow_url,omitempty"`
	SourceSHA      string    `json:"source_sha,omitempty"`
	RollbackStatus string    `json:"rollback_status,omitempty"`
	DiagnosticID   string    `json:"diagnostic_id,omitempty"`
}

type stateTransaction struct {
	Current  StoredRelease  `json:"current"`
	Previous *StoredRelease `json:"previous,omitempty"`
}

type StateStore struct {
	root string
}

func NewStateStore(root string) *StateStore {
	return &StateStore{root: root}
}

func NewStoredRelease(manifest Manifest, policy ManifestPolicy) (StoredRelease, error) {
	if err := ValidateManifest(manifest, policy); err != nil {
		return StoredRelease{}, fmt.Errorf("%w：%v", ErrInvalidRelease, err)
	}
	return StoredRelease{
		TargetID:  strconv.FormatInt(manifest.CandidateRunID, 10),
		Origin:    OriginGHCR,
		SourceSHA: manifest.SourceSHA,
		Images: ServiceImages{
			API: manifest.Images.Services, Scheduler: manifest.Images.Services,
			Web: manifest.Images.Web, Ops: manifest.Images.Ops,
		},
		Compatibility: manifest.Compatibility,
		validated:     true,
	}, nil
}

func (store *StateStore) SaveValidated(release StoredRelease) error {
	if store == nil || !release.validated || release.Origin != OriginGHCR {
		return ErrInvalidRelease
	}
	if err := validateStoredRelease(release, false); err != nil {
		return err
	}
	candidate := release
	candidate.SuccessfulAt = time.Time{}
	return store.saveHistory(candidate)
}

func (store *StateStore) CreateBootstrap(images ServiceImages, compatibility Compatibility, successfulAt time.Time) (StoredRelease, error) {
	if store == nil {
		return StoredRelease{}, ErrInvalidRelease
	}
	release := StoredRelease{
		TargetID: "bootstrap", Origin: OriginBootstrap, Images: images,
		Compatibility: compatibility, SuccessfulAt: successfulAt, validated: true,
	}
	if err := validateStoredRelease(release, true); err != nil {
		return StoredRelease{}, err
	}
	if err := store.saveHistory(release); err != nil {
		return StoredRelease{}, err
	}
	if err := store.writeSuccess(release); err != nil {
		return StoredRelease{}, err
	}
	if err := store.commitState(release); err != nil {
		return StoredRelease{}, err
	}
	return release, nil
}

func (store *StateStore) CommitSuccess(release StoredRelease) error {
	if store == nil || !release.validated {
		return ErrInvalidRelease
	}
	if err := validateStoredRelease(release, true); err != nil {
		return err
	}
	validated, err := store.loadReleaseFile(filepath.Join(store.root, release.TargetID, releaseManifestFile), false)
	if err != nil {
		return fmt.Errorf("读取已验证候选：%w", err)
	}
	if !sameReleaseIdentity(validated, release) {
		return fmt.Errorf("%w：成功版本与已验证候选不一致", ErrInvalidRelease)
	}
	if err := store.writeSuccess(release); err != nil {
		return err
	}
	return store.commitState(release)
}

func (store *StateStore) LoadCurrent() (StoredRelease, error) {
	if store == nil {
		return StoredRelease{}, ErrInvalidRelease
	}
	if err := store.recoverState(); err != nil {
		return StoredRelease{}, err
	}
	return store.loadReleaseFile(filepath.Join(store.root, currentStateFile), true)
}

func (store *StateStore) LoadPrevious() (StoredRelease, error) {
	if store == nil {
		return StoredRelease{}, ErrInvalidRelease
	}
	if err := store.recoverState(); err != nil {
		return StoredRelease{}, err
	}
	return store.loadReleaseFile(filepath.Join(store.root, previousStateFile), true)
}

func (store *StateStore) LoadTarget(targetID string) (StoredRelease, error) {
	if store == nil || !validTargetID(targetID) {
		return StoredRelease{}, ErrInvalidRelease
	}
	if err := store.recoverState(); err != nil {
		return StoredRelease{}, err
	}
	successPath := filepath.Join(store.root, targetID, releaseSuccessFile)
	release, err := store.loadReleaseFile(successPath, true)
	if err == nil {
		return release, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return StoredRelease{}, err
	}
	if _, manifestErr := os.Stat(filepath.Join(store.root, targetID, releaseManifestFile)); manifestErr == nil {
		return StoredRelease{}, ErrReleaseNotSuccessful
	} else if !errors.Is(manifestErr, os.ErrNotExist) {
		return StoredRelease{}, manifestErr
	}
	return StoredRelease{}, ErrReleaseNotFound
}

func (store *StateStore) AppendAudit(event AuditEvent) error {
	if store == nil || (event.Operation != "deploy" && event.Operation != "rollback") ||
		!validTargetID(event.TargetID) || event.Status == "" || !isUTCNonZero(event.OccurredAt) {
		return ErrInvalidRelease
	}
	if err := store.ensureRoot(); err != nil {
		return err
	}
	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("编码审计事件：%w", err)
	}
	file, err := os.OpenFile(filepath.Join(store.root, auditFile), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("打开发布审计：%w", err)
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		file.Close()
		return fmt.Errorf("写入发布审计：%w", err)
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return fmt.Errorf("同步发布审计：%w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("关闭发布审计：%w", err)
	}
	return nil
}

func (store *StateStore) saveHistory(release StoredRelease) error {
	if err := store.ensureRoot(); err != nil {
		return err
	}
	targetDir := filepath.Join(store.root, release.TargetID)
	if _, err := os.Lstat(targetDir); err == nil {
		return ErrReleaseExists
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	temporaryDir, err := os.MkdirTemp(store.root, ".release-")
	if err != nil {
		return fmt.Errorf("创建发布历史临时目录：%w", err)
	}
	defer os.RemoveAll(temporaryDir)
	if err := os.Chmod(temporaryDir, 0o700); err != nil {
		return fmt.Errorf("限制发布历史临时目录权限：%w", err)
	}
	if err := writeJSONAtomic(filepath.Join(temporaryDir, releaseManifestFile), release); err != nil {
		return err
	}
	if err := os.Rename(temporaryDir, targetDir); err != nil {
		if _, statErr := os.Lstat(targetDir); statErr == nil {
			return ErrReleaseExists
		}
		return fmt.Errorf("保存发布历史：%w", err)
	}
	return syncDirectory(store.root)
}

func (store *StateStore) writeSuccess(release StoredRelease) error {
	path := filepath.Join(store.root, release.TargetID, releaseSuccessFile)
	data, err := marshalJSONLine(release)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if errors.Is(err, os.ErrExist) {
		existing, readErr := os.ReadFile(path)
		if readErr == nil && bytes.Equal(existing, data) {
			return nil
		}
		if readErr == nil {
			var recorded StoredRelease
			if decodeErr := decodeStrictJSON(bytes.NewReader(existing), &recorded); decodeErr == nil &&
				sameReleaseIdentity(recorded, release) {
				return nil
			}
		}
		return ErrReleaseExists
	}
	if err != nil {
		return fmt.Errorf("创建成功发布记录：%w", err)
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return fmt.Errorf("写入成功发布记录：%w", err)
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return fmt.Errorf("同步成功发布记录：%w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("关闭成功发布记录：%w", err)
	}
	return syncDirectory(filepath.Dir(path))
}

func (store *StateStore) commitState(release StoredRelease) error {
	if err := store.recoverState(); err != nil {
		return err
	}
	var previous *StoredRelease
	current, err := store.loadReleaseFile(filepath.Join(store.root, currentStateFile), true)
	if err == nil {
		previous = &current
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	transaction := stateTransaction{Current: release, Previous: previous}
	if err := writeJSONAtomic(filepath.Join(store.root, stateTransactionFile), transaction); err != nil {
		return err
	}
	return store.recoverState()
}

func (store *StateStore) recoverState() error {
	if err := store.ensureRoot(); err != nil {
		return err
	}
	path := filepath.Join(store.root, stateTransactionFile)
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("打开状态事务：%w", err)
	}
	var transaction stateTransaction
	decodeErr := decodeStrictJSON(file, &transaction)
	closeErr := file.Close()
	if decodeErr != nil {
		return fmt.Errorf("解析状态事务：%w", decodeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("关闭状态事务：%w", closeErr)
	}
	if err := validateStoredRelease(transaction.Current, true); err != nil {
		return err
	}
	if transaction.Previous != nil {
		if err := validateStoredRelease(*transaction.Previous, true); err != nil {
			return err
		}
		if err := writeJSONAtomic(filepath.Join(store.root, previousStateFile), *transaction.Previous); err != nil {
			return err
		}
	} else if err := os.Remove(filepath.Join(store.root, previousStateFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("清理上一版本状态：%w", err)
	}
	if err := writeJSONAtomic(filepath.Join(store.root, currentStateFile), transaction.Current); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("完成状态事务：%w", err)
	}
	return syncDirectory(store.root)
}

func (store *StateStore) loadReleaseFile(path string, requireSuccess bool) (StoredRelease, error) {
	file, err := os.Open(path)
	if err != nil {
		return StoredRelease{}, err
	}
	defer file.Close()
	var release StoredRelease
	if err := decodeStrictJSON(file, &release); err != nil {
		return StoredRelease{}, fmt.Errorf("解析发布状态 %s：%w", path, err)
	}
	if err := validateStoredRelease(release, requireSuccess); err != nil {
		return StoredRelease{}, err
	}
	release.validated = true
	return release, nil
}

func (store *StateStore) ensureRoot() error {
	if store.root == "" {
		return ErrInvalidRelease
	}
	info, err := os.Lstat(store.root)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(store.root, 0o700); err != nil {
			return fmt.Errorf("创建发布状态目录：%w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("读取发布状态目录：%w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w：发布状态根路径必须是目录", ErrInvalidRelease)
	}
	return nil
}

func validateStoredRelease(release StoredRelease, requireSuccess bool) error {
	if !validCompatibility(release.Compatibility) {
		return fmt.Errorf("%w：兼容性摘要无效", ErrInvalidRelease)
	}
	if requireSuccess && !isUTCNonZero(release.SuccessfulAt) {
		return fmt.Errorf("%w：成功时间无效", ErrInvalidRelease)
	}
	if !release.SuccessfulAt.IsZero() && !isUTCNonZero(release.SuccessfulAt) {
		return fmt.Errorf("%w：成功时间必须使用 UTC", ErrInvalidRelease)
	}
	switch release.Origin {
	case OriginGHCR:
		if !targetIDPattern.MatchString(release.TargetID) || !lowerHex40Pattern.MatchString(release.SourceSHA) {
			return ErrInvalidRelease
		}
		servicesOwner, servicesName, ok := parseStoredGHCRImage(release.Images.API)
		if !ok || servicesName != "services" || release.Images.Scheduler != release.Images.API {
			return ErrInvalidRelease
		}
		webOwner, webName, webOK := parseStoredGHCRImage(release.Images.Web)
		opsOwner, opsName, opsOK := parseStoredGHCRImage(release.Images.Ops)
		if !webOK || !opsOK || webName != "web" || opsName != "ops" || webOwner != servicesOwner || opsOwner != servicesOwner {
			return ErrInvalidRelease
		}
	case OriginBootstrap:
		if release.TargetID != "bootstrap" || release.SourceSHA != "" {
			return ErrInvalidRelease
		}
		for service, image := range map[string]string{
			"api": release.Images.API, "scheduler": release.Images.Scheduler,
			"web": release.Images.Web, "ops": release.Images.Ops,
		} {
			match := bootstrapImagePattern.FindStringSubmatch(image)
			if len(match) != 2 || match[1] != service {
				return ErrInvalidRelease
			}
		}
	default:
		return ErrInvalidRelease
	}
	return nil
}

func validCompatibility(value Compatibility) bool {
	return lowerHex64Pattern.MatchString(value.MigrationTreeSHA256) &&
		lowerHex64Pattern.MatchString(value.DeploymentContractSHA256) &&
		versionPattern.MatchString(value.AgentVersion) &&
		lowerHex64Pattern.MatchString(value.AgentManifestSHA256)
}

func parseStoredGHCRImage(value string) (string, string, bool) {
	match := ghcrStoredImagePattern.FindStringSubmatch(value)
	if len(match) != 3 {
		return "", "", false
	}
	return match[1], match[2], true
}

func validTargetID(value string) bool {
	return value == "bootstrap" || targetIDPattern.MatchString(value)
}

func isUTCNonZero(value time.Time) bool {
	if value.IsZero() {
		return false
	}
	_, offset := value.Zone()
	return offset == 0
}

func sameReleaseIdentity(left, right StoredRelease) bool {
	left.SuccessfulAt = time.Time{}
	right.SuccessfulAt = time.Time{}
	left.validated = false
	right.validated = false
	return left == right
}

func writeJSONAtomic(path string, value any) error {
	data, err := marshalJSONLine(value)
	if err != nil {
		return err
	}
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".tmp-")
	if err != nil {
		return fmt.Errorf("创建状态临时文件：%w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("限制状态临时文件权限：%w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return fmt.Errorf("写入状态临时文件：%w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("同步状态临时文件：%w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("关闭状态临时文件：%w", err)
	}
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w：状态目标不能是符号链接", ErrInvalidRelease)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("原子替换状态文件：%w", err)
	}
	return syncDirectory(directory)
}

func marshalJSONLine(value any) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("编码发布状态：%w", err)
	}
	return append(data, '\n'), nil
}

func syncDirectory(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	directory, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("打开状态目录：%w", err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("同步状态目录：%w", err)
	}
	return nil
}
