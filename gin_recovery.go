package logger

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

const (
	// defaultGinPanicRequestBodyMaxBytes 是 panic 日志默认保存的请求体最大字节数
	defaultGinPanicRequestBodyMaxBytes = 64 * 1024
	// ginPanicFieldsKey 是 gin.Context 中保存业务 panic 字段的内部 key
	ginPanicFieldsKey = "__logger_panic_fields"
	// ginRecoveryRequestFieldsKey 是 gin.Context 中保存请求级 panic 字段的内部 key
	ginRecoveryRequestFieldsKey = "__logger_recovery_request_fields"
)

// GinRecoveryConfig 配置 Gin panic recovery 的上下文字段收集方式
type GinRecoveryConfig struct {
	// StatusCode 是 recovery 后返回给客户端的 HTTP 状态码
	StatusCode int
	// RecoveryWriter 是 Gin 原生 recovery 日志输出位置，默认使用 gin.DefaultErrorWriter
	RecoveryWriter io.Writer
	// DisableGinRecoveryLog 设置为 true 时关闭 Gin 原生 recovery 日志输出
	DisableGinRecoveryLog bool
	// RecoveryHandler 用于自定义 recovery 后的响应逻辑，默认返回 StatusCode
	RecoveryHandler gin.RecoveryFunc
	// DisableRequestBody 设置为 true 时不采集请求体
	DisableRequestBody bool
	// MaxRequestBodyBytes 是允许写入 panic 日志的最大请求体字节数
	MaxRequestBodyBytes int64
	// ContextKeys 指定需要从 gin.Context 中自动复制到 panic 日志的 key
	ContextKeys []string
	// FieldCollector 用于统一补充项目级字段，例如当前用户、租户、公司 ID
	FieldCollector func(*gin.Context) map[string]interface{}
}

// InitGinRecovery 返回记录 panic 的 Gin recovery 中间件
func InitGinRecovery() gin.HandlerFunc {
	return InitGinRecoveryWithConfig(GinRecoveryConfig{})
}

// InitGinRecoveryWithConfig 返回可配置字段收集逻辑的 Gin recovery 中间件
func InitGinRecoveryWithConfig(cfg GinRecoveryConfig) gin.HandlerFunc {
	cfg = normalizeGinRecoveryConfig(cfg)
	recovery := gin.CustomRecoveryWithWriter(ginRecoveryWriter(cfg), func(c *gin.Context, recovered interface{}) {
		logGinPanic(c, recovered, cfg)
		if cfg.RecoveryHandler != nil {
			cfg.RecoveryHandler(c, recovered)
			return
		}

		c.AbortWithStatus(cfg.StatusCode)
	})
	return func(c *gin.Context) {
		c.Set(ginRecoveryRequestFieldsKey, buildGinRecoveryFields(c, cfg))
		recovery(c)
	}
}

// AddPanicField 向当前 Gin 请求追加 panic 排查字段
func AddPanicField(c *gin.Context, key string, value interface{}) {
	if c == nil || key == "" {
		return
	}

	AddPanicFields(c, map[string]interface{}{key: value})
}

// AddPanicFields 向当前 Gin 请求批量追加 panic 排查字段
func AddPanicFields(c *gin.Context, fields map[string]interface{}) {
	if c == nil || len(fields) == 0 {
		return
	}

	panicFields := ginPanicFields(c)
	for key, value := range fields {
		if key != "" {
			panicFields[key] = value
		}
	}
	c.Set(ginPanicFieldsKey, panicFields)
}

// normalizeGinRecoveryConfig 补齐 Gin recovery 默认配置
func normalizeGinRecoveryConfig(cfg GinRecoveryConfig) GinRecoveryConfig {
	if cfg.StatusCode == 0 {
		cfg.StatusCode = http.StatusInternalServerError
	}
	if cfg.MaxRequestBodyBytes <= 0 {
		cfg.MaxRequestBodyBytes = defaultGinPanicRequestBodyMaxBytes
	}
	if cfg.ContextKeys == nil {
		cfg.ContextKeys = []string{"username"}
	}

	return cfg
}

// ginRecoveryWriter 返回 Gin 原生 recovery 使用的日志输出位置
func ginRecoveryWriter(cfg GinRecoveryConfig) io.Writer {
	if cfg.DisableGinRecoveryLog {
		return nil
	}
	if cfg.RecoveryWriter != nil {
		return cfg.RecoveryWriter
	}

	return gin.DefaultErrorWriter
}

// logGinPanic 将 Gin recovery 捕获到的 panic 写入 logger hook
func logGinPanic(c *gin.Context, recovered interface{}, cfg GinRecoveryConfig) {
	fields := ginRecoveryRequestFields(c)
	if len(fields) == 0 {
		fields = buildGinRecoveryFields(c, cfg)
	}

	addGinContextFields(c, cfg, fields)
	mergeMapFields(fields, safeGinFieldCollector(c, cfg.FieldCollector))
	mergeMapFields(fields, ginPanicFields(c))

	LogPanic(recovered, fields)
}

// safeGinFieldCollector 安全执行业务字段收集函数，避免 recovery 过程再次 panic
func safeGinFieldCollector(c *gin.Context, collector func(*gin.Context) map[string]interface{}) (fields map[string]interface{}) {
	if collector == nil {
		return nil
	}

	defer func() {
		if recovered := recover(); recovered != nil {
			fields = map[string]interface{}{
				"fieldCollectorPanic": fmt.Sprint(recovered),
			}
		}
	}()

	return collector(c)
}

// ginRecoveryRequestFields 读取当前请求开始时已经采集的 panic 字段
func ginRecoveryRequestFields(c *gin.Context) map[string]interface{} {
	value, ok := c.Get(ginRecoveryRequestFieldsKey)
	if !ok {
		return map[string]interface{}{}
	}

	fields, ok := value.(map[string]interface{})
	if !ok {
		return map[string]interface{}{}
	}

	return cloneMapFields(fields)
}

// buildGinRecoveryFields 收集一次 Gin 请求的通用 panic 排查字段
func buildGinRecoveryFields(c *gin.Context, cfg GinRecoveryConfig) map[string]interface{} {
	fields := map[string]interface{}{
		"source":      "gin",
		"clientIP":    c.ClientIP(),
		"httpMethod":  c.Request.Method,
		"path":        c.Request.URL.Path,
		"fullPath":    c.FullPath(),
		"rawQuery":    c.Request.URL.RawQuery,
		"queryParams": c.Request.URL.Query(),
		"statusCode":  cfg.StatusCode,
	}

	if contentType := c.ContentType(); contentType != "" {
		fields["contentType"] = contentType
	}
	if traceID := c.Request.Header.Get(TraceID); traceID != "" {
		fields[TraceID] = traceID
	}
	if routeParams := ginRouteParams(c); len(routeParams) > 0 {
		fields["routeParams"] = routeParams
	}

	addRequestBodyFields(c, cfg, fields)

	return fields
}

// addGinContextFields 将指定 gin.Context key 复制到 panic 日志字段
func addGinContextFields(c *gin.Context, cfg GinRecoveryConfig, fields map[string]interface{}) {
	for _, key := range cfg.ContextKeys {
		if value, ok := c.Get(key); ok {
			fields[key] = value
		}
	}
}

// ginRouteParams 将 Gin 路由参数转换成普通 map
func ginRouteParams(c *gin.Context) map[string]string {
	params := make(map[string]string, len(c.Params))
	for _, param := range c.Params {
		params[param.Key] = param.Value
	}

	return params
}

// ginPanicFields 读取当前请求已经追加的业务 panic 字段
func ginPanicFields(c *gin.Context) map[string]interface{} {
	value, ok := c.Get(ginPanicFieldsKey)
	if !ok {
		return map[string]interface{}{}
	}

	fields, ok := value.(map[string]interface{})
	if !ok {
		return map[string]interface{}{}
	}

	return cloneMapFields(fields)
}

// addRequestBodyFields 在不影响后续读取的前提下采集小体量文本请求体
func addRequestBodyFields(c *gin.Context, cfg GinRecoveryConfig, fields map[string]interface{}) {
	if cfg.DisableRequestBody || c.Request == nil || c.Request.Body == nil {
		return
	}

	contentLength := c.Request.ContentLength
	if contentLength == 0 {
		return
	}
	if contentLength < 0 {
		fields["requestBodySkipped"] = "unknown content length"
		return
	}
	if contentLength > cfg.MaxRequestBodyBytes {
		fields["requestBodySkipped"] = "content length exceeds max"
		fields["requestBodySize"] = contentLength
		return
	}
	if !isTextContentType(c.ContentType()) {
		fields["requestBodySkipped"] = "non text content type"
		fields["requestBodySize"] = contentLength
		return
	}

	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		fields["requestBodyReadError"] = err.Error()
		return
	}
	c.Request.Body = io.NopCloser(bytes.NewBuffer(body))

	if len(body) > 0 {
		fields["requestBody"] = string(body)
		fields["requestBodySize"] = len(body)
	}
}

// isTextContentType 判断请求体是否适合直接写入日志字段
func isTextContentType(contentType string) bool {
	contentType = strings.ToLower(contentType)
	return contentType == "" ||
		strings.HasPrefix(contentType, "text/") ||
		strings.Contains(contentType, "json") ||
		strings.Contains(contentType, "xml") ||
		strings.Contains(contentType, "x-www-form-urlencoded")
}
