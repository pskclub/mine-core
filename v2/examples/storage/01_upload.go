package main

import (
	"fmt"
	"io"
	"net/http"
	"path"

	core "github.com/pskclub/mine-core/v2"
	"github.com/pskclub/mine-core/v2/errmsgs"
	"github.com/pskclub/mine-core/v2/utils"
)

// --- Example 1: putting bytes in a bucket -----------------------------------
//
// ctx.Storage() returns a handle already bound to the request, so the methods
// take no ctx and a client that hangs up aborts the upload it started.
//
// Storage does not degrade the way the cache does. A service with no S3_*
// configuration still gets a working handle, but every call on it fails with
// STORAGE_DISABLED — because a cache miss is recoverable (recompute the value)
// while an upload that was quietly dropped is a file the caller believes it
// saved and nobody can get back.

// maxUpload is what the service accepts through itself. Anything genuinely large
// should skip the process entirely — see 03_presign.go.
const maxUpload = 10 << 20 // 10 MiB

// documentKey builds the whole address of an object. Three decisions live here,
// and all three are easier to make once than to fix later:
//
//   - the prefix is structured (tenant → entity), so a tenant can be listed,
//     deleted or migrated without guessing which keys are theirs;
//   - the name is a UUID, because a filename the user supplied is untrusted and
//     because two people uploading "cv.pdf" must not become one object;
//   - the extension is kept, so a content type can still be guessed and a link
//     still looks like a file.
//
// The readable name is not lost — it goes in the database row and in
// Content-Disposition. The key must be unguessable and the filename must be
// readable, and those are different jobs.
func documentKey(tenantID, entity, filename string) string {
	return path.Join("tenants", tenantID, entity, utils.NewUUID()+path.Ext(filename))
}

// uploadReceipt stores something the service generated itself, so the type and
// the download name are known facts rather than claims from a client.
func uploadReceipt(ctx core.IContext, tenantID string, pdf []byte) (string, core.IError) {
	key := documentKey(tenantID, "receipts", "receipt.pdf")

	if err := ctx.Storage().PutBytes(key, pdf, core.StoragePutOptions{
		ContentType: "application/pdf",
		// non-ASCII survives: the header is written in both the quoted and the
		// RFC 5987 form
		Attachment: "ใบเสร็จ.pdf",
		// x-amz-meta-*, handed back by Stat. Small facts that should travel with
		// the object — not a database: it cannot be queried and is only visible
		// one object at a time
		Metadata: map[string]string{"tenant": tenantID},
	}); err != nil {
		return "", err
	}
	return key, nil
}

// uploadFromRequest streams a browser upload straight through to the bucket.
// Nothing is buffered, so a 200MB file does not become 200MB of heap.
func uploadFromRequest(c core.IHTTPContext) error {
	file, err := c.FormFile("file")
	if err != nil {
		return c.NewError(err, errmsgs.BadRequest)
	}
	// file.Size is the real size from the multipart parser — but the whole body
	// has already arrived by the time we can read it. To reject earlier, bound
	// the request body in middleware; to not receive it at all, presign.
	if file.Size > maxUpload {
		return c.NewError(nil, errmsgs.BadRequest)
	}

	src, err := file.Open()
	if err != nil {
		return c.NewError(err, errmsgs.BadRequest)
	}
	defer func() { _ = src.Close() }()

	key := documentKey(tenantOf(c), "uploads", file.Filename)

	// The Content-Type header is the uploader's claim, and an HTML file stored as
	// text/html *renders* when it is served — a stored-XSS hole. For anything a
	// user supplied, force a download instead of trusting the label.
	if err := c.Storage().Put(key, src, core.StoragePutOptions{
		ContentType: "application/octet-stream",
		Attachment:  file.Filename,
	}); err != nil {
		return err
	}

	// hand back the key, so the client can reference the object without the
	// service having to invent a URL for it
	return c.JSON(http.StatusOK, map[string]any{"key": key, "filename": file.Filename})
}

// uploadGenerated writes a file that never exists on disk and never exists whole
// in memory: the generator writes into one end of a pipe while the uploader
// reads the other.
func uploadGenerated(ctx core.IContext, key string, rows []string) core.IError {
	pr, pw := io.Pipe()

	go func() {
		// CloseWithError is the part that matters. Without it, a generator that
		// fails half way closes the pipe cleanly and produces a truncated object
		// that uploads *successfully*.
		_ = pw.CloseWithError(writeRows(pw, rows))
	}()

	return ctx.Storage().Put(key, pr, core.StoragePutOptions{
		ContentType: "text/csv",
		Attachment:  "orders.csv",
	})
}

func writeRows(w io.Writer, rows []string) error {
	for _, row := range rows {
		if _, err := fmt.Fprintln(w, row); err != nil {
			return err
		}
	}
	return nil
}

// tenantOf reads the tenant off the authenticated user. ContextUser carries the
// identity the auth middleware resolved; anything beyond the named fields lives
// in Data.
func tenantOf(c core.IHTTPContext) string {
	if user := c.GetUser(); user != nil {
		return user.Data["tenant_id"]
	}
	return "public"
}
