package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"yunling.local/platform/internal/agentprotocol"
)

type ArtifactCollector struct {
	workRoot   string
	controlURL *url.URL
	credential string
	client     *http.Client
}

func NewArtifactCollector(workRoot, controlURL, credential string, client *http.Client) (*ArtifactCollector, error) {
	base, err := url.Parse(controlURL)
	if err != nil || base.Host == "" || (base.Scheme != "https" && !(base.Scheme == "http" && isLoopbackHostname(base.Hostname()))) || base.User != nil {
		return nil, errors.New("产物上传控制地址无效")
	}
	root, err := filepath.Abs(workRoot)
	if err != nil {
		return nil, err
	}
	if client == nil {
		client = &http.Client{Timeout: 45 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	return &ArtifactCollector{workRoot: root, controlURL: base, credential: credential, client: client}, nil
}

var artifactRunID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

func (c *ArtifactCollector) CollectAndUpload(ctx context.Context, assignment agentprotocol.Assignment) error {
	policy := assignment.Artifacts
	if policy == nil {
		return nil
	}
	if err := policy.Validate(); err != nil {
		return err
	}
	if !artifactRunID.MatchString(assignment.RunID) {
		return errors.New("运行产物目录标识无效")
	}
	directory := filepath.Join(c.workRoot, assignment.RunID)
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("运行产物目录不可用")
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return fmt.Errorf("打开运行产物目录：%w", err)
	}
	defer root.Close()
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	entries, err := dir.ReadDir(-1)
	_ = dir.Close()
	if err != nil {
		return err
	}
	type candidate struct {
		name string
		size int64
		info os.FileInfo
	}
	files := []candidate{}
	var total int64
	for _, entry := range entries {
		if !policy.Allows(entry.Name()) {
			continue
		}
		info, err := root.Lstat(entry.Name())
		if err != nil {
			return fmt.Errorf("读取产物信息 %s：%w", entry.Name(), err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("产物 %s 必须是普通文件，不能是目录或符号链接", entry.Name())
		}
		if info.Size() > policy.MaxFileBytes {
			return fmt.Errorf("产物 %s 超过单文件大小上限", entry.Name())
		}
		total += info.Size()
		if total > policy.MaxTotalBytes {
			return errors.New("运行产物总大小超过策略上限")
		}
		files = append(files, candidate{name: entry.Name(), size: info.Size(), info: info})
		if len(files) > agentprotocol.MaxArtifactFiles {
			return errors.New("运行产物数量超过 100 个")
		}
	}
	for _, file := range files {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		body, err := root.Open(file.name)
		if err != nil {
			return fmt.Errorf("读取产物 %s：%w", file.name, err)
		}
		opened, statErr := body.Stat()
		if statErr != nil || !opened.Mode().IsRegular() || !os.SameFile(file.info, opened) {
			_ = body.Close()
			return fmt.Errorf("产物 %s 在采集期间被替换", file.name)
		}
		contents, readErr := io.ReadAll(io.LimitReader(body, policy.MaxFileBytes+1))
		_ = body.Close()
		if readErr != nil {
			return fmt.Errorf("读取产物 %s：%w", file.name, readErr)
		}
		if int64(len(contents)) != file.size {
			return fmt.Errorf("产物 %s 在采集期间发生变化", file.name)
		}
		sum := sha256.Sum256(contents)
		if err := c.upload(ctx, assignment, file.name, contents, hex.EncodeToString(sum[:])); err != nil {
			return err
		}
	}
	return nil
}

func (c *ArtifactCollector) upload(ctx context.Context, assignment agentprotocol.Assignment, name string, contents []byte, checksum string) error {
	endpoint := c.controlURL.ResolveReference(&url.URL{Path: "/api/agent/runs/" + assignment.RunID + "/artifacts/" + name})
	var last error
	for attempt := 0; attempt < 3; attempt++ {
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(contents))
		if err != nil {
			return err
		}
		request.Header.Set("Authorization", "Bearer "+c.credential)
		request.Header.Set("X-Execution-Token", assignment.ExecutionToken)
		request.Header.Set("X-Content-SHA256", checksum)
		request.Header.Set("Content-Type", "application/octet-stream")
		response, err := c.client.Do(request)
		if err == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK || response.StatusCode == http.StatusCreated || response.StatusCode == http.StatusNoContent {
				return nil
			}
			last = fmt.Errorf("产物 %s 上传未成功（状态码 %d）", name, response.StatusCode)
			if response.StatusCode < 500 && response.StatusCode != http.StatusTooManyRequests {
				return last
			}
		} else {
			last = fmt.Errorf("上传产物 %s：%w", name, err)
		}
		if attempt < 2 {
			timer := time.NewTimer(time.Duration(attempt+1) * 200 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
	return last
}
