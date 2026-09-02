package ops

import (
	"context"
	"errors"
	"time"
)

type RuleScanner interface {
	Scan(context.Context) error
}

type OutboxScanner interface {
	DeliverDue(context.Context) error
}

type SuccessRecorder interface {
	MarkSuccessfulScan(time.Time)
}

type OperationalScanner interface {
	EnsureSchedules(context.Context, time.Time) error
	RunBackup(context.Context) error
	RunVerification(context.Context) error
}

type Loop struct {
	rules    RuleScanner
	outbox   OutboxScanner
	interval time.Duration
	timeout  time.Duration
	health   SuccessRecorder
	onError  func(error)
	now      func() time.Time
	backup   OperationalScanner
}

func (l *Loop) WithBackup(scanner OperationalScanner) *Loop {
	if l != nil {
		l.backup = scanner
	}
	return l
}

func NewLoop(
	rules RuleScanner,
	outbox OutboxScanner,
	interval, timeout time.Duration,
	health SuccessRecorder,
	onError func(error),
) *Loop {
	if interval <= 0 {
		interval = 15 * time.Second
	}
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	if onError == nil {
		onError = func(error) {}
	}
	return &Loop{
		rules: rules, outbox: outbox, interval: interval, timeout: timeout,
		health: health, onError: onError, now: time.Now,
	}
}

func (l *Loop) Run(ctx context.Context) error {
	if l == nil || l.rules == nil || l.outbox == nil {
		return errors.New("运维扫描循环尚未配置")
	}
	if l.backup != nil {
		go l.runOperationalLoop(ctx)
	}
	l.runControlOnce(ctx)
	ticker := time.NewTicker(l.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			l.runControlOnce(ctx)
		}
	}
}

func (l *Loop) runOnce(parent context.Context) {
	l.runOperationalOnce(parent)
	l.runControlOnce(parent)
}

func (l *Loop) runOperationalLoop(ctx context.Context) {
	l.runOperationalOnce(ctx)
	ticker := time.NewTicker(l.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			l.runOperationalOnce(ctx)
		}
	}
}

func (l *Loop) runOperationalOnce(parent context.Context) {
	succeeded := false
	if l.backup != nil {
		scheduleContext, cancelSchedule := context.WithTimeout(parent, l.timeout)
		if err := l.backup.EnsureSchedules(scheduleContext, l.now().UTC()); err != nil {
			l.onError(err)
		} else {
			succeeded = true
		}
		cancelSchedule()

		backupContext, cancelBackup := context.WithTimeout(parent, 30*time.Minute)
		if err := l.backup.RunBackup(backupContext); err != nil {
			l.onError(err)
		} else {
			succeeded = true
		}
		cancelBackup()

		verificationContext, cancelVerification := context.WithTimeout(parent, 30*time.Minute)
		if err := l.backup.RunVerification(verificationContext); err != nil {
			l.onError(err)
		} else {
			succeeded = true
		}
		cancelVerification()
	}
	if succeeded && l.health != nil {
		l.health.MarkSuccessfulScan(l.now().UTC())
	}
}

func (l *Loop) runControlOnce(parent context.Context) {
	succeeded := false
	ruleContext, cancelRules := context.WithTimeout(parent, l.timeout)
	if err := l.rules.Scan(ruleContext); err != nil {
		l.onError(err)
	} else {
		succeeded = true
	}
	cancelRules()

	outboxContext, cancelOutbox := context.WithTimeout(parent, l.timeout)
	if err := l.outbox.DeliverDue(outboxContext); err != nil {
		l.onError(err)
	} else {
		succeeded = true
	}
	cancelOutbox()
	if succeeded && l.health != nil {
		l.health.MarkSuccessfulScan(l.now().UTC())
	}
}
