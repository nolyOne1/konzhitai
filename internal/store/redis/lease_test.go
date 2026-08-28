package redisstore_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
	"yunling.local/platform/internal/scheduler"
	redisstore "yunling.local/platform/internal/store/redis"
	"yunling.local/platform/internal/task"
)

func TestTryReserveAllowsOnlyOneConcurrentWinner(t *testing.T) {
	server := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	store := redisstore.NewLeaseStore(client)
	start := make(chan struct{})
	var wait sync.WaitGroup
	winners := make(chan bool, 2)
	for _, runID := range []string{"run-a", "run-b"} {
		wait.Add(1)
		go func(id string) {
			defer wait.Done()
			<-start
			_, ok, err := store.TryReserve(context.Background(), scheduler.LeaseRequest{
				RunID: id, ServerID: "server-a", Now: now, TTL: time.Minute,
				Available: task.Resources{CPUMillicores: 4000, MemoryBytes: 8 << 30, DiskBytes: 20 << 30},
				Required:  task.Resources{CPUMillicores: 4000, MemoryBytes: 8 << 30, DiskBytes: 20 << 30},
			})
			if err != nil {
				t.Errorf("申请资源租约：%v", err)
				return
			}
			winners <- ok
		}(runID)
	}
	close(start)
	wait.Wait()
	close(winners)
	count := 0
	for won := range winners {
		if won {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("并发申请只能有一个赢家，实际=%d", count)
	}
}

func TestFailedReservationDoesNotDeductAnyResource(t *testing.T) {
	server := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	store := redisstore.NewLeaseStore(client)
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	available := task.Resources{CPUMillicores: 4000, MemoryBytes: 8 << 30, DiskBytes: 20 << 30}

	_, ok, err := store.TryReserve(context.Background(), scheduler.LeaseRequest{
		RunID: "run-too-large", ServerID: "server-a", Now: now, TTL: time.Minute,
		Available: available, Required: task.Resources{CPUMillicores: 5000, MemoryBytes: 1, DiskBytes: 1},
	})
	if err != nil || ok {
		t.Fatalf("超额申请必须完整失败，ok=%v err=%v", ok, err)
	}
	_, ok, err = store.TryReserve(context.Background(), scheduler.LeaseRequest{
		RunID: "run-exact", ServerID: "server-a", Now: now, TTL: time.Minute,
		Available: available, Required: available,
	})
	if err != nil || !ok {
		t.Fatalf("失败申请不得残留部分扣减，ok=%v err=%v", ok, err)
	}
}
