package main

import (
	"errors"
	"net/http"
	"strconv"

	core "github.com/pskclub/mine-core/v2"
	"github.com/pskclub/mine-core/v2/errmsgs"
)

// --- Example 2: reading, listing, deleting ----------------------------------
//
// Object storage is not a filesystem, and most storage bugs come from treating
// it like one: the key space is flat, an object is immutable, there is no
// rename, and "ls" is a paginated scan. Reading is a network call with network
// latency and network failures — including the most ordinary one of all, the key
// that is not there.

// readSmallObject reads a whole object into memory. That is exactly Get plus
// io.ReadAll, so the size limit is however much memory you are willing to spend
// per concurrent caller — fine for a JSON blob, wrong for a video.
func readSmallObject(ctx core.IContext, key string) ([]byte, core.IError) {
	data, err := ctx.Storage().GetBytes(key)
	if errors.Is(err, core.ErrObjectNotFound) {
		// an absent key is an answer, not a failure: it becomes a 404, and it
		// must not be reported to Sentry as a server error
		return nil, ctx.NewError(err, errmsgs.NotFound)
	}
	if err != nil {
		return nil, err
	}
	return data, nil
}

// serveObject proxies an object through the service. Do this when access has to
// be *checked* — a private document, a per-user file. Otherwise prefer a signed
// link (03_presign.go): proxying moves every byte through the process, and one
// large download holds a request slot for its whole duration.
func serveObject(c core.IHTTPContext, key string) error {
	// Stat first, because the response needs the length and the type — and it
	// transfers no body, so it costs one cheap round trip rather than a guess
	info, err := c.Storage().Stat(key)
	if errors.Is(err, core.ErrObjectNotFound) {
		return c.NewError(err, errmsgs.NotFound)
	}
	if err != nil {
		return err
	}

	body, err := c.Storage().Get(key)
	if err != nil {
		return err
	}
	// Close it. An unclosed body holds an HTTP connection from the SDK's pool, so
	// a handler that leaks one per request runs out of connections rather than
	// out of memory — a far more confusing outage.
	defer func() { _ = body.Close() }()

	c.Response().Header().Set("Content-Length", strconv.FormatInt(info.Size, 10))
	return c.Stream(http.StatusOK, info.ContentType, body)
}

// listTenantObjects lists one tenant's objects through a prefixed handle, so no
// call site ever writes the tenant id into a key and no call site can get it
// wrong.
//
// A Limit is passed on purpose: without one, List follows pagination to the end
// and returns every object under the prefix — many round trips and a large slice
// for a prefix nobody has bounded.
func listTenantObjects(ctx core.IContext, tenantID, prefix string) ([]core.StorageObject, core.IError) {
	tenant := ctx.Storage().WithPrefix("tenants/" + tenantID)
	// keys come back without the storage prefix — the same names they were
	// written under, so a listing feeds straight back into Get or Delete
	return tenant.List(prefix, core.StorageListOptions{Limit: 100})
}

// checkThenRead is the mistake worth naming: two round trips to answer one
// question, and the object can disappear between them. Just Get and handle the
// not-found. Exists earns its keep when the answer itself is the product ("is
// this upload finished yet"), not as a guard.
func checkThenRead(ctx core.IContext, key string) ([]byte, core.IError) {
	ok, err := ctx.Storage().Exists(key) // absence is false, not an error
	if err != nil || !ok {
		return nil, err
	}
	return ctx.Storage().GetBytes(key)
}

// promoteUpload moves an object from its temporary key to its final one. S3 has
// no rename, so Move is a copy followed by a delete and therefore not atomic: a
// failure in between leaves both keys. For this pattern that is harmless — the
// temporary key is swept by a lifecycle rule.
//
// Copy happens inside S3: the bytes never travel to this process, so promoting a
// 2GB object costs one API call rather than 2GB in each direction.
func promoteUpload(ctx core.IContext, tmpKey, finalKey string) core.IError {
	return ctx.Storage().Move(tmpKey, finalKey)
}

// deleteDocument removes the object after the row that referenced it is gone.
// Delete takes any number of keys, batches them into calls of 1000, and does not
// mind an absent one — which makes cleanup code idempotent for free.
//
// Unless the bucket has versioning, a delete is permanent. For anything a user
// can trigger, prefer marking the row deleted and letting a lifecycle rule
// remove the object after a grace period.
func deleteDocument(ctx core.IContext, keys ...string) core.IError {
	return ctx.Storage().Delete(keys...)
}
