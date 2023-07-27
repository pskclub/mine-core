package core

import (
	"fmt"
	"github.com/getsentry/sentry-go"
	"github.com/labstack/echo/v4"
	"github.com/pskclub/mine-core/consts"
	"github.com/pskclub/mine-core/utils"
	"github.com/sirupsen/logrus"
	"io"
	"os"
	"reflect"
	"runtime"
	"strings"
)

type ILogger interface {
	Info(args ...interface{})
	Warn(args ...interface{})
	Debug(args ...interface{})
	DebugWithSkip(skip int, args ...interface{})
	Error(message error, args ...interface{})
	ErrorWithSkip(skip int, message error, args ...interface{})
}

// Log is the logger utility with information of request context
type Logger struct {
	log       *logrus.Logger
	simple    bool
	RequestID string
	HostName  string
	ctx       IContext
	Type      consts.ContextType
}

// NewLogger will create the logger with context from echo context
func NewLogger(ctx IContext) *Logger {
	logger := logrus.New()
	logger.SetLevel(ctx.ENV().Config().LogLevel)
	logger.Formatter = &logrus.JSONFormatter{
		TimestampFormat:  "2006-01-02 15:04:05",
		DisableTimestamp: false,
		FieldMap: logrus.FieldMap{
			logrus.FieldKeyTime:  "timestamp",
			logrus.FieldKeyMsg:   "full_message",
			logrus.FieldKeyLevel: "level_type",
		},
	}
	logMulti := io.MultiWriter(os.Stderr)

	logger.Out = logMulti
	hostName, _ := os.Hostname()
	return &Logger{
		log:      logger,
		simple:   false,
		Type:     ctx.Type(),
		HostName: hostName,
		ctx:      ctx,
	}
}

// NewLoggerSimple return plain text simple logger
func NewLoggerSimple() *Logger {
	log2 := logrus.New()
	log2.SetLevel(NewEnv().Config().LogLevel)

	log2.Formatter = new(logrus.JSONFormatter)
	log2.Out = io.MultiWriter(os.Stderr)
	hostName, _ := os.Hostname()
	return &Logger{
		log:      log2,
		HostName: hostName,
		simple:   true,
	}
}

func (logger *Logger) isStruct(value interface{}) bool {
	val := reflect.ValueOf(value)
	kind := val.Kind()

	if kind == reflect.Ptr {
		kind = val.Elem().Kind()
	}

	return kind == reflect.Struct
}

func (logger *Logger) isMap(value interface{}) bool {
	val := reflect.ValueOf(value)
	kind := val.Kind()
	if kind == reflect.Ptr {
		kind = val.Elem().Kind()
	}
	return kind == reflect.Map
}
func (logger *Logger) updateArgsToJSON(args ...interface{}) {
	for i, arg := range args {
		if logger.isStruct(arg) || logger.isMap(arg) {
			args[i] = utils.StructToStringNoPretty(arg)
		}
	}
}

func (logger *Logger) getLogFields(fn string, function string, line int, shortMessage string) logrus.Fields {
	fields := logrus.Fields{
		"version":       "1.1",
		"host":          logger.HostName,
		"short_message": shortMessage,
		"_service_type": logger.Type,
		"_env":          logger.ctx.ENV().Config().ENV,
		"_file":         fn,
		"_function":     function,
		"_line":         line,
	}

	if logger.ctx.GetUser() != nil {
		fields["_user_id"] = logger.ctx.GetUser().ID
	}

	if logger.Type == consts.HTTP {
		ctx := logger.ctx.(IHTTPContext)
		fields["_request_id"] = ctx.Get(echo.HeaderXRequestID)
		fields["_source_ip"] = ctx.RealIP()
		fields["_http_method"] = ctx.Request().Method
		fields["_endpoint"] = ctx.Request().URL.RequestURI()
	}

	return fields
}

// Info log information level
func (logger *Logger) Info(args ...interface{}) {
	fmt.Println("")
	logger.updateArgsToJSON(args...)
	//CaptureError(logger.ctx, sentry.LevelInfo, message, args)
	pc, fn, line, _ := runtime.Caller(1)
	funcnameTemp := runtime.FuncForPC(pc).Name()
	funcname := funcnameTemp[strings.LastIndex(funcnameTemp, "/")+1:]
	if logger.simple {
		logger.log.Info(args...)
	} else {
		logger.log.WithFields(logger.getLogFields(fn, funcname, line, fmt.Sprintf("%v", args[0]))).Info(args...)
	}
}

// Warn log warnning level
func (logger *Logger) Warn(args ...interface{}) {
	fmt.Println("")
	logger.updateArgsToJSON(args...)
	//CaptureError(logger.ctx, sentry.LevelWarning, message, args)
	pc, fn, line, _ := runtime.Caller(1)
	funcnameTemp := runtime.FuncForPC(pc).Name()
	funcname := funcnameTemp[strings.LastIndex(funcnameTemp, "/")+1:]

	if logger.simple {
		logger.log.Warn(args...)
	} else {
		logger.log.WithFields(logger.getLogFields(fn, funcname, line, fmt.Sprintf("%v", args[0]))).Warn(args...)
	}
}

// Debug log debug level
func (logger *Logger) Debug(args ...interface{}) {
	fmt.Println("")
	logger.updateArgsToJSON(args...)
	//CaptureError(logger.ctx, sentry.LevelDebug, message, args)
	pc, fn, line, _ := runtime.Caller(1)
	funcnameTemp := runtime.FuncForPC(pc).Name()
	funcname := funcnameTemp[strings.LastIndex(funcnameTemp, "/")+1:]
	if logger.simple {
		logger.log.Debug(args...)
	} else {
		logger.log.WithFields(logger.getLogFields(fn, funcname, line, fmt.Sprintf("%v", args[0]))).Debug(args...)
	}
}

func (logger *Logger) DebugWithSkip(skip int, args ...interface{}) {
	fmt.Println("")
	logger.updateArgsToJSON(args...)
	//CaptureError(logger.ctx, sentry.LevelDebug, message, args)
	pc, fn, line, _ := runtime.Caller(skip)
	funcnameTemp := runtime.FuncForPC(pc).Name()
	funcname := funcnameTemp[strings.LastIndex(funcnameTemp, "/")+1:]
	if logger.simple {
		logger.log.Debug(args...)
	} else {
		logger.log.WithFields(logger.getLogFields(fn, funcname, line, fmt.Sprintf("%v", args[0]))).Debug(args...)
	}
}

// Error log error level
func (logger *Logger) Error(message error, args ...interface{}) {
	fmt.Println("")
	logger.updateArgsToJSON(args...)
	logger.addBreadcrumb(message.Error(), sentry.LevelError, args)
	CaptureError(logger.ctx, sentry.LevelError, message, args)
	pc, fn, line, _ := runtime.Caller(1)
	funcnameTemp := runtime.FuncForPC(pc).Name()
	funcname := funcnameTemp[strings.LastIndex(funcnameTemp, "/")+1:]
	if logger.simple {
		logger.log.Error(message, args)
	} else {
		logger.log.WithFields(logger.getLogFields(fn, funcname, line, message.Error())).Error(message.Error(), args)
	}
}

// Error log error level
func (logger *Logger) ErrorWithSkip(skip int, message error, args ...interface{}) {
	fmt.Println("")
	logger.updateArgsToJSON(args...)
	logger.addBreadcrumb(message.Error(), sentry.LevelError, args)
	CaptureError(logger.ctx, sentry.LevelError, message, args)
	pc, fn, line, _ := runtime.Caller(skip)
	funcnameTemp := runtime.FuncForPC(pc).Name()
	funcname := funcnameTemp[strings.LastIndex(funcnameTemp, "/")+1:]
	if logger.simple {
		logger.log.Error(message, args)
	} else {
		logger.log.WithFields(logger.getLogFields(fn, funcname, line, message.Error())).Error(message.Error(), args)
	}
}

func (logger *Logger) addBreadcrumb(message string, level sentry.Level, args ...interface{}) {
	breadcrumbs, ok := logger.ctx.GetData("breadcrumb").([]sentry.Breadcrumb)
	if !ok {
		breadcrumbs = make([]sentry.Breadcrumb, 0)
	}

	argData := make(map[string]interface{})
	for i, arg := range args {
		argData[fmt.Sprintf("ARG-%v", i)] = arg
	}

	breadcrumbs = append(breadcrumbs, sentry.Breadcrumb{
		Data:      argData,
		Level:     level,
		Message:   message,
		Timestamp: *utils.GetCurrentDateTime(),
	})
	logger.ctx.SetData("breadcrumbs", breadcrumbs)
}
