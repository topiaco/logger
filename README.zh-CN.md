# logger

中文 | [ENG](README.md)

## 说明

这是 pms-server、tms-server 等服务使用的公共日志依赖包。

公共 API 仍然保留在根 `logger` 包中，所以依赖方可以继续 import `github.com/topiaco/logger`，原来的 `logger.Error(...)`、`logger.Infof(...)`、`logger.WithFields(...).Error(...)` 等调用方式不需要大面积调整。

请根据接入项目使用的 Redis client 版本选择依赖版本：

- `vx.x.x`，适用于使用 go-redis v6 的项目
- `vx.x.x-redis.9`，适用于使用 go-redis v9 的项目

## 项目结构

- `logger.go`：核心 logger 类型、日志等级、构造函数和实例方法
- `std.go`：包级方法，例如 `logger.Error(...)` 和 `logger.WithFields(...)`
- `config.go`：logger 配置和文件日志 writer 初始化
- `hook.go`：hook 注册、异步分发和日志事件解析
- `hook_redis.go`：Redis Stream hook 实现
- `panic_recovery.go`：panic recovery 辅助方法，将 recover 到的 panic 转成 `Error` 日志
- `gin_logger.go` 和 `gin_recovery.go`：Gin 请求日志和增强版 Gin recovery 中间件
- `mysql_logger.go`：GORM/MySQL logger 适配器
- `redis_client.go`：共享 Redis client 初始化和获取方法

新的示例或 demo 可以放在子目录中，但兼容性 API 应继续保留在根 `logger` 包中。

## Hooks

logger 支持在应用启动时注册异步 hook。现有 `logger.Error(...)`、`logger.Errorf(...)`、`logger.WithFields(...).Error(...)` 等调用不需要改。

示例：将 error 日志写入 Redis Stream。

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

默认情况下，hook 只接收 `Error` 及以上等级的日志，异步执行，并且在 hook 队列满时丢弃新的事件，避免阻塞业务日志调用。

## PMS/TMS 接入步骤

下面以 pms-server 当前代码结构为例说明接入方式。依赖方 import 路径仍然是 `github.com/topiaco/logger`，升级到包含 hook/panic API 的 logger 版本后，按下面几个入口接入即可。tms-server 接入方式一致，只需要替换服务名和 Redis Stream 名称。

### 1. 启动时初始化 logger

pms-server 当前在 `server/core/load.go` 中初始化配置、Redis options、logger 和 hook，建议保持这个入口：

```go
func Load() *gin.Engine {
	currentPath, _ := os.Getwd()
	config.LoadConfig(path.Join(currentPath, "/config/yamls/"))
	options := db.InitRedisOptions()

	logger.SetConfig(&config.GlobalConfig.Logger, options)
	logger.Std = logger.New("pms").Caller(4)

	if err := middleware.LoadLoggerHook(); err != nil {
		logger.Error("加载 logger hook 失败: ", err)
	}

	db.InitMysql()
	db.InitRedis(options)

	engine := gin.New()
	return engine
}
```

`logger.SetConfig` 需要在 `logger.NewRedisClient()` 之前调用，因为 Redis hook 会复用 logger 包内的 Redis 配置。

### 2. 注册 Redis Hook

pms-server 当前可以继续放在 `server/middleware/logger-hook.go`：

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

tms-server 只需要调整服务名和 Stream：

```go
redisHook, err := logger.NewRedisStreamHook(logger.NewRedisClient(), logger.RedisStreamHookConfig{
	Service: "tms-server",
	Stream:  "logger:tms:error_logs",
	MaxLen:  10000,
})
```

注册完成后，原有业务代码里的 `logger.Error(...)`、`logger.Errorf(...)`、`logger.WithFields(...).Error(...)` 不需要改，Error 日志会继续写本地日志，同时异步写入 Redis Stream。

### 3. 接入 Gin 请求日志和 panic 记录

pms-server 当前在 `server/router/router.go` 中使用了：

```go
engine.Use(logger.InitGinLogger())
engine.Use(gin.Recovery())
engine.Use(middleware.LoginAuth())
```

如果希望请求 handler 中的 panic 也写入 Redis，需要把 `gin.Recovery()` 替换成 logger 提供的增强版 Gin recovery：

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

`logger.InitGinRecoveryWithConfig(...)` 内部复用 `gin.CustomRecoveryWithWriter(...)`，默认仍然使用 `gin.DefaultErrorWriter` 输出原生 recovery 日志，并保持 Gin 对 broken pipe、connection reset、`http.ErrAbortHandler` 的处理方式。logger 只在 Gin 执行 recovery handler 时额外调用 `logger.LogPanic(...)`，让 panic 进入 Redis hook。

不要同时注册 `gin.Recovery()` 和 `logger.InitGinRecovery()`，否则 panic 会被其中一个 recovery 先消费，另一个无法再记录。

`InitGinRecoveryWithConfig` 默认会记录 panic 值、panic 类型、调用栈、请求路径、路由参数、query 参数、`req-id`、指定的 gin.Context 字段，以及小体量文本请求体。

如果项目原来使用了自定义 recovery handler，可以继续放到 `RecoveryHandler` 中：

```go
engine.Use(logger.InitGinRecoveryWithConfig(logger.GinRecoveryConfig{
	ContextKeys: []string{"username", "account_level"},
	RecoveryHandler: func(c *gin.Context, recovered interface{}) {
		c.AbortWithStatus(http.StatusInternalServerError)
	},
}))
```

如果请求体里可能包含密码、token 或敏感业务数据，可以关闭请求体采集：

```go
engine.Use(logger.InitGinRecoveryWithConfig(logger.GinRecoveryConfig{
	ContextKeys:        []string{"username", "account_level"},
	DisableRequestBody: true,
}))
```

### 4. 普通业务错误日志

普通错误日志不需要特殊处理，继续使用现有写法即可：

```go
logger.Error("查询项目失败: ", err)

logger.WithFields(map[string]interface{}{
	"project_id": projectID,
	"flow_id":    flowID,
}).Error("流程节点处理失败")
```

这些 Error 日志会进入已注册 hook，所以会被写入 Redis。

### 5. Gin handler 中补充 panic 排查字段

不需要为每个接口单独写一套 recovery。公共 recovery 负责捕获 panic，业务代码只在已经拿到关键数据的位置追加字段即可：

```go
func UpdateFlowNode(c *gin.Context) {
	projectID := c.Param("project_id")

	logger.AddPanicFields(c, map[string]interface{}{
		"module":     "flow",
		"project_id": projectID,
		"step":       "update_flow_node",
	})

	// 后续代码如果发生 panic，上面的字段会一起进入 Redis 日志
}
```

建议优先追加排查问题最有用的字段，例如 `project_id`、`flow_id`、`node_id`、`order_id`、`username`、当前处理步骤等。

### 6. 非 Gin 请求链路的 panic

cron、Kafka/MQ 消费、启动后的监听器、后台同步任务都不在 Gin 请求链路中，`InitGinRecovery` 无法覆盖这些场景。需要在入口处使用 `logger.GoSafe` 或 `logger.RecoverPanic`。

pms-server 当前 `server/main.go` 中有：

```go
go listner.StartEventListner()
```

建议改为：

```go
logger.GoSafe("dingtalk-event-listener", map[string]interface{}{
	"service": "pms-server",
}, func() {
	listner.StartEventListner()
})
```

pms-server 当前 `server/cronjob/cron.go` 中有多处 cron 回调内再启动 goroutine，例如：

```go
_, _ = c.AddFunc("0 9 * * *", func() {
	go flowService.StartFlow()
})
```

建议改为：

```go
_, _ = c.AddFunc("0 9 * * *", func() {
	logger.GoSafe("cron:start-flow", map[string]interface{}{
		"service": "pms-server",
	}, func() {
		flowService.StartFlow()
	})
})
```

如果原来是匿名 goroutine，也放进 `GoSafe`：

```go
logger.GoSafe("cron:start-flow-aps", map[string]interface{}{
	"service": "pms-server",
}, func() {
	if err := flowService.StartFlowAps(); err != nil {
		logger.Error("aps项目排程数据同步异常: ", err)
	}
})
```

如果不是启动 goroutine，只是在普通函数入口兜底，可以使用 `defer logger.RecoverPanic(...)`：

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

### 7. 请求 handler 中新启动的 goroutine

Gin recovery 只能覆盖当前请求协程，不能覆盖 handler 里新启动的 goroutine。pms-server 中类似下面的写法：

```go
go flowService.InitExpectArrange(flowNode.FlowID, flowNode.Step, &n, false)
```

也建议替换为：

```go
logger.GoSafe("flow:init-expect-arrange", map[string]interface{}{
	"service": "pms-server",
	"flow_id": flowNode.FlowID,
	"step":    flowNode.Step,
}, func() {
	flowService.InitExpectArrange(flowNode.FlowID, flowNode.Step, &n, false)
})
```

这样即使后台 goroutine 因空指针等运行时错误 panic，也会通过 `logger.Error` 进入 Redis hook。

### 8. 程序退出前刷新 hook

hook 默认异步执行，应用退出前建议刷新队列。pms-server 当前 `server/main.go` 已经有类似逻辑：

```go
defer func() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = logger.ShutdownHooks(ctx)
}()
```

如果启动流程或 main 入口本身需要兜底 panic，可以在 logger 初始化完成后增加：

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

注意 `log.Fatal` 会直接 `os.Exit(1)`，不会执行 defer，也不会等待异步 hook 刷新。希望进入 Redis 的启动失败或运行失败，优先使用 `logger.Error(...)` 后再返回错误或显式调用 `logger.ShutdownHooks(...)`。

### 9. 后续增加消息提醒 Hook

新增消息提醒不需要改业务代码，也不需要改 `logger.Error(...)` 的调用方式。只需要实现一个新的 hook，然后在应用启动时注册。

```go
type MessageHook struct {
	// webhook/client/config
}

func (h *MessageHook) Name() string {
	return "message"
}

func (h *MessageHook) Handle(ctx context.Context, event logger.LogEvent) error {
	// 这里不要调用 logger.Error/logger.Info，避免递归触发 hook
	// 失败时直接 return err，或使用标准库 log/os.Stderr 记录 hook 自身错误
	return nil
}
```

应用启动时追加注册即可：

```go
if err := logger.RegisterHook(redisHook); err != nil {
	return err
}
if err := logger.RegisterHook(messageHook, logger.WithMinLevel(logger.ErrorLevel)); err != nil {
	return err
}
```

Redis hook 放在 logger 包中，是因为 pms-server 和 tms-server 当前都有相同的 Redis 日志查看诉求，可以作为通用能力复用。消息提醒通常会和具体项目的钉钉、飞书、企微、值班人、告警规则强绑定，更适合先放在接入应用中实现，等多个项目的规则稳定一致后再沉淀到 logger 包。

## 场景覆盖范围

- `logger.Error(...)`、`logger.Errorf(...)`、`logger.WithFields(...).Error(...)`：注册 hook 后会自动进入 Redis
- Gin 请求 handler 内的 panic：使用 `logger.InitGinRecovery()` 或 `logger.InitGinRecoveryWithConfig(...)` 后会自动进入 Redis
- Gin 原生视作 broken pipe 的 panic：保持 Gin 原生行为，不返回 500，也不会额外写入 Redis panic 日志
- Gin handler 里新启动的 goroutine panic：不会被 Gin recovery 捕获，需要用 `logger.GoSafe(...)`
- cron、MQ 消费、监听器、后台同步任务 panic：不属于 Gin 请求链路，需要用 `logger.GoSafe(...)` 或 `defer logger.RecoverPanic(...)`
- `log.Fatal`、`os.Exit`、容器强制 kill：不会执行 defer，异步 hook 可能来不及写入 Redis
- hook 自身异常：`Hook.Handle` 返回 error 即可，不要在 hook 内再次调用 logger 包记录错误

## Hook 开发注意事项

- 不要在 `Hook.Handle` 中调用 `logger.Error`、`logger.Info` 或 logger 包的其他日志方法，因为这条日志会再次进入 hook 流程，可能造成递归触发
- Redis、消息推送或其他输出方式失败时，在 `Hook.Handle` 中直接 `return err` 即可
- 如果 hook 必须记录自身失败原因，使用标准库 `log` 或写入 `os.Stderr`，不要再调用当前 logger 包
- 同一个进程内 `Hook.Name()` 必须唯一，重复名称会被 `RegisterHook` 拒绝
