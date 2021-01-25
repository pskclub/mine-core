package core

import (
	"fmt"
	"github.com/getsentry/sentry-go"
	"time"
)

func NewMQServer(options *MQContextOptions) IMQContext {
	// To initialize Sentry's handler, you need to initialize Sentry itself beforehand
	if options.ContextOptions.ENV.Config().SentryDSN != "" {
		if err := sentry.Init(sentry.ClientOptions{
			Dsn: options.ContextOptions.ENV.Config().SentryDSN,
		}); err != nil {
			fmt.Printf("Sentry initialization failed: %v\n", err)
		}
		// Flush buffered events before the program terminates.
		defer sentry.Flush(2 * time.Second)
	}

	fmt.Println(fmt.Sprintf("MQ Service: %s", options.ContextOptions.ENV.Config().Service))

	return NewMQContext(options)
}
