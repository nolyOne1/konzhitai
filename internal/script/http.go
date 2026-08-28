package script

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"yunling.local/platform/internal/auth"
)

type Manager interface {
	List(context.Context) ([]Script, error)
	Get(context.Context, string) (Detail, error)
	Create(context.Context, CreateInput) (Script, error)
	SaveDraft(context.Context, DraftInput) (Draft, error)
	Publish(context.Context, PublishInput) (Version, error)
	Rollback(context.Context, RollbackInput) (Version, error)
	ListVersions(context.Context, string) ([]Version, error)
	VersionContent(context.Context, string, string) (string, error)
}

type SyncManager interface {
	PrepareVersion(context.Context, string) (int64, error)
	List(context.Context, string) ([]SyncView, error)
	Retry(context.Context, string) error
}

type HandlerOption func(*handlerOptions)

type handlerOptions struct{ sync SyncManager }

func WithSyncManager(sync SyncManager) HandlerOption {
	return func(options *handlerOptions) { options.sync = sync }
}

func Handler(manager Manager, options ...HandlerOption) http.Handler {
	configuration := handlerOptions{}
	for _, option := range options {
		option(&configuration)
	}
	router := http.NewServeMux()
	router.Handle("GET /api/scripts", auth.Require(auth.PermissionRead)(listHandler(manager)))
	router.Handle("POST /api/scripts", auth.Require(auth.PermissionPublishScript)(createHandler(manager)))
	router.Handle("POST /api/scripts/import", auth.Require(auth.PermissionPublishScript)(importHandler(manager)))
	router.Handle("GET /api/scripts/{id}", auth.Require(auth.PermissionRead)(detailHandler(manager)))
	router.Handle("PUT /api/scripts/{id}/draft", auth.Require(auth.PermissionPublishScript)(draftHandler(manager)))
	router.Handle("POST /api/scripts/{id}/publish", auth.Require(auth.PermissionPublishScript)(publishHandler(manager, configuration.sync)))
	router.Handle("GET /api/scripts/{id}/versions", auth.Require(auth.PermissionRead)(versionsHandler(manager)))
	router.Handle("GET /api/scripts/{id}/versions/{versionID}/content", auth.Require(auth.PermissionRead)(versionContentHandler(manager)))
	router.Handle("POST /api/scripts/{id}/rollback", auth.Require(auth.PermissionPublishScript)(rollbackHandler(manager, configuration.sync)))
	if configuration.sync != nil {
		router.Handle("GET /api/scripts/{id}/syncs", auth.Require(auth.PermissionRead)(syncListHandler(configuration.sync)))
		router.Handle("POST /api/scripts/{id}/syncs/{syncID}/retry", auth.Require(auth.PermissionExecute)(syncRetryHandler(configuration.sync)))
	}
	return router
}

func listHandler(manager Manager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		scripts, err := manager.List(r.Context())
		if err != nil {
			writeScriptError(w, http.StatusInternalServerError, "读取脚本列表失败")
			return
		}
		if scripts == nil {
			scripts = []Script{}
		}
		writeScriptJSON(w, http.StatusOK, map[string]any{"scripts": scripts})
	})
}

func detailHandler(manager Manager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		detail, err := manager.Get(r.Context(), r.PathValue("id"))
		if writeKnownScriptError(w, err) {
			return
		}
		if err != nil {
			writeScriptError(w, http.StatusInternalServerError, "读取脚本详情失败")
			return
		}
		if detail.Versions == nil {
			detail.Versions = []Version{}
		}
		writeScriptJSON(w, http.StatusOK, detail)
	})
}

func createHandler(manager Manager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Name        string   `json:"name"`
			Description string   `json:"description"`
			Runtime     string   `json:"runtime"`
			Entrypoint  string   `json:"entrypoint"`
			Category    string   `json:"category"`
			Tags        []string `json:"tags"`
			Content     string   `json:"content"`
		}
		if err := decodeScriptJSON(w, r, &request); err != nil {
			writeScriptError(w, http.StatusBadRequest, "脚本信息格式不正确")
			return
		}
		principal, _ := auth.PrincipalFromContext(r.Context())
		created, err := manager.Create(r.Context(), CreateInput{
			Name: request.Name, Description: request.Description, Runtime: request.Runtime,
			Entrypoint: request.Entrypoint, Category: request.Category, Tags: request.Tags,
			Content: []byte(request.Content), AuthorID: principal.UserID,
		})
		if writeKnownScriptError(w, err) {
			return
		}
		if err != nil {
			writeScriptError(w, http.StatusInternalServerError, "创建脚本失败")
			return
		}
		writeScriptJSON(w, http.StatusCreated, created)
	})
}

func importHandler(manager Manager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, MaxScriptBytes+(64<<10))
		if err := r.ParseMultipartForm(MaxScriptBytes + (64 << 10)); err != nil {
			writeScriptError(w, http.StatusBadRequest, "导入文件不能超过 1 MB")
			return
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			writeScriptError(w, http.StatusBadRequest, "请选择要导入的脚本文件")
			return
		}
		defer file.Close()
		runtime, entrypoint, ok := importedScriptType(header)
		if !ok {
			writeScriptError(w, http.StatusBadRequest, "仅支持 .sh、.py、.js 和 .ps1 文本脚本")
			return
		}
		content, err := readImportedScript(file)
		if err != nil {
			writeScriptError(w, http.StatusBadRequest, err.Error())
			return
		}
		name := strings.TrimSpace(r.FormValue("name"))
		if name == "" {
			name = strings.TrimSuffix(entrypoint, filepath.Ext(entrypoint))
		}
		principal, _ := auth.PrincipalFromContext(r.Context())
		created, err := manager.Create(r.Context(), CreateInput{
			Name: name, Description: strings.TrimSpace(r.FormValue("description")), Runtime: runtime,
			Entrypoint: entrypoint, Content: content, AuthorID: principal.UserID,
		})
		if writeKnownScriptError(w, err) {
			return
		}
		if err != nil {
			writeScriptError(w, http.StatusInternalServerError, "导入脚本失败")
			return
		}
		writeScriptJSON(w, http.StatusCreated, created)
	})
}

func draftHandler(manager Manager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request editorRequest
		if err := decodeScriptJSON(w, r, &request); err != nil {
			writeScriptError(w, http.StatusBadRequest, "脚本草稿格式不正确")
			return
		}
		principal, _ := auth.PrincipalFromContext(r.Context())
		draft, err := manager.SaveDraft(r.Context(), DraftInput{
			ScriptID: r.PathValue("id"), Content: []byte(request.Content), Runtime: request.Runtime,
			Entrypoint: request.Entrypoint, Distribution: request.Distribution,
			Category: request.Category, Tags: request.Tags,
			ParameterDefinitions: request.ParameterDefinitions, Resources: request.Resources, AuthorID: principal.UserID,
		})
		if writeKnownScriptError(w, err) {
			return
		}
		if err != nil {
			writeScriptError(w, http.StatusInternalServerError, "保存脚本草稿失败")
			return
		}
		writeScriptJSON(w, http.StatusOK, draft)
	})
}

func publishHandler(manager Manager, sync SyncManager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request publishRequest
		if err := decodeScriptJSON(w, r, &request); err != nil {
			writeScriptError(w, http.StatusBadRequest, "发布信息格式不正确")
			return
		}
		principal, _ := auth.PrincipalFromContext(r.Context())
		version, err := manager.Publish(r.Context(), PublishInput{
			ScriptID: r.PathValue("id"), Content: []byte(request.Content), Runtime: request.Runtime,
			Entrypoint: request.Entrypoint, ReleaseNotes: request.ReleaseNotes,
			Category: request.Category, Tags: request.Tags,
			Distribution: request.Distribution, ParameterDefinitions: request.ParameterDefinitions,
			Resources: request.Resources, AuthorID: principal.UserID,
		})
		if writeKnownScriptError(w, err) {
			return
		}
		if err != nil {
			writeScriptError(w, http.StatusInternalServerError, "发布脚本失败")
			return
		}
		if sync != nil {
			if _, err := sync.PrepareVersion(r.Context(), version.ID); err != nil {
				w.Header().Set("Warning", `199 yunling "版本已发布，但同步记录创建失败，请稍后重试"`)
			}
		}
		writeScriptJSON(w, http.StatusCreated, version)
	})
}

func versionsHandler(manager Manager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		versions, err := manager.ListVersions(r.Context(), r.PathValue("id"))
		if err != nil {
			writeScriptError(w, http.StatusInternalServerError, "读取脚本版本失败")
			return
		}
		if versions == nil {
			versions = []Version{}
		}
		writeScriptJSON(w, http.StatusOK, map[string]any{"versions": versions})
	})
}

func versionContentHandler(manager Manager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		content, err := manager.VersionContent(r.Context(), r.PathValue("id"), r.PathValue("versionID"))
		if writeKnownScriptError(w, err) {
			return
		}
		if err != nil {
			writeScriptError(w, http.StatusInternalServerError, "读取版本内容失败")
			return
		}
		writeScriptJSON(w, http.StatusOK, map[string]string{"content": content})
	})
}

func rollbackHandler(manager Manager, sync SyncManager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			VersionID    string `json:"versionId"`
			ReleaseNotes string `json:"releaseNotes"`
		}
		if err := decodeScriptJSON(w, r, &request); err != nil {
			writeScriptError(w, http.StatusBadRequest, "回滚信息格式不正确")
			return
		}
		principal, _ := auth.PrincipalFromContext(r.Context())
		version, err := manager.Rollback(r.Context(), RollbackInput{
			ScriptID: r.PathValue("id"), VersionID: request.VersionID,
			ReleaseNotes: request.ReleaseNotes, AuthorID: principal.UserID,
		})
		if writeKnownScriptError(w, err) {
			return
		}
		if err != nil {
			writeScriptError(w, http.StatusInternalServerError, "回滚脚本失败")
			return
		}
		if sync != nil {
			if _, err := sync.PrepareVersion(r.Context(), version.ID); err != nil {
				w.Header().Set("Warning", `199 yunling "回滚版本已创建，但同步记录创建失败，请稍后重试"`)
			}
		}
		writeScriptJSON(w, http.StatusCreated, version)
	})
}

func syncListHandler(sync SyncManager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		items, err := sync.List(r.Context(), r.PathValue("id"))
		if err != nil {
			writeScriptError(w, http.StatusInternalServerError, "读取脚本同步状态失败")
			return
		}
		if items == nil {
			items = []SyncView{}
		}
		writeScriptJSON(w, http.StatusOK, map[string]any{"syncs": items})
	})
}

func syncRetryHandler(sync SyncManager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := sync.Retry(r.Context(), r.PathValue("syncID")); err != nil {
			if errors.Is(err, ErrSyncNotFound) {
				writeScriptError(w, http.StatusNotFound, err.Error())
				return
			}
			writeScriptError(w, http.StatusInternalServerError, "重试脚本同步失败")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

type editorRequest struct {
	Content              string                `json:"content"`
	Runtime              string                `json:"runtime"`
	Entrypoint           string                `json:"entrypoint"`
	Category             string                `json:"category"`
	Tags                 []string              `json:"tags"`
	Distribution         DistributionRule      `json:"distribution"`
	ParameterDefinitions []ParameterDefinition `json:"parameterDefinitions"`
	Resources            ResourceRequirements  `json:"resources"`
}

type publishRequest struct {
	editorRequest
	ReleaseNotes string `json:"releaseNotes"`
}

func importedScriptType(header *multipart.FileHeader) (runtime, entrypoint string, ok bool) {
	entrypoint = filepath.Base(strings.ReplaceAll(header.Filename, "\\", "/"))
	switch strings.ToLower(filepath.Ext(entrypoint)) {
	case ".sh":
		return "bash", entrypoint, true
	case ".py":
		return "python3", entrypoint, true
	case ".js":
		return "node", entrypoint, true
	case ".ps1":
		return "powershell", entrypoint, true
	default:
		return "", "", false
	}
}

func readImportedScript(file multipart.File) ([]byte, error) {
	content, err := io.ReadAll(io.LimitReader(file, MaxScriptBytes+1))
	if err != nil {
		return nil, fmt.Errorf("读取导入文件失败")
	}
	if int64(len(content)) > MaxScriptBytes {
		return nil, ErrScriptContentTooLarge
	}
	if len(content) == 0 || !utf8.Valid(content) || bytesContainZero(content) {
		return nil, errors.New("导入文件必须是非空 UTF-8 文本脚本")
	}
	return content, nil
}

func bytesContainZero(content []byte) bool {
	for _, value := range content {
		if value == 0 {
			return true
		}
	}
	return false
}

func decodeScriptJSON(w http.ResponseWriter, r *http.Request, destination any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, MaxScriptBytes+(64<<10)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("请求只能包含一个 JSON 对象")
	}
	return nil
}

func writeKnownScriptError(w http.ResponseWriter, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, ErrScriptNotFound), errors.Is(err, ErrVersionNotFound):
		writeScriptError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, ErrInvalidScript), errors.Is(err, ErrInvalidDraft), errors.Is(err, ErrInvalidPublish),
		errors.Is(err, ErrInvalidReleaseNotes), errors.Is(err, ErrInvalidDistribution), errors.Is(err, ErrScriptContentTooLarge):
		writeScriptError(w, http.StatusBadRequest, err.Error())
	default:
		return false
	}
	return true
}

func writeScriptError(w http.ResponseWriter, status int, message string) {
	writeScriptJSON(w, status, map[string]string{"message": message})
}

func writeScriptJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

var _ Manager = (*Service)(nil)
