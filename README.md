## Description

This is the dependency package for the logging system.

Please note that the dependency package is divided into two versions:

vx.x.x(v1.2.0)

if you are using go-redis v6 version of the project please use the version without suffix.

vx.x.x-redis.9(v1.2.0-redis.9)

if you are using go-redis v9 version of the project please use the version with suffix redis.9.

## Project layout

This module keeps all public APIs in the root `logger` package so existing projects can continue importing
`github.com/topiaco/logger` without code changes.

- `logger.go`: core logger type, level definitions, constructor, and instance methods
- `std.go`: package-level helpers such as `logger.Error(...)` and `logger.WithFields(...)`
- `config.go`: logger configuration and file writer setup
- `hook.go`: hook registration, async dispatch, and log event parsing
- `hook_redis.go`: Redis Stream hook implementation
- `panic_recovery.go`: panic recovery helpers that turn recovered panics into `Error` logs
- `gin_logger.go` and `gin_recovery.go`: Gin request logging and panic recovery middleware
- `mysql_logger.go`: GORM/MySQL logger adapter
- `redis_client.go`: shared Redis client initialization and accessor

New optional examples or demos can live in subdirectories, but compatibility APIs should stay in the root package.

## Hooks

logger supports registering async hooks at application startup. Existing calls such as `logger.Error(...)`,
`logger.Errorf(...)`, and `logger.WithFields(...).Error(...)` do not need to change.

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

## Panic logging

Direct `panic(...)` and runtime panics do not pass through `logger.Error(...)` by themselves. Use recovery helpers to
convert recovered panics into `Error` logs, then registered hooks such as Redis Stream will receive them.

Gin applications can add the recovery middleware:

```go
r := gin.New()
r.Use(logger.InitGinLogger())
r.Use(logger.InitGinRecovery())
```

If the application already has a custom Gin recovery, call `logger.LogPanic` inside it:

```go
func RecoveryLogger() gin.HandlerFunc {
	return gin.CustomRecovery(func(c *gin.Context, recovered interface{}) {
		logger.LogPanic(recovered, map[string]interface{}{
			"source":     "gin",
			"clientIP":   c.ClientIP(),
			"httpMethod": c.Request.Method,
			"path":       c.Request.URL.Path,
		})

		c.AbortWithStatus(http.StatusInternalServerError)
	})
}
```

Background goroutines need their own recovery:

```go
go func() {
	defer logger.RecoverPanic(map[string]interface{}{
		"source":    "goroutine",
		"goroutine": "sync-order-status",
	})

	// task logic
}()
```

For startup or fatal panics, log the panic and flush hooks before the process exits:

```go
defer func() {
	if recovered := recover(); recovered != nil {
		logger.LogPanic(recovered, map[string]interface{}{
			"source": "main",
		})

		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = logger.ShutdownHooks(ctx)

		panic(recovered)
	}
}()
```

### Hook development notes

- Do not call `logger.Error`, `logger.Info`, or other logger package methods inside `Hook.Handle`, because that log will enter the hook pipeline again and may trigger recursive handling
- Return the error directly from `Hook.Handle` when Redis, message push, or another sink fails
- If a hook must record its own failure, use the standard library `log` package or write to `os.Stderr` instead of this logger package
- Keep `Hook.Name()` unique in one process; duplicate names will be rejected by `RegisterHook`
