package main

import (
	"time"

	"gorm.io/gorm"

	core "github.com/pskclub/mine-core/v2"
	"github.com/pskclub/mine-core/v2/repository"
)

// --- Example 4: the index, the bytes, and testing both ----------------------
//
// The arrangement that stays healthy at every size: the database row is the
// truth that a file exists, the bucket holds the bytes, and the key is the join.
// Listing a user's documents is then a WHERE, not a bucket scan — S3 lists keys
// in lexicographic order and can do nothing else, so a service that treats List
// as a query gets slower every month whether or not anybody changed it.

// Document is the index. Everything queryable lives in the row; the object holds
// only bytes.
type Document struct {
	ID        string     `gorm:"column:id;primaryKey"`
	TenantID  string     `gorm:"column:tenant_id"`
	Key       string     `gorm:"column:s3_key"`   // the join
	Filename  string     `gorm:"column:filename"` // what the user called it
	Size      int64      `gorm:"column:size"`
	Type      string     `gorm:"column:content_type"`
	CreatedAt *time.Time `gorm:"column:created_at"`
}

func (Document) TableName() string { return "documents" }

// storeDocument writes the object first and the row second, which is the only
// ordering that fails safely. The two ways this can go wrong are not equal:
//
//	object with no row  →  a few wasted bytes, swept by a lifecycle rule
//	row with no object  →  a broken page, and no way to repair it
//
// An upload cannot be rolled back, so the transaction must not contain one:
// holding a lock open across a network call to S3 is how a slow bucket becomes a
// database outage.
func storeDocument(ctx core.IContext, doc *Document, data []byte) core.IError {
	if err := ctx.Storage().PutBytes(doc.Key, data, core.StoragePutOptions{
		ContentType: doc.Type,
		Attachment:  doc.Filename,
	}); err != nil {
		return err
	}

	repo := repository.New[Document](ctx)
	if err := repo.Transaction(func(tx *gorm.DB) error {
		return repository.NewWithDB[Document](ctx, tx).Create(doc)
	}); err != nil {
		// best effort: the object is already there, and the lifecycle rule on
		// tmp/ is the backstop if this delete also fails
		_ = ctx.Storage().Delete(doc.Key)
		return err
	}
	return nil
}

// sweepOrphans deletes objects under tmp/ that no row refers to — the files that
// uploaded successfully just before a transaction rolled back.
//
// Note what this job is *not*: routine expiry. Deleting everything older than N
// days belongs in a bucket lifecycle rule, which runs whether or not the service
// is deployed. This job exists for the orphans a lifecycle rule cannot recognise,
// because only the database knows which keys are still referenced.
func sweepOrphans(c core.ICronjobContext) error {
	objects, err := c.Storage().List("tmp/", core.StorageListOptions{Limit: 1000})
	if err != nil {
		return err
	}

	cutoff := time.Now().Add(-24 * time.Hour)
	candidates := make([]string, 0, len(objects))
	for _, obj := range objects {
		// a young object may belong to an upload still in flight
		if obj.LastModified.Before(cutoff) {
			candidates = append(candidates, obj.Key)
		}
	}
	if len(candidates) == 0 {
		return nil
	}

	// one query for the whole page, not one per key: N round trips to a database
	// is what turns a cleanup job into a nightly incident
	referenced, err := repository.New[Document](c).
		Where("s3_key IN ?", candidates).
		FindAll()
	if err != nil {
		return err
	}
	keep := make(map[string]struct{}, len(referenced))
	for _, doc := range referenced {
		keep[doc.Key] = struct{}{}
	}

	orphans := make([]string, 0, len(candidates))
	for _, key := range candidates {
		if _, ok := keep[key]; !ok {
			orphans = append(orphans, key)
		}
	}
	if len(orphans) == 0 {
		return nil
	}

	// Delete batches into calls of 1000 and does not mind a key that has already
	// gone, so re-running this job is free
	if err := c.Storage().Delete(orphans...); err != nil {
		return err
	}
	c.Log().Info("swept orphaned objects", "count", len(orphans))
	return nil
}

func registerStorageJobs(reg *core.JobRegistry) {
	_ = reg.Register(core.JobDef{
		Name:        "storage.sweep-orphans",
		Description: "delete tmp/ objects no row refers to",
		Schedule:    core.Cron("15 4 * * *"),
		Timeout:     10 * time.Minute,
	}, sweepOrphans)
}

// newTestApp is the whole storage fixture: the memory backend is a real store
// in-process, so keys, content types, metadata, prefixes, listing and Stat all
// behave. Most storage tests need nothing else.
//
//	func TestUploadStoresAndRecords(t *testing.T) {
//	    store := core.NewMemoryStorage()
//	    app := newTestApp(env, store)
//
//	    // ... exercise the upload path ...
//
//	    info, err := store.Stat(key)
//	    require.NoError(t, err)
//	    // the type was decided by us, not taken from the client's header
//	    require.Equal(t, "application/pdf", info.ContentType)
//	    // and the key was generated, not built from the filename
//	    require.NotContains(t, key, "cv.pdf")
//	}
//
// What it cannot prove: presigned URLs (there is no signer and no server),
// PublicURL's shape (built from configuration memory does not have), multipart
// uploads, bucket policies and ACLs, and S3() — which returns nil. Those need
// MinIO and the integration build tag.
//
// Worth its own test: storage failing loudly is a feature, so assert it.
//
//	app := newTestApp(env, core.NewNoopStorage())
//	err := app.NewContext(context.Background()).Storage().Put("k", r)
//	require.ErrorIs(t, err, core.ErrStorageDisabled)
func newTestApp(env core.IENV, store core.IStorage) *core.App {
	app, err := core.NewApp(env, core.WithStorage(store))
	if err != nil {
		panic(err)
	}
	return app
}
