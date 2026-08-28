package agent

import (
	"bufio"
	"bytes"
	"errors"
	"strconv"
	"strings"
)

var ErrInvalidSystemStats = errors.New("系统资源数据格式无效")

type cpuCounters struct {
	total uint64
	idle  uint64
}

func parseProcMeminfo(contents []byte) (totalBytes int64, usedBytes int64, err error) {
	var totalKB uint64
	var availableKB uint64
	scanner := bufio.NewScanner(bytes.NewReader(contents))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		value, parseErr := strconv.ParseUint(fields[1], 10, 64)
		if parseErr != nil {
			return 0, 0, ErrInvalidSystemStats
		}
		switch fields[0] {
		case "MemTotal:":
			totalKB = value
		case "MemAvailable:":
			availableKB = value
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, 0, err
	}
	if totalKB == 0 || availableKB > totalKB {
		return 0, 0, ErrInvalidSystemStats
	}
	return int64(totalKB * 1024), int64((totalKB - availableKB) * 1024), nil
}

func parseProcCPU(contents []byte) (cpuCounters, error) {
	scanner := bufio.NewScanner(bytes.NewReader(contents))
	if !scanner.Scan() {
		return cpuCounters{}, ErrInvalidSystemStats
	}
	fields := strings.Fields(scanner.Text())
	if len(fields) < 5 || fields[0] != "cpu" {
		return cpuCounters{}, ErrInvalidSystemStats
	}
	values := make([]uint64, 0, len(fields)-1)
	for _, field := range fields[1:] {
		value, err := strconv.ParseUint(field, 10, 64)
		if err != nil {
			return cpuCounters{}, ErrInvalidSystemStats
		}
		values = append(values, value)
	}
	var total uint64
	for _, value := range values {
		total += value
	}
	idle := values[3]
	if len(values) > 4 {
		idle += values[4]
	}
	return cpuCounters{total: total, idle: idle}, nil
}

func calculateCPUUsedMilli(before, after cpuCounters, totalMilli int64) int64 {
	if totalMilli <= 0 || after.total <= before.total || after.idle < before.idle {
		return 0
	}
	totalDelta := after.total - before.total
	idleDelta := after.idle - before.idle
	if idleDelta >= totalDelta {
		return 0
	}
	busyDelta := totalDelta - idleDelta
	return int64(busyDelta * uint64(totalMilli) / totalDelta)
}
