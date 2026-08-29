package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"yunling.local/platform/internal/agentprotocol"
	"yunling.local/platform/internal/secret"
)

const (
	DefaultBatchSize     = 20
	DefaultRetryInterval = 10 * time.Second
	DefaultScanInterval  = 2 * time.Second
	DefaultTasksMax      = 64

	parametersEnvironmentKey = "YUNLING_PARAMETERS_JSON"
	secretsEnvironmentKey    = "YUNLING_SECRETS_JSON"
	runIDEnvironmentKey      = "YUNLING_RUN_ID"
	versionEnvironmentKey    = "YUNLING_SCRIPT_VERSION_ID"

	invalidPayloadMessage    = "任务执行配置无效，无法下发"
	secretUnavailableMessage = "任务敏感参数不可用，无法下发"
	transientDispatchMessage  = "目标服务器暂时不可用，任务将自动重试"
)

type CommandSender interface {
	SendExecutionCommand(context.Context, string, agentprotocol.ExecutionCommand) error
}

type SecretResolver interface {
	ResolveForRun(context.Context, []secret.ID) (map[string]string, error)
}

type FailureSink interface {
	Apply(context.Context, agentprotocol.RunEvent) error
}

type Service struct {
	store    Store
	sender   CommandSender
	resolver SecretResolver
	failures FailureSink
	now      func() time.Time
}

func NewService(store Store, sender CommandSender, resolver SecretResolver, failures FailureSink, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{store: store, sender: sender, resolver: resolver, failures: failures, now: now}
}

func RunLoop(
	ctx context.Context,
	service interface{ Dispatch(context.Context) error },
	interval time.Duration,
	logError func(error),
) {
	if service == nil {
		return
	}
	if interval <= 0 {
		interval = DefaultScanInterval
	}
	dispatchOnce := func() {
		if err := service.Dispatch(ctx); err != nil && logError != nil {
			logError(err)
		}
	}
	dispatchOnce()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			dispatchOnce()
		}
	}
}

func (s *Service) Dispatch(ctx context.Context) error {
	if s == nil || s.store == nil {
		return fmt.Errorf("任务派发存储不可用")
	}
	now := s.now().UTC()
	runs, err := s.store.Claim(ctx, now.Add(-DefaultRetryInterval), now, DefaultBatchSize)
	if err != nil {
		return fmt.Errorf("领取待派发任务：%w", err)
	}
	var infrastructureErrors []error
	for _, run := range runs {
		command, failureMessage, valid := s.executionCommand(ctx, run)
		if !valid {
			if s.failures == nil {
				infrastructureErrors = append(infrastructureErrors, fmt.Errorf("运行 %s 的失败事件服务不可用", run.ID))
				continue
			}
			event := agentprotocol.RunEvent{
				RunID:          run.ID,
				ExecutionToken: run.ExecutionToken,
				Sequence:       1,
				Type:           "failed",
				OccurredAt:     now,
				ExitCode:       -1,
				Message:        failureMessage,
			}
			if err := s.failures.Apply(ctx, event); err != nil {
				infrastructureErrors = append(infrastructureErrors, fmt.Errorf("保存运行 %s 的派发失败事件：%w", run.ID, err))
			}
			continue
		}
		if s.sender == nil {
			if err := s.store.RecordResult(ctx, run.ID, run.ExecutionToken, transientDispatchMessage); err != nil {
				infrastructureErrors = append(infrastructureErrors, fmt.Errorf("记录运行 %s 的派发结果：%w", run.ID, err))
			}
			continue
		}
		if err := s.sender.SendExecutionCommand(ctx, run.ServerID, command); err != nil {
			if recordErr := s.store.RecordResult(ctx, run.ID, run.ExecutionToken, transientDispatchMessage); recordErr != nil {
				infrastructureErrors = append(infrastructureErrors, fmt.Errorf("记录运行 %s 的派发结果：%w", run.ID, recordErr))
			}
		}
	}
	return errors.Join(infrastructureErrors...)
}

func (s *Service) executionCommand(ctx context.Context, run Run) (agentprotocol.ExecutionCommand, string, bool) {
	entrypoint := run.Entrypoint
	cleaned := path.Clean(entrypoint)
	if strings.TrimSpace(entrypoint) == "" || cleaned != entrypoint || path.IsAbs(entrypoint) || cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") || strings.Contains(entrypoint, `\`) {
		return agentprotocol.ExecutionCommand{}, invalidPayloadMessage, false
	}
	parameters := run.Parameters
	if parameters == nil {
		parameters = map[string]any{}
	}
	parametersJSON, err := json.Marshal(parameters)
	if err != nil {
		return agentprotocol.ExecutionCommand{}, invalidPayloadMessage, false
	}
	secretValues, ok := s.resolveSecretBindings(ctx, run.SecretBindings)
	if !ok {
		return agentprotocol.ExecutionCommand{}, secretUnavailableMessage, false
	}
	secretsJSON, err := json.Marshal(secretValues)
	if err != nil {
		return agentprotocol.ExecutionCommand{}, secretUnavailableMessage, false
	}
	resources := run.Resources
	if resources.TasksMax <= 0 {
		resources.TasksMax = DefaultTasksMax
	}
	assignment := &agentprotocol.Assignment{
		RunID:           run.ID,
		ExecutionToken:  run.ExecutionToken,
		ScriptVersionID: run.ScriptVersionID,
		Runtime:         run.Runtime,
		ScriptPath: path.Join(
			"/var/lib/yunling-agent/script-cache/scripts",
			run.ScriptID,
			run.ScriptVersionID,
			entrypoint,
		),
		Arguments: []string{},
		Environment: map[string]string{
			runIDEnvironmentKey:       run.ID,
			versionEnvironmentKey:     run.ScriptVersionID,
			parametersEnvironmentKey: string(parametersJSON),
			secretsEnvironmentKey:    string(secretsJSON),
		},
		Resources: resources,
		Timeout:   run.Timeout,
	}
	return agentprotocol.ExecutionCommand{Type: agentprotocol.CommandAssign, Assignment: assignment}, "", true
}

func (s *Service) resolveSecretBindings(ctx context.Context, bindings map[string]string) (map[string]string, bool) {
	if len(bindings) == 0 {
		return map[string]string{}, true
	}
	if s.resolver == nil {
		return nil, false
	}
	names := make([]string, 0, len(bindings))
	for name := range bindings {
		names = append(names, name)
	}
	sort.Strings(names)
	refs := make([]secret.ID, 0, len(names))
	for _, name := range names {
		ref := strings.TrimSpace(bindings[name])
		if strings.TrimSpace(name) == "" || ref == "" {
			return nil, false
		}
		refs = append(refs, secret.ID(ref))
	}
	resolved, err := s.resolver.ResolveForRun(ctx, refs)
	if err != nil {
		return nil, false
	}
	valuesByName := make(map[string]string, len(names))
	for _, name := range names {
		value, exists := resolved[strings.TrimSpace(bindings[name])]
		if !exists {
			return nil, false
		}
		valuesByName[name] = value
	}
	return valuesByName, true
}
