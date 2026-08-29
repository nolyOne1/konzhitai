package logstream

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

type Spool struct {
	root      string
	chunkSize int
	maxBytes  int64
	usedBytes int64
	mu        sync.Mutex
}

const (
	sequenceCursorName   = ".next-sequence"
	DefaultSpoolMaxBytes = int64(512 << 20)
)

var ErrSpoolLimitExceeded = errors.New("日志缓冲已达到容量上限")

type SpoolOption func(*Spool)

func WithSpoolMaxBytes(maxBytes int64) SpoolOption {
	return func(spool *Spool) { spool.maxBytes = maxBytes }
}

func NewSpool(root string, chunkSize int, options ...SpoolOption) (*Spool, error) {
	root = strings.TrimSpace(root)
	if root == "" || chunkSize <= 0 || chunkSize > DefaultChunkSize {
		return nil, ErrInvalidChunk
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("解析日志缓冲目录：%w", err)
	}
	if err := os.MkdirAll(absolute, 0o750); err != nil {
		return nil, fmt.Errorf("创建日志缓冲目录：%w", err)
	}
	spool := &Spool{root: absolute, chunkSize: chunkSize, maxBytes: DefaultSpoolMaxBytes}
	for _, option := range options {
		option(spool)
	}
	if spool.maxBytes <= 0 {
		return nil, ErrInvalidChunk
	}
	used, err := bufferedBytes(absolute)
	if err != nil {
		return nil, fmt.Errorf("统计日志缓冲容量：%w", err)
	}
	spool.usedBytes = used
	return spool, nil
}

func (s *Spool) Append(runID, token string, stream Stream, content []byte) ([]LogChunk, error) {
	if strings.TrimSpace(runID) == "" || strings.TrimSpace(token) == "" || !validStream(stream) {
		return nil, ErrInvalidChunk
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	pending, err := s.pending(runID, token, stream)
	if err != nil {
		return nil, err
	}
	next, err := s.nextSequence(runID, token, stream, pending)
	if err != nil {
		return nil, err
	}
	result := make([]LogChunk, 0, len(content)/s.chunkSize+1)
	for len(content) > 0 {
		length := min(len(content), s.chunkSize)
		for length > 0 && length < len(content) && !utf8.RuneStart(content[length]) {
			length--
		}
		if length == 0 {
			length = min(len(content), s.chunkSize)
		}
		chunk := LogChunk{RunID: runID, ExecutionToken: token, Sequence: next, Stream: stream, Content: strings.ToValidUTF8(string(content[:length]), "�"), CreatedAt: time.Now().UTC()}
		result = append(result, chunk)
		next++
		content = content[length:]
	}
	var pendingBytes int64
	for _, chunk := range result {
		body, err := json.Marshal(chunk)
		if err != nil {
			return nil, fmt.Errorf("编码日志块：%w", err)
		}
		pendingBytes += int64(len(body))
	}
	if len(result) > 0 && (s.usedBytes >= s.maxBytes || pendingBytes > s.maxBytes-s.usedBytes) {
		return nil, ErrSpoolLimitExceeded
	}
	for _, chunk := range result {
		if err := s.write(chunk); err != nil {
			return nil, err
		}
	}
	if len(result) > 0 {
		if err := s.writeSequenceCursor(s.directory(runID, token, stream), next); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func (s *Spool) Usage() (usedBytes, maxBytes int64) {
	if s == nil {
		return 0, 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.usedBytes, s.maxBytes
}

func (s *Spool) Pending(runID, token string, stream Stream) ([]LogChunk, error) {
	if !validSpoolKey(runID, token, stream) {
		return nil, ErrInvalidChunk
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pending(runID, token, stream)
}

func (s *Spool) AllPending() ([]LogChunk, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	chunks := []LogChunk{}
	err := filepath.WalkDir(s.root, func(filePath string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if _, ok := sequenceFromName(entry.Name()); !ok {
			return nil
		}
		body, err := os.ReadFile(filePath)
		if err != nil {
			return err
		}
		var chunk LogChunk
		if err := json.Unmarshal(body, &chunk); err != nil {
			return err
		}
		chunks = append(chunks, chunk)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("扫描本地日志缓冲：%w", err)
	}
	sort.Slice(chunks, func(i, j int) bool {
		if chunks[i].RunID != chunks[j].RunID {
			return chunks[i].RunID < chunks[j].RunID
		}
		if chunks[i].ExecutionToken != chunks[j].ExecutionToken {
			return chunks[i].ExecutionToken < chunks[j].ExecutionToken
		}
		if chunks[i].Sequence != chunks[j].Sequence {
			return chunks[i].Sequence < chunks[j].Sequence
		}
		if chunks[i].Stream != chunks[j].Stream {
			return chunks[i].Stream < chunks[j].Stream
		}
		return chunks[i].CreatedAt.Before(chunks[j].CreatedAt)
	})
	return chunks, nil
}

func (s *Spool) Acknowledge(runID, token string, stream Stream, nextSequence uint64) error {
	if !validSpoolKey(runID, token, stream) || nextSequence == 0 {
		return ErrInvalidChunk
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	directory := s.directory(runID, token, stream)
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("读取日志缓冲：%w", err)
	}
	for _, entry := range entries {
		sequence, ok := sequenceFromName(entry.Name())
		if !ok || sequence >= nextSequence {
			continue
		}
		filePath := filepath.Join(directory, entry.Name())
		info, statErr := os.Stat(filePath)
		if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			return fmt.Errorf("统计待清理日志块：%w", statErr)
		}
		if err := os.Remove(filePath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("清理已确认日志块：%w", err)
		}
		if statErr == nil {
			s.usedBytes = max(0, s.usedBytes-info.Size())
		}
	}
	return nil
}

func (s *Spool) pending(runID, token string, stream Stream) ([]LogChunk, error) {
	directory := s.directory(runID, token, stream)
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return []LogChunk{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取日志缓冲：%w", err)
	}
	chunks := make([]LogChunk, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if _, ok := sequenceFromName(entry.Name()); !ok {
			continue
		}
		body, err := os.ReadFile(filepath.Join(directory, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("读取日志块：%w", err)
		}
		var chunk LogChunk
		if err := json.Unmarshal(body, &chunk); err != nil {
			return nil, fmt.Errorf("解析日志块：%w", err)
		}
		chunks = append(chunks, chunk)
	}
	sort.Slice(chunks, func(i, j int) bool { return chunks[i].Sequence < chunks[j].Sequence })
	return chunks, nil
}

func (s *Spool) write(chunk LogChunk) error {
	directory := s.directory(chunk.RunID, chunk.ExecutionToken, chunk.Stream)
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return fmt.Errorf("创建日志流缓冲目录：%w", err)
	}
	body, err := json.Marshal(chunk)
	if err != nil {
		return fmt.Errorf("编码日志块：%w", err)
	}
	temporary, err := os.CreateTemp(directory, ".chunk-*")
	if err != nil {
		return fmt.Errorf("创建临时日志块：%w", err)
	}
	temporaryName := temporary.Name()
	defer func() { _ = os.Remove(temporaryName) }()
	if err := temporary.Chmod(0o640); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(body); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("写入日志块：%w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("同步日志块：%w", err)
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	target := filepath.Join(directory, fmt.Sprintf("%020d.json", chunk.Sequence))
	if err := os.Rename(temporaryName, target); err != nil {
		return fmt.Errorf("保存日志块：%w", err)
	}
	s.usedBytes += int64(len(body))
	return nil
}

func bufferedBytes(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(filePath string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if _, ok := sequenceFromName(entry.Name()); !ok {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	return total, err
}

func (s *Spool) nextSequence(runID, token string, stream Stream, pending []LogChunk) (uint64, error) {
	cursorPath := filepath.Join(s.directory(runID, token, stream), sequenceCursorName)
	body, err := os.ReadFile(cursorPath)
	if err == nil {
		next, parseErr := strconv.ParseUint(strings.TrimSpace(string(body)), 10, 64)
		if parseErr != nil || next == 0 {
			return 0, fmt.Errorf("解析日志序号游标：%w", ErrInvalidChunk)
		}
		if len(pending) > 0 && next <= pending[len(pending)-1].Sequence {
			next = pending[len(pending)-1].Sequence + 1
		}
		return next, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return 0, fmt.Errorf("读取日志序号游标：%w", err)
	}
	if len(pending) > 0 {
		return pending[len(pending)-1].Sequence + 1, nil
	}
	return 1, nil
}

func (s *Spool) writeSequenceCursor(directory string, next uint64) error {
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return fmt.Errorf("创建日志流缓冲目录：%w", err)
	}
	temporary, err := os.CreateTemp(directory, ".sequence-*")
	if err != nil {
		return fmt.Errorf("创建日志序号游标：%w", err)
	}
	temporaryName := temporary.Name()
	defer func() { _ = os.Remove(temporaryName) }()
	if err := temporary.Chmod(0o640); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.WriteString(strconv.FormatUint(next, 10)); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("写入日志序号游标：%w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("同步日志序号游标：%w", err)
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	target := filepath.Join(directory, sequenceCursorName)
	if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("替换日志序号游标：%w", err)
	}
	if err := os.Rename(temporaryName, target); err != nil {
		return fmt.Errorf("保存日志序号游标：%w", err)
	}
	return nil
}

func (s *Spool) directory(runID, token string, stream Stream) string {
	return filepath.Join(s.root, digest(runID), digest(token), string(stream))
}

func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func validStream(stream Stream) bool {
	return stream == StreamStdout || stream == StreamStderr || stream == StreamSystem
}

func validSpoolKey(runID, token string, stream Stream) bool {
	return strings.TrimSpace(runID) != "" && strings.TrimSpace(token) != "" && validStream(stream)
}

func sequenceFromName(name string) (uint64, bool) {
	if filepath.Ext(name) != ".json" {
		return 0, false
	}
	sequence, err := strconv.ParseUint(strings.TrimSuffix(name, ".json"), 10, 64)
	return sequence, err == nil && sequence > 0
}
