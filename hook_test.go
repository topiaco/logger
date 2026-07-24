package logger

import (
	"context"
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

	done := make(chan struct{})
	go func() {
		globalHookDispatcher.dispatch(event)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(50 * time.Millisecond):
		t.Fatal("dispatch blocked when queue was full")
	}

	close(hook.release)
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
