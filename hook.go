package logger

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

const (
	// defaultHookQueueSize 是每个 Hook 默认的异步缓冲队列长度
	defaultHookQueueSize = 1024
	// defaultHookTimeout 是单条日志调用 Hook.Handle 的默认最长时间
	defaultHookTimeout = 3 * time.Second
)

// LogEvent 表示一次已经写出的结构化日志事件，会被分发给已注册的 Hook
type LogEvent struct {
	Time time.Time `json:"time"` // Time 是日志产生时间
	// Level 是 logger 包内定义的日志等级
	Level Level `json:"level"`
	// LevelName 是 zerolog 原始等级名称，例如 error、warn、info
	LevelName string `json:"level_name"`
	// Message 是日志正文内容
	Message string `json:"message"`
	// TraceID 是请求链路 ID，对应包级 TraceID 配置的字段名
	TraceID string `json:"trace_id,omitempty"`
	// Caller 是 zerolog 记录的调用位置
	Caller string `json:"caller,omitempty"`
	// Fields 是除 time、level、message、caller、trace id 外的业务字段
	Fields map[string]interface{} `json:"fields,omitempty"`
	// Raw 是 zerolog 输出的原始 JSON 字段，便于 Hook 自行扩展处理
	Raw map[string]interface{} `json:"raw,omitempty"`
}

// Hook 接收 logger 产生的日志事件，用于扩展 Redis、消息通知等输出方式
type Hook interface {
	// Name 返回 Hook 唯一名称，用于注册、去重和注销
	Name() string
	// Handle 处理单条日志事件；实现中不要再调用 logger，避免递归触发 Hook
	Handle(ctx context.Context, event LogEvent) error
}

// hookConfig 保存单个 Hook 的分发配置
type hookConfig struct {
	minLevel  Level
	queueSize int
	timeout   time.Duration
}

// HookOption 修改 Hook 注册时的分发配置
type HookOption func(*hookConfig)

// WithMinLevel 设置发送给 Hook 的最低日志等级
func WithMinLevel(level Level) HookOption {
	return func(cfg *hookConfig) {
		cfg.minLevel = level
	}
}

// WithQueueSize 设置 Hook 的异步队列长度
func WithQueueSize(size int) HookOption {
	return func(cfg *hookConfig) {
		if size > 0 {
			cfg.queueSize = size
		}
	}
}

// WithTimeout 设置 Hook 处理单条日志的最大耗时
func WithTimeout(timeout time.Duration) HookOption {
	return func(cfg *hookConfig) {
		if timeout > 0 {
			cfg.timeout = timeout
		}
	}
}

// defaultHookConfig 返回 Hook 的默认分发配置
func defaultHookConfig() hookConfig {
	return hookConfig{
		minLevel:  ErrorLevel,
		queueSize: defaultHookQueueSize,
		timeout:   defaultHookTimeout,
	}
}

// hookWorker 持有一个 Hook 及其独立的异步消费队列
type hookWorker struct {
	hook Hook
	cfg  hookConfig
	ch   chan LogEvent
	done chan struct{}
}

// hookDispatcher 管理所有已注册的 Hook worker
type hookDispatcher struct {
	mu      sync.RWMutex
	workers map[string]*hookWorker
}

// newHookDispatcher 创建一个空的 Hook 分发器
func newHookDispatcher() *hookDispatcher {
	return &hookDispatcher{
		workers: make(map[string]*hookWorker),
	}
}

var globalHookDispatcher = newHookDispatcher()

// RegisterHook 注册日志 Hook；Hook 会异步执行，不阻塞正常日志调用
func RegisterHook(h Hook, opts ...HookOption) error {
	return globalHookDispatcher.register(h, opts...)
}

// UnregisterHook 根据名称注销 Hook，并等待它的队列处理结束
func UnregisterHook(name string) {
	globalHookDispatcher.unregister(name)
}

// ShutdownHooks 停止全部 Hook，并等待队列内日志处理完成或 ctx 超时
func ShutdownHooks(ctx context.Context) error {
	return globalHookDispatcher.shutdown(ctx)
}

// register 校验并注册单个 Hook，同时启动它的异步 worker
func (d *hookDispatcher) register(h Hook, opts ...HookOption) error {
	if h == nil {
		return errors.New("logger: hook is nil")
	}

	name := h.Name()
	if name == "" {
		return errors.New("logger: hook name is empty")
	}

	cfg := defaultHookConfig()
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}

	worker := &hookWorker{
		hook: h,
		cfg:  cfg,
		ch:   make(chan LogEvent, cfg.queueSize),
		done: make(chan struct{}),
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	if _, ok := d.workers[name]; ok {
		return fmt.Errorf("logger: hook %q already registered", name)
	}

	d.workers[name] = worker
	go worker.run()

	return nil
}

// unregister 注销指定 Hook，并在返回前等待它的 worker 退出
func (d *hookDispatcher) unregister(name string) {
	d.mu.Lock()
	worker, ok := d.workers[name]
	if ok {
		delete(d.workers, name)
	}
	d.mu.Unlock()

	if ok {
		close(worker.ch)
		<-worker.done
	}
}

// shutdown 注销所有 Hook，并按传入 ctx 控制等待时间
func (d *hookDispatcher) shutdown(ctx context.Context) error {
	d.mu.Lock()
	workers := make([]*hookWorker, 0, len(d.workers))
	for name, worker := range d.workers {
		workers = append(workers, worker)
		delete(d.workers, name)
	}
	d.mu.Unlock()

	for _, worker := range workers {
		close(worker.ch)
	}

	for _, worker := range workers {
		select {
		case <-worker.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	return nil
}

// hasWorkers 判断当前是否存在已注册 Hook，用于减少无 Hook 时的解析开销
func (d *hookDispatcher) hasWorkers() bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return len(d.workers) > 0
}

// dispatch 按日志等级把事件投递给匹配的 Hook，队列满时直接丢弃新事件
func (d *hookDispatcher) dispatch(event LogEvent) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	for _, worker := range d.workers {
		if event.Level < worker.cfg.minLevel {
			continue
		}

		select {
		case worker.ch <- event:
		default:
			// 队列满说明下游处理偏慢，丢弃新事件以保护业务日志调用不被阻塞
		}
	}
}

// run 持续消费 Hook 队列，并为每条事件设置独立超时时间
func (w *hookWorker) run() {
	defer close(w.done)

	for event := range w.ch {
		ctx := context.Background()
		cancel := func() {}
		if w.cfg.timeout > 0 {
			ctx, cancel = context.WithTimeout(ctx, w.cfg.timeout)
		}

		_ = w.hook.Handle(ctx, event)
		cancel()
	}
}

// hookWriter 把 zerolog 的原始 JSON 输出转换成 Hook 事件
type hookWriter struct{}

// Write 实现 io.Writer，使 hookWriter 可以接入 zerolog 的 MultiWriter
func (hookWriter) Write(p []byte) (int, error) {
	if !globalHookDispatcher.hasWorkers() {
		return len(p), nil
	}

	event, err := parseLogEvent(p)
	if err == nil {
		globalHookDispatcher.dispatch(event)
	}

	return len(p), nil
}

var _ io.Writer = hookWriter{}

// parseLogEvent 把 zerolog 输出的 JSON 字节解析成 LogEvent
func parseLogEvent(p []byte) (LogEvent, error) {
	decoder := json.NewDecoder(bytes.NewReader(p))
	decoder.UseNumber()

	raw := make(map[string]interface{})
	if err := decoder.Decode(&raw); err != nil {
		return LogEvent{}, err
	}

	event := LogEvent{
		Time:      parseEventTime(raw["time"]),
		LevelName: valueToString(raw["level"]),
		Message:   valueToString(raw["message"]),
		TraceID:   valueToString(raw[TraceID]),
		Caller:    valueToString(raw["caller"]),
		Raw:       cloneFields(raw),
		Fields:    cloneFields(raw),
	}
	event.Level = levelFromName(event.LevelName)

	delete(event.Fields, "time")
	delete(event.Fields, "level")
	delete(event.Fields, "message")
	delete(event.Fields, "caller")
	delete(event.Fields, TraceID)

	return event, nil
}

// parseEventTime 兼容解析 zerolog 的毫秒时间戳和字符串时间
func parseEventTime(value interface{}) time.Time {
	switch v := value.(type) {
	case json.Number:
		i, err := v.Int64()
		if err == nil {
			return time.UnixMilli(i)
		}
		f, err := v.Float64()
		if err == nil {
			return time.UnixMilli(int64(f))
		}
	case float64:
		return time.UnixMilli(int64(v))
	case string:
		if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
			return t
		}
		if i, err := parseInt64(v); err == nil {
			return time.UnixMilli(i)
		}
	}

	return time.Now()
}

// parseInt64 将字符串转成 int64，用于兼容字符串形式的毫秒时间戳
func parseInt64(value string) (int64, error) {
	var result int64
	_, err := fmt.Sscan(value, &result)
	return result, err
}

// levelFromName 将 zerolog 的等级名称转换成 logger.Level
func levelFromName(name string) Level {
	switch name {
	case "trace":
		return TraceLevel
	case "debug":
		return DebugLevel
	case "info":
		return InfoLevel
	case "warn":
		return WarnLevel
	case "error":
		return ErrorLevel
	case "fatal":
		return FatalLevel
	case "panic":
		return PanicLevel
	default:
		return NoLevel
	}
}

// valueToString 将 JSON 字段值转换成字符串，避免 Hook 处理类型断言
func valueToString(value interface{}) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	case json.Number:
		return v.String()
	default:
		return fmt.Sprint(v)
	}
}

// cloneFields 复制 map，避免后续删除标准字段时影响 Raw 数据
func cloneFields(fields map[string]interface{}) map[string]interface{} {
	if len(fields) == 0 {
		return nil
	}

	clone := make(map[string]interface{}, len(fields))
	for key, value := range fields {
		clone[key] = value
	}

	return clone
}
