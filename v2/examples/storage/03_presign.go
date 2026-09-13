package main

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	core "github.com/pskclub/mine-core/v2"
	"github.com/pskclub/mine-core/v2/errmsgs"
)

// --- Example 3: temporary links ---------------------------------------------
//
// A presigned URL lets a client read or write *one* object for a limited time
// without holding credentials. It is the mechanism that keeps large files out of
// the service entirely: authorisation stays here, the transfer happens between
// the browser and S3.
//
// A presigned URL is also a bearer token in a query string. It will end up in
// browser history, in Referer headers, in the chat message where somebody pasted
// it, and in whatever logs sat in between — so the TTL is a security decision,
// not a convenience one.

const (
	// presignShortTTL fits a link the page uses immediately.
	presignShortTTL = 5 * time.Minute
	// presignHumanTTL fits something a person will read an email about and click.
	// An hour is the outer edge of reasonable; longer means "use a proxy
	// endpoint", because a signed link cannot be revoked at all.
	presignHumanTTL = time.Hour
)

// downloadLink signs a read of one object, downloading under a readable name
// rather than the UUID the key actually is.
//
// Signing makes no network call, so it cannot tell you the object exists:
// PresignGet on a missing key succeeds and returns a link that 404s. Call Exists
// first only when the caller has to be told now rather than later.
func downloadLink(ctx core.IContext, key, filename string) (string, core.IError) {
	return ctx.Storage().PresignGet(key, presignShortTTL,
		core.StoragePresignOptions{Attachment: filename})
}

// documentURL is the handler shape: check permission, then hand back a link.
// Re-issuing a short link costs one API call and re-checks permission every
// time, which is why short-and-reissued beats long-and-convenient.
func documentURL(c core.IHTTPContext) error {
	key := c.Param("key")
	if !strings.HasPrefix(key, "tenants/"+tenantOf(c)+"/") {
		return c.NewError(nil, errmsgs.Forbidden)
	}

	url, err := downloadLink(c, key, "document.pdf")
	if err != nil {
		return err
	}
	// the service did the authorisation; S3 does the transfer, so a 500MB
	// download costs this process one request that returns 200 bytes
	return c.JSON(http.StatusOK, map[string]any{"url": url})
}

// requestUpload is step 1 of a direct browser upload: the client asks for a
// slot, and the *service* decides the key. A client that names its own key can
// overwrite somebody else's object.
func requestUpload(c core.IHTTPContext) error {
	var body struct {
		ContentType string `json:"content_type"`
		Filename    string `json:"filename"`
	}
	if err := c.BindOnly(&body); err != nil {
		return err
	}
	switch body.ContentType {
	case "image/png", "image/jpeg", "application/pdf":
	default:
		return c.NewError(nil, errmsgs.BadRequest)
	}

	key := documentKey(tenantOf(c), "uploads", body.Filename)

	// The client MUST send the same Content-Type this link was signed with, or
	// S3 rejects the signature — and its error message says almost nothing about
	// why. It is the single most common thing to get wrong here.
	url, err := c.Storage().PresignPut(key, presignShortTTL,
		core.StoragePutOptions{ContentType: body.ContentType})
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"url": url, "key": key})
}

// confirmUpload is step 3: the client PUT the bytes straight to S3 without the
// service watching, so everything is re-checked here.
//
// A presigned PUT cannot enforce a maximum size — that is what makes Stat the
// only size limit available after the fact. If a hard limit matters before the
// bytes are written, use a POST policy or take the upload through the service.
func confirmUpload(c core.IHTTPContext) error {
	var body struct {
		Key string `json:"key"`
	}
	if err := c.BindOnly(&body); err != nil {
		return err
	}
	// ownership is re-derived from the key, because step 2 happened elsewhere
	if !strings.HasPrefix(body.Key, "tenants/"+tenantOf(c)+"/") {
		return c.NewError(nil, errmsgs.Forbidden)
	}

	info, err := c.Storage().Stat(body.Key)
	if errors.Is(err, core.ErrObjectNotFound) {
		return c.NewError(err, errmsgs.BadRequest) // nothing was actually uploaded
	}
	if err != nil {
		return err
	}
	if info.Size > maxUpload {
		_ = c.Storage().Delete(body.Key)
		return c.NewError(nil, errmsgs.BadRequest)
	}

	return c.JSON(http.StatusOK, map[string]any{"key": body.Key, "size": info.Size})
}

// emailableLink signs from a job, whose context ends when the run does. The
// handle is request-bound but the *link* carries no context, so a URL signed for
// an hour happily outlives the run that produced it.
//
// Prefer emailing a link to your own endpoint, which re-checks permission and
// signs a short URL on each click. An inbox keeps a link forever.
func emailableLink(ctx core.IContext, key, filename string) (string, core.IError) {
	return ctx.Storage().WithContext(context.Background()).
		PresignGet(key, presignHumanTTL, core.StoragePresignOptions{Attachment: filename})
}

// publicLink builds the unsigned address, from S3_PUBLIC_URL when it is set — so
// putting a CDN in front of the bucket is one config key rather than a code
// change. It says nothing about whether the object is readable: that is the
// bucket policy's decision, not this string's.
func publicLink(ctx core.IContext, key string) string {
	return ctx.Storage().PublicURL(key)
}
