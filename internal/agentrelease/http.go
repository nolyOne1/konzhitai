package agentrelease

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultMaxRequestsPerWindow = 240
	defaultMaxConcurrent        = 16
	retryAfterSeconds           = "5"
)

type HandlerOption func(*handlerOptions)

type handlerOptions struct {
	maxRequestsPerWindow int
	maxConcurrent        int
	window               time.Duration
	now                  func() time.Time
}

func WithLimits(maxRequestsPerWindow, maxConcurrent int, window time.Duration, now func() time.Time) HandlerOption {
	return func(options *handlerOptions) {
		options.maxRequestsPerWindow = maxRequestsPerWindow
		options.maxConcurrent = maxConcurrent
		options.window = window
		options.now = now
	}
}

func Handler(catalog *Catalog, options ...HandlerOption) http.Handler {
	if catalog == nil {
		return UnavailableHandler()
	}
	configuration := handlerOptions{
		maxRequestsPerWindow: defaultMaxRequestsPerWindow,
		maxConcurrent:        defaultMaxConcurrent,
		window:               time.Minute,
		now:                  time.Now,
	}
	for _, option := range options {
		if option != nil {
			option(&configuration)
		}
	}
	if configuration.maxRequestsPerWindow <= 0 {
		configuration.maxRequestsPerWindow = defaultMaxRequestsPerWindow
	}
	if configuration.maxConcurrent <= 0 {
		configuration.maxConcurrent = defaultMaxConcurrent
	}
	if configuration.window <= 0 {
		configuration.window = time.Minute
	}
	if configuration.now == nil {
		configuration.now = time.Now
	}
	limits := newReleaseLimits(configuration)

	router := http.NewServeMux()
	router.HandleFunc("GET /api/releases/agent/latest", manifestHandler(catalog))
	router.HandleFunc("GET /api/releases/agent/{version}/{sha256}/{fileName}", artifactHandler(catalog, limits))
	return limits.limitRequests(router)
}

func UnavailableHandler() http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		writeFixedError(response, http.StatusServiceUnavailable, "代理发布暂不可用")
	})
}

func manifestHandler(catalog *Catalog) http.HandlerFunc {
	return func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "application/json; charset=utf-8")
		response.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		response.Header().Set("X-Content-Type-Options", "nosniff")
		if err := json.NewEncoder(response).Encode(catalog.Manifest()); err != nil {
			return
		}
	}
}

func artifactHandler(catalog *Catalog, limits *releaseLimits) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		version := request.PathValue("version")
		digest := request.PathValue("sha256")
		fileName := request.PathValue("fileName")
		artifact, ok := catalog.lookup(version, digest, fileName)
		if !ok {
			http.NotFound(response, request)
			return
		}

		etag := `"` + artifact.SHA256 + `"`
		if strings.TrimSpace(request.Header.Get("If-None-Match")) == etag {
			setArtifactCacheHeaders(response, etag)
			response.WriteHeader(http.StatusNotModified)
			return
		}
		if !limits.acquireDownload() {
			writeTooManyRequests(response)
			return
		}
		defer limits.releaseDownload()

		file, verified, err := catalog.Open(version, digest, fileName)
		if errors.Is(err, ErrArtifactNotFound) {
			http.NotFound(response, request)
			return
		}
		if err != nil {
			writeFixedError(response, http.StatusInternalServerError, "读取代理安装包失败")
			return
		}
		defer file.Close()

		setArtifactCacheHeaders(response, etag)
		response.Header().Set("Content-Type", "application/gzip")
		response.Header().Set("Content-Length", strconv.FormatInt(verified.ByteSize, 10))
		response.Header().Set("Content-Disposition", `attachment; filename="`+verified.FileName+`"`)
		response.WriteHeader(http.StatusOK)
		_, _ = io.Copy(response, file)
	}
}

func setArtifactCacheHeaders(response http.ResponseWriter, etag string) {
	response.Header().Set("ETag", etag)
	response.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	response.Header().Set("X-Content-Type-Options", "nosniff")
}

func writeFixedError(response http.ResponseWriter, status int, message string) {
	response.Header().Set("Content-Type", "text/plain; charset=utf-8")
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("X-Content-Type-Options", "nosniff")
	response.WriteHeader(status)
	_, _ = io.WriteString(response, message+"\n")
}

func writeTooManyRequests(response http.ResponseWriter) {
	response.Header().Set("Retry-After", retryAfterSeconds)
	writeFixedError(response, http.StatusTooManyRequests, "请求过于频繁，请稍后重试")
}

type releaseLimits struct {
	mutex                sync.Mutex
	maxRequestsPerWindow int
	maxGlobalPerWindow   int
	window               time.Duration
	now                  func() time.Time
	clients              map[string]requestWindow
	global               requestWindow
	nextCleanup          time.Time
	downloadSlots        chan struct{}
}

type requestWindow struct {
	endsAt   time.Time
	requests int
}

func newReleaseLimits(options handlerOptions) *releaseLimits {
	return &releaseLimits{
		maxRequestsPerWindow: options.maxRequestsPerWindow,
		maxGlobalPerWindow:   options.maxRequestsPerWindow * 64,
		window:               options.window,
		now:                  options.now,
		clients:              make(map[string]requestWindow),
		downloadSlots:        make(chan struct{}, options.maxConcurrent),
	}
}

func (limits *releaseLimits) limitRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if !limits.allowRequest(request) {
			writeTooManyRequests(response)
			return
		}
		next.ServeHTTP(response, request)
	})
}

func (limits *releaseLimits) allowRequest(request *http.Request) bool {
	limits.mutex.Lock()
	defer limits.mutex.Unlock()
	now := limits.now()
	if limits.global.endsAt.IsZero() || !now.Before(limits.global.endsAt) {
		limits.global = requestWindow{endsAt: now.Add(limits.window)}
	}
	if limits.global.requests >= limits.maxGlobalPerWindow {
		return false
	}
	if limits.nextCleanup.IsZero() || !now.Before(limits.nextCleanup) {
		for key, current := range limits.clients {
			if !now.Before(current.endsAt) {
				delete(limits.clients, key)
			}
		}
		limits.nextCleanup = now.Add(limits.window)
	}
	key := releaseClientKey(request)
	current := limits.clients[key]
	if current.endsAt.IsZero() || !now.Before(current.endsAt) {
		current = requestWindow{endsAt: now.Add(limits.window)}
	}
	if current.requests >= limits.maxRequestsPerWindow {
		return false
	}
	current.requests++
	limits.clients[key] = current
	limits.global.requests++
	return true
}

func releaseClientKey(request *http.Request) string {
	host, _, err := net.SplitHostPort(strings.TrimSpace(request.RemoteAddr))
	if err != nil {
		host = strings.TrimSpace(request.RemoteAddr)
	}
	peer := net.ParseIP(host)
	if peer != nil && (peer.IsPrivate() || peer.IsLoopback()) {
		forwarded, _, _ := strings.Cut(request.Header.Get("X-Forwarded-For"), ",")
		if client := net.ParseIP(strings.TrimSpace(forwarded)); client != nil {
			return client.String()
		}
	}
	if peer != nil {
		return peer.String()
	}
	return host
}

func (limits *releaseLimits) acquireDownload() bool {
	select {
	case limits.downloadSlots <- struct{}{}:
		return true
	default:
		return false
	}
}

func (limits *releaseLimits) releaseDownload() {
	<-limits.downloadSlots
}
