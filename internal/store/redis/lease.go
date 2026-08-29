package redisstore

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	goredis "github.com/redis/go-redis/v9"
	"yunling.local/platform/internal/scheduler"
)

const reserveScript = `
local expired = redis.call('ZRANGEBYSCORE', KEYS[3], '-inf', ARGV[1])
for _, run_id in ipairs(expired) do
  local packed = redis.call('HGET', KEYS[2], run_id)
  if packed then
    local cpu, memory, disk = string.match(packed, '^(%d+):(%d+):(%d+)$')
    if cpu then
      redis.call('HINCRBY', KEYS[1], 'cpu', -tonumber(cpu))
      redis.call('HINCRBY', KEYS[1], 'memory', -tonumber(memory))
      redis.call('HINCRBY', KEYS[1], 'disk', -tonumber(disk))
    end
    redis.call('HDEL', KEYS[2], run_id)
  end
  redis.call('ZREM', KEYS[3], run_id)
end

if redis.call('HEXISTS', KEYS[2], ARGV[2]) == 1 then
  return 0
end

local reserved_cpu = tonumber(redis.call('HGET', KEYS[1], 'cpu') or '0')
local reserved_memory = tonumber(redis.call('HGET', KEYS[1], 'memory') or '0')
local reserved_disk = tonumber(redis.call('HGET', KEYS[1], 'disk') or '0')
local required_cpu = tonumber(ARGV[3])
local required_memory = tonumber(ARGV[4])
local required_disk = tonumber(ARGV[5])
if required_cpu > tonumber(ARGV[6]) - reserved_cpu
  or required_memory > tonumber(ARGV[7]) - reserved_memory
  or required_disk > tonumber(ARGV[8]) - reserved_disk then
  return 0
end

redis.call('HINCRBY', KEYS[1], 'cpu', required_cpu)
redis.call('HINCRBY', KEYS[1], 'memory', required_memory)
redis.call('HINCRBY', KEYS[1], 'disk', required_disk)
redis.call('HSET', KEYS[2], ARGV[2], ARGV[3] .. ':' .. ARGV[4] .. ':' .. ARGV[5])
redis.call('ZADD', KEYS[3], ARGV[9], ARGV[2])
redis.call('SET', KEYS[4], ARGV[10], 'PX', ARGV[11])
return 1
`

const releaseScript = `
local packed = redis.call('HGET', KEYS[2], ARGV[1])
if packed then
  local cpu, memory, disk = string.match(packed, '^(%d+):(%d+):(%d+)$')
  if cpu then
    redis.call('HINCRBY', KEYS[1], 'cpu', -tonumber(cpu))
    redis.call('HINCRBY', KEYS[1], 'memory', -tonumber(memory))
    redis.call('HINCRBY', KEYS[1], 'disk', -tonumber(disk))
  end
  redis.call('HDEL', KEYS[2], ARGV[1])
end
redis.call('ZREM', KEYS[3], ARGV[1])
redis.call('DEL', KEYS[4])
return 1
`

const restoreScript = `
local packed = redis.call('HGET', KEYS[2], ARGV[1])
if not packed then
  redis.call('HINCRBY', KEYS[1], 'cpu', ARGV[2])
  redis.call('HINCRBY', KEYS[1], 'memory', ARGV[3])
  redis.call('HINCRBY', KEYS[1], 'disk', ARGV[4])
else
  local old_cpu, old_memory, old_disk = string.match(packed, '^(%d+):(%d+):(%d+)$')
  if not old_cpu then
    return redis.error_reply('invalid stored lease amounts')
  end
  redis.call('HINCRBY', KEYS[1], 'cpu', tonumber(ARGV[2]) - tonumber(old_cpu))
  redis.call('HINCRBY', KEYS[1], 'memory', tonumber(ARGV[3]) - tonumber(old_memory))
  redis.call('HINCRBY', KEYS[1], 'disk', tonumber(ARGV[4]) - tonumber(old_disk))
end
redis.call('HSET', KEYS[2], ARGV[1], ARGV[2] .. ':' .. ARGV[3] .. ':' .. ARGV[4])
redis.call('ZADD', KEYS[3], ARGV[5], ARGV[1])
redis.call('SET', KEYS[4], ARGV[6], 'PX', ARGV[7])
return 1
`

type LeaseStore struct {
	client goredis.UniversalClient
}

func NewLeaseStore(client goredis.UniversalClient) *LeaseStore {
	return &LeaseStore{client: client}
}

func (s *LeaseStore) TryReserve(ctx context.Context, request scheduler.LeaseRequest) (scheduler.Lease, bool, error) {
	if err := validateRequest(request); err != nil {
		return scheduler.Lease{}, false, err
	}
	leaseID := uuid.NewString()
	expiresAt := request.Now.Add(request.TTL)
	keys := leaseKeys(request.ServerID, request.RunID)
	result, err := s.client.Eval(ctx, reserveScript, keys,
		request.Now.UnixMilli(), request.RunID,
		request.Required.CPUMillicores, request.Required.MemoryBytes, request.Required.DiskBytes,
		request.Available.CPUMillicores, request.Available.MemoryBytes, request.Available.DiskBytes,
		expiresAt.UnixMilli(), leaseID, request.TTL.Milliseconds(),
	).Int64()
	if err != nil {
		return scheduler.Lease{}, false, fmt.Errorf("申请 Redis 资源租约：%w", err)
	}
	if result != 1 {
		return scheduler.Lease{}, false, nil
	}
	return scheduler.Lease{ID: leaseID, RunID: request.RunID, ServerID: request.ServerID, Resources: request.Required, ExpiresAt: expiresAt}, true, nil
}

func (s *LeaseStore) Release(ctx context.Context, lease scheduler.Lease) error {
	if lease.RunID == "" || lease.ServerID == "" {
		return scheduler.ErrInvalidLeaseRequest
	}
	if _, err := s.client.Eval(ctx, releaseScript, leaseKeys(lease.ServerID, lease.RunID), lease.RunID).Result(); err != nil {
		return fmt.Errorf("释放 Redis 资源租约：%w", err)
	}
	return nil
}

// Restore rebuilds a database-backed active lease after Redis state loss. The
// operation is idempotent for a run so scheduler scans can safely repeat it.
func (s *LeaseStore) Restore(ctx context.Context, lease scheduler.Lease, now time.Time) error {
	if lease.ID == "" || lease.RunID == "" || lease.ServerID == "" || now.IsZero() ||
		lease.Resources.CPUMillicores <= 0 || lease.Resources.MemoryBytes <= 0 || lease.Resources.DiskBytes <= 0 {
		return scheduler.ErrInvalidLeaseRequest
	}
	ttl := lease.ExpiresAt.Sub(now)
	if ttl <= 0 {
		return nil
	}
	if _, err := s.client.Eval(ctx, restoreScript, leaseKeys(lease.ServerID, lease.RunID),
		lease.RunID, lease.Resources.CPUMillicores, lease.Resources.MemoryBytes,
		lease.Resources.DiskBytes, lease.ExpiresAt.UnixMilli(), lease.ID, ttl.Milliseconds(),
	).Result(); err != nil {
		return fmt.Errorf("恢复 Redis 资源租约：%w", err)
	}
	return nil
}

func validateRequest(request scheduler.LeaseRequest) error {
	if request.RunID == "" || request.ServerID == "" || request.TTL <= 0 || request.Now.IsZero() ||
		request.Required.CPUMillicores <= 0 || request.Required.MemoryBytes <= 0 || request.Required.DiskBytes <= 0 ||
		request.Available.CPUMillicores < 0 || request.Available.MemoryBytes < 0 || request.Available.DiskBytes < 0 {
		return scheduler.ErrInvalidLeaseRequest
	}
	return nil
}

func leaseKeys(serverID, runID string) []string {
	tag := "{" + serverID + "}"
	base := "yunling:scheduler:" + tag
	return []string{
		base + ":reserved",
		base + ":amounts",
		base + ":expirations",
		base + ":lease:" + runID,
	}
}
