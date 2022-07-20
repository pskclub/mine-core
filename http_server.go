package core

import (
	"context"
	"fmt"
	"github.com/getsentry/sentry-go"
	sentryecho "github.com/getsentry/sentry-go/echo"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/sirupsen/logrus"
	"gitlab.finema.co/finema/idin-core/middlewares"
	"gitlab.finema.co/finema/idin-core/utils"
	"net/http"
	"os"
	"os/signal"
	"time"
)

func NewHTTPServer(options *HTTPContextOptions) *echo.Echo {
	e := echo.New()

	// To initialize Sentry's handler, you need to initialize Sentry itself beforehand
	if options.ContextOptions.ENV.Config().SentryDSN != "" {
		if err := sentry.Init(sentry.ClientOptions{
			Dsn: options.ContextOptions.ENV.Config().SentryDSN,
		}); err != nil {
			fmt.Printf("Sentry initialization failed: %v\n", err)
		}
		// Flush buffered events before the program terminates.
		defer sentry.Flush(2 * time.Second)

		e.Use(sentryecho.New(sentryecho.Options{
			Repanic: true,
		}))
	}

	if options.ContextOptions.ENV.Config().LogLevel == logrus.DebugLevel {
		e.Debug = true
		e.Use(middleware.LoggerWithConfig(middleware.LoggerConfig{
			Format: "method=${method}, uri=${uri}, status=${status}\n",
		}))
	}

	e.Use(Core(options))
	e.Use(middleware.CORS())
	e.Use(middlewares.HTTPRequestID())
	e.Use(CreateLoggerMiddleware)
	e.Use(RecoverWithConfig(options.ContextOptions.ENV, middleware.RecoverConfig{
		StackSize: 1 << 20, // 1 KB
	}))
	e.HTTPErrorHandler = HandleError
	echo.NotFoundHandler = HandleNotFound
	e.Use(middleware.Secure())
	e.HideBanner = true
	fmt.Println(fmt.Sprintf("HTTP Service: %s", options.ContextOptions.ENV.Config().Service))

	utils.MockExplorer()
	return e
}

func StartHTTPServer(e *echo.Echo, env IENV) {
	if env.Config().ENV == "dev" {
		e.Logger.Fatal(e.Start(env.Config().Host))
	} else {
		// Start server
		go func() {
			if err := e.Start(env.Config().Host); err != nil && err != http.ErrServerClosed {
				e.Logger.Fatal("shutting down the server")
			}
		}()

		// Wait for interrupt signal to gracefully shutdown the server with a timeout of 10 seconds.
		// Use a buffered channel to avoid missing signals as recommended for signal.Notify
		quit := make(chan os.Signal, 1)
		signal.Notify(quit, os.Interrupt)
		<-quit
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := e.Shutdown(ctx); err != nil {
			e.Logger.Fatal(err)
		}
	}
}
