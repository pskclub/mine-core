package core

import (
	"fmt"
	"net/http"
	"time"

	"github.com/go-co-op/gocron"
	"github.com/pskclub/mine-core/consts"
)

var cronjobError = Error{
	Status:  http.StatusInternalServerError,
	Code:    "CRONJOB_ERROR",
	Message: "cronjob internal error"}

type ICronjobContext interface {
	IContext
	Job() *gocron.Scheduler
	Start()
	AddJob(job *gocron.Scheduler, handlerFunc func(ctx ICronjobContext) error)
}

type CronjobContext struct {
	IContext
	cron *gocron.Scheduler
}

func (c CronjobContext) AddJob(job *gocron.Scheduler, handlerFunc func(ctx ICronjobContext) error) {
	_, err := job.Do(func() {
		defer func() {
			if err := recover(); err != nil {
				err, ok := err.(error)
				if !ok {
					err = fmt.Errorf("%v", err)
				}
				c.NewError(err, cronjobError)
			}
		}()

		err := handlerFunc(c)
		if err != nil {
			c.NewError(err, cronjobError)
		}
	})
	if err != nil {
		c.NewError(err, cronjobError)
	}
}

func (c CronjobContext) Start() {
	c.cron.StartBlocking()
}

func (c CronjobContext) Job() *gocron.Scheduler {
	return c.cron
}

type CronjobContextOptions struct {
	ContextOptions *ContextOptions
	TimeLocation   *time.Location
}

func NewCronjobContext(options *CronjobContextOptions) ICronjobContext {
	ctxOptions := options.ContextOptions
	ctxOptions.contextType = consts.CRONJOB

	if options.TimeLocation == nil {
		options.TimeLocation = time.UTC
	}
	cron := gocron.NewScheduler(options.TimeLocation)

	fmt.Println(fmt.Sprintf("Cronjob Service: %s", options.ContextOptions.ENV.Config().Service))
	return &CronjobContext{IContext: NewContext(ctxOptions), cron: cron}
}
