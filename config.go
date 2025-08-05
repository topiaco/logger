package logger

import (
	"io"
	"os"
	"path/filepath"

	"github.com/redis/go-redis/v9"
	"gopkg.in/natefinch/lumberjack.v2"
)

// Config for logging
type LoggerConf struct {
	// log level
	LogLevel Level `yaml:"log_level"`

	// Enable console logging
	ConsoleLoggingEnabled bool `yaml:"console_logging_enabled"`

	// EncodeLogsAsJSON makes the log framework log JSON
	EncodeLogsAsJSON bool `yaml:"encode_logs_as_json"`

	// FileLoggingEnabled makes the framework log to a file, the fields below can be skipped if this value is false!
	FileLoggingEnabled bool `yaml:"file_logging_enabled"`

	// Filename is the name of the logfile which will be placed inside the directory
	Filename string `yaml:"filename"`

	// MaxSize the max size in MB of the logfile before it's rolled
	MaxSize int `yaml:"max_size"`

	// MaxBackups the max number of rolled files to keep
	MaxBackups int `yaml:"max_backups"`

	// MaxAge the max age in days to keep a logfile
	MaxAge int `yaml:"max_age"`

	RollingWrite io.Writer
}

var loggerConf = &LoggerConf{
	ConsoleLoggingEnabled: true,
	EncodeLogsAsJSON:      false,
	FileLoggingEnabled:    false,
}

// SetConfig set logger config
func SetConfig(cfg *LoggerConf, options *redis.Options) {
	loggerConf = cfg

	if loggerConf.FileLoggingEnabled {
		if loggerConf.Filename == "" {
			name := filepath.Base(os.Args[0]) + "-fox.log"
			loggerConf.Filename = filepath.Join(os.TempDir(), name)
		}

		loggerConf.RollingWrite = &lumberjack.Logger{
			Filename:   cfg.Filename,
			MaxSize:    cfg.MaxSize,
			MaxBackups: cfg.MaxBackups,
			MaxAge:     cfg.MaxAge,
		}
	}

	InitRedis(options)
}
