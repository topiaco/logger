package logger

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

type fakeRedisStreamClient struct {
	args *redis.XAddArgs
	err  error
}

func (c *fakeRedisStreamClient) XAdd(ctx context.Context, args *redis.XAddArgs) *redis.StringCmd {
	c.args = args
	return redis.NewStringResult("1-0", c.err)
}

func TestNewRedisStreamHookDefaults(t *testing.T) {
	client := &fakeRedisStreamClient{}
	hook, err := NewRedisStreamHook(client, RedisStreamHookConfig{})
	if err != nil {
		t.Fatalf("NewRedisStreamHook() error = %v", err)
	}

	if hook.Name() != defaultRedisStreamHookName {
		t.Fatalf("Name() = %q, want %q", hook.Name(), defaultRedisStreamHookName)
	}
	if hook.stream != defaultRedisStreamKey {
		t.Fatalf("stream = %q, want %q", hook.stream, defaultRedisStreamKey)
	}
	if hook.maxLen != defaultRedisStreamMaxLen {
		t.Fatalf("maxLen = %d, want %d", hook.maxLen, defaultRedisStreamMaxLen)
	}
	if hook.service == "" {
		t.Fatal("service is empty")
	}
}

func TestRedisStreamHookHandleWritesXAdd(t *testing.T) {
	client := &fakeRedisStreamClient{}
	hook, err := NewRedisStreamHook(client, RedisStreamHookConfig{
		Name:    "redis_errors",
		Service: "pms-server",
		Stream:  "logger:pms:error_logs",
		MaxLen:  5000,
	})
	if err != nil {
		t.Fatalf("NewRedisStreamHook() error = %v", err)
	}

	event := LogEvent{
		Time:      time.Date(2026, 7, 24, 10, 30, 0, 0, time.Local),
		EventID:   "123e4567-e89b-42d3-a456-426614174000",
		Level:     ErrorLevel,
		LevelName: "error",
		Message:   "failed to save",
		TraceID:   "trace-123",
		Caller:    "service.go:10",
		Fields: map[string]interface{}{
			"order_id": "A001",
		},
		Raw: map[string]interface{}{
			"level":   "error",
			"message": "failed to save",
		},
	}

	if err := hook.Handle(context.Background(), event); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}

	args := client.args
	if args == nil {
		t.Fatal("XAdd was not called")
	}
	if args.Stream != "logger:pms:error_logs" {
		t.Fatalf("Stream = %q, want logger:pms:error_logs", args.Stream)
	}
	if args.MaxLen != 5000 {
		t.Fatalf("MaxLen = %d, want 5000", args.MaxLen)
	}
	if !args.Approx {
		t.Fatal("Approx = false, want true")
	}

	values, ok := args.Values.(map[string]interface{})
	if !ok {
		t.Fatalf("Values type = %T, want map[string]interface{}", args.Values)
	}
	wantFields := []string{
		"schema_version", "event_id", "time", "level", "message",
		"trace_id", "caller", "service", "fields", "raw",
	}
	if len(values) != len(wantFields) {
		t.Fatalf("field count = %d, want %d: %v", len(values), len(wantFields), values)
	}
	for _, field := range wantFields {
		if _, exists := values[field]; !exists {
			t.Fatalf("field %q is missing from XADD values", field)
		}
	}
	if values["service"] != "pms-server" {
		t.Fatalf("service = %v, want pms-server", values["service"])
	}
	if values["schema_version"] != RedisStreamEventSchemaVersion {
		t.Fatalf("schema_version = %v, want %s", values["schema_version"], RedisStreamEventSchemaVersion)
	}
	if values["event_id"] != event.EventID {
		t.Fatalf("event_id = %v, want %s", values["event_id"], event.EventID)
	}
	if values["level"] != "error" {
		t.Fatalf("level = %v, want error", values["level"])
	}
	if values["message"] != "failed to save" {
		t.Fatalf("message = %v, want failed to save", values["message"])
	}
	if values["trace_id"] != "trace-123" {
		t.Fatalf("trace_id = %v, want trace-123", values["trace_id"])
	}
	if !strings.Contains(values["fields"].(string), `"order_id":"A001"`) {
		t.Fatalf("fields = %v, want order_id JSON", values["fields"])
	}

	var raw map[string]interface{}
	if err := json.Unmarshal([]byte(values["raw"].(string)), &raw); err != nil {
		t.Fatalf("raw is not JSON: %v", err)
	}
	if raw["message"] != "failed to save" {
		t.Fatalf("raw[message] = %v, want failed to save", raw["message"])
	}
}

func TestRedisStreamHookHandleGeneratesEventID(t *testing.T) {
	client := &fakeRedisStreamClient{}
	hook, err := NewRedisStreamHook(client, RedisStreamHookConfig{Service: "pms-server"})
	if err != nil {
		t.Fatalf("NewRedisStreamHook() error = %v", err)
	}

	event := LogEvent{
		Time:      time.Date(2026, 7, 24, 10, 30, 0, 0, time.Local),
		LevelName: "error",
		Message:   "failed to save",
	}
	if err := hook.Handle(context.Background(), event); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}

	values := client.args.Values.(map[string]interface{})
	eventID, ok := values["event_id"].(string)
	if !ok || !isUUIDV4(eventID) {
		t.Fatalf("event_id = %v, want UUID v4", values["event_id"])
	}
	if _, err := DecodeRedisStreamEvent(values); err != nil {
		t.Fatalf("DecodeRedisStreamEvent() error = %v", err)
	}
}

func TestRedisStreamHookRejectsInvalidEventID(t *testing.T) {
	client := &fakeRedisStreamClient{}
	hook, err := NewRedisStreamHook(client, RedisStreamHookConfig{Service: "pms-server"})
	if err != nil {
		t.Fatalf("NewRedisStreamHook() error = %v", err)
	}

	err = hook.Handle(context.Background(), LogEvent{
		Time:      time.Date(2026, 7, 24, 10, 30, 0, 0, time.Local),
		EventID:   "not-a-uuid",
		LevelName: "error",
	})
	if err == nil {
		t.Fatal("Handle() error = nil, want invalid event_id error")
	}
	if client.args != nil {
		t.Fatal("XAdd was called for an invalid event_id")
	}
}
