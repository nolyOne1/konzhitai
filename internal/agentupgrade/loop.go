package agentupgrade

import (
	"context"
	"time"
)

func RunLoop(ctx context.Context, coordinator interface{ Scan(context.Context) error }, interval time.Duration, onError func(error)) {
	if onError == nil {
		onError = func(error) {}
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := coordinator.Scan(ctx); err != nil {
			onError(err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
