package logger

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// InitGinRecovery 返回记录 panic 的 Gin recovery 中间件
func InitGinRecovery() gin.HandlerFunc {
	return gin.CustomRecovery(func(c *gin.Context, recovered interface{}) {
		fields := map[string]interface{}{
			"source":     "gin",
			"clientIP":   c.ClientIP(),
			"httpMethod": c.Request.Method,
			"path":       c.Request.URL.Path,
			"rawQuery":   c.Request.URL.RawQuery,
			"statusCode": http.StatusInternalServerError,
		}

		if traceID := c.Request.Header.Get(TraceID); traceID != "" {
			fields[TraceID] = traceID
		}

		LogPanic(recovered, fields)
		c.AbortWithStatus(http.StatusInternalServerError)
	})
}
