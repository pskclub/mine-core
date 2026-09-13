package main

import (
	"errors"
	"math/rand/v2"
	"time"

	core "github.com/pskclub/mine-core/v2"
)

// --- Example 4: cache-aside with Remember -----------------------------------
//
// Look in the cache; on a miss compute the value and store it. Remember is that
// in one call, and its guarantee is what makes it safe to sprinkle around: a
// cache that is down, disabled, or holding a value written by an older version
// of the struct is not an error — the loader runs and the result is served.
// Only the loader failing fails the call.
//
// Adding a cache therefore cannot introduce a new way for a request to fail.
// The worst case is the speed you had before it.
//
//	with redis:                one GET
//	without redis:             one loadUser, every time
//	with a stale-shaped value: one loadUser, and the key is overwritten

// errNotFound stands in for the repository's not-found error.
var errNotFound = errors.New("not found")

func runRemember(ctx core.IContext) {
	// Jitter the TTL: a thousand keys written in the same second expire in the
	// same second, and the cliff arrives at the database as a spike. This turns
	// a periodic cliff into a flat line for the price of one addition.
	u, err := core.Remember(ctx.Cache(), "user:v1:42", jitter(5*time.Minute, 30*time.Second),
		func() (user, error) { return loadUser(ctx, "42") })
	if err != nil {
		ctx.Log().Error("could not load the user at all", "err", err)
		return
	}

	// RememberOnce puts a lock around the loader: one caller computes, the rest
	// wait for it to publish, and fall back to loading it themselves rather
	// than failing. Use it when the loader is expensive *and* many callers want
	// the same key at once — Remember is cheaper and right for everything else.
	//
	// The wait should be a little longer than the loader's p99. Too short and
	// everybody falls through and computes it anyway; too long and a slow
	// loader holds a thousand requests instead of failing them.
	report, err := core.RememberOnce(ctx.Cache(), "report:tenant-1",
		time.Hour,     // how long the result is cached
		3*time.Second, // how long to wait for whoever is building it
		func() (string, error) { return buildReport(ctx, "tenant-1") })

	found, _ := lookupUser(ctx, "does-not-exist")
	ctx.Log().Info("remember", "user", u.Name, "report", report, "found", found != nil, "err", err)
}

// jitter spreads expiries. math/rand is the right tool here: this is
// load-shaping, not a secret.
func jitter(base, spread time.Duration) time.Duration {
	return base + time.Duration(rand.Int64N(int64(spread)))
}

// userLookup is how a cache stores "nothing". A nil value is indistinguishable
// from a miss once it comes back out, so the answer has to carry a flag.
type userLookup struct {
	User  *user `json:"user"`
	Found bool  `json:"found"`
}

// lookupUser caches the absence too. A key that is looked up constantly and
// does not exist hits the database every single time otherwise — an easy way
// for one bad client to become a load problem.
//
// The negative TTL is deliberately much shorter than the positive one: somebody
// who has just signed up should not be told they do not exist for the next five
// minutes.
func lookupUser(ctx core.IContext, id string) (*user, core.IError) {
	res, err := core.Remember(ctx.Cache(), "user:v1:lookup:"+id, 30*time.Second,
		func() (userLookup, error) {
			u, err := loadUser(ctx, id)
			if errors.Is(err, errNotFound) {
				return userLookup{Found: false}, nil
			}
			if err != nil {
				return userLookup{}, err
			}
			return userLookup{User: &u, Found: true}, nil
		})
	if err != nil {
		return nil, err
	}
	if !res.Found {
		return nil, core.New(404, "USER_NOT_FOUND", "user not found")
	}
	return res.User, nil
}

func loadUser(_ core.IContext, id string) (user, error) {
	if id == "does-not-exist" {
		return user{}, errNotFound
	}
	return user{ID: id, Name: "ann"}, nil
}

func buildReport(_ core.IContext, tenantID string) (string, error) {
	return "report for " + tenantID, nil
}

// Choosing what to cache is most of the work:
//
//	worth caching                              not worth caching
//	expensive reads, far more read than written written more often than read
//	the result of an external API call          data that must be exactly current
//	computed aggregates and reports             large blobs — that is storage
//	session and token lookups                   the only copy of anything
//
// The measurement worth doing first: how long does the uncached path actually
// take, and how often is it called? A cache in front of a 2ms query called
// twice a minute adds a class of bug and saves nothing.
//
// And the boundary that matters: if the uncached path cannot serve traffic at
// all, the cache is not a cache any more. It is a dependency — and this one is
// designed to be lost.
