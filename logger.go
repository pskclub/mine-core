package core

import (
	"github.com/getsentry/sentry-go"
	"github.com/sirupsen/logrus"
	"io"
	"os"
	"runtime"
)

type ILogger interface {
	Info(args ...interface{})
	Warn(args ...interface{})
	Debug(args ...interface{})
	Error(message error, args ...interface{})
}

// Log is the logger utility with information of request context
type Logger struct {
	log        *logrus.Logger
	simple     bool
	Type       string
	RequestID  string
	TrackingID string
	AppID      string
	ctx        IContext
}

// NewLogger will create the logger with context from echo context
func NewLogger(ctx IContext) *Logger {
	logger := logrus.New()
	logger.SetLevel(ctx.ENV().Config().LogLevel)
	formatter := new(logrus.TextFormatter)
	formatter.TimestampFormat = "2006-01-02 15:04:05"
	formatter.DisableTimestamp = false
	formatter.DisableColors = false
	formatter.DisableSorting = false
	logger.Formatter = formatter
	multi := io.MultiWriter(os.Stderr)
	logger.Out = multi

	return &Logger{
		log:    logger,
		simple: false,
		ctx:    ctx,
	}
}

// NewLoggerSimple return plain text simple logger
func NewLoggerSimple() *Logger {
	log2 := logrus.New()
	log2.SetLevel(NewEnv().Config().LogLevel)
	formatter := new(logrus.TextFormatter)
	formatter.DisableTimestamp = true
	formatter.DisableColors = true
	formatter.DisableSorting = true

	log2.Formatter = formatter
	multi := io.MultiWriter(os.Stderr)
	log2.Out = multi
	return &Logger{
		log:    log2,
		simple: true,
	}
}

func (logger *Logger) getLogFields(fn string, line int) logrus.Fields {
	return logrus.Fields{
		"type":        logger.Type,
		"tracking_id": logger.TrackingID,
		"app_id":      logger.AppID,
		"function":    fn,
		"line":        line,
	}
}

// Info log information level
func (logger *Logger) Info(args ...interface{}) {
	//CaptureError(logger.ctx, sentry.LevelInfo, message, args)
	_, fn, line, _ := runtime.Caller(1)
	if logger.simple {
		logger.log.Info(args...)
	} else {
		logger.log.WithFields(logger.getLogFields(fn, line)).Info(args...)
	}
}

// Warn log warnning level
func (logger *Logger) Warn(args ...interface{}) {
	//CaptureError(logger.ctx, sentry.LevelWarning, message, args)
	_, fn, line, _ := runtime.Caller(1)
	if logger.simple {
		logger.log.Warn(args...)
	} else {
		logger.log.WithFields(logger.getLogFields(fn, line)).Warn(args...)
	}
}

// Debug log debug level
func (logger *Logger) Debug(args ...interface{}) {
	//CaptureError(logger.ctx, sentry.LevelDebug, message, args)
	_, fn, line, _ := runtime.Caller(1)
	if logger.simple {
		logger.log.Debug(args...)
	} else {
		logger.log.WithFields(logger.getLogFields(fn, line)).Debug(args...)
	}
}

// Error log error level
func (logger *Logger) Error(message error, args ...interface{}) {
	CaptureError(logger.ctx, sentry.LevelError, message, args)
	_, fn, line, _ := runtime.Caller(1)
	if logger.simple {
		logger.log.Error(message, args)
	} else {
		logger.log.WithFields(logger.getLogFields(fn, line)).Error(message, args)
	}
}
