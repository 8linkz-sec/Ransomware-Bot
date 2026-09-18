package logger

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
)

const serviceName = "ransomware-news-bot"

// Fields and Entry expose the small structured logging surface used by runtime
// packages while keeping the concrete backend contained in this package.
type Fields = logrus.Fields
type Entry = logrus.Entry

// LogRotationConfig contains log rotation settings
type LogRotationConfig struct {
	MaxSizeMB  int
	MaxBackups int
	MaxAgeDays int
	Compress   bool
}

// logFileWriter holds the active rotating file writer for cleanup.
var (
	logFileWriter        *rotatingFileWriter
	logFileWriterMu      sync.Mutex
	serviceHookInstalled bool
)

type serviceHook struct{}

func (serviceHook) Levels() []logrus.Level {
	return logrus.AllLevels
}

func (serviceHook) Fire(entry *logrus.Entry) error {
	if _, ok := entry.Data["service"]; !ok {
		entry.Data["service"] = serviceName
	}
	return nil
}

// NewLogger creates a new logger instance with file and console output
// Sets the global logrus logger configuration
func NewLogger(logLevel string, logFilePath string, rotationConfig LogRotationConfig) error {
	// Set log level on global logger
	level, err := parseLogLevel(logLevel)
	if err != nil {
		return fmt.Errorf("invalid log level: %w", err)
	}
	logrus.SetLevel(level)

	logFileWriterMu.Lock()
	defer logFileWriterMu.Unlock()

	// Close previous logger if open, after pointing logrus somewhere safe.
	_ = resetOutputAndCloseCurrentLocked()

	writer, err := newRotatingFileWriter(logFilePath, rotationConfig)
	if err != nil {
		return err
	}
	logFileWriter = writer

	// Set up multi-writer to write to both file (with rotation) and console
	multiWriter := io.MultiWriter(os.Stdout, logFileWriter)
	logrus.SetOutput(multiWriter)

	// Set structured formatter on global logger
	ensureServiceHook()
	logrus.SetFormatter(&logrus.JSONFormatter{
		TimestampFormat: time.RFC3339,
	})

	// Log initial message
	logrus.WithFields(logrus.Fields{
		"level":        logLevel,
		"log_file":     logFilePath,
		"max_size_mb":  rotationConfig.MaxSizeMB,
		"max_backups":  rotationConfig.MaxBackups,
		"max_age_days": rotationConfig.MaxAgeDays,
		"compress":     rotationConfig.Compress,
	}).Info("Logger initialized with log rotation")

	return nil
}

// NewStdoutLogger configures the global logger for stdout-only output.
func NewStdoutLogger(logLevel string) error {
	level, err := parseLogLevel(logLevel)
	if err != nil {
		return fmt.Errorf("invalid log level: %w", err)
	}
	logrus.SetLevel(level)

	logFileWriterMu.Lock()
	defer logFileWriterMu.Unlock()

	_ = resetOutputAndCloseCurrentLocked()

	ensureServiceHook()
	logrus.SetFormatter(&logrus.JSONFormatter{
		TimestampFormat: time.RFC3339,
	})

	logrus.WithField("level", logLevel).Info("Logger initialized with stdout output only")
	return nil
}

func WithField(key string, value interface{}) *Entry {
	return logrus.WithField(key, value)
}

func WithFields(fields Fields) *Entry {
	return logrus.WithFields(logrus.Fields(fields))
}

func WithError(err error) *Entry {
	return logrus.WithError(err)
}

func Trace(args ...interface{}) {
	logrus.Trace(args...)
}

func Debug(args ...interface{}) {
	logrus.Debug(args...)
}

func Info(args ...interface{}) {
	logrus.Info(args...)
}

func Warn(args ...interface{}) {
	logrus.Warn(args...)
}

func Error(args ...interface{}) {
	logrus.Error(args...)
}

// SetLevel updates the global logger level using the package's level policy.
func SetLevel(logLevel string) error {
	level, err := parseLogLevel(logLevel)
	if err != nil {
		return fmt.Errorf("invalid log level: %w", err)
	}
	logrus.SetLevel(level)
	return nil
}

func ensureServiceHook() {
	if serviceHookInstalled {
		return
	}
	logrus.AddHook(serviceHook{})
	serviceHookInstalled = true
}

// resetOutputAndCloseCurrentLocked points the global logger back at stdout and
// then closes the active file writer. Callers must hold logFileWriterMu.
// Order matters: a closed writer refuses every write, so logrus has to be
// writing somewhere else before the close happens.
func resetOutputAndCloseCurrentLocked() error {
	logrus.SetOutput(os.Stdout)
	if logFileWriter == nil {
		return nil
	}
	err := logFileWriter.Close()
	logFileWriter = nil
	return err
}

// Close closes the log file and should be called during application shutdown
func Close() error {
	logFileWriterMu.Lock()
	defer logFileWriterMu.Unlock()

	return resetOutputAndCloseCurrentLocked()
}

// parseLogLevel converts string log level to logrus.Level
func parseLogLevel(level string) (logrus.Level, error) {
	switch strings.ToUpper(level) {
	case "TRACE":
		return logrus.TraceLevel, nil
	case "DEBUG":
		return logrus.DebugLevel, nil
	case "INFO":
		return logrus.InfoLevel, nil
	case "WARNING", "WARN":
		return logrus.WarnLevel, nil
	case "ERROR":
		return logrus.ErrorLevel, nil
	default:
		return logrus.InfoLevel, fmt.Errorf("unknown log level: %s", level)
	}
}
