package core

import (
	"github.com/sirupsen/logrus"
	"io"
	"os"
	"runtime"
)

// Log is the logger utility with information of request context
type E2ELogger struct {
	log    *logrus.Logger
	simple bool
	Type   string
	ctx    IE2EContext
}

// NewLogger will create the logger with context from echo context
func NewE2ELogger(ctx IE2EContext) ILogger {
	logger := logrus.New()
	logger.SetLevel(ctx.ENV().Config().LogLevel)
	formatter := new(logrus.JSONFormatter)
	formatter.TimestampFormat = "2006-01-02 15:04:05"
	formatter.DisableTimestamp = false
	logger.Formatter = formatter
	multi := io.MultiWriter(os.Stderr)
	logger.Out = multi

	return &E2ELogger{
		log:    logger,
		simple: false,
		Type:   ctx.Type(),
		ctx:    ctx,
	}

}

func (logger *E2ELogger) getLogFields(fn string, line int) logrus.Fields {
	return logrus.Fields{
		"type":     logger.Type,
		"function": fn,
		"line":     line,
	}
}

// Info log information level
func (logger *E2ELogger) Info(args ...interface{}) {
	_, fn, line, _ := runtime.Caller(1)
	if logger.simple {
		logger.log.Info(args...)
	} else {
		logger.log.WithFields(logger.getLogFields(fn, line)).Info(args...)
	}
}

// Warn log warnning level
func (logger *E2ELogger) Warn(args ...interface{}) {
	_, fn, line, _ := runtime.Caller(1)
	if logger.simple {
		logger.log.Warn(args...)
	} else {
		logger.log.WithFields(logger.getLogFields(fn, line)).Warn(args...)
	}
}

// Debug log debug level
func (logger *E2ELogger) Debug(args ...interface{}) {
	_, fn, line, _ := runtime.Caller(1)
	if logger.simple {
		logger.log.Debug(args...)
	} else {
		logger.log.WithFields(logger.getLogFields(fn, line)).Debug(args...)
	}
}

// Error log error level
func (logger *E2ELogger) Error(message error, args ...interface{}) {
	_, fn, line, _ := runtime.Caller(1)
	if logger.simple {
		logger.log.Error(message, args)
	} else {
		logger.log.WithFields(logger.getLogFields(fn, line)).Error(message, args)
	}
}
