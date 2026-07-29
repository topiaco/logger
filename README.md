# logger

[中文](README.zh-CN.md) | ENG

## Description

This module is the shared logging dependency used by services such as pms-server and tms-server.

It keeps the public API in the root `logger` package, so existing code can continue importing `github.com/topiaco/logger` and keep using calls such as `logger.Error(...)`, `logger.Infof(...)`, and `logger.WithFields(...).Error(...)`.

Please choose the version by the Redis client used by the consuming service:

- `vx.x.x`, for projects using go-redis v6
- `vx.x.x-redis.9`, for projects using go-redis v9

## Project Layout

- `logger.go`: core logger type, level definitions, constructor, and instance methods
- `std.go`: package-level helpers such as `logger.Error(...)` and `logger.WithFields(...)`
- `config.go`: logger configuration and file writer setup
- `hook.go`: hook registration, async dispatch, and log event parsing
- `hook_redis.go`: Redis Stream hook implementation
- `panic_recovery.go`: panic recovery helpers that turn recovered panics into `Error` logs
- `gin_logger.go` and `gin_recovery.go`: Gin request logging and enhanced Gin recovery middleware
- `mysql_logger.go`: GORM/MySQL logger adapter
- `redis_client.go`: shared Redis client initialization and accessor

New optional examples or demos can live in subdirectories, but compatibility APIs should stay in the root package.

## Hooks

logger supports async hooks registered at application startup. Existing calls such as `logger.Error(...)`, `logger.Errorf(...)`, and `logger.WithFields(...).Error(...)` do not need to change.

Example: write error logs to Redis Stream.

```go
logger.SetConfig(cfg, redisOptions)

redisHook, err := logger.NewRedisStreamHook(logger.NewRedisClient(), logger.RedisStreamHookConfig{
	Service: "pms-server",
	Stream:  "logger:pms:error_logs",
	MaxLen:  10000,
})
if err != nil {
	panic(err)
}

if err := logger.RegisterHook(redisHook); err != nil {
	panic(err)
}
defer logger.ShutdownHooks(context.Background())
```

By default, hooks receive `Error` and above, run asynchronously, and drop new events when the hook queue is full.

## PMS/TMS Integration Guide

The following guide uses the current pms-server structure as the example. The import path remains `github.com/topiaco/logger`; after upgrading to a logger version that contains hook and panic APIs, update the following application entry points. tms-server follows the same steps and only needs different service and Redis Stream names.

### 1. Initialize logger at startup

pms-server currently initializes config, Redis options, logger, and hooks in `server/core/load.go`. Keep this as the startup entry:

```go
func Load() *gin.Engine {
	currentPath, _ := os.Getwd()
	config.LoadConfig(path.Join(currentPath, "/config/yamls/"))
	options := db.InitRedisOptions()

	logger.SetConfig(&config.GlobalConfig.Logger, options)
	logger.Std = logger.New("pms").Caller(4)

	if err := middleware.LoadLoggerHook(); err != nil {
		logger.Error("failed to load logger hook: ", err)
	}

	db.InitMysql()
	db.InitRedis(options)

	engine := gin.New()
	return engine
}
```

Call `logger.SetConfig` before `logger.NewRedisClient()`, because the Redis hook reuses the Redis config stored in the logger package.

### 2. Register the Redis hook

pms-server can keep the hook registration in `server/middleware/logger-hook.go`:

```go
func LoadLoggerHook() error {
	redisHook, err := logger.NewRedisStreamHook(logger.NewRedisClient(), logger.RedisStreamHookConfig{
		Service: "pms-server",
		Stream:  "logger:pms:error_logs",
		MaxLen:  10000,
	})
	if err != nil {
		return err
	}

	return logger.RegisterHook(redisHook)
}
```

For tms-server, change only the service and stream:

```go
redisHook, err := logger.NewRedisStreamHook(logger.NewRedisClient(), logger.RedisStreamHookConfig{
	Service: "tms-server",
	Stream:  "logger:tms:error_logs",
	MaxLen:  10000,
})
```

After registration, existing business calls such as `logger.Error(...)`, `logger.Errorf(...)`, and `logger.WithFields(...).Error(...)` continue writing local logs and will also be asynchronously written to Redis Stream.

### 3. Add Gin request logging and panic logging

pms-server currently uses this middleware order in `server/router/router.go`:

```go
engine.Use(logger.InitGinLogger())
engine.Use(gin.Recovery())
engine.Use(middleware.LoginAuth())
```

To write request-handler panics into Redis, replace `gin.Recovery()` with the enhanced Gin recovery provided by this package:

```go
engine.Use(logger.InitGinLogger())
engine.Use(logger.InitGinRecoveryWithConfig(logger.GinRecoveryConfig{
	ContextKeys: []string{"username", "account_level"},
	FieldCollector: func(c *gin.Context) map[string]interface{} {
		return map[string]interface{}{
			"service": "pms-server",
		}
	},
}))
engine.Use(middleware.LoginAuth())
```

`logger.InitGinRecoveryWithConfig(...)` internally uses `gin.CustomRecoveryWithWriter(...)`. By default, it still writes Gin's native recovery output to `gin.DefaultErrorWriter` and keeps Gin's behavior for broken pipe, connection reset, and `http.ErrAbortHandler`. logger only adds one extra action in Gin's recovery handler: calling `logger.LogPanic(...)`, so the panic can enter the Redis hook.

Do not register `gin.Recovery()` and `logger.InitGinRecovery()` together. A panic can only be consumed by one recovery middleware; once one middleware recovers it, the other one will not receive it.

`InitGinRecoveryWithConfig` records the panic value, panic type, stack, request path, route params, query params, `req-id`, configured `gin.Context` keys, and a small text request body by default.

If the project already has custom recovery response logic, put it in `RecoveryHandler`:

```go
engine.Use(logger.InitGinRecoveryWithConfig(logger.GinRecoveryConfig{
	ContextKeys: []string{"username", "account_level"},
	RecoveryHandler: func(c *gin.Context, recovered interface{}) {
		c.AbortWithStatus(http.StatusInternalServerError)
	},
}))
```

If request bodies may contain passwords, tokens, or sensitive business data, disable request-body collection:

```go
engine.Use(logger.InitGinRecoveryWithConfig(logger.GinRecoveryConfig{
	ContextKeys:        []string{"username", "account_level"},
	DisableRequestBody: true,
}))
```

### 4. Keep normal business error logs unchanged

Normal business error logs do not require special handling:

```go
logger.Error("failed to query project: ", err)

logger.WithFields(map[string]interface{}{
	"project_id": projectID,
	"flow_id":    flowID,
}).Error("failed to process flow node")
```

These `Error` logs enter registered hooks and will be written to Redis.

### 5. Add panic troubleshooting fields in Gin handlers

You do not need a custom recovery for every handler. The shared recovery captures the panic. Business code only needs to attach useful fields at points where the key data is already available:

```go
func UpdateFlowNode(c *gin.Context) {
	projectID := c.Param("project_id")

	logger.AddPanicFields(c, map[string]interface{}{
		"module":     "flow",
		"project_id": projectID,
		"step":       "update_flow_node",
	})

	// If later code panics, these fields will be included in the Redis log
}
```

Prefer fields that shorten troubleshooting time, such as `project_id`, `flow_id`, `node_id`, `order_id`, `username`, and the current processing step.

### 6. Cover panics outside the Gin request chain

Cron jobs, Kafka/MQ consumers, listeners, and background sync tasks are not part of the Gin request chain. `InitGinRecovery` cannot cover them. Use `logger.GoSafe` or `logger.RecoverPanic` at their entry points.

pms-server currently has this in `server/main.go`:

```go
go listner.StartEventListner()
```

Change it to:

```go
logger.GoSafe("dingtalk-event-listener", map[string]interface{}{
	"service": "pms-server",
}, func() {
	listner.StartEventListner()
})
```

pms-server also has multiple cron callbacks in `server/cronjob/cron.go` that start goroutines:

```go
_, _ = c.AddFunc("0 9 * * *", func() {
	go flowService.StartFlow()
})
```

Change them to:

```go
_, _ = c.AddFunc("0 9 * * *", func() {
	logger.GoSafe("cron:start-flow", map[string]interface{}{
		"service": "pms-server",
	}, func() {
		flowService.StartFlow()
	})
})
```

Anonymous goroutines can also be wrapped by `GoSafe`:

```go
logger.GoSafe("cron:start-flow-aps", map[string]interface{}{
	"service": "pms-server",
}, func() {
	if err := flowService.StartFlowAps(); err != nil {
		logger.Error("failed to sync APS project scheduling data: ", err)
	}
})
```

If the code is not starting a new goroutine and only needs function-level protection, use `defer logger.RecoverPanic(...)`:

```go
func StartPendingProjects() {
	defer logger.RecoverPanic(map[string]interface{}{
		"service": "pms-server",
		"source":  "cron",
		"job":     "start-pending-projects",
	})

	// job logic
}
```

### 7. Cover goroutines started inside request handlers

Gin recovery only covers the current request goroutine. It cannot catch panics from new goroutines started inside a handler. For code like this in pms-server:

```go
go flowService.InitExpectArrange(flowNode.FlowID, flowNode.Step, &n, false)
```

Use `logger.GoSafe`:

```go
logger.GoSafe("flow:init-expect-arrange", map[string]interface{}{
	"service": "pms-server",
	"flow_id": flowNode.FlowID,
	"step":    flowNode.Step,
}, func() {
	flowService.InitExpectArrange(flowNode.FlowID, flowNode.Step, &n, false)
})
```

Then runtime panics such as nil pointer dereferences in that goroutine will become `Error` logs and enter the Redis hook.

### 8. Flush hooks before process exit

Hooks run asynchronously, so flush them before application shutdown. pms-server already has similar logic in `server/main.go`:

```go
defer func() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = logger.ShutdownHooks(ctx)
}()
```

If startup or the main entry needs top-level panic protection, add this after logger initialization:

```go
defer func() {
	if recovered := recover(); recovered != nil {
		logger.LogPanic(recovered, map[string]interface{}{
			"service": "pms-server",
			"source":  "main",
		})

		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = logger.ShutdownHooks(ctx)

		panic(recovered)
	}
}()
```

`log.Fatal` directly calls `os.Exit(1)`, so deferred functions will not run and async hooks may not flush. If a startup or runtime failure must be written to Redis, prefer `logger.Error(...)` and return the error, or explicitly call `logger.ShutdownHooks(...)` before exiting.

### 9. Add another output hook, such as message alerts

Adding message alerts does not require changing business code or existing `logger.Error(...)` calls. Implement another hook and register it at startup.

```go
type MessageHook struct {
	// webhook/client/config
}

func (h *MessageHook) Name() string {
	return "message"
}

func (h *MessageHook) Handle(ctx context.Context, event logger.LogEvent) error {
	// Do not call logger.Error/logger.Info here, otherwise the hook may trigger itself recursively
	// Return err directly, or use standard log/os.Stderr to record hook-internal failures
	return nil
}
```

Register it together with the Redis hook:

```go
if err := logger.RegisterHook(redisHook); err != nil {
	return err
}
if err := logger.RegisterHook(messageHook, logger.WithMinLevel(logger.ErrorLevel)); err != nil {
	return err
}
```

The Redis hook lives in the logger package because pms-server and tms-server share the same Redis-based log viewing requirement. Message alerts are usually tied to project-specific providers, owners, on-call rules, and alert policies, so they are better implemented inside the consuming application first. Once multiple projects share the same rules, they can be moved into this package.

## Coverage

- `logger.Error(...)`, `logger.Errorf(...)`, and `logger.WithFields(...).Error(...)`: automatically enter Redis after hook registration
- Panic inside a Gin request handler: automatically enters Redis after using `logger.InitGinRecovery()` or `logger.InitGinRecoveryWithConfig(...)`
- Panic treated by Gin as broken pipe: keeps Gin's native behavior, does not return 500, and does not write an extra Redis panic log
- Panic in goroutines started inside Gin handlers: not covered by Gin recovery, use `logger.GoSafe(...)`
- Panic in cron jobs, MQ consumers, listeners, and background sync tasks: not part of the Gin request chain, use `logger.GoSafe(...)` or `defer logger.RecoverPanic(...)`
- `log.Fatal`, `os.Exit`, and forced container kill: deferred functions will not run, so async hooks may not flush
- Hook-internal failure: return the error from `Hook.Handle`; do not call this logger package again inside the hook

## Hook Development Notes

- Do not call `logger.Error`, `logger.Info`, or other logger package methods inside `Hook.Handle`, because that log will enter the hook pipeline again and may trigger recursive handling
- Return the error directly from `Hook.Handle` when Redis, message push, or another sink fails
- If a hook must record its own failure, use the standard library `log` package or write to `os.Stderr` instead of this logger package
- Keep `Hook.Name()` unique in one process; duplicate names will be rejected by `RegisterHook`
