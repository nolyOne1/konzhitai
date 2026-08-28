//go:build !linux && !windows

package agent

import (
	"context"
	"errors"
)

type SystemStatsSource struct{}

func NewSystemStatsSource(string, func() int) (*SystemStatsSource, error) {
	return nil, errors.New("当前操作系统暂不支持资源采集")
}

func (*SystemStatsSource) Snapshot(context.Context) (Stats, error) {
	return Stats{}, errors.New("当前操作系统暂不支持资源采集")
}
