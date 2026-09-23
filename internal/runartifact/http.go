package runartifact

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"yunling.local/platform/internal/agentprotocol"
	"yunling.local/platform/internal/artifact"
	"yunling.local/platform/internal/auth"
	"yunling.local/platform/internal/task"
)

type Manager interface {
	Upload(context.Context, UploadInput, io.Reader) (Record, bool, error)
	List(context.Context, string) ([]Record, error)
	Open(context.Context, string, string) (Record, io.ReadCloser, error)
}

type AgentAuthenticator interface {
	Authenticate(context.Context, string) (string, error)
}

func UploadHandler(service Manager, agents AgentAuthenticator) http.Handler {
	router := http.NewServeMux()
	router.HandleFunc("POST /api/agent/runs/{id}/artifacts/{name}", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			writeError(w, http.StatusUnauthorized, "缺少代理凭据")
			return
		}
		credential := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		if credential == "" {
			writeError(w, http.StatusUnauthorized, "缺少代理凭据")
			return
		}
		serverID, err := agents.Authenticate(r.Context(), credential)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "代理凭据无效或已撤销")
			return
		}
		mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mediaType != "application/octet-stream" {
			writeError(w, http.StatusUnsupportedMediaType, "请上传二进制文件正文")
			return
		}
		input := UploadInput{RunID: r.PathValue("id"), ServerID: serverID, ExecutionToken: r.Header.Get("X-Execution-Token"), Name: r.PathValue("name"), SHA256: r.Header.Get("X-Content-SHA256"), ByteSize: r.ContentLength}
		if !validUpload(input) {
			writeError(w, http.StatusBadRequest, ErrInvalid.Error())
			return
		}
		body := http.MaxBytesReader(w, r.Body, agentprotocol.MaxArtifactFileBytes)
		record, created, err := service.Upload(r.Context(), input, body)
		if writeServiceError(w, err) {
			return
		}
		status := http.StatusOK
		if created {
			status = http.StatusCreated
		}
		writeJSON(w, status, record)
	})
	return router
}

func ReadHandler(service Manager) http.Handler {
	router := http.NewServeMux()
	router.Handle("GET /api/runs/{id}/artifacts", auth.Require(auth.PermissionRead)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !validID(r.PathValue("id")) {
			writeServiceError(w, ErrInvalid)
			return
		}
		values, err := service.List(r.Context(), r.PathValue("id"))
		if writeServiceError(w, err) {
			return
		}
		if values == nil {
			values = []Record{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"artifacts": values})
	})))
	router.Handle("GET /api/runs/{id}/artifacts/{artifactID}", auth.Require(auth.PermissionRead)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !validID(r.PathValue("id")) || !validID(r.PathValue("artifactID")) {
			writeServiceError(w, ErrInvalid)
			return
		}
		record, body, err := service.Open(r.Context(), r.PathValue("id"), r.PathValue("artifactID"))
		if writeServiceError(w, err) {
			return
		}
		defer body.Close()
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": record.Name}))
		w.Header().Set("Content-Length", strconv.FormatInt(record.ByteSize, 10))
		w.Header().Set("X-Content-SHA256", record.SHA256)
		w.Header().Set("ETag", `"`+record.SHA256+`"`)
		w.Header().Set("Cache-Control", "private, no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = io.Copy(w, body)
	})))
	return router
}

func writeServiceError(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	status, message := http.StatusInternalServerError, "运行产物服务暂不可用，请重试"
	switch {
	case errors.Is(err, ErrInvalid):
		status, message = http.StatusBadRequest, err.Error()
	case errors.Is(err, ErrAccess):
		status, message = http.StatusForbidden, err.Error()
	case errors.Is(err, ErrConflict):
		status, message = http.StatusConflict, err.Error()
	case errors.Is(err, ErrLimit):
		status, message = http.StatusRequestEntityTooLarge, err.Error()
	case errors.Is(err, ErrNotFound), errors.Is(err, task.ErrRunNotFound):
		status, message = http.StatusNotFound, err.Error()
	case errors.Is(err, artifact.ErrObjectMissing):
		status, message = http.StatusServiceUnavailable, "产物文件暂不可用，请稍后重试"
	}
	writeError(w, status, message)
	return true
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"message": message})
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
