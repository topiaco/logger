package logger

import (
	"fmt"
	"runtime/debug"
)

const (
	// PanicField 是 panic 值写入日志字段时使用的字段名
	PanicField = "panic"
	// PanicTypeField 是 panic 值类型写入日志字段时使用的字段名
	PanicTypeField = "panic_type"
	// StackField 是调用栈写入日志字段时使用的字段名
	StackField = "stack"
)

// LogPanic 将 recover 捕获到的 panic 写成 Error 日志，从而触发已注册 Hook
func LogPanic(recovered interface{}, fields map[string]interface{}) {
	if recovered == nil {
		return
	}

	WithFields(buildPanicFields(recovered, fields)).Caller(4).Error("panic recovered")
}

// RecoverPanic 用于 defer 中恢复 panic 并记录 Error 日志
func RecoverPanic(fields map[string]interface{}) {
	if recovered := recover(); recovered != nil {
		LogPanic(recovered, fields)
	}
}

// buildPanicFields 合并业务字段、panic 内容、panic 类型和调用栈
func buildPanicFields(recovered interface{}, fields map[string]interface{}) map[string]interface{} {
	panicFields := make(map[string]interface{}, len(fields)+3)
	for key, value := range fields {
		panicFields[key] = value
	}

	panicFields[PanicField] = fmt.Sprint(recovered)
	panicFields[PanicTypeField] = fmt.Sprintf("%T", recovered)
	panicFields[StackField] = string(debug.Stack())

	return panicFields
}
