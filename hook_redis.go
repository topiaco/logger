package logger

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/redis/go-redis/v9"
)

const (
	// defaultRedisStreamHookName 是 Redis Stream Hook 的默认注册名称
	defaultRedisStreamHookName = "redis_stream"
	// defaultRedisStreamKey 是默认写入的 Redis Stream key
	defaultRedisStreamKey = "logger:error_logs"
	// defaultRedisStreamMaxLen 是 Redis Stream 默认保留的近似最大条数
	defaultRedisStreamMaxLen = 10000
)

// RedisStreamClient 定义 RedisStreamHook 需要的最小 Redis 命令能力
type RedisStreamClient interface {
	// XAdd 向 Redis Stream 追加一条日志事件
	XAdd(ctx context.Context, a *redis.XAddArgs) *redis.StringCmd
}

// RedisStreamHookConfig 配置 RedisStreamHook 的注册名称和写入目标
type RedisStreamHookConfig struct {
	// Name 是 Hook 注册名称，同一进程内必须唯一
	Name string
	// Service 是服务名称，例如 pms-server、tms-server，便于页面区分来源
	Service string
	// Stream 是 Redis Stream key
	Stream string
	// MaxLen 是 Redis Stream 近似保留条数，小于等于 0 时使用默认值
	MaxLen int64
}

// RedisStreamHook 将日志事件写入 Redis Stream，供页面查询或后续消费
type RedisStreamHook struct {
	name    string
	client  RedisStreamClient
	service string
	stream  string
	maxLen  int64
}

// NewRedisStreamHook 创建写入 Redis Stream 的 Hook，并补齐默认配置
func NewRedisStreamHook(client RedisStreamClient, cfg RedisStreamHookConfig) (*RedisStreamHook, error) {
	if client == nil {
		return nil, errors.New("logger: redis stream client is nil")
	}

	if cfg.Name == "" {
		cfg.Name = defaultRedisStreamHookName
	}
	if cfg.Service == "" {
		cfg.Service = filepath.Base(os.Args[0])
	}
	if cfg.Stream == "" {
		cfg.Stream = defaultRedisStreamKey
	}
	if cfg.MaxLen <= 0 {
		cfg.MaxLen = defaultRedisStreamMaxLen
	}

	return &RedisStreamHook{
		name:    cfg.Name,
		client:  client,
		service: cfg.Service,
		stream:  cfg.Stream,
		maxLen:  cfg.MaxLen,
	}, nil
}

// Name 返回 Hook 注册名称，用于 RegisterHook 去重和 UnregisterHook 注销
func (h *RedisStreamHook) Name() string {
	return h.name
}

// Handle 将单条 LogEvent 写入 Redis Stream
func (h *RedisStreamHook) Handle(ctx context.Context, event LogEvent) error {
	return h.client.XAdd(ctx, h.xAddArgs(event)).Err()
}

// xAddArgs 将 LogEvent 转换成 go-redis 的 XADD 参数
func (h *RedisStreamHook) xAddArgs(event LogEvent) *redis.XAddArgs {
	return &redis.XAddArgs{
		Stream: h.stream,
		MaxLen: h.maxLen,
		Approx: true,
		Values: h.xAddValues(event),
	}
}

// xAddValues 生成 Redis Stream 中存储的字段集合
func (h *RedisStreamHook) xAddValues(event LogEvent) map[string]interface{} {
	fields := mustMarshalString(event.Fields)
	raw := mustMarshalString(event.Raw)

	return map[string]interface{}{
		"time":     event.Time.Format(DefaultLogTimeFormat),
		"level":    event.LevelName,
		"message":  event.Message,
		"trace_id": event.TraceID,
		"caller":   event.Caller,
		"service":  h.service,
		"fields":   fields,
		"raw":      raw,
	}
}

// mustMarshalString 将字段序列化成 JSON 字符串，失败时返回空对象
func mustMarshalString(value interface{}) string {
	if value == nil {
		return "{}"
	}

	b, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}

	return string(b)
}
