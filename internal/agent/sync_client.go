package agent

import (
	"context"
	"fmt"
	"time"

	"yunling.local/platform/internal/agentprotocol"
)

const DriftScanInterval = time.Minute

type SyncCache interface {
	Ensure(context.Context, agentprotocol.SyncCommand) (string, error)
}

type DriftDetector interface {
	Scan(context.Context) ([]agentprotocol.SyncResult, error)
}

type SyncTransport interface {
	ReceiveSyncCommand(context.Context) (agentprotocol.SyncCommand, error)
	SendSyncResult(context.Context, agentprotocol.SyncResult) error
}

type SyncClientOption func(*SyncClient)

func WithDriftTickerFactory(factory TickerFactory) SyncClientOption {
	return func(client *SyncClient) { client.newTicker = factory }
}

type SyncClient struct {
	cache     SyncCache
	drift     DriftDetector
	transport SyncTransport
	newTicker TickerFactory
}

func NewSyncClient(cache SyncCache, drift DriftDetector, transport SyncTransport, options ...SyncClientOption) *SyncClient {
	client := &SyncClient{
		cache: cache, drift: drift, transport: transport,
		newTicker: func(interval time.Duration) Ticker { return realTicker{Ticker: time.NewTicker(interval)} },
	}
	for _, option := range options {
		option(client)
	}
	return client
}

func (c *SyncClient) Run(ctx context.Context) error {
	ticker := c.newTicker(DriftScanInterval)
	defer ticker.Stop()
	commands := make(chan agentprotocol.SyncCommand)
	receiveErrors := make(chan error, 1)
	go func() {
		for {
			command, err := c.transport.ReceiveSyncCommand(ctx)
			if err != nil {
				select {
				case receiveErrors <- err:
				case <-ctx.Done():
				}
				return
			}
			select {
			case commands <- command:
			case <-ctx.Done():
				return
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-receiveErrors:
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("接收脚本同步命令：%w", err)
		case command := <-commands:
			result := c.ensure(ctx, command)
			if err := c.transport.SendSyncResult(ctx, result); err != nil {
				return fmt.Errorf("上报脚本同步结果：%w", err)
			}
		case <-ticker.C():
			results, err := c.drift.Scan(ctx)
			if err != nil {
				return fmt.Errorf("扫描脚本版本漂移：%w", err)
			}
			for _, result := range results {
				if err := c.transport.SendSyncResult(ctx, result); err != nil {
					return fmt.Errorf("上报脚本版本漂移：%w", err)
				}
			}
		}
	}
}

func (c *SyncClient) ensure(ctx context.Context, command agentprotocol.SyncCommand) agentprotocol.SyncResult {
	result := agentprotocol.SyncResult{ScriptID: command.ScriptID, VersionID: command.VersionID, SHA256: command.SHA256}
	if _, err := c.cache.Ensure(ctx, command); err != nil {
		result.State = agentprotocol.SyncFailed
		result.ErrorCode = "sync_failed"
		result.ErrorMessage = "同步脚本失败：" + err.Error()
		return result
	}
	result.State = agentprotocol.SyncReady
	return result
}
