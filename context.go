package core

import (
	"fmt"
	"github.com/getsentry/sentry-go"
	"github.com/go-errors/errors"
	"github.com/pskclub/mine-core/consts"
	"gorm.io/gorm"
	"time"
)

type ContextUser struct {
	ID       string            `json:"id,omitempty"`
	Email    string            `json:"email,omitempty"`
	Username string            `json:"username,omitempty"`
	Name     string            `json:"name,omitempty"`
	Segment  string            `json:"segment,omitempty"`
	Data     map[string]string `json:"data,omitempty"`
}
type IContext interface {
	MQ() IMQ
	DB() *gorm.DB
	DBS(name string) *gorm.DB
	DBMongo() IMongoDB
	DBSMongo(name string) IMongoDB
	ENV() IENV
	Log() ILogger
	Type() consts.ContextType
	NewError(err error, errorType IError, args ...interface{}) IError
	Requester() IRequester
	Cache() ICache
	Caches(name string) ICache
	GetData(name string) interface{}
	GetAllData() map[string]interface{}
	SetData(name string, data interface{})
	SetUser(user *ContextUser)
	GetUser() *ContextUser
}

type ContextOptions struct {
	DB          *gorm.DB
	DBS         map[string]*gorm.DB
	MongoDB     IMongoDB
	MongoDBS    map[string]IMongoDB
	Cache       ICache
	Caches      map[string]ICache
	ENV         IENV
	MQ          IMQ
	contextType consts.ContextType
	DATA        map[string]interface{}
}

func NewContext(options *ContextOptions) IContext {
	// To initialize Sentry's handler, you need to initialize Sentry itself beforehand
	if options.ENV.Config().SentryDSN != "" {
		if err := sentry.Init(sentry.ClientOptions{
			Dsn: options.ENV.Config().SentryDSN,
		}); err != nil {
			fmt.Printf("Sentry initialization failed: %v\n", err)
		}
		// Flush buffered events before the program terminates.
		defer sentry.Flush(2 * time.Second)
	}

	return &coreContext{
		database:       options.DB,
		databases:      options.DBS,
		contextType:    options.contextType,
		databaseMongo:  options.MongoDB,
		databasesMongo: options.MongoDBS,
		env:            options.ENV,
		cache:          options.Cache,
		caches:         options.Caches,
		mq:             options.MQ,
		data:           options.DATA,
	}
}

type coreContext struct {
	contextType    consts.ContextType
	database       *gorm.DB
	databases      map[string]*gorm.DB
	cache          ICache
	caches         map[string]ICache
	databaseMongo  IMongoDB
	databasesMongo map[string]IMongoDB
	mq             IMQ
	env            IENV
	logger         ILogger
	data           map[string]interface{}
	user           *ContextUser
}

func (c *coreContext) SetUser(user *ContextUser) {
	c.user = user
}

func (c *coreContext) GetUser() *ContextUser {
	return c.user
}

func (c *coreContext) GetAllData() map[string]interface{} {
	if c.data == nil {
		c.data = make(map[string]interface{})
	}

	return c.data
}

func (c *coreContext) GetData(name string) interface{} {
	if c.data == nil {
		c.data = make(map[string]interface{})
	}
	return c.data[name]
}

func (c *coreContext) SetData(name string, data interface{}) {
	if c.data == nil {
		c.data = make(map[string]interface{})
	}
	c.data[name] = data
}

func (c *coreContext) Cache() ICache {
	return c.cache
}

func (c *coreContext) MQ() IMQ {
	return c.mq
}

func (c *coreContext) Caches(name string) ICache {
	cache, ok := c.caches[name]
	if !ok {
		return nil
	}
	return cache
}

func (c *coreContext) Requester() IRequester {
	return NewRequester(c)
}

func (c *coreContext) SetType(t consts.ContextType) {
	c.contextType = t
}

// Log return the logger
func (c *coreContext) Log() ILogger {
	if c.logger == nil {
		c.logger = NewLogger(c)
	}

	return c.logger.(ILogger)
}

func (c *coreContext) Type() consts.ContextType {
	return c.contextType
}

func (c *coreContext) DB() *gorm.DB {
	return c.database
}

func (c *coreContext) DBS(name string) *gorm.DB {
	db, ok := c.databases[name]
	if !ok {
		return nil
	}
	return db
}

func (c *coreContext) DBMongo() IMongoDB {
	return c.databaseMongo
}

func (c *coreContext) DBSMongo(name string) IMongoDB {
	db, ok := c.databasesMongo[name]
	if !ok {
		return nil
	}
	return db
}

func (c *coreContext) NewError(err error, errorType IError, args ...interface{}) IError {
	return newError(c, err, errorType, args...)
}

func (c *coreContext) ENV() IENV {
	return c.env
}

func newError(c IContext, err error, errorType IError, args ...interface{}) IError {
	if err != nil {
		if ierr, ok := err.(Error); ok {
			errorMessage := errorType.(Error).Message
			if c.ENV().IsDev() && ierr.originalError != nil {
				errorMessage = ierr.originalError.Error()
			}

			errorType = Error{
				Status:        errorType.(Error).Status,
				Code:          errorType.(Error).Code,
				Message:       errorMessage,
				Fields:        errorType.(Error).Fields,
				originalError: ierr.originalError,
			}
		} else {
			errorMessage := errorType.GetMessage()
			if c.ENV().IsDev() {
				errorMessage = err.Error()
			}

			errorType = Error{
				Status:        errorType.GetStatus(),
				Code:          errorType.GetCode(),
				Message:       errorMessage,
				originalError: err,
			}
		}

		skip := 1
		if c.Type() == consts.HTTP {
			skip = 2
		}

		errWrap := errors.Wrap(err, skip)
		stack := errWrap.ErrorStack()
		if errorType.GetStatus() >= 500 {
			fmt.Println(stack)
			args = append(args, stack)
			c.Log().ErrorWithSkip(3, err, args...)
		} else {
			args = append([]interface{}{err}, args...)
			args = append(args, stack)
			c.Log().DebugWithSkip(3, args...)
		}
	}

	return errorType
}
