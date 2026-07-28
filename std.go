package logger

// Debug debug level
func Debug(arguments ...interface{}) {
	Std.Debug(arguments...)
}

// Info info level
func Info(arguments ...interface{}) {
	Std.Info(arguments...)
}

// Warn warn level
func Warn(arguments ...interface{}) {
	Std.Warn(arguments...)
}

// Error error level
func Error(arguments ...interface{}) {
	Std.Error(arguments...)
}

// Fatal fatal level
func Fatal(arguments ...interface{}) {
	Std.Fatal(arguments...)
}

// Panic panic level
func Panic(arguments ...interface{}) {
	Std.Panic(arguments...)
}

// Debugf debug format
func Debugf(format string, arguments ...interface{}) {
	Std.Debugf(format, arguments...)
}

// Infof info format
func Infof(format string, arguments ...interface{}) {
	Std.Infof(format, arguments...)
}

// Warnf warn format
func Warnf(format string, arguments ...interface{}) {
	Std.Warnf(format, arguments...)
}

// Errorf error format
func Errorf(format string, arguments ...interface{}) {
	Std.Errorf(format, arguments...)
}

// Fatalf fatal format
func Fatalf(format string, arguments ...interface{}) {
	Std.Fatalf(format, arguments...)
}

// Panicf panic format
func Panicf(format string, arguments ...interface{}) {
	Std.Panicf(format, arguments...)
}

// WithField add new field
func WithField(key string, value interface{}) Logger {
	return Std.WithField(key, value)
}

// WithFields add new fields
func WithFields(fields map[string]interface{}) Logger {
	return Std.WithFields(fields)
}

// WithError adds the field "error" with serialized err to the logger context.
func WithError(err error) Logger {
	return Std.WithError(err)
}
