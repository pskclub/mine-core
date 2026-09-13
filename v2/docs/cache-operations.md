# Values & keys

```go
c := ctx.Cache()
```

## Set and Get

```go
c.Set("user:1", user, time.Minute)    // structs are JSON-encoded
c.Set("token", "abc", time.Hour)      // string/[]byte stored raw
c.Set("config", cfg, core.NoExpiry)   // no expiry
c.Set("user:1", user, core.KeepTTL)   // rewrite, keep the current expiry

var u User
err := c.Get("user:1", &u)            // JSON-decoded into u
if errors.Is(err, core.ErrCacheMiss) {
    // key absent — an ordinary outcome, not a failure
}
```

### Encoding

| `dest` / `value` | On the wire |
|---|---|
| `string`, `[]byte`, `json.RawMessage` | the bytes, unchanged |
| anything else | JSON |

Storing a string raw matters more than it looks: a token stored as JSON comes
back with quotes around it, and the bug shows up wherever the value is compared
rather than where it was written.

```go
var s string
c.Set("token", "abc", time.Hour)
c.Get("token", &s)          // "abc" — not "\"abc\""
```

### TTL values

| Constant | Meaning |
|---|---|
| `core.NoExpiry` | store until deleted |
| `core.KeepTTL` | on `Set`, keep whatever expiry the key already had |
| `core.TTLNoExpiry` | what `TTL()` returns for a key that exists and never expires |

Prefer a TTL to `NoExpiry`, even when the value never changes. A key with no
expiry and no owner is a leak that surfaces months later as a memory alert, and
a TTL is the cheapest correctness guarantee a cache has: whatever goes stale
fixes itself.

## Existence and expiry

```go
ok, _ := c.Exists("user:1")

ttl, err := c.TTL("user:1")
switch {
case errors.Is(err, core.ErrCacheMiss):    // the key is not there
case ttl == core.TTLNoExpiry:              // it is, and it never expires
default:                                   // ttl is what remains
}

ok, _ = c.Expire("user:1", time.Hour)      // reports whether the key existed
```

## Deleting

```go
c.Del("user:1", "token")               // absent keys are not an error
n, _ := c.DelByPrefix("session:")      // a whole namespace
```

`DelByPrefix` SCANs in batches rather than running `KEYS`, so it is safe against a
production instance — but it is still *O(keyspace)*, not *O(matches)*. It walks
every key on the instance to find yours. Fine on a deploy or an admin action;
wrong in a request handler.

## Write once: idempotency keys and one-shot flags

`SetNX` writes only when the key is absent, and reports whether it did:

```go
first, _ := c.SetNX("charge:"+idempotencyKey, "1", 24*time.Hour)
if !first {
    return ctx.NewError(nil, errmsgs.DuplicateRequest)
}
```

This is the primitive behind "process this request exactly once". Give it a TTL
longer than the window in which a client would retry, and shorter than forever.

> On a **disabled** cache `SetNX` returns `true` — every caller believes it is the
> first. Deduplication is one of the things that genuinely needs a real cache;
> guard it with `Enabled()` if a duplicate would be more than an annoyance.

## Read once: OTPs and single-use tokens

```go
var code string
err := c.GetDel("otp:"+phone, &code)   // read and delete in one round trip
if errors.Is(err, core.ErrCacheMiss) {
    return ctx.NewError(nil, errmsgs.OTPExpired)
}
```

Read-then-delete as two calls lets the same OTP be accepted twice by two requests
that arrive together. One round trip makes that impossible.

## Batches

```go
c.MSet(map[string]any{"u:1": a, "u:2": b}, time.Minute)   // one pipeline, one TTL

raw, _ := c.MGet("u:1", "u:2", "u:3")   // map[string][]byte; absent keys are absent

users, _ := core.GetJSONMany[User](c, "u:1", "u:2", "u:3")   // typed
```

`MGet` returns only the keys that were there, so the result is *empty* rather
than an error when none exist. Iterate over the keys you asked for, not over the
result, if you need to know which ones missed:

```go
ids := []string{"1", "2", "3"}
keys := make([]string, len(ids))
for i, id := range ids {
    keys[i] = "user:" + id
}
found, _ := core.GetJSONMany[User](c, keys...)

var missing []string
for i, k := range keys {
    if _, ok := found[k]; !ok {
        missing = append(missing, ids[i])
    }
}
// load only the missing ones from the database
```

That shape — one `MGet`, one query for the misses — is the difference between
*N* round trips and two.

## Type-safe helpers

```go
user, err := core.GetJSON[User](ctx.Cache(), "user:1")
err = core.SetJSON(ctx.Cache(), "user:1", user, time.Minute)
core.Forget(ctx.Cache(), "user:1", "user:1:permissions")
```

`Forget` deletes and ignores the error, because failing a request over a failed
cache *invalidation* trades a stale read for an outage. Use it where a stale
value is survivable and `Del` where it is not.

## Changing the shape of a cached value

A cached JSON document written by an older deploy will not decode into the new
struct. Two answers, and only one of them is safe:

```go
// ✅ the key changes with the shape — old values expire on their own
core.SetJSON(c, "user:v2:"+id, user, time.Minute)

// ❌ hoping the old value is still readable
```

`Remember` already treats a decode failure as a miss and reloads
([patterns](./cache-patterns.md)), so a plain cache-aside path recovers by
itself. Anything reading a key directly does not.

## The raw client

```go
rdb := ctx.Cache().Redis()      // nil for the memory and disabled backends
if rdb == nil {
    return ctx.NewError(nil, errmsgs.CacheError)
}
err := rdb.ZAdd(ctx, ctx.Cache().Prefix()+"leaderboard", redis.Z{…}).Err()
```

Sorted sets, streams, HyperLogLog, Lua — everything the interface does not wrap
is here. Two things to remember: `Redis()` is **nil** on the memory and disabled
backends, and it does **not** apply the prefix.
