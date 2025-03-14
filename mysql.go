package logger

import (
	"context"
	"fmt"
	"time"

	gormlogger "gorm.io/gorm/logger"
)

type MysqlLogger struct {
	Logger
	SlowThreshold time.Duration
}

var defaultSlowThreshold = 50 * time.Millisecond

func toLoggerLevel(level gormlogger.LogLevel) Level {
	switch level {
	case gormlogger.Error:
		return ErrorLevel
	case gormlogger.Info:
		return InfoLevel
	case gormlogger.Silent:
		return NoLevel
	case gormlogger.Warn:
		return WarnLevel
	default:
		return TraceLevel
	}
}

// FromContext from context logger
func (l *MysqlLogger) FromContext(ctx context.Context) Logger {
	// 首先尝试从传入的context中获取req-id
	if requestID, ok := ctx.Value(TraceID).(string); ok {
		return l.WithField(TraceID, requestID)
	}

	// 如果context中没有req-id，尝试从Redis中获取
	redisClient := NewRedisClient()
	if redisClient != nil {
		// 获取当前协程ID
		goroutineID := getGoroutineID()
		redisKey := fmt.Sprintf("logger_goroutine_:%d_reqid", goroutineID)

		// 从Redis中获取req-id
		if requestID, err := redisClient.Get(redisKey).Result(); err == nil {
			return l.WithField(TraceID, requestID)
		}
	}

	return l.Logger
}

// LogMode implement gorm logger
func (l *MysqlLogger) LogMode(level gormlogger.LogLevel) gormlogger.Interface {
	l.Logger = l.Logger.SetLevel(toLoggerLevel(level))
	return l
}

// Info implement gorm logger
func (l *MysqlLogger) Info(ctx context.Context, s string, vals ...interface{}) {
	l.FromContext(ctx).Infof(s, vals...)
}

// Warn implement gorm logger
func (l *MysqlLogger) Warn(ctx context.Context, s string, vals ...interface{}) {
	l.FromContext(ctx).Warnf(s, vals...)
}

// Error implement gorm logger
func (l *MysqlLogger) Error(ctx context.Context, s string, vals ...interface{}) {
	l.FromContext(ctx).Errorf(s, vals...)
}

// Trace implement gorm logger
func (l *MysqlLogger) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	var (
		elapsed   = time.Since(begin)
		sql, rows = fc()
		fields    = map[string]interface{}{
			"latency":       elapsed.String(),
			"sql":           sql,
			"rows_affected": rows,
		}
		logger = l.FromContext(ctx)
	)

	switch {
	case err != nil:
		logger.WithFields(fields).Errorf("%v", err)
	case elapsed > l.SlowThreshold:
		fields["slow_query"] = true
		logger.WithFields(fields).Warnf("Elapsed %s exceeded, Max %s", elapsed.String(), l.SlowThreshold.String())
	default:
		logger.WithFields(fields).Info()
	}
}

// NewMysqlLogger return custom logger
func NewMysqlLogger(slowThreshold int, requestID ...string) gormlogger.Interface {

	fields := map[string]interface{}{"type": "DATABASE"}

	if len(requestID) > 0 {
		fields[TraceID] = requestID[0]
	}

	l := New().Caller(6).WithFields(fields)

	threshold := defaultSlowThreshold
	if slowThreshold > 0 {
		threshold = time.Duration(slowThreshold) * time.Millisecond
	}

	return &MysqlLogger{Logger: l, SlowThreshold: threshold}
}
