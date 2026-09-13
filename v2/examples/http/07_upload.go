package main

import (
	"errors"
	"io"
	"net/http"
	"time"

	core "github.com/pskclub/mine-core/v2"
	"github.com/pskclub/mine-core/v2/errmsgs"
	"github.com/pskclub/mine-core/v2/utils"
	"github.com/pskclub/mine-core/v2/valid"
)

// --- Example 7: files in, files out -----------------------------------------
//
// Multipart parts are streams, which is why files are not bound into the
// request struct: a struct field would mean reading the whole upload into
// memory before the handler could decide it did not want it.

const (
	maxUploadBytes = 10 << 20
	uploadPrefix   = "uploads/"
)

// uploadRequest is the text half of the form. Same tags, same Valid, same
// BindWithValidate as a JSON endpoint — only c.FormFile is extra.
type uploadRequest struct {
	Title *string `form:"title"`
	Kind  *string `form:"kind"`
}

func (r *uploadRequest) Valid(ctx core.IContext) core.IError {
	v := valid.New(ctx)
	v.Str("title", r.Title).Trim().Required().Length(1, 120)
	v.Str("kind", r.Kind).Required().In("id_card", "passport")

	return v.Error()
}

func mountFiles(e *core.Server) {
	// The route's own limit, because the server's is sized for JSON. It has to
	// clear maxUploadBytes with room to spare: the body is the file *plus* the
	// multipart envelope and the text fields, and the limit counts all of it.
	files := e.Group("/files", core.BodyLimit(maxUploadBytes+(1<<20)))
	files.POST("", uploadDocument)
	files.GET("/:id", downloadDocument)
}

func uploadDocument(c core.IHTTPContext) error {
	req := &uploadRequest{}
	if err := c.BindWithValidate(req); err != nil {
		return err
	}

	store := c.Storage()
	// Storage fails loudly rather than degrading — a dropped upload is a file
	// the caller believes it saved and nobody can get back. Checking first turns
	// a per-request 500 into an answer that says which deployment is at fault.
	if !store.Enabled() {
		return core.New(http.StatusServiceUnavailable, "STORAGE_UNAVAILABLE",
			"file storage is not configured on this deployment")
	}

	fh, fErr := c.FormFile("file")
	if fErr != nil {
		// A missing attachment is the client's mistake, not a 500.
		return errmsgs.BadRequest.WithMessage("the file part is required")
	}

	// The declared size rejects an oversized upload without opening anything.
	// It is not the guard — a client writes this header — but it is free, and
	// core.BodyLimit on the group is what actually stops the bytes.
	if fh.Size > maxUploadBytes {
		return core.Newf(http.StatusRequestEntityTooLarge, "FILE_TOO_LARGE",
			"the file must not exceed %d bytes", maxUploadBytes)
	}

	src, oErr := fh.Open()
	if oErr != nil {
		return c.NewError(oErr, errmsgs.InternalServerError)
	}
	defer src.Close()

	// fh.Filename and the part's Content-Type are whatever the client typed.
	// Neither is evidence: the name may be "../../etc/passwd" and the type may
	// say image/png over a script. Sniff the real type from the first bytes,
	// and build the key from an id we generated so the name never becomes path.
	head := make([]byte, 512)
	n, _ := src.Read(head)
	mime := http.DetectContentType(head[:n])

	ext, ok := extensionFor(mime)
	if !ok {
		return core.Newf(http.StatusUnsupportedMediaType, "UNSUPPORTED_FILE_TYPE",
			"%s files are not accepted", mime)
	}

	// multipart.File is a Seeker, so the sniffed bytes are read again rather
	// than buffered and re-joined.
	if _, sErr := src.Seek(0, io.SeekStart); sErr != nil {
		return c.NewError(sErr, errmsgs.InternalServerError)
	}

	key := uploadPrefix + utils.NewUUID() + ext
	// Put streams and uploads in parts, so a 200 MB file is not 200 MB of this
	// process. The handle is bound to the request: a client that hangs up
	// cancels the upload it started instead of paying for all of it.
	if err := store.Put(key, src, core.StoragePutOptions{
		ContentType: mime,
		Metadata:    map[string]string{"kind": deref(req.Kind), "title": deref(req.Title)},
	}); err != nil {
		return c.NewError(err, errmsgs.InternalServerError)
	}

	return c.JSON(http.StatusCreated, map[string]any{
		"id":           key[len(uploadPrefix):],
		"size":         fh.Size,
		"content_type": mime,
	})
}

func downloadDocument(c core.IHTTPContext) error {
	store := c.Storage()
	if !store.Enabled() {
		return core.New(http.StatusServiceUnavailable, "STORAGE_UNAVAILABLE",
			"file storage is not configured on this deployment")
	}

	key := uploadPrefix + c.Param("id")

	info, sErr := store.Stat(key)
	if sErr != nil {
		if errors.Is(sErr, core.ErrObjectNotFound) {
			return errmsgs.NotFoundCustomError("file")
		}

		return c.NewError(sErr, errmsgs.InternalServerError)
	}

	// A presigned link is the better default once the files are large or many:
	// the bytes travel from the bucket to the client and never occupy a worker
	// of this process. It needs a client that can reach the bucket directly,
	// which is exactly what the streaming branch below is for when it cannot.
	if c.QueryParam("link") != "" {
		url, pErr := store.PresignGet(key, 5*time.Minute,
			core.StoragePresignOptions{Attachment: c.Param("id")})
		if pErr != nil {
			return c.NewError(pErr, errmsgs.InternalServerError)
		}

		return c.Redirect(http.StatusFound, url)
	}

	body, gErr := store.Get(key)
	if gErr != nil {
		return c.NewError(gErr, errmsgs.InternalServerError)
	}
	defer body.Close()

	// Stream, never GetBytes: an object read into memory is its whole size per
	// concurrent download, and the one request that ends the process is the one
	// nobody sized for.
	return c.Stream(http.StatusOK, info.ContentType, body)
}

// extensionFor is the allow-list, keyed by the sniffed type rather than the
// claimed one. A switch and not a package-level map: the set is fixed at
// compile time and nothing should be able to add to it at runtime.
func extensionFor(mime string) (string, bool) {
	switch mime {
	case "image/jpeg":
		return ".jpg", true
	case "image/png":
		return ".png", true
	case "application/pdf":
		return ".pdf", true
	default:
		return "", false
	}
}
