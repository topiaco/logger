## Description

This is the dependency package for the logging system.

Please note that the dependency package is divided into two versions:

vx.x.x(v1.2.0)

if you are using go-redis v6 version of the project please use the version without suffix.

vx.x.x-redis.9(v1.2.0-redis.9)

if you are using go-redis v9 version of the project please use the version with suffix redis.9.

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
