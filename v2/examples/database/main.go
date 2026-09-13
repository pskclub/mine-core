// Command database is a runnable tour of the v2 SQL stack: models, the generic
// repository, relations, transactions, pagination, batching and named
// connections.
//
// Each example lives in its own file:
//
//	01_models.go       core.IModel, column types, and who owns the schema
//	02_crud.go         repository.New[M](ctx), copy-on-write, NOT_FOUND
//	03_relations.go    Preload vs Joins, and the N+1 between them
//	04_transactions.go Transaction, NewWithDB, locking, what to keep outside
//	05_pagination.go   PageOptions, Page[T], allow-listed ordering, keyset paging
//	06_batch.go        CreateInBatches and FindInBatches on a table too big to hold
//	07_connections.go  named connections, replicas, replication lag, the pool
//
// It needs no infrastructure: with no DB_* configuration it opens an in-memory
// sqlite database, builds the schema from the models and runs everything
// against it. Point DB_* (or DB_CONNECTION_STRING) at a real postgres and the
// same tour runs there instead — worth doing, because sqlite accepts SQL that
// postgres rejects.
//
// Run it with: go run ./examples/database
package main

import (
	"context"
	"time"

	core "github.com/pskclub/mine-core/v2"
)

func main() {
	env, err := core.NewEnv()
	if err != nil {
		panic(err)
	}
	log := core.NewLogger(env)

	// A misconfigured database is a boot failure in a real service. Here it is
	// a reason to say so and stop, rather than to panic inside the first query.
	db, dbErr := openDatabase(env)
	if dbErr != nil {
		log.Warn("no database available — skipping the tour", "err", dbErr)
		return
	}

	// Both names point at the same handle so the tour runs with no
	// infrastructure. In a service, "readonly" is a second DSN pointed at a
	// replica — see 07_connections.go.
	app, appErr := registerConnections(env, db, db)
	if appErr != nil {
		panic(appErr)
	}
	defer func() {
		// The pools are closed after everything using them has stopped, never
		// before, and the deadline bounds the drain rather than the close.
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = app.Shutdown(shutdownCtx)
	}()

	// One context for the whole tour. In an HTTP service this is per request; in
	// a job it is per run — either way it carries the connection, the deadline
	// and the log fields, so nothing below takes a ctx argument.
	ctx := app.NewContext(context.Background(), core.ModeCron)

	// Development only: production schema belongs to a migration tool run as
	// its own deploy step. See 01_models.go.
	if err := devMigrate(ctx.DB()); err != nil {
		log.Error("dev migrate failed", "err", err)
		return
	}

	steps := []struct {
		name string
		run  func(core.IContext) core.IError
	}{
		{"crud", crudTour},
		{"relations", relationsTour},
		{"transactions", transactionsTour},
		{"pagination", paginationTour},
		{"batch", batchTour},
		{"connections", connectionsTour},
	}

	for _, step := range steps {
		started := time.Now()
		if err := step.run(ctx); err != nil {
			log.Error("step failed", "step", step.name, "err", err)
			return
		}
		log.Info("step ok", "step", step.name, "took", time.Since(started).String())
	}
}
