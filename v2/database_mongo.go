package core

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"
)

// ErrDocumentNotFound is wrapped by the error a read returns when nothing
// matches. Compare with errors.Is(err, core.ErrDocumentNotFound).
var ErrDocumentNotFound = errors.New("mongo: document not found")

// ErrDuplicateKey is wrapped by the error a write returns when it collides with
// a unique index — the "this email is already registered" case, which is a
// conflict to answer rather than a failure to report.
var ErrDuplicateKey = errors.New("mongo: duplicate key")

// ErrMongoDisabled is wrapped by the error every operation returns when no
// DB_MONGO_* configuration is set. Like storage and unlike the cache, Mongo does
// not degrade quietly: a read that silently returns nothing, or a write that
// silently goes nowhere, is worse than a clear failure.
var ErrMongoDisabled = errors.New("mongo: not configured")

// IDocument is implemented by Mongo models, the way IModel is by SQL ones. It
// is what lets mongorepo.New[User](ctx) know which collection to read.
//
//	func (u User) CollectionName() string { return "users" }
//
// Declare it on the value, not the pointer, so New[User] rather than New[*User]
// satisfies the constraint.
type IDocument interface {
	CollectionName() string
}

// MongoFindOptions shapes a read. Every field is optional.
//
// Sort names a field per entry, "-" first for descending ("-created_at"), which
// is the same shape PageOptions.OrderBy uses.
type MongoFindOptions struct {
	Sort  []string
	Limit int64
	Skip  int64
	// Projection is passed to the driver as-is (bson.M{"password": 0}), so the
	// full projection language is available.
	Projection any
	// Hint forces an index ("email_1", or bson.D of the keys) when the planner
	// picks the wrong one.
	Hint any
	// Collation decides how strings compare — the way to get a case- or
	// accent-insensitive query instead of a regex that cannot use an index.
	Collation *options.Collation
	// MaxTime bounds the query on the *server*, so a slow one is killed there
	// rather than only abandoned here.
	MaxTime time.Duration
	// BatchSize tunes how many documents come back per round trip.
	BatchSize int32
}

// MongoAggregateOptions shapes a pipeline run.
type MongoAggregateOptions struct {
	// AllowDiskUse lets $group and $sort spill to disk past the 100MB in-memory
	// limit. It is the difference between a report that runs and one that fails
	// with "Sort exceeded memory limit" the first time the data gets big.
	AllowDiskUse bool
	// MaxTime bounds the pipeline on the server.
	MaxTime time.Duration
	// BatchSize tunes how many documents come back per round trip.
	BatchSize int32
	// Hint forces an index for the initial $match.
	Hint any
	// Let declares variables the pipeline reads with "$$name".
	Let any
	// Collation decides how strings compare and group.
	Collation *options.Collation
	// Comment shows up in the profiler and in currentOp, which is how a
	// long-running pipeline is identified in production.
	Comment string
}

// MongoUpdateOptions shapes a write.
type MongoUpdateOptions struct {
	// Upsert inserts the document when the filter matches nothing.
	Upsert bool
	// ArrayFilters targets the elements a positional "$[name]" update applies
	// to (bson.A{bson.M{"name.score": bson.M{"$lt": 50}}}).
	ArrayFilters []any
	// Hint forces an index for the filter.
	Hint any
	// Collation decides how the filter compares strings.
	Collation *options.Collation
}

// MongoFindModifyOptions shapes an atomic find-and-modify.
type MongoFindModifyOptions struct {
	// Upsert inserts when nothing matches (FindOneAndUpdate/Replace only).
	Upsert bool
	// ReturnNew decodes the document as it is *after* the change. The default
	// is before, which is Mongo's, and is what you want when the old value is
	// the thing being claimed.
	ReturnNew bool
	// Sort decides which document is modified when several match — the ordering
	// that turns this into a queue pop.
	Sort         []string
	Projection   any
	ArrayFilters []any
	Collation    *options.Collation
	MaxTime      time.Duration
}

// MongoUpdateResult is what a write actually did. Matched being zero is how a caller
// tells "no such document" from "nothing needed changing" — a distinction the
// previous IError-only signature threw away.
type MongoUpdateResult struct {
	Matched    int64
	Modified   int64
	Upserted   int64
	UpsertedID string
}

// MongoBulkResult is the tally of a BulkWrite.
type MongoBulkResult struct {
	Inserted    int64
	Matched     int64
	Modified    int64
	Deleted     int64
	Upserted    int64
	UpsertedIDs map[int64]string
}

// MongoIndex describes an index to create at startup.
type MongoIndex struct {
	// Keys names a field per entry, "-" first for descending. A compound index
	// is several entries, in order. A non-numeric direction is given as
	// "field:text" / "field:2dsphere" / "field:hashed".
	Keys   []string
	Unique bool
	Sparse bool
	// TTL expires documents this long after the (single, date-typed) key.
	TTL time.Duration
	// Name is generated from the keys when empty.
	Name string
	// Partial restricts which documents are indexed (bson.M{"deleted_at": nil}).
	Partial any
	// Collation decides how the indexed strings compare; a query only uses the
	// index when its collation matches.
	Collation *options.Collation
	// Weights tunes a text index's field importance.
	Weights any
	// Background is accepted and ignored by MongoDB 4.2+, which always builds
	// this way. It is here so an index definition ported from an older service
	// still compiles.
	Background bool
}

// IMongoDB is the Mongo abstraction (same name as v1). Methods use the bound
// context from ctx.DBMongo(), so they take no ctx.
//
// It no longer hides the driver. The goal of doing so was to keep callers off
// mongo-driver, but a caller has to write bson.M for every filter anyway — so
// the abstraction never bought isolation, it only put aggregation, transactions
// and indexes out of reach. Collection(), Database() and Client() are part of
// the interface; the helpers around them cover what most code needs, and nothing
// the driver can do is off limits.
type IMongoDB interface {
	// --- reads ---

	// FindOne decodes the first match into dest. The error wraps
	// ErrDocumentNotFound when nothing matches.
	FindOne(dest any, collection string, filter any, opts ...MongoFindOptions) IError
	// Find decodes every match into dest (a pointer to a slice). Without a Limit
	// it reads the whole result set into memory — pass one, use MongoPaginate,
	// or stream with FindCursor.
	Find(dest any, collection string, filter any, opts ...MongoFindOptions) IError
	// FindCursor returns the cursor instead of the documents, for a result set
	// too large to hold. Close it, and read it with MongoEach.
	FindCursor(collection string, filter any, opts ...MongoFindOptions) (*mongo.Cursor, IError)
	// Count counts matches. An empty filter counts the collection.
	Count(collection string, filter any) (int64, IError)
	// EstimatedCount reads the collection's metadata instead of counting, which
	// is instant on a large collection and approximate after an unclean
	// shutdown. It cannot take a filter.
	EstimatedCount(collection string) (int64, IError)
	// Exists reports whether anything matches, without transferring it.
	Exists(collection string, filter any) (bool, IError)
	// Distinct decodes the distinct values of a field into dest.
	Distinct(dest any, collection, field string, filter any) IError

	// --- aggregation ---

	// Aggregate runs a pipeline into dest. The pipeline is passed to the driver
	// as it is — []bson.M, bson.A or mongo.Pipeline — so every stage is
	// available, including $lookup, $facet, $graphLookup, $unionWith, $merge
	// and $out.
	//
	// Set AllowDiskUse for anything that groups or sorts a large collection.
	Aggregate(dest any, collection string, pipeline any, opts ...MongoAggregateOptions) IError
	// AggregateCursor returns the cursor instead of the documents, for a
	// pipeline whose output does not fit in memory. Close it.
	AggregateCursor(collection string, pipeline any, opts ...MongoAggregateOptions) (*mongo.Cursor, IError)

	// --- writes ---

	// InsertOne stores a document and returns its id as a hex string — one that
	// can be handed back to a filter. (v2.2 returned the driver's Go rendering,
	// `ObjectID("…")`, which matched nothing when it was.)
	InsertOne(collection string, document any) (string, IError)
	// InsertMany stores documents and returns their ids, in order.
	InsertMany(collection string, documents []any) ([]string, IError)
	// UpdateOne applies update (bson.M{"$set": …}) to the first match.
	UpdateOne(collection string, filter, update any, opts ...MongoUpdateOptions) (MongoUpdateResult, IError)
	// UpdateMany applies update to every match.
	UpdateMany(collection string, filter, update any, opts ...MongoUpdateOptions) (MongoUpdateResult, IError)
	// ReplaceOne swaps the whole document, keeping its id.
	ReplaceOne(collection string, filter, document any, opts ...MongoUpdateOptions) (MongoUpdateResult, IError)
	// DeleteOne removes the first match and reports how many it removed.
	DeleteOne(collection string, filter any) (int64, IError)
	// DeleteMany removes every match.
	DeleteMany(collection string, filter any) (int64, IError)
	// BulkWrite sends many writes in one round trip. Ordered stops at the first
	// failure; unordered runs them all and reports what failed.
	BulkWrite(collection string, models []mongo.WriteModel, ordered bool) (MongoBulkResult, IError)

	// --- atomic find-and-modify ---

	// FindOneAndUpdate updates the first match and decodes it into dest, in one
	// atomic step. This is how a job is claimed or a counter is read as it is
	// incremented — a Find followed by an Update is a race.
	FindOneAndUpdate(dest any, collection string, filter, update any, opts ...MongoFindModifyOptions) IError
	// FindOneAndReplace swaps the first match and decodes it into dest.
	FindOneAndReplace(dest any, collection string, filter, document any, opts ...MongoFindModifyOptions) IError
	// FindOneAndDelete removes the first match and decodes it into dest.
	FindOneAndDelete(dest any, collection string, filter any, opts ...MongoFindModifyOptions) IError

	// --- schema ---

	// EnsureIndex creates an index when it is missing. Run it at startup; it is
	// safe to call on every boot.
	EnsureIndex(collection string, index MongoIndex) IError
	// EnsureIndexes creates several in one round trip.
	EnsureIndexes(collection string, indexes ...MongoIndex) IError
	// DropIndex removes one by name.
	DropIndex(collection, name string) IError
	// ListIndexes returns the index documents of a collection.
	ListIndexes(collection string) ([]map[string]any, IError)
	// DropCollection removes a collection and everything in it.
	DropCollection(collection string) IError
	// ListCollections returns the collection names of the database.
	ListCollections() ([]string, IError)

	// --- sessions and streams ---

	// Transaction runs fn inside a session transaction, committing when it
	// returns nil and aborting when it returns an error or panics. The handle
	// passed to fn is the one to use — the outer one is not in the transaction.
	//
	// Mongo only offers transactions on a replica set or a sharded cluster; a
	// standalone server fails with a message saying so.
	Transaction(fn func(tx IMongoDB) error) IError
	// Watch opens a change stream on a collection, or on the whole database
	// when collection is "". Close it. Needs a replica set.
	Watch(collection string, pipeline any) (*mongo.ChangeStream, IError)

	// --- plumbing ---

	// Ping checks the connection — for a readiness probe.
	Ping() IError
	// Enabled reports whether Mongo is configured.
	Enabled() bool
	// Name is the database every operation runs against.
	Name() string
	// Collection is the driver handle, for anything this interface does not
	// cover. Nil when disabled. Use the bound context from Context() with it.
	Collection(name string) *mongo.Collection
	// Database is the driver's database handle (GridFS, RunCommand). Nil when
	// disabled.
	Database() *mongo.Database
	// Client is the driver's client. Nil when disabled.
	Client() *mongo.Client
	// Context is the context this handle is bound to, for driver calls made
	// through Collection.
	Context() context.Context
	// WithContext returns a handle bound to another context.
	WithContext(ctx context.Context) IMongoDB
	// WithReadPreference returns a handle whose reads may go elsewhere —
	// readpref.SecondaryPreferred() to keep a heavy report off the primary.
	// Writes always go to the primary regardless.
	WithReadPreference(pref *readpref.ReadPref) IMongoDB
	// Close disconnects. Owned by App.Shutdown.
	Close() IError
}

// MongoPaginate runs a counted, sorted, limited query and returns a Page — the
// Mongo counterpart of Paginate for SQL.
//
//	page, err := core.MongoPaginate[User](ctx.DBMongo(), "users",
//	    bson.M{"status": "active"}, c.GetPageOptions())
//
// An unfiltered page — the first screen of a list with no search applied, which
// is the most requested page there is — takes its total from the collection's
// metadata instead of counting. See mongoFilterMatchesEverything for what that
// costs.
func MongoPaginate[T any](m IMongoDB, collection string, filter any, opts *PageOptions) (*Page[T], IError) {
	if m == nil {
		return nil, mongoDisabled()
	}
	if opts == nil {
		opts = &PageOptions{}
	}
	opts.normalize()

	var (
		total int64
		err   IError
	)
	if mongoFilterMatchesEverything(filter) {
		total, err = m.EstimatedCount(collection)
	} else {
		total, err = m.Count(collection, filter)
	}
	if err != nil {
		return nil, err
	}

	items := []T{}
	if total > 0 {
		if findErr := m.Find(&items, collection, filter, MongoFindOptions{
			Sort:  opts.OrderBy,
			Limit: opts.Limit,
			Skip:  (opts.Page - 1) * opts.Limit,
		}); findErr != nil {
			return nil, findErr
		}
	}

	return &Page[T]{
		Items:   items,
		Total:   total,
		Count:   int64(len(items)),
		Page:    opts.Page,
		Limit:   opts.Limit,
		Q:       opts.Q,
		OrderBy: opts.OrderBy,
	}, nil
}

// mongoFacetPageLimit is the largest page MongoAggregatePage will collect with
// $facet. $facet returns everything it produces inside a single document, so the
// whole page has to fit under the 16MB BSON limit — a ceiling PageLimitMax no
// longer keeps a caller away from. Pages up to this size still go in one pass;
// bigger ones are fetched separately.
const mongoFacetPageLimit int64 = 100

// MongoAggregatePage runs a pipeline for the page and for the count.
//
// Use it when the list being paged is the output of a pipeline rather than of a
// filter — a join, a computed field, a grouping. Pass the pipeline *without*
// $skip/$limit; they are appended.
//
// A page of up to mongoFacetPageLimit comes back in one round trip via $facet,
// so the count cannot drift from the items. A larger page would not fit in the
// one document $facet builds, so it costs two round trips and the count is read
// from a collection that may have changed in between.
func MongoAggregatePage[T any](m IMongoDB, collection string, pipeline []map[string]any,
	opts *PageOptions, aggOpts ...MongoAggregateOptions,
) (*Page[T], IError) {
	if m == nil {
		return nil, mongoDisabled()
	}
	if opts == nil {
		opts = &PageOptions{}
	}
	opts.normalize()

	paging := []map[string]any{}
	if sort := mongoSort(opts.OrderBy); sort != nil {
		paging = append(paging, map[string]any{"$sort": sort})
	}
	paging = append(paging,
		map[string]any{"$skip": (opts.Page - 1) * opts.Limit},
		map[string]any{"$limit": opts.Limit},
	)

	var (
		items []T
		total int64
		err   IError
	)
	if opts.Limit <= mongoFacetPageLimit {
		items, total, err = mongoFacetPage[T](m, collection, pipeline, paging, aggOpts...)
	} else {
		items, total, err = mongoSplitPage[T](m, collection, pipeline, paging, aggOpts...)
	}
	if err != nil {
		return nil, err
	}
	if items == nil {
		items = []T{}
	}

	return &Page[T]{
		Items:   items,
		Total:   total,
		Count:   int64(len(items)),
		Page:    opts.Page,
		Limit:   opts.Limit,
		Q:       opts.Q,
		OrderBy: opts.OrderBy,
	}, nil
}

// mongoFacetPage reads the page and the count from one pass, so nothing can
// change between them.
func mongoFacetPage[T any](m IMongoDB, collection string, pipeline, paging []map[string]any,
	aggOpts ...MongoAggregateOptions,
) ([]T, int64, IError) {
	faceted := append(append([]map[string]any{}, pipeline...), map[string]any{
		"$facet": map[string]any{
			"items": paging,
			"total": []map[string]any{{"$count": "n"}},
		},
	})

	var result []struct {
		Items []T `bson:"items"`
		Total []struct {
			N int64 `bson:"n"`
		} `bson:"total"`
	}
	if err := m.Aggregate(&result, collection, faceted, aggOpts...); err != nil {
		return nil, 0, err
	}
	if len(result) == 0 {
		return nil, 0, nil
	}
	var total int64
	if len(result[0].Total) > 0 {
		total = result[0].Total[0].N
	}
	return result[0].Items, total, nil
}

// mongoSplitPage reads the page and the count in two round trips, for a page too
// large to come back inside the single document $facet builds.
func mongoSplitPage[T any](m IMongoDB, collection string, pipeline, paging []map[string]any,
	aggOpts ...MongoAggregateOptions,
) ([]T, int64, IError) {
	counting := append(append([]map[string]any{}, pipeline...),
		map[string]any{"$count": "n"})

	var counted []struct {
		N int64 `bson:"n"`
	}
	if err := m.Aggregate(&counted, collection, counting, aggOpts...); err != nil {
		return nil, 0, err
	}
	var total int64
	if len(counted) > 0 {
		total = counted[0].N
	}
	if total == 0 {
		return nil, 0, nil
	}

	var items []T
	if err := m.Aggregate(&items, collection,
		append(append([]map[string]any{}, pipeline...), paging...), aggOpts...); err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// MongoEach decodes a cursor one document at a time, so a result set larger than
// memory can be processed. It closes the cursor.
//
//	cur, err := m.AggregateCursor("events", pipeline, core.MongoAggregateOptions{AllowDiskUse: true})
//	if err != nil { return err }
//	err = core.MongoEach(ctx, cur, func(row Row) error { return write(row) })
//
// Returning an error from fn stops the iteration and returns it.
func MongoEach[T any](ctx context.Context, cur *mongo.Cursor, fn func(T) error) IError {
	if cur == nil {
		return New(500, "MONGO_CURSOR", "mongo: nil cursor")
	}
	defer func() { _ = cur.Close(ctx) }()

	for cur.Next(ctx) {
		var doc T
		if err := cur.Decode(&doc); err != nil {
			return Wrap(err, "mongo: decode")
		}
		if err := fn(doc); err != nil {
			return Wrap(err, "mongo: each")
		}
	}
	if err := cur.Err(); err != nil {
		return Wrap(err, "mongo: cursor")
	}
	return nil
}

// ---------------------------------------------------------------------------
// Errors
// ---------------------------------------------------------------------------

// documentNotFound keeps v1's NOT_FOUND code — a handler that returns it says
// the right thing — and adds the sentinel so errors.Is can tell it apart.
func documentNotFound(collection string) *Error {
	return &Error{
		Status:  404,
		Code:    "NOT_FOUND",
		Message: "not found",
		cause:   fmt.Errorf("%w: %s", ErrDocumentNotFound, collection),
	}
}

func duplicateKey(collection string, err error) *Error {
	return &Error{
		Status:  409,
		Code:    "DUPLICATE_KEY",
		Message: "already exists",
		cause:   fmt.Errorf("%w: %s: %w", ErrDuplicateKey, collection, err),
	}
}

func mongoDisabled() *Error {
	return &Error{
		Status:  503,
		Code:    "MONGO_DISABLED",
		Message: "mongo: no database is configured (set DB_MONGO_NAME and DB_MONGO_HOST or DB_MONGO_CONNECTION_STRING)",
		cause:   ErrMongoDisabled,
	}
}

// ---------------------------------------------------------------------------
// Disabled Mongo
// ---------------------------------------------------------------------------

// noopMongo is what ctx.DBMongo() returns when Mongo is not configured. Every
// call fails with the same error, which names the missing configuration — a
// clear failure rather than a nil dereference.
type noopMongo struct{}

var _ IMongoDB = noopMongo{}

// NewNoopMongoDB returns a Mongo handle that refuses every operation.
func NewNoopMongoDB() IMongoDB { return noopMongo{} }

func (noopMongo) FindOne(any, string, any, ...MongoFindOptions) IError { return mongoDisabled() }
func (noopMongo) Find(any, string, any, ...MongoFindOptions) IError    { return mongoDisabled() }

func (noopMongo) FindCursor(string, any, ...MongoFindOptions) (*mongo.Cursor, IError) {
	return nil, mongoDisabled()
}

func (noopMongo) Count(string, any) (int64, IError)        { return 0, mongoDisabled() }
func (noopMongo) EstimatedCount(string) (int64, IError)    { return 0, mongoDisabled() }
func (noopMongo) Exists(string, any) (bool, IError)        { return false, mongoDisabled() }
func (noopMongo) Distinct(any, string, string, any) IError { return mongoDisabled() }

func (noopMongo) Aggregate(any, string, any, ...MongoAggregateOptions) IError { return mongoDisabled() }

func (noopMongo) AggregateCursor(string, any, ...MongoAggregateOptions) (*mongo.Cursor, IError) {
	return nil, mongoDisabled()
}

func (noopMongo) InsertOne(string, any) (string, IError)      { return "", mongoDisabled() }
func (noopMongo) InsertMany(string, []any) ([]string, IError) { return nil, mongoDisabled() }

func (noopMongo) UpdateOne(string, any, any, ...MongoUpdateOptions) (MongoUpdateResult, IError) {
	return MongoUpdateResult{}, mongoDisabled()
}

func (noopMongo) UpdateMany(string, any, any, ...MongoUpdateOptions) (MongoUpdateResult, IError) {
	return MongoUpdateResult{}, mongoDisabled()
}

func (noopMongo) ReplaceOne(string, any, any, ...MongoUpdateOptions) (MongoUpdateResult, IError) {
	return MongoUpdateResult{}, mongoDisabled()
}

func (noopMongo) DeleteOne(string, any) (int64, IError)  { return 0, mongoDisabled() }
func (noopMongo) DeleteMany(string, any) (int64, IError) { return 0, mongoDisabled() }

func (noopMongo) BulkWrite(string, []mongo.WriteModel, bool) (MongoBulkResult, IError) {
	return MongoBulkResult{}, mongoDisabled()
}

func (noopMongo) FindOneAndUpdate(any, string, any, any, ...MongoFindModifyOptions) IError {
	return mongoDisabled()
}

func (noopMongo) FindOneAndReplace(any, string, any, any, ...MongoFindModifyOptions) IError {
	return mongoDisabled()
}

func (noopMongo) FindOneAndDelete(any, string, any, ...MongoFindModifyOptions) IError {
	return mongoDisabled()
}

func (noopMongo) EnsureIndex(string, MongoIndex) IError      { return mongoDisabled() }
func (noopMongo) EnsureIndexes(string, ...MongoIndex) IError { return mongoDisabled() }
func (noopMongo) DropIndex(string, string) IError            { return mongoDisabled() }
func (noopMongo) ListIndexes(string) ([]map[string]any, IError) {
	return nil, mongoDisabled()
}
func (noopMongo) DropCollection(string) IError        { return mongoDisabled() }
func (noopMongo) ListCollections() ([]string, IError) { return nil, mongoDisabled() }

func (noopMongo) Transaction(func(IMongoDB) error) IError { return mongoDisabled() }

func (noopMongo) Watch(string, any) (*mongo.ChangeStream, IError) { return nil, mongoDisabled() }

func (noopMongo) Ping() IError                         { return mongoDisabled() }
func (noopMongo) Enabled() bool                        { return false }
func (noopMongo) Name() string                         { return "" }
func (noopMongo) Collection(string) *mongo.Collection  { return nil }
func (noopMongo) Database() *mongo.Database            { return nil }
func (noopMongo) Client() *mongo.Client                { return nil }
func (noopMongo) Context() context.Context             { return context.Background() }
func (noopMongo) WithContext(context.Context) IMongoDB { return noopMongo{} }
func (noopMongo) Close() IError                        { return nil }

func (noopMongo) WithReadPreference(*readpref.ReadPref) IMongoDB { return noopMongo{} }
