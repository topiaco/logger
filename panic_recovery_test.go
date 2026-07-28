package logger

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestRecoverPanicLogsErrorEvent(t *testing.T) {
	withTestLoggerConfig(t)

	hook := &captureHook{events: make(chan LogEvent, 1)}
	if err := RegisterHook(hook, WithTimeout(time.Second)); err != nil {
		t.Fatalf("RegisterHook() error = %v", err)
	}

	func() {
		defer RecoverPanic(map[string]interface{}{
			"source": "test",
		})
		panic("nil pointer")
	}()

	select {
	case event := <-hook.events:
		if event.Level != ErrorLevel {
			t.Fatalf("event.Level = %v, want %v", event.Level, ErrorLevel)
		}
		if event.Message != "panic recovered" {
			t.Fatalf("event.Message = %q, want panic recovered", event.Message)
		}
		if event.Fields[PanicField] != "nil pointer" {
			t.Fatalf("event.Fields[%s] = %v, want nil pointer", PanicField, event.Fields[PanicField])
		}
		if event.Fields["source"] != "test" {
			t.Fatalf("event.Fields[source] = %v, want test", event.Fields["source"])
		}
		stack, ok := event.Fields[StackField].(string)
		if !ok || !strings.Contains(stack, "TestRecoverPanicLogsErrorEvent") {
			t.Fatalf("event.Fields[%s] does not contain test stack", StackField)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for hook event")
	}
}

func TestInitGinRecoveryLogsPanic(t *testing.T) {
	withTestLoggerConfig(t)

	oldMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() {
		gin.SetMode(oldMode)
	})

	hook := &captureHook{events: make(chan LogEvent, 1)}
	if err := RegisterHook(hook, WithTimeout(time.Second)); err != nil {
		t.Fatalf("RegisterHook() error = %v", err)
	}

	r := gin.New()
	r.Use(InitGinRecovery())
	r.GET("/panic", func(c *gin.Context) {
		panic("handler panic")
	})

	req := httptest.NewRequest(http.MethodGet, "/panic?foo=bar", nil)
	req.Header.Set(TraceID, "trace-123")
	res := httptest.NewRecorder()

	r.ServeHTTP(res, req)

	if res.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusInternalServerError)
	}

	select {
	case event := <-hook.events:
		if event.TraceID != "trace-123" {
			t.Fatalf("event.TraceID = %q, want trace-123", event.TraceID)
		}
		if event.Fields[PanicField] != "handler panic" {
			t.Fatalf("event.Fields[%s] = %v, want handler panic", PanicField, event.Fields[PanicField])
		}
		if event.Fields["source"] != "gin" {
			t.Fatalf("event.Fields[source] = %v, want gin", event.Fields["source"])
		}
		if event.Fields["path"] != "/panic" {
			t.Fatalf("event.Fields[path] = %v, want /panic", event.Fields["path"])
		}
		if event.Fields["rawQuery"] != "foo=bar" {
			t.Fatalf("event.Fields[rawQuery] = %v, want foo=bar", event.Fields["rawQuery"])
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for hook event")
	}
}

func TestLogPanicIgnoresNilRecoveredValue(t *testing.T) {
	withTestLoggerConfig(t)

	hook := &captureHook{events: make(chan LogEvent, 1)}
	if err := RegisterHook(hook, WithTimeout(time.Second)); err != nil {
		t.Fatalf("RegisterHook() error = %v", err)
	}

	LogPanic(nil, nil)

	select {
	case event := <-hook.events:
		t.Fatalf("unexpected event: %+v", event)
	case <-time.After(50 * time.Millisecond):
	}
}
