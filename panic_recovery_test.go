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

func TestGoSafeLogsPanicEvent(t *testing.T) {
	withTestLoggerConfig(t)

	hook := &captureHook{events: make(chan LogEvent, 1)}
	if err := RegisterHook(hook, WithTimeout(time.Second)); err != nil {
		t.Fatalf("RegisterHook() error = %v", err)
	}

	GoSafe("sync-order-status", map[string]interface{}{
		"order_id": "O-1001",
	}, func() {
		panic("sync failed")
	})

	select {
	case event := <-hook.events:
		if event.Fields[PanicField] != "sync failed" {
			t.Fatalf("event.Fields[%s] = %v, want sync failed", PanicField, event.Fields[PanicField])
		}
		if event.Fields["source"] != "goroutine" {
			t.Fatalf("event.Fields[source] = %v, want goroutine", event.Fields["source"])
		}
		if event.Fields["goroutine"] != "sync-order-status" {
			t.Fatalf("event.Fields[goroutine] = %v, want sync-order-status", event.Fields["goroutine"])
		}
		if event.Fields["order_id"] != "O-1001" {
			t.Fatalf("event.Fields[order_id] = %v, want O-1001", event.Fields["order_id"])
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
	r.Use(InitGinRecoveryWithConfig(GinRecoveryConfig{DisableGinRecoveryLog: true}))
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

func TestInitGinRecoveryUsesGinRecoveryWriter(t *testing.T) {
	withTestLoggerConfig(t)

	oldMode := gin.Mode()
	gin.SetMode(gin.ReleaseMode)
	t.Cleanup(func() {
		gin.SetMode(oldMode)
	})

	var recoveryLog strings.Builder
	r := gin.New()
	r.Use(InitGinRecoveryWithConfig(GinRecoveryConfig{RecoveryWriter: &recoveryLog}))
	r.GET("/panic", func(c *gin.Context) {
		panic("native recovery output")
	})

	req := httptest.NewRequest(http.MethodGet, "/panic", nil)
	res := httptest.NewRecorder()

	r.ServeHTTP(res, req)

	if res.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusInternalServerError)
	}
	if !strings.Contains(recoveryLog.String(), "panic recovered") {
		t.Fatalf("recovery log does not contain panic recovered: %s", recoveryLog.String())
	}
	if !strings.Contains(recoveryLog.String(), "native recovery output") {
		t.Fatalf("recovery log does not contain panic value: %s", recoveryLog.String())
	}
}

func TestInitGinRecoverySupportsCustomRecoveryHandler(t *testing.T) {
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
	r.Use(InitGinRecoveryWithConfig(GinRecoveryConfig{
		DisableGinRecoveryLog: true,
		RecoveryHandler: func(c *gin.Context, recovered interface{}) {
			c.AbortWithStatus(http.StatusTeapot)
		},
	}))
	r.GET("/panic", func(c *gin.Context) {
		panic("custom recovery")
	})

	req := httptest.NewRequest(http.MethodGet, "/panic", nil)
	res := httptest.NewRecorder()

	r.ServeHTTP(res, req)

	if res.Code != http.StatusTeapot {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusTeapot)
	}

	select {
	case event := <-hook.events:
		if event.Fields[PanicField] != "custom recovery" {
			t.Fatalf("event.Fields[%s] = %v, want custom recovery", PanicField, event.Fields[PanicField])
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for hook event")
	}
}

func TestInitGinRecoveryKeepsGinBrokenPipeBehavior(t *testing.T) {
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

	var recoveryLog strings.Builder
	r := gin.New()
	r.Use(InitGinRecoveryWithConfig(GinRecoveryConfig{RecoveryWriter: &recoveryLog}))
	r.GET("/abort", func(c *gin.Context) {
		c.Status(http.StatusNoContent)
		panic(http.ErrAbortHandler)
	})

	req := httptest.NewRequest(http.MethodGet, "/abort", nil)
	res := httptest.NewRecorder()

	r.ServeHTTP(res, req)

	if res.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusNoContent)
	}
	if !strings.Contains(recoveryLog.String(), http.ErrAbortHandler.Error()) {
		t.Fatalf("recovery log does not contain abort handler error: %s", recoveryLog.String())
	}
	if strings.Contains(recoveryLog.String(), "panic recovered") {
		t.Fatalf("recovery log contains panic recovered for broken pipe: %s", recoveryLog.String())
	}

	select {
	case event := <-hook.events:
		t.Fatalf("unexpected hook event for broken pipe: %+v", event)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestInitGinRecoveryCollectsContextFields(t *testing.T) {
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
	r.Use(InitGinRecoveryWithConfig(GinRecoveryConfig{
		DisableGinRecoveryLog: true,
		ContextKeys:           []string{"username", "tenant_id"},
		FieldCollector: func(c *gin.Context) map[string]interface{} {
			return map[string]interface{}{
				"collector": "project",
			}
		},
	}))
	r.POST("/orders/:id", func(c *gin.Context) {
		c.Set("username", "alice")
		c.Set("tenant_id", "tenant-1")
		AddPanicFields(c, map[string]interface{}{
			"order_id": c.Param("id"),
			"step":     "check_inventory",
		})
		panic("nil pointer")
	})

	req := httptest.NewRequest(http.MethodPost, "/orders/O-1001?channel=web", strings.NewReader(`{"sku":"S-1"}`))
	req.Header.Set(TraceID, "trace-ctx")
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()

	r.ServeHTTP(res, req)

	if res.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", res.Code, http.StatusInternalServerError)
	}

	select {
	case event := <-hook.events:
		if event.TraceID != "trace-ctx" {
			t.Fatalf("event.TraceID = %q, want trace-ctx", event.TraceID)
		}
		if event.Fields["username"] != "alice" {
			t.Fatalf("event.Fields[username] = %v, want alice", event.Fields["username"])
		}
		if event.Fields["tenant_id"] != "tenant-1" {
			t.Fatalf("event.Fields[tenant_id] = %v, want tenant-1", event.Fields["tenant_id"])
		}
		if event.Fields["collector"] != "project" {
			t.Fatalf("event.Fields[collector] = %v, want project", event.Fields["collector"])
		}
		if event.Fields["order_id"] != "O-1001" {
			t.Fatalf("event.Fields[order_id] = %v, want O-1001", event.Fields["order_id"])
		}
		if event.Fields["step"] != "check_inventory" {
			t.Fatalf("event.Fields[step] = %v, want check_inventory", event.Fields["step"])
		}
		if event.Fields["requestBody"] != `{"sku":"S-1"}` {
			t.Fatalf("event.Fields[requestBody] = %v, want JSON body", event.Fields["requestBody"])
		}
		if event.Fields["fullPath"] != "/orders/:id" {
			t.Fatalf("event.Fields[fullPath] = %v, want /orders/:id", event.Fields["fullPath"])
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
