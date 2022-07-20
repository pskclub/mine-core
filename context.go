package core

import (
	"fmt"
	"github.com/go-errors/errors"
	"github.com/pskclub/mine-core/consts"
	"gorm.io/gorm"
)

type IContext interface {
	MQ() IMQ
	DB() *gorm.DB
	DBS(name string) *gorm.DB
	DBMongo() IMongoDB
	DBSMongo(name string) IMongoDB
	ENV() IENV
	Log() ILogger
	Type() string
	NewError(err error, errorType IError, args ...interface{}) IError
	Requester() IRequester
	Cache() ICache
	Caches(name string) ICache
	GetData(name string) interface{}
	SetData(name string, data interface{})
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
}

func (c *coreContext) GetData(name string) interface{} {
	return c.data[name]
}

func (c *coreContext) SetData(name string, data interface{}) {
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

func (c *coreContext) Type() string {
	return string(c.contextType)
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
	if err != nil {
		if ierr, ok := err.(Error); ok {
			errorType = Error{
				Status:        errorType.(Error).Status,
				Code:          errorType.(Error).Code,
				Message:       errorType.(Error).Message,
				Fields:        errorType.(Error).Fields,
				originalError: ierr.originalError,
			}
		} else {
			errorType = Error{
				Status:        errorType.GetStatus(),
				Code:          errorType.GetCode(),
				Message:       errorType.GetMessage(),
				originalError: err,
			}
		}

		errWrap := errors.Wrap(errorType.OriginalError(), 1)
		if errorType.GetStatus() >= 500 {
			stack := errWrap.ErrorStack()
			fmt.Println(stack)
			c.Log().Error(errWrap, args...)
		} else {
			c.Log().Debug(errWrap.ErrorStack(), args)
		}
	}

	return errorType
}

func (c *coreContext) ENV() IENV {
	return c.env
}
