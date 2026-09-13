// Command mongo is a runnable tour of the v2 MongoDB support: connecting, the
// driver layer, the typed repository, aggregation pipelines, indexes, change
// streams and transactions.
//
// Each example lives in its own file:
//
//	01_connect.go       connecting, the document type, what "not configured" does
//	02_queries.go       the driver layer — find, insert, update, bulk, claim
//	03_repo_queries.go  mongorepo operators, projections, paging, streaming
//	04_repo_writes.go   create, update, upsert, atomic operators, delete
//	05_aggregation.go   the typed pipeline builder — $lookup, $group, paging
//	06_indexes.go       EnsureIndexes, TTL, partial indexes, change streams
//	07_transactions.go  two collections that have to agree
//
// It needs a MongoDB, and a single-node replica set rather than a standalone
// server — transactions and change streams are refused without one:
//
//	docker run -d --rm -p 27017:27017 --name idin-mongo mongo:7 --replSet rs0 && sleep 3 && docker exec idin-mongo mongosh --quiet --eval 'rs.initiate()'
//
// Then run it with:
//
//	APP_DB_MONGO_HOST=127.0.0.1 APP_DB_MONGO_NAME=examples APP_DB_MONGO_REPLICA_NAME=rs0 go run ./examples/mongo
//
// With no Mongo reachable it says so and stops. An example that cannot connect
// has nothing to demonstrate, and a panic would only bury the one line that
// matters.
package main

import (
	"context"

	core "github.com/pskclub/mine-core/v2"
)

func main() {
	env, err := core.NewEnv()
	if err != nil {
		panic(err) // no configuration at all — nothing can be built from this
	}
	log := core.NewLogger(env)

	app, appErr := newApp(env)
	if appErr != nil {
		log.Warn("no MongoDB reachable — nothing to demonstrate",
			"err", appErr.Error(),
			"start_one", "docker run -d --rm -p 27017:27017 --name idin-mongo mongo:7 --replSet rs0",
			"then", "docker exec idin-mongo mongosh --quiet --eval 'rs.initiate()'")
		return
	}
	// Shutdown closes the client that was handed to core.WithMongo — handing it
	// over transfers that responsibility, so nothing else Close()s it.
	defer func() { _ = app.Shutdown(context.Background()) }()

	// ModeTest because this is a script rather than a request or a job. The mode
	// only labels the unit of work for the logger and the Sentry scope.
	ctx := app.NewContext(context.Background(), core.ModeTest)

	// The files are numbered in reading order; this runs them in *working*
	// order, which differs in one place: the writes go first, so the reads that
	// follow have something to find.
	steps := []struct {
		name string
		run  func(core.IContext) core.IError
	}{
		{"01 connect", mongoIsNeverNil},
		{"04 repository writes", repoWrites},
		{"02 driver layer", driverLayer},
		{"03 repository queries", repoQueries},
		{"05 aggregation", aggregations},
		{"06 indexes and change streams", indexesAndStreams},
		{"07 transactions", transactions},
	}

	failed := 0
	for _, step := range steps {
		// A failing step is reported and the tour continues: transactions and
		// change streams need a replica set, and "this server cannot do that" is
		// information about the environment rather than a reason to stop showing
		// the parts that work.
		if stepErr := step.run(ctx); stepErr != nil {
			failed++
			ctx.Log().Warn("step failed", "step", step.name,
				"code", stepErr.GetCode(), "err", stepErr.Error())
			continue
		}
		ctx.Log().Info("step ok", "step", step.name)
	}
	ctx.Log().Info("mongo examples finished", "steps", len(steps), "failed", failed)
}
