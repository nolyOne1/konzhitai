package logstream

import (
	"bytes"
	"errors"
	"testing"
	"time"
)

func TestSpoolSplitsPersistsAndAcknowledgesChunks(t *testing.T) {
	root := t.TempDir()
	spool, err := NewSpool(root, DefaultChunkSize)
	if err != nil {
		t.Fatal(err)
	}
	payload := append(bytes.Repeat([]byte("甲"), DefaultChunkSize/3), []byte("结束")...)
	chunks, err := spool.Append("run-1", "token-1", StreamStdout, payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 2 || chunks[0].Sequence != 1 || chunks[1].Sequence != 2 {
		t.Fatalf("日志必须按 64 KiB 分块并连续编号：%+v", chunks)
	}

	reopened, err := NewSpool(root, DefaultChunkSize)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := reopened.Pending("run-1", "token-1", StreamStdout)
	if err != nil || len(pending) != 2 {
		t.Fatalf("代理重启后必须恢复未确认日志：count=%d err=%v", len(pending), err)
	}
	if err := reopened.Acknowledge("run-1", "token-1", StreamStdout, 2); err != nil {
		t.Fatal(err)
	}
	pending, err = reopened.Pending("run-1", "token-1", StreamStdout)
	if err != nil || len(pending) != 1 || pending[0].Sequence != 2 {
		t.Fatalf("确认后只删除更小序号：pending=%+v err=%v", pending, err)
	}
}

func TestSpoolKeepsSequenceAfterAllChunksAreAcknowledgedAndRestarted(t *testing.T) {
	root := t.TempDir()
	spool, err := NewSpool(root, DefaultChunkSize)
	if err != nil {
		t.Fatal(err)
	}
	first, err := spool.Append("run-1", "token-1", StreamStdout, []byte("第一段"))
	if err != nil || len(first) != 1 || first[0].Sequence != 1 {
		t.Fatalf("首个日志块编号错误：chunks=%+v err=%v", first, err)
	}
	if err := spool.Acknowledge("run-1", "token-1", StreamStdout, 2); err != nil {
		t.Fatal(err)
	}

	reopened, err := NewSpool(root, DefaultChunkSize)
	if err != nil {
		t.Fatal(err)
	}
	second, err := reopened.Append("run-1", "token-1", StreamStdout, []byte("第二段"))
	if err != nil || len(second) != 1 || second[0].Sequence != 2 {
		t.Fatalf("确认清理并重启后日志序号必须继续递增：chunks=%+v err=%v", second, err)
	}
}

func TestSpoolRejectsInvalidStreamOnReadAndAcknowledge(t *testing.T) {
	spool, err := NewSpool(t.TempDir(), DefaultChunkSize)
	if err != nil {
		t.Fatal(err)
	}
	invalid := Stream("../../outside")
	if _, err := spool.Pending("run-1", "token-1", invalid); err != ErrInvalidChunk {
		t.Fatalf("读取非法日志流必须被拒绝：%v", err)
	}
	if err := spool.Acknowledge("run-1", "token-1", invalid, 2); err != ErrInvalidChunk {
		t.Fatalf("确认非法日志流必须被拒绝：%v", err)
	}
}

func TestSpoolUploadsEachStreamInSequenceOrderWhenClockMovesBackward(t *testing.T) {
	spool, err := NewSpool(t.TempDir(), DefaultChunkSize)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := spool.write(LogChunk{RunID: "run-1", ExecutionToken: "token-1", Stream: StreamStdout, Sequence: 1, Content: "第一段", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := spool.write(LogChunk{RunID: "run-1", ExecutionToken: "token-1", Stream: StreamStdout, Sequence: 2, Content: "第二段", CreatedAt: now.Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	pending, err := spool.AllPending()
	if err != nil || len(pending) != 2 || pending[0].Sequence != 1 || pending[1].Sequence != 2 {
		t.Fatalf("系统时钟回拨时仍必须按日志序号上传：pending=%+v err=%v", pending, err)
	}
}

func TestSpoolEnforcesLimitAndReportsUsage(t *testing.T) {
	spool, err := NewSpool(t.TempDir(), DefaultChunkSize, WithSpoolMaxBytes(512))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := spool.Append("run-1", "token-1", StreamStdout, []byte("第一段日志")); err != nil {
		t.Fatal(err)
	}
	used, limit := spool.Usage()
	if used <= 0 || limit != 512 {
		t.Fatalf("日志缓冲容量统计错误：used=%d limit=%d", used, limit)
	}
	if _, err := spool.Append("run-1", "token-1", StreamStdout, bytes.Repeat([]byte("x"), 512)); !errors.Is(err, ErrSpoolLimitExceeded) {
		t.Fatalf("超过缓冲上限必须被拒绝：%v", err)
	}
}
