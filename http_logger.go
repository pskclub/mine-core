package core

import (
	"github.com/getsentry/sentry-go"
	"github.com/sirupsen/logrus"
	"io"
	"os"
	"runtime"
)

// Log is the logger utility with information of request context
type HTTPLogger struct {
	log    *logrus.Logger
	simple bool
	Type   string
	ctx    IHTTPContext
}

// NewLogger will create the logger with context from echo context
func NewHTTPLogger(ctx IHTTPContext) ILogger {
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

	return &HTTPLogger{
		log:    logger,
		simple: false,
		Type:   ctx.Type(),
		ctx:    ctx,
	}

}

func (logger *HTTPLogger) getLogFields(fn string, line int) logrus.Fields {
	return logrus.Fields{
		"type":        logger.Type,
		"function":    fn,
		"line":        line,
		"source_ip":   logger.ctx.RealIP(),
		"http_method": logger.ctx.Request().Method,
		"endpoint":    logger.ctx.Request().URL.RequestURI(),
	}
}

// Info log information level
func (logger *HTTPLogger) Info(args ...interface{}) {
	//CaptureHTTPError(logger.ctx, sentry.LevelInfo, message, args...)
	_, fn, line, _ := runtime.Caller(1)
	if logger.simple {
		logger.log.Info(args...)
	} else {
		logger.log.WithFields(logger.getLogFields(fn, line)).Info(args...)
	}
}

// Warn log warnning level
func (logger *HTTPLogger) Warn(args ...interface{}) {
	//CaptureHTTPError(logger.ctx, sentry.LevelWarning, message, args...)
	_, fn, line, _ := runtime.Caller(1)
	if logger.simple {
		logger.log.Warn(args...)
	} else {
		logger.log.WithFields(logger.getLogFields(fn, line)).Warn(args...)
	}
}

// Debug log debug level
func (logger *HTTPLogger) Debug(args ...interface{}) {
	//CaptureHTTPError(logger.ctx, sentry.LevelDebug, message, args...)
	_, fn, line, _ := runtime.Caller(1)
	if logger.simple {
		logger.log.Debug(args...)
	} else {
		logger.log.WithFields(logger.getLogFields(fn, line)).Debug(args...)
	}
}

// Error log error level
func (logger *HTTPLogger) Error(message error, args ...interface{}) {
	CaptureHTTPError(logger.ctx, sentry.LevelError, message, args...)
	_, fn, line, _ := runtime.Caller(1)
	if logger.simple {
		logger.log.Error(message, args)
	} else {
		logger.log.WithFields(logger.getLogFields(fn, line)).Error(message, args)
	}
}
