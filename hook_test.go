package logger

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type captureHook struct {
	name   string
	events chan LogEvent
}

func (h *captureHook) Name() string {
	if h.name == "" {
		return "capture"
	}
	return h.name
}

func (h *captureHook) Handle(ctx context.Context, event LogEvent) error {
	select {
	case h.events <- event:
	case <-ctx.Done():
		return ctx.Err()
	}
	return nil
}

type funcHook struct {
	name   string
	handle func(context.Context, LogEvent) error
}

func (h *funcHook) Name() string {
	return h.name
}

func (h *funcHook) Handle(ctx context.Context, event LogEvent) error {
	return h.handle(ctx, event)
}

func requireHookStats(t *testing.T, name string, want HookStats) {
	t.Helper()

	got, ok := GetHookStats(name)
	if !ok {
		t.Fatalf("GetHookStats(%q) ok = false, want true", name)
	}
	if got != want {
		t.Fatalf("GetHookStats(%q) = %+v, want %+v", name, got, want)
	}
}

func withTestLoggerConfig(t *testing.T) {
	t.Helper()

	oldConf := loggerConf
	oldDefaultLevel := DefaultLogLevel
	oldStd := Std
	t.Cleanup(func() {
		loggerConf = oldConf
		DefaultLogLevel = oldDefaultLevel
		Std = oldStd
		_ = ShutdownHooks(context.Background())
	})

	loggerConf = &LoggerConf{
		LogLevel:              TraceLevel,
		ConsoleLoggingEnabled: false,
		FileLoggingEnabled:    false,
	}
	DefaultLogLevel = TraceLevel
	_ = ShutdownHooks(context.Background())
}

func TestRegisterHookDispatchesErrorEvents(t *testing.T) {
	withTestLoggerConfig(t)

	hook := &captureHook{events: make(chan LogEvent, 1)}
	if err := RegisterHook(hook, WithTimeout(time.Second)); err != nil {
		t.Fatalf("RegisterHook() error = %v", err)
	}

	New("trace-123").WithField("order_id", "A001").Caller(4).Errorf("failed: %s", "redis")

	select {
	case event := <-hook.events:
		if event.Level != ErrorLevel {
			t.Fatalf("event.Level = %v, want %v", event.Level, ErrorLevel)
		}
		if event.LevelName != "error" {
			t.Fatalf("event.LevelName = %q, want error", event.LevelName)
		}
		if event.Message != "failed: redis" {
			t.Fatalf("event.Message = %q, want failed: redis", event.Message)
		}
		if event.TraceID != "trace-123" {
			t.Fatalf("event.TraceID = %q, want trace-123", event.TraceID)
		}
		if event.Fields["order_id"] != "A001" {
			t.Fatalf("event.Fields[order_id] = %v, want A001", event.Fields["order_id"])
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for hook event")
	}
}

func TestRegisterHookFiltersBelowMinLevel(t *testing.T) {
	withTestLoggerConfig(t)

	hook := &captureHook{events: make(chan LogEvent, 1)}
	if err := RegisterHook(hook, WithTimeout(time.Second)); err != nil {
		t.Fatalf("RegisterHook() error = %v", err)
	}

	New("trace-123").Info("hello")

	select {
	case event := <-hook.events:
		t.Fatalf("unexpected event: %+v", event)
	case <-time.After(50 * time.Millisecond):
	}
}

type blockingHook struct {
	started sync.Once
	start   chan struct{}
	release chan struct{}
}

func (h *blockingHook) Name() string {
	return "blocking"
}

func (h *blockingHook) Handle(ctx context.Context, event LogEvent) error {
	h.started.Do(func() {
		close(h.start)
	})

	select {
	case <-h.release:
	case <-ctx.Done():
		return ctx.Err()
	}

	return nil
}

func TestHookDispatchDropsWhenQueueIsFull(t *testing.T) {
	withTestLoggerConfig(t)

	hook := &blockingHook{
		start:   make(chan struct{}),
		release: make(chan struct{}),
	}
	if err := RegisterHook(hook, WithQueueSize(1), WithTimeout(time.Second)); err != nil {
		t.Fatalf("RegisterHook() error = %v", err)
	}

	event := LogEvent{Level: ErrorLevel, LevelName: "error", Message: "failed"}
	globalHookDispatcher.dispatch(event)

	select {
	case <-hook.start:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for blocking hook to start")
	}

	globalHookDispatcher.dispatch(event)

	dispatchDone := make(chan struct{})
	go func() {
		globalHookDispatcher.dispatch(event)
		close(dispatchDone)
	}()

	select {
	case <-dispatchDone:
	case <-time.After(50 * time.Millisecond):
		t.Fatal("dispatch blocked when queue was full")
	}

	requireHookStats(t, hook.Name(), HookStats{
		Name:       hook.Name(),
		Dispatched: 2,
		Dropped:    1,
	})

	close(hook.release)
	if err := ShutdownHooks(context.Background()); err != nil {
		t.Fatalf("ShutdownHooks() error = %v", err)
	}
	requireHookStats(t, hook.Name(), HookStats{
		Name:       hook.Name(),
		Dispatched: 2,
		Processed:  2,
		Dropped:    1,
	})
}

func TestHookStatsCountsHandleFailure(t *testing.T) {
	withTestLoggerConfig(t)

	hook := &funcHook{
		name: "failure",
		handle: func(context.Context, LogEvent) error {
			return errors.New("sink unavailable")
		},
	}
	if err := RegisterHook(hook, WithTimeout(time.Second)); err != nil {
		t.Fatalf("RegisterHook() error = %v", err)
	}

	globalHookDispatcher.dispatch(LogEvent{Level: ErrorLevel})
	if err := ShutdownHooks(context.Background()); err != nil {
		t.Fatalf("ShutdownHooks() error = %v", err)
	}

	requireHookStats(t, hook.Name(), HookStats{
		Name:       hook.Name(),
		Dispatched: 1,
		Processed:  1,
		Failed:     1,
	})
}

func TestHookStatsCountsTimeoutAsFailure(t *testing.T) {
	withTestLoggerConfig(t)

	hook := &funcHook{
		name: "timeout",
		handle: func(ctx context.Context, _ LogEvent) error {
			<-ctx.Done()
			return nil
		},
	}
	if err := RegisterHook(hook, WithTimeout(20*time.Millisecond)); err != nil {
		t.Fatalf("RegisterHook() error = %v", err)
	}

	globalHookDispatcher.dispatch(LogEvent{Level: ErrorLevel})
	if err := ShutdownHooks(context.Background()); err != nil {
		t.Fatalf("ShutdownHooks() error = %v", err)
	}

	requireHookStats(t, hook.Name(), HookStats{
		Name:       hook.Name(),
		Dispatched: 1,
		Processed:  1,
		Failed:     1,
	})
}

func TestShutdownHooksDrainsQueueAndKeepsFinalStats(t *testing.T) {
	withTestLoggerConfig(t)

	hook := &funcHook{
		name: "shutdown-drain",
		handle: func(context.Context, LogEvent) error {
			return nil
		},
	}
	if err := RegisterHook(hook, WithQueueSize(8), WithTimeout(time.Second)); err != nil {
		t.Fatalf("RegisterHook() error = %v", err)
	}

	for i := 0; i < 5; i++ {
		globalHookDispatcher.dispatch(LogEvent{Level: ErrorLevel})
	}
	if err := ShutdownHooks(context.Background()); err != nil {
		t.Fatalf("ShutdownHooks() error = %v", err)
	}

	requireHookStats(t, hook.Name(), HookStats{
		Name:       hook.Name(),
		Dispatched: 5,
		Processed:  5,
	})
}

func TestShutdownHooksHonorsContextDeadline(t *testing.T) {
	withTestLoggerConfig(t)

	started := make(chan struct{})
	release := make(chan struct{})
	hook := &funcHook{
		name: "shutdown-timeout",
		handle: func(context.Context, LogEvent) error {
			close(started)
			<-release
			return nil
		},
	}
	if err := RegisterHook(hook, WithTimeout(time.Second)); err != nil {
		t.Fatalf("RegisterHook() error = %v", err)
	}

	globalHookDispatcher.dispatch(LogEvent{Level: ErrorLevel})
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for hook to start")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := ShutdownHooks(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("ShutdownHooks() error = %v, want %v", err, context.DeadlineExceeded)
	}
	requireHookStats(t, hook.Name(), HookStats{
		Name:       hook.Name(),
		Dispatched: 1,
	})

	close(release)
	deadline := time.Now().Add(time.Second)
	for {
		stats, ok := GetHookStats(hook.Name())
		if ok && stats.Processed == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for hook to finish after ShutdownHooks deadline")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestHookStatsAreSafeDuringConcurrentDispatch(t *testing.T) {
	withTestLoggerConfig(t)

	hook := &funcHook{
		name: "concurrent-stats",
		handle: func(context.Context, LogEvent) error {
			return nil
		},
	}
	if err := RegisterHook(hook, WithQueueSize(1024), WithTimeout(time.Second)); err != nil {
		t.Fatalf("RegisterHook() error = %v", err)
	}

	const eventCount = 500
	dispatched := make(chan struct{})
	go func() {
		defer close(dispatched)
		for i := 0; i < eventCount; i++ {
			globalHookDispatcher.dispatch(LogEvent{Level: ErrorLevel})
		}
	}()

	for {
		_, _ = GetHookStats(hook.Name())
		_ = ListHookStats()
		select {
		case <-dispatched:
			goto dispatchComplete
		default:
		}
	}

dispatchComplete:
	if err := ShutdownHooks(context.Background()); err != nil {
		t.Fatalf("ShutdownHooks() error = %v", err)
	}
	requireHookStats(t, hook.Name(), HookStats{
		Name:       hook.Name(),
		Dispatched: eventCount,
		Processed:  eventCount,
	})
}

func TestUnregisterHookStopsDispatch(t *testing.T) {
	withTestLoggerConfig(t)

	hook := &captureHook{name: "remove-me", events: make(chan LogEvent, 1)}
	if err := RegisterHook(hook, WithTimeout(time.Second)); err != nil {
		t.Fatalf("RegisterHook() error = %v", err)
	}

	UnregisterHook("remove-me")
	New("trace-123").Error("hello")

	select {
	case event := <-hook.events:
		t.Fatalf("unexpected event after unregister: %+v", event)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestRegisterHookRejectsDuplicateNames(t *testing.T) {
	withTestLoggerConfig(t)

	if err := RegisterHook(&captureHook{name: "dup", events: make(chan LogEvent, 1)}); err != nil {
		t.Fatalf("RegisterHook() error = %v", err)
	}

	if err := RegisterHook(&captureHook{name: "dup", events: make(chan LogEvent, 1)}); err == nil {
		t.Fatal("RegisterHook() error = nil, want duplicate error")
	}
}

func TestPackageFormatFunctionsUseFormatting(t *testing.T) {
	withTestLoggerConfig(t)

	hook := &captureHook{events: make(chan LogEvent, 1)}
	if err := RegisterHook(hook, WithTimeout(time.Second)); err != nil {
		t.Fatalf("RegisterHook() error = %v", err)
	}

	Std = New("trace-std")
	Errorf("failed: %s", "redis")

	select {
	case event := <-hook.events:
		if event.Message != "failed: redis" {
			t.Fatalf("event.Message = %q, want failed: redis", event.Message)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for hook event")
	}
}
