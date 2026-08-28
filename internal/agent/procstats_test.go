package agent

import "testing"

func TestParseProcMeminfoCalculatesUsedMemory(t *testing.T) {
	total, used, err := parseProcMeminfo([]byte("MemTotal:       16384 kB\nMemAvailable:    4096 kB\n"))
	if err != nil {
		t.Fatalf("解析 /proc/meminfo：%v", err)
	}
	if total != 16<<20 || used != 12<<20 {
		t.Fatalf("内存总量和已用量计算错误：total=%d used=%d", total, used)
	}
}

func TestCalculateCPUUsedMilliUsesCounterDelta(t *testing.T) {
	before, err := parseProcCPU([]byte("cpu  100 0 50 850 0 0 0 0 0 0\n"))
	if err != nil {
		t.Fatalf("解析第一次 /proc/stat：%v", err)
	}
	after, err := parseProcCPU([]byte("cpu  160 0 70 970 0 0 0 0 0 0\n"))
	if err != nil {
		t.Fatalf("解析第二次 /proc/stat：%v", err)
	}

	used := calculateCPUUsedMilli(before, after, 4000)
	if used != 1600 {
		t.Fatalf("CPU 忙碌比例为 40%%、总量为 4000 毫核时应使用 1600，实际为 %d", used)
	}
}
