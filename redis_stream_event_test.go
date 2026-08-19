package logger

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

const testRedisStreamEventID = "123e4567-e89b-42d3-a456-426614174000"

func validRedisStreamValues() map[string]interface{} {
	return map[string]interface{}{
		"schema_version": RedisStreamEventSchemaVersion,
		"event_id":       testRedisStreamEventID,
		"time":           "2026-07-24 10:30:00",
		"level":          "error",
		"message":        "failed to save",
		"trace_id":       "trace-123",
		"caller":         "service.go:10",
		"service":        "pms-server",
		"fields":         `{"order_id":"A001","attempt":2}`,
		"raw":            `{"level":"error","message":"failed to save"}`,
	}
}

func TestDecodeRedisStreamEventV1(t *testing.T) {
	values := validRedisStreamValues()
	values["schema_version"] = []byte(RedisStreamEventSchemaVersion)
	values["message"] = []byte("failed to save")

	event, err := DecodeRedisStreamEvent(values)
	if err != nil {
		t.Fatalf("DecodeRedisStreamEvent() error = %v", err)
	}
	if event.SchemaVersion != RedisStreamEventSchemaVersion {
		t.Fatalf("SchemaVersion = %q, want %q", event.SchemaVersion, RedisStreamEventSchemaVersion)
	}
	if event.EventID != testRedisStreamEventID {
		t.Fatalf("EventID = %q, want %q", event.EventID, testRedisStreamEventID)
	}
	if event.Message != "failed to save" {
		t.Fatalf("Message = %q, want failed to save", event.Message)
	}
	if event.Fields["order_id"] != "A001" {
		t.Fatalf("Fields[order_id] = %v, want A001", event.Fields["order_id"])
	}
	if fmt.Sprint(event.Fields["attempt"]) != "2" {
		t.Fatalf("Fields[attempt] = %v, want 2", event.Fields["attempt"])
	}
	if event.Raw["level"] != "error" {
		t.Fatalf("Raw[level] = %v, want error", event.Raw["level"])
	}
}

func TestDecodeRedisStreamEventLegacyV0(t *testing.T) {
	values := validRedisStreamValues()
	delete(values, "schema_version")
	delete(values, "event_id")

	event, err := DecodeRedisStreamEvent(values)
	if err != nil {
		t.Fatalf("DecodeRedisStreamEvent() error = %v", err)
	}
	if event.SchemaVersion != RedisStreamLegacySchemaVersion {
		t.Fatalf("SchemaVersion = %q, want %q", event.SchemaVersion, RedisStreamLegacySchemaVersion)
	}
	if event.EventID != "" {
		t.Fatalf("EventID = %q, want empty for legacy event", event.EventID)
	}
}

func TestDecodeRedisStreamEventRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(map[string]interface{})
		wantError string
	}{
		{
			name: "unknown schema",
			mutate: func(values map[string]interface{}) {
				values["schema_version"] = "2"
			},
			wantError: "unsupported schema_version",
		},
		{
			name: "missing v1 event id",
			mutate: func(values map[string]interface{}) {
				delete(values, "event_id")
			},
			wantError: "event_id is required",
		},
		{
			name: "invalid event id",
			mutate: func(values map[string]interface{}) {
				values["event_id"] = "not-a-uuid"
			},
			wantError: "event_id must be a UUID v4",
		},
		{
			name: "invalid time",
			mutate: func(values map[string]interface{}) {
				values["time"] = "2026-07-24T10:30:00Z"
			},
			wantError: "time must use format",
		},
		{
			name: "missing existing field",
			mutate: func(values map[string]interface{}) {
				delete(values, "trace_id")
			},
			wantError: `field "trace_id" is missing`,
		},
		{
			name: "invalid fields JSON",
			mutate: func(values map[string]interface{}) {
				values["fields"] = "[]"
			},
			wantError: `field "fields" is not a JSON object`,
		},
		{
			name: "trailing raw JSON",
			mutate: func(values map[string]interface{}) {
				values["raw"] = `{} {}`
			},
			wantError: `field "raw" is not a single JSON object`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			values := validRedisStreamValues()
			tt.mutate(values)

			_, err := DecodeRedisStreamEvent(values)
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("DecodeRedisStreamEvent() error = %v, want containing %q", err, tt.wantError)
			}
		})
	}
}

func TestValidateRedisStreamEventAcceptsConstructedLegacyEvent(t *testing.T) {
	event := RedisStreamEvent{
		Time:    "2026-07-24 10:30:00",
		Level:   "error",
		Service: "pms-server",
		Fields:  map[string]interface{}{},
		Raw:     map[string]interface{}{},
	}
	if err := ValidateRedisStreamEvent(event); err != nil {
		t.Fatalf("ValidateRedisStreamEvent() error = %v", err)
	}
}

func TestLogEventIDIsStableAcrossHooks(t *testing.T) {
	withTestLoggerConfig(t)

	first := &captureHook{name: "event-id-first", events: make(chan LogEvent, 1)}
	second := &captureHook{name: "event-id-second", events: make(chan LogEvent, 1)}
	if err := RegisterHook(first, WithTimeout(time.Second)); err != nil {
		t.Fatalf("RegisterHook(first) error = %v", err)
	}
	if err := RegisterHook(second, WithTimeout(time.Second)); err != nil {
		t.Fatalf("RegisterHook(second) error = %v", err)
	}

	New("trace-123").Error("failed to save")
	firstEvent := receiveLogEvent(t, first.events)
	secondEvent := receiveLogEvent(t, second.events)
	if !isUUIDV4(firstEvent.EventID) {
		t.Fatalf("EventID = %q, want UUID v4", firstEvent.EventID)
	}
	if firstEvent.EventID != secondEvent.EventID {
		t.Fatalf("EventID differs across hooks: %q != %q", firstEvent.EventID, secondEvent.EventID)
	}

	if err := ShutdownHooks(context.Background()); err != nil {
		t.Fatalf("ShutdownHooks() error = %v", err)
	}
}

func receiveLogEvent(t *testing.T, events <-chan LogEvent) LogEvent {
	t.Helper()
	select {
	case event := <-events:
		return event
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for hook event")
		return LogEvent{}
	}
}
