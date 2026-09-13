// Command storage is a runnable tour of core.IStorage: uploading, reading,
// listing, temporary links, and the arrangement that keeps a bucket and a
// database agreeing with each other.
//
// Each example lives in its own file:
//
//	01_upload.go    structured keys, content types, streaming a body in
//	02_download.go  reading, listing, deleting, and the key that is not there
//	03_presign.go   signed GET, signed PUT, direct browser upload, public URLs
//	04_testing.go   the memory backend, index-vs-bytes, sweeping orphans
//
// Run it with: go run ./examples/storage
//
// It needs no bucket. With no S3_* configuration it runs on the in-process
// memory backend, which behaves like a real store for keys, content types,
// metadata, prefixes, listing and Stat — everything except signatures,
// multipart and bucket policy. To point it at a real one:
//
//	docker run -p 9000:9000 minio/minio server /data
//
//	APP_S3_ENDPOINT=127.0.0.1:9000 APP_S3_BUCKET=coretest \
//	  APP_S3_ACCESS_KEY=minioadmin APP_S3_SECRET_KEY=minioadmin \
//	  APP_S3_FORCE_PATH_STYLE=true go run ./examples/storage
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

	app, err := core.NewApp(env, core.WithStorage(exampleStore(env)))
	if err != nil {
		panic(err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = app.Shutdown(shutdownCtx)
	}()

	ctx := app.NewContext(context.Background())
	log := ctx.Log()
	log.Info("storage example", "bucket", ctx.Storage().Bucket(), "enabled", ctx.Storage().Enabled())

	// 1. upload: the service picks the key, the service decides the type
	key, err := uploadReceipt(ctx, "t-42", []byte("%PDF-1.4 pretend receipt"))
	if err != nil {
		log.Error("upload failed", "err", err)
		return
	}
	if info, sErr := ctx.Storage().Stat(key); sErr == nil {
		log.Info("stored", "key", key, "size", info.Size, "type", info.ContentType)
	}

	// 2. an absent key is an ordinary not-found, not a server error — the branch
	//    a handler turns into a 404
	if _, gErr := readSmallObject(ctx, "tenants/t-42/receipts/never-written.pdf"); gErr != nil {
		log.Info("absent key reported as expected", "err", gErr)
	}

	objects, err := listTenantObjects(ctx, "t-42", "receipts/")
	if err != nil {
		log.Error("list failed", "err", err)
		return
	}
	log.Info("listed", "objects", len(objects))

	// 3. a link the caller can use without ever holding credentials. On the
	//    memory backend the URL is a memory:// placeholder: there is no signer
	//    and no server, which is the honest outcome rather than a fake that works
	link, err := downloadLink(ctx, key, "receipt.pdf")
	if err != nil {
		log.Error("presign failed", "err", err)
		return
	}
	log.Info("signed a download link", "ttl", presignShortTTL.String(), "url", link)

	// 4. cleanup. DeleteByPrefix lists and then deletes, so it costs one call per
	//    page plus one per batch of 1000 — right for a user action, wrong for a
	//    routine sweep (that belongs in a bucket lifecycle rule)
	n, err := ctx.Storage().DeleteByPrefix("tenants/t-42/")
	if err != nil {
		log.Error("cleanup failed", "err", err)
		return
	}
	log.Info("cleaned up", "deleted", n)
}

// exampleStore picks a backend without failing, because this tour has to run on
// a laptop with nothing installed.
//
// A real service does the opposite: it builds the S3 client at boot and refuses
// to start when the configuration is wrong. Silently falling back to memory
// there would mean uploads that "succeed" and vanish on the next deploy —
// exactly the quiet failure the disabled backend exists to prevent.
func exampleStore(env core.IENV) core.IStorage {
	if env.Config().S3Bucket == "" {
		return core.NewMemoryStorage()
	}
	store, err := core.NewStorage(env)
	if err != nil {
		return core.NewMemoryStorage()
	}
	return store
}
