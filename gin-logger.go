package logger

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"runtime"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	maxResponseLength = 1024 // 限制最大1024字符
)

// getGoroutineID returns the current goroutine ID
func getGoroutineID() uint64 {
	b := make([]byte, 64)
	b = b[:runtime.Stack(b, false)]
	b = bytes.TrimPrefix(b, []byte("goroutine "))
	b = b[:bytes.IndexByte(b, ' ')]
	n := uint64(0)
	for _, c := range b {
		n = n*10 + uint64(c-'0')
	}
	return n
}

type responseBodyWriter struct {
	gin.ResponseWriter
	body *bytes.Buffer
}

func (r *responseBodyWriter) Write(b []byte) (int, error) {
	r.body.Write(b)
	return r.ResponseWriter.Write(b)
}

func InitGinLogger() gin.HandlerFunc {
	// 不需要记录日志的路由，如静态资源文件
	notLoggerPath := []string{
		"/static",
	}
	return func(c *gin.Context) {
		for _, v := range notLoggerPath {
			if strings.HasPrefix(c.Request.URL.Path, v) {
				c.Next()
				return
			}
		}

		xTraceId := c.Request.Header.Get("req-id")
		if xTraceId == "" {
			xTraceId = DefaultGenRequestID()
			c.Header("req-id", xTraceId)
		}
		c.Request.Header.Set("req-id", xTraceId)

		// 获取当前协程ID并存储context到Redis
		goroutineID := getGoroutineID()
		redisKey := fmt.Sprintf("logger_goroutine_:%d_reqid", goroutineID)

		redisClient := NewRedisClient()
		// 将context存入Redis，过期时间3分钟
		if redisClient != nil {
			ctx := context.WithValue(c.Request.Context(), TraceID, xTraceId)
			c.Request = c.Request.WithContext(ctx)
			redisClient.Set(redisKey, xTraceId, 3*time.Minute)
		}

		l := New(xTraceId)
		params, _ := io.ReadAll(c.Request.Body)
		c.Request.Body = io.NopCloser(bytes.NewBuffer(params))

		rbw := &responseBodyWriter{body: bytes.NewBufferString(""), ResponseWriter: c.Writer}
		c.Writer = rbw

		t := time.Now()

		c.Next()

		// 请求结束时清除Redis中的键值对
		if redisClient != nil {
			redisClient.Del(redisKey)
		}

		statusCode := c.Writer.Status()
		latency := time.Since(t)
		guest := c.Request.Header.Get("Authentication")
		username, _ := c.Get("username")
		dataLength := c.Writer.Size()
		if dataLength < 0 {
			dataLength = 0
		}

		responseContent := rbw.body.String()
		if len(responseContent) > maxResponseLength {
			responseContent = responseContent[:maxResponseLength] + "...[truncated]"
		}

		loggerFields := map[string]interface{}{
			"type":       "request",
			"statusCode": statusCode,
			"httpMethod": c.Request.Method,
			"path":       c.Request.URL.Path,
			"params":     string(params),
			"response":   responseContent,
			"dataLength": dataLength,
			"latency":    latency.String(),
			"guest":      guest, // 身份标识
			"username":   username,
			"clientIP":   c.ClientIP(),
		}

		// 如果是 GET, 则 params 为 querystring
		if c.Request.Method == "GET" {
			loggerFields["params"] = c.Request.URL.Query().Encode()
		}

		l = l.WithFields(loggerFields)
		if len(c.Errors) > 0 {
			loggerFields["comment"] = c.Errors.ByType(gin.ErrorTypePrivate).String()
			l.Error("request")
		} else {
			switch {
			case statusCode > 499:
				l.Error("request")
			case statusCode > 399:
				l.Warn("request")
			default:
				l.Info("request")
			}
		}
	}
}
