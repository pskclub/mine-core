package main

import (
	"time"

	"github.com/glebarez/sqlite"
	core "github.com/pskclub/mine-core/v2"
	"github.com/pskclub/mine-core/v2/repository"
	"gorm.io/gorm"
)

// --- Example 7: connections, replicas and the pool --------------------------
//
// A connection is opened once, at startup, and registered on the App. Nothing
// opens one per request — that was v1's bug, and it is why IContext.Close() no
// longer exists. "default" is what ctx.DB() returns; every other name is
// ctx.DBS(name).

// openDatabase gives this example something to run against.
//
// core.NewDatabase supports postgres, mysql, sqlserver and oracle — and on
// purpose not sqlite, because no schema you deploy is ever sqlite. So: use the
// configured database when DB_* is set, and otherwise fall back to an in-memory
// sqlite opened directly, which is what the tour runs on with no setup at all.
func openDatabase(env core.IENV) (*gorm.DB, core.IError) {
	cfg := env.Config()
	if cfg.DBConnectionString != "" || cfg.DBDriver != "" {
		// Pool sizes are options here rather than environment keys: the right
		// numbers depend on what the process *is* — an API serving 200
		// concurrent requests and a cron worker running one query at a time do
		// not want the same pool — not on which environment it runs in. The
		// arithmetic worth doing once is MaxOpenConns × replicas staying well
		// under the server's max_connections, with room for migrations and a
		// human holding a psql session.
		return core.NewDatabase(env,
			core.WithMaxOpenConns(20),
			core.WithMaxIdleConns(5),
			core.WithConnMaxLifetime(30*time.Minute), // shorter than any proxy's idle timeout
		)
	}

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		// NewDatabase would set this for you. Without it, timestamps depend on
		// the machine's timezone and two servers disagree about "now".
		NowFunc: func() time.Time { return time.Now().UTC() },
	})
	if err != nil {
		return nil, core.Wrap(err, "database example: open sqlite")
	}
	sqlDB, sqlErr := db.DB()
	if sqlErr != nil {
		return nil, core.Wrap(sqlErr, "database example: sql handle")
	}
	// Every connection to ":memory:" gets its *own* empty database, so a pool of
	// more than one would see tables come and go. One connection, one database.
	sqlDB.SetMaxOpenConns(1)
	return db, nil
}

// registerConnections is the shape a service with a read replica uses. Reads
// that can tolerate being a little behind go to the replica; everything else
// stays on the primary.
//
// NewDatabase reads one set of DB_* keys, so a second connection is opened
// directly — env.String reads any key that is not part of ENVConfig, so a
// second DSN needs no change to the framework.
func registerConnections(env core.IENV, primary, replica *gorm.DB) (*core.App, core.IError) {
	return core.NewApp(env,
		core.WithSQL("default", primary),
		core.WithSQL("readonly", replica),
	)
	// app.Shutdown closes both. Handing a *gorm.DB to WithSQL transfers that
	// responsibility — do not also close it yourself.
}

// reportFromReplica is the good case for a replica: a heavy read whose answer
// being a second old changes nothing.
func reportFromReplica(ctx core.IContext) (int64, core.IError) {
	// ctx.DBS on a name that was never registered returns nil, and nil panics at
	// the first use. That is deliberate: a wiring mistake should be loud and
	// immediate rather than a confusing error much later.
	replica := ctx.DBS("readonly")
	if replica == nil {
		return 0, core.New(500, "INVALID_CONFIG", `no "readonly" connection registered`)
	}
	return repository.NewWithDB[User](ctx, replica).
		Where("status = ?", UserActive).
		Count()
}

// createThenRead is the trap. Reads sent to a replica arrive *behind* the
// primary, so a row written a millisecond ago may genuinely not be there yet —
// replication lag is not a bug, and no retry loop makes it correct.
//
// Route by whether the caller can tolerate staleness, not by whether the
// statement happens to be a SELECT.
func createThenRead(ctx core.IContext, email string) (*User, core.IError) {
	users := repository.New[User](ctx) // primary
	u := User{Email: email, Name: "Fresh", Status: UserActive}
	if err := users.Create(&u); err != nil {
		return nil, err
	}
	// ✅ read it back from the primary, not from ctx.DBS("readonly")
	return users.FindOne("id = ?", u.ID)
}

func connectionsTour(ctx core.IContext) core.IError {
	if err := pingDB(ctx); err != nil {
		return err
	}
	active, err := reportFromReplica(ctx)
	if err != nil {
		return err
	}
	if _, err := createThenRead(ctx, "conn-fresh@example.com"); err != nil {
		return err
	}
	ctx.Log().Info("replica report", "active_users", active)
	return nil
}

// pingDB belongs in a readiness probe, not a liveness probe: a database that is
// briefly unreachable should stop traffic being routed to the pod, not restart
// it.
func pingDB(ctx core.IContext) core.IError {
	sqlDB, err := ctx.DB().DB()
	if err != nil {
		return core.Wrap(err, "database example: sql handle")
	}
	if err := sqlDB.PingContext(ctx); err != nil {
		return core.Wrap(err, "database example: ping")
	}
	return nil
}
