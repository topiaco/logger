package logger

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"time"
)

const (
	// RedisStreamLegacySchemaVersion 表示尚未写入 schema_version 和 event_id 的旧版事件。
	RedisStreamLegacySchemaVersion = "0"
	// RedisStreamEventSchemaVersion 是 RedisStreamHook 当前写入的事件契约版本。
	RedisStreamEventSchemaVersion = "1"
)

// RedisStreamEvent 是 RedisStreamHook 写入及错误日志管理页面读取的稳定事件契约。
//
// Fields 和 Raw 在 Redis 中仍以 JSON 对象字符串存储；DecodeRedisStreamEvent
// 会将它们解码成 map。旧记录缺少 schema_version 时会被归一为 legacy v0。
type RedisStreamEvent struct {
	SchemaVersion string                 `json:"schema_version"`
	EventID       string                 `json:"event_id,omitempty"`
	Time          string                 `json:"time"`
	Level         string                 `json:"level"`
	Message       string                 `json:"message"`
	TraceID       string                 `json:"trace_id,omitempty"`
	Caller        string                 `json:"caller,omitempty"`
	Service       string                 `json:"service"`
	Fields        map[string]interface{} `json:"fields"`
	Raw           map[string]interface{} `json:"raw"`
}

// DecodeRedisStreamEvent 将 redis.XMessage.Values 解码成公开事件类型并校验契约。
//
// 当前 v1 和缺少 schema_version/event_id 的 legacy v0 记录均可解码；未知版本、
// 错误字段类型和无效 JSON 会返回错误。
func DecodeRedisStreamEvent(values map[string]interface{}) (RedisStreamEvent, error) {
	if values == nil {
		return RedisStreamEvent{}, errors.New("logger: redis stream event values are nil")
	}

	schemaVersion := RedisStreamLegacySchemaVersion
	if _, ok := values["schema_version"]; ok {
		var err error
		schemaVersion, err = redisStreamStringField(values, "schema_version", true)
		if err != nil {
			return RedisStreamEvent{}, err
		}
	}

	eventID, err := redisStreamStringField(values, "event_id", false)
	if err != nil {
		return RedisStreamEvent{}, err
	}
	timestamp, err := redisStreamStringField(values, "time", true)
	if err != nil {
		return RedisStreamEvent{}, err
	}
	level, err := redisStreamStringField(values, "level", true)
	if err != nil {
		return RedisStreamEvent{}, err
	}
	message, err := redisStreamStringField(values, "message", true)
	if err != nil {
		return RedisStreamEvent{}, err
	}
	traceID, err := redisStreamStringField(values, "trace_id", true)
	if err != nil {
		return RedisStreamEvent{}, err
	}
	caller, err := redisStreamStringField(values, "caller", true)
	if err != nil {
		return RedisStreamEvent{}, err
	}
	service, err := redisStreamStringField(values, "service", true)
	if err != nil {
		return RedisStreamEvent{}, err
	}
	fields, err := redisStreamJSONField(values, "fields")
	if err != nil {
		return RedisStreamEvent{}, err
	}
	raw, err := redisStreamJSONField(values, "raw")
	if err != nil {
		return RedisStreamEvent{}, err
	}

	event := RedisStreamEvent{
		SchemaVersion: schemaVersion,
		EventID:       eventID,
		Time:          timestamp,
		Level:         level,
		Message:       message,
		TraceID:       traceID,
		Caller:        caller,
		Service:       service,
		Fields:        fields,
		Raw:           raw,
	}
	if err := ValidateRedisStreamEvent(event); err != nil {
		return RedisStreamEvent{}, err
	}

	return event, nil
}

// ValidateRedisStreamEvent 校验解码后或调用方自行构造的 Redis Stream 事件。
//
// 空版本和 v0 按 legacy 事件处理，不强制 event_id；v1 强制使用 UUID v4 event_id。
func ValidateRedisStreamEvent(event RedisStreamEvent) error {
	var validationErrors []error

	switch event.SchemaVersion {
	case "", RedisStreamLegacySchemaVersion:
		if event.EventID != "" && !isUUIDV4(event.EventID) {
			validationErrors = append(validationErrors, errors.New("event_id must be a UUID v4 when present"))
		}
	case RedisStreamEventSchemaVersion:
		if event.EventID == "" {
			validationErrors = append(validationErrors, errors.New("event_id is required for schema version 1"))
		} else if !isUUIDV4(event.EventID) {
			validationErrors = append(validationErrors, errors.New("event_id must be a UUID v4"))
		}
	default:
		validationErrors = append(validationErrors, fmt.Errorf("unsupported schema_version %q", event.SchemaVersion))
	}

	if event.Time == "" {
		validationErrors = append(validationErrors, errors.New("time is required"))
	} else if _, err := time.Parse(DefaultLogTimeFormat, event.Time); err != nil {
		validationErrors = append(validationErrors, fmt.Errorf("time must use format %q", DefaultLogTimeFormat))
	}
	if event.Level == "" {
		validationErrors = append(validationErrors, errors.New("level is required"))
	}
	if event.Service == "" {
		validationErrors = append(validationErrors, errors.New("service is required"))
	}
	if event.Fields == nil {
		validationErrors = append(validationErrors, errors.New("fields must be a JSON object"))
	}
	if event.Raw == nil {
		validationErrors = append(validationErrors, errors.New("raw must be a JSON object"))
	}

	if err := errors.Join(validationErrors...); err != nil {
		return fmt.Errorf("logger: invalid redis stream event: %w", err)
	}
	return nil
}

func redisStreamStringField(values map[string]interface{}, name string, required bool) (string, error) {
	value, ok := values[name]
	if !ok {
		if required {
			return "", fmt.Errorf("logger: redis stream event field %q is missing", name)
		}
		return "", nil
	}

	switch value := value.(type) {
	case string:
		return value, nil
	case []byte:
		return string(value), nil
	case json.Number:
		return value.String(), nil
	case int:
		return fmt.Sprint(value), nil
	case int8:
		return fmt.Sprint(value), nil
	case int16:
		return fmt.Sprint(value), nil
	case int32:
		return fmt.Sprint(value), nil
	case int64:
		return fmt.Sprint(value), nil
	case uint:
		return fmt.Sprint(value), nil
	case uint8:
		return fmt.Sprint(value), nil
	case uint16:
		return fmt.Sprint(value), nil
	case uint32:
		return fmt.Sprint(value), nil
	case uint64:
		return fmt.Sprint(value), nil
	case float32:
		return fmt.Sprint(value), nil
	case float64:
		return fmt.Sprint(value), nil
	default:
		return "", fmt.Errorf("logger: redis stream event field %q has type %T, want string or bytes", name, value)
	}
}

func redisStreamJSONField(values map[string]interface{}, name string) (map[string]interface{}, error) {
	value, err := redisStreamStringField(values, name, true)
	if err != nil {
		return nil, err
	}

	decoder := json.NewDecoder(strings.NewReader(value))
	decoder.UseNumber()
	result := make(map[string]interface{})
	if err := decoder.Decode(&result); err != nil {
		return nil, fmt.Errorf("logger: redis stream event field %q is not a JSON object: %w", name, err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return nil, fmt.Errorf("logger: redis stream event field %q is not a single JSON object: %w", name, err)
	}
	if result == nil {
		// 旧实现可能把 typed nil map 编码为 null；读取时将其兼容为空对象。
		result = make(map[string]interface{})
	}

	return result, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var trailing interface{}
	err := decoder.Decode(&trailing)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("unexpected trailing JSON value")
	}
	return err
}

var fallbackEventIDCounter atomic.Uint64

// newLogEventID 返回一个 UUID v4。crypto/rand 极端失败时使用进程内时间和原子计数器兜底，
// 避免日志主链路仅因随机源不可用而丢失事件。
func newLogEventID() string {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		binary.BigEndian.PutUint64(id[0:8], uint64(time.Now().UnixNano()))
		binary.BigEndian.PutUint64(id[8:16], fallbackEventIDCounter.Add(1))
	}

	id[6] = (id[6] & 0x0f) | 0x40
	id[8] = (id[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", id[0:4], id[4:6], id[6:8], id[8:10], id[10:16])
}

func isUUIDV4(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	for i := range value {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			continue
		}
		if !isHexCharacter(value[i]) {
			return false
		}
	}

	return value[14] == '4' && strings.ContainsRune("89abAB", rune(value[19]))
}

func isHexCharacter(value byte) bool {
	return value >= '0' && value <= '9' || value >= 'a' && value <= 'f' || value >= 'A' && value <= 'F'
}
