package main

import (
	"encoding/json"
	"errors"
	"time"

	core "github.com/pskclub/mine-core/v2"
)

// --- Example 1: values, keys and TTLs ---------------------------------------
//
// The cache stores bytes. What decides the encoding is the Go type, not a flag:
// string, []byte and json.RawMessage go in unchanged, everything else is JSON.
// The distinction is worth having in the type system — a token stored as JSON
// comes back with quotes around it, and that bug surfaces wherever the value is
// compared rather than where it was written.
//
// Every read can miss, and a miss is an ordinary outcome rather than a failure.
// Code that treats one as an error turns a redis restart into an outage.

type user struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func runValues(ctx core.IContext) {
	c := ctx.Cache()

	// Keys are one flat namespace shared by everything on the instance, so give
	// them a structure and keep to it: <type>:<version>:<id>. The type first is
	// what makes DelByPrefix work on a whole class of key; the version means a
	// deploy that changes the struct never has to guess whether the values an
	// older deploy wrote are still readable.
	_ = c.Set("user:v1:42", user{ID: "42", Name: "ann"}, 5*time.Minute) // JSON
	_ = c.Set("token:42", "abc123", time.Hour)                          // raw bytes
	_ = c.Set("flags", json.RawMessage(`{"beta":true}`), time.Hour)     // passed through

	var u user
	if err := c.Get("user:v1:42", &u); err != nil && !errors.Is(err, core.ErrCacheMiss) {
		// A cache that is answering badly is worth recording — it explains the
		// timeout somebody will ask about later — but it is not worth failing
		// the request for.
		ctx.Log().Warn("cache read failed", "err", err)
	}

	var token string
	_ = c.Get("token:42", &token) // "abc123", not "\"abc123\""
	ctx.Log().Info("read back", "name", u.Name, "token", token)

	// The one branch every cache read needs.
	if err := c.Get("user:v1:nobody", &u); errors.Is(err, core.ErrCacheMiss) {
		ctx.Log().Info("miss: recompute, do not fail")
	}

	runTTLs(ctx)
	runOneShotValues(ctx)
	runNamespaces(ctx)
}

// runTTLs covers the three TTL constants and the two questions worth asking
// about a key that is already there.
func runTTLs(ctx core.IContext) {
	c := ctx.Cache()

	// Prefer a TTL to NoExpiry even for a value that never changes. A key with
	// no expiry and no owner is a leak that surfaces months later as a memory
	// alert, and a TTL is the cheapest correctness guarantee a cache has:
	// whatever goes stale fixes itself.
	_ = c.Set("config:rates", map[string]int{"THB": 1}, core.NoExpiry)
	_ = c.Set("user:v1:42", user{ID: "42", Name: "ann"}, core.KeepTTL) // rewrite, same expiry

	exists, _ := c.Exists("user:v1:42")

	ttl, err := c.TTL("user:v1:42")
	switch {
	case errors.Is(err, core.ErrCacheMiss):
		ttl = 0 // the key is not there at all
	case ttl == core.TTLNoExpiry:
		ctx.Log().Warn("key never expires — is that deliberate?", "key", "user:v1:42")
	}

	// Expire reports whether the key existed, which is the only way to tell a
	// refreshed session apart from one that had already lapsed.
	refreshed, _ := c.Expire("session:abc", 30*time.Minute)

	ctx.Log().Info("values", "exists", exists, "ttl", ttl, "session_refreshed", refreshed)
}

// runOneShotValues covers the two operations that are not plain "store this":
// a write that happens only when nothing is there, and a read that consumes.
func runOneShotValues(ctx core.IContext) {
	c := ctx.Cache()

	// SetNX is the primitive behind "handle this request exactly once". Give it
	// a TTL longer than the window a client would retry in, and shorter than
	// forever.
	first, _ := c.SetNX("charge:idem-1", "1", 24*time.Hour)
	if !first {
		ctx.Log().Info("duplicate request, already handled")
	}
	// On a disabled cache SetNX answers true for everybody, so deduplication is
	// one of the few things that genuinely needs a real cache. Say so out loud
	// rather than discovering it from a double charge.
	if !c.Enabled() {
		ctx.Log().Warn("no cache configured: duplicate requests are not detected")
	}

	// GetDel reads and deletes in one round trip. As two calls, two requests
	// that arrive together can both accept the same one-time token.
	var refreshToken string
	if err := c.GetDel("refresh:once", &refreshToken); errors.Is(err, core.ErrCacheMiss) {
		ctx.Log().Info("token already used or expired")
	}

	// One MGet plus one query for whatever missed is the difference between two
	// round trips and N. Absent keys are simply left out of the result, so
	// iterate the keys you asked for rather than the ones you got back.
	keys := []string{"user:v1:1", "user:v1:2"}
	found, _ := core.GetJSONMany[user](c, keys...)
	for _, key := range keys {
		if _, hit := found[key]; !hit {
			ctx.Log().Debug("load this one from the database", "key", key)
		}
	}

	_ = c.Del("charge:idem-1") // deleting an absent key is not an error
}

// runNamespaces: CACHE_PREFIX already namespaces the service and the
// environment; WithPrefix narrows further, which is how a per-feature namespace
// stays one thing that can be cleared in a single call.
func runNamespaces(ctx core.IContext) {
	otp := ctx.Cache().WithPrefix("otp") // stores "<CACHE_PREFIX>otp:<key>"
	_ = otp.Set("0812345678", "123456", 5*time.Minute)

	// Clearing "" under a narrowed handle clears exactly that namespace and
	// nothing else. DelByPrefix SCANs in batches instead of running KEYS, so it
	// is safe against a production instance — but it is still O(keyspace), not
	// O(matches): it walks every key on the instance to find yours. Fine on a
	// deploy or an admin action, wrong in a request handler.
	cleared, _ := otp.DelByPrefix("")

	// The raw client is the escape hatch for sorted sets, streams and Lua. Two
	// things it will not do: exist on the memory and disabled backends (it is
	// nil), and apply the prefix — build keys with Prefix() when you use it.
	if rdb := ctx.Cache().Redis(); rdb != nil {
		_ = rdb // e.g. rdb.ZAdd(ctx, ctx.Cache().Prefix()+"leaderboard", …)
	}

	ctx.Log().Info("namespaces", "prefix", otp.Prefix(), "cleared", cleared)
}
