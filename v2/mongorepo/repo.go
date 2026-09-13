// Package mongorepo provides a generic, Mongo-backed repository — the
// counterpart of the GORM-backed repository package, with the same shape: the
// context is bound once at New, query methods take no ctx, and the fluent chain
// is copy-on-write so a base query can be branched safely.
//
// It is a layer over core.IMongoDB, not a replacement for it. Everything the
// repository does can still be done by hand through ctx.DBMongo(), and anything
// the repository does not wrap is reachable through Collection(), which returns
// the driver handle scoped to this document's collection.
//
//	type User struct {
//	    ID     string `bson:"_id,omitempty" json:"id"`
//	    Email  string `bson:"email"         json:"email"`
//	    Status string `bson:"status"        json:"status"`
//	}
//
//	func (User) CollectionName() string { return "users" }
//
//	users := mongorepo.New[User](ctx)
//	user, err := users.Eq("email", email).FindOne()
package mongorepo

import (
	"errors"
	"reflect"
	"strings"

	core "github.com/pskclub/mine-core/v2"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

// Reader is the read-only subset of a repository (for consumer-side mocking).
type Reader[D core.IDocument] interface {
	FindOne(filter ...bson.M) (*D, core.IError)
	FindAll(filter ...bson.M) ([]D, core.IError)
	Count() (int64, core.IError)
	Exists() (bool, core.IError)
	Pagination(opts *core.PageOptions) (*core.Page[D], core.IError)
}

// Writer is the write subset of a repository (for consumer-side mocking).
type Writer[D core.IDocument] interface {
	Create(d *D) core.IError
	Updates(values bson.M) (core.MongoUpdateResult, core.IError)
	Delete() (int64, core.IError)
}

// Repo is the generic Mongo repository.
type Repo[D core.IDocument] struct {
	db         core.IMongoDB
	collection string
	filter     bson.M
	find       core.MongoFindOptions
}

// New builds a repository on the context's default Mongo connection. The
// collection comes from the document's CollectionName.
func New[D core.IDocument](ctx core.IContext) *Repo[D] {
	return NewWithDB[D](ctx, ctx.DBMongo())
}

// NewWithDB binds a specific handle — a named connection, or the transaction
// handle inside core.IMongoDB.Transaction.
func NewWithDB[D core.IDocument](ctx core.IContext, db core.IMongoDB) *Repo[D] {
	if db == nil {
		db = ctx.DBMongo()
	}
	var zero D
	return &Repo[D]{db: db, collection: zero.CollectionName(), filter: bson.M{}}
}

// NewIn builds a repository on a handle that is already bound — inside a
// transaction, where there is no IContext to take the connection from.
//
//	ctx.DBMongo().Transaction(func(tx core.IMongoDB) error {
//	    users := mongorepo.NewIn[User](tx)
//	    ...
//	})
func NewIn[D core.IDocument](db core.IMongoDB) *Repo[D] {
	var zero D
	return &Repo[D]{db: db, collection: zero.CollectionName(), filter: bson.M{}}
}

// clone copies the receiver so a chain never mutates the repo it branched from.
func (r *Repo[D]) clone() *Repo[D] {
	cp := *r
	cp.filter = cloneFilter(r.filter)
	cp.find = r.find
	if r.find.Sort != nil {
		cp.find.Sort = append([]string(nil), r.find.Sort...)
	}
	return &cp
}

// ---------------------------------------------------------------------------
// Escape hatches
// ---------------------------------------------------------------------------

// DB returns the handle this repository reads and writes through.
func (r *Repo[D]) DB() core.IMongoDB { return r.db }

// Collection returns the driver handle for this document's collection — the
// escape hatch for anything the repository does not wrap. Use it with
// DB().Context(), which is the transaction's context inside one.
func (r *Repo[D]) Collection() *mongo.Collection { return r.db.Collection(r.collection) }

// CollectionName is the collection this repository reads.
func (r *Repo[D]) CollectionName() string { return r.collection }

// Filter is the query built so far, for logging or for handing to IMongoDB
// directly.
func (r *Repo[D]) Filter() bson.M { return cloneFilter(r.filter) }

// FindOptions are the sort, limit, skip and projection built so far.
func (r *Repo[D]) FindOptions() core.MongoFindOptions { return r.find }

// WithContext rebinds the underlying handle to another context (escape hatch
// for work that must outlive the request).
func (r *Repo[D]) WithContext(ctx core.IContext) *Repo[D] {
	cp := r.clone()
	cp.db = r.db.WithContext(ctx)
	return cp
}

// ---------------------------------------------------------------------------
// Chainable (copy-on-write; never mutates the receiver)
// ---------------------------------------------------------------------------

// Where ANDs a filter onto the query. Several calls compose, and a key used
// twice becomes an $and rather than silently replacing the first condition.
func (r *Repo[D]) Where(filter bson.M) *Repo[D] {
	cp := r.clone()
	cp.filter = andFilter(cp.filter, filter)
	return cp
}

// Eq matches a field exactly — the condition almost every query starts with.
func (r *Repo[D]) Eq(field string, value any) *Repo[D] {
	return r.Where(bson.M{field: value})
}

// Ne matches everything except a value.
func (r *Repo[D]) Ne(field string, value any) *Repo[D] {
	return r.Where(bson.M{field: bson.M{"$ne": value}})
}

// In matches any of the values. Passing none matches nothing, which is what the
// caller asked for — it is not silently dropped.
func (r *Repo[D]) In(field string, values ...any) *Repo[D] {
	return r.Where(bson.M{field: bson.M{"$in": values}})
}

// NotIn matches none of the values.
func (r *Repo[D]) NotIn(field string, values ...any) *Repo[D] {
	return r.Where(bson.M{field: bson.M{"$nin": values}})
}

func (r *Repo[D]) Gt(field string, value any) *Repo[D] {
	return r.Where(bson.M{field: bson.M{"$gt": value}})
}

func (r *Repo[D]) Gte(field string, value any) *Repo[D] {
	return r.Where(bson.M{field: bson.M{"$gte": value}})
}

func (r *Repo[D]) Lt(field string, value any) *Repo[D] {
	return r.Where(bson.M{field: bson.M{"$lt": value}})
}

func (r *Repo[D]) Lte(field string, value any) *Repo[D] {
	return r.Where(bson.M{field: bson.M{"$lte": value}})
}

// Between matches a closed range, which is what a date filter almost always
// wants and what two separate calls get subtly wrong on the same field.
func (r *Repo[D]) Between(field string, from, to any) *Repo[D] {
	return r.Where(bson.M{field: bson.M{"$gte": from, "$lte": to}})
}

// HasField matches documents where the field is present (or absent).
func (r *Repo[D]) HasField(field string, present bool) *Repo[D] {
	return r.Where(bson.M{field: bson.M{"$exists": present}})
}

// ElemMatch matches documents where *one* element of an array field satisfies
// every condition at once.
//
// It is the difference a nested query usually turns on: a plain dotted filter
// on two fields of the same array is satisfied by two *different* elements —
//
//	Eq("items.sku", "A").Eq("items.qty", 2)   // element 1 is A, element 2 has qty 2
//	ElemMatch("items", bson.M{"sku": "A", "qty": 2})   // one element is both
func (r *Repo[D]) ElemMatch(field string, conditions bson.M) *Repo[D] {
	return r.Where(bson.M{field: bson.M{"$elemMatch": conditions}})
}

// HasSize matches an array field by its length.
func (r *Repo[D]) HasSize(field string, length int) *Repo[D] {
	return r.Where(bson.M{field: bson.M{"$size": length}})
}

// HasAll matches arrays containing every one of the values.
func (r *Repo[D]) HasAll(field string, values ...any) *Repo[D] {
	return r.Where(bson.M{field: bson.M{"$all": values}})
}

// Regex matches a pattern. Pass options "i" for case-insensitive — though a
// prefix-anchored pattern is the only kind an index can help with.
func (r *Repo[D]) Regex(field, pattern string, options ...string) *Repo[D] {
	opt := ""
	if len(options) > 0 {
		opt = options[0]
	}
	return r.Where(bson.M{field: bson.Regex{Pattern: pattern, Options: opt}})
}

// Search matches a case-insensitive substring — the "q" of a list endpoint.
// Several fields are ORed together.
func (r *Repo[D]) Search(term string, fields ...string) *Repo[D] {
	if term == "" || len(fields) == 0 {
		return r.clone()
	}
	quoted := bson.Regex{Pattern: regexpQuote(term), Options: "i"}
	ors := make([]bson.M, 0, len(fields))
	for _, field := range fields {
		ors = append(ors, bson.M{field: quoted})
	}
	return r.Or(ors...)
}

// Or ANDs a group of alternatives onto the query: (a OR b) AND whatever came
// before, which is what a caller reading the chain expects.
func (r *Repo[D]) Or(filters ...bson.M) *Repo[D] {
	if len(filters) == 0 {
		return r.clone()
	}
	if len(filters) == 1 {
		return r.Where(filters[0])
	}
	return r.Where(bson.M{"$or": filters})
}

// Not negates a filter.
func (r *Repo[D]) Not(filter bson.M) *Repo[D] {
	return r.Where(bson.M{"$nor": []bson.M{filter}})
}

// ByID filters on _id, parsing a hex string into an ObjectID when it is one.
func (r *Repo[D]) ByID(id string) *Repo[D] {
	return r.Where(core.MongoByID(id))
}

// Sort orders the result: "-created_at" is descending, and several entries make
// a compound sort, in order.
func (r *Repo[D]) Sort(fields ...string) *Repo[D] {
	cp := r.clone()
	cp.find.Sort = append(cp.find.Sort, fields...)
	return cp
}

// Limit caps how many documents come back.
func (r *Repo[D]) Limit(n int64) *Repo[D] {
	cp := r.clone()
	cp.find.Limit = n
	return cp
}

// Skip drops the first n documents.
func (r *Repo[D]) Skip(n int64) *Repo[D] {
	cp := r.clone()
	cp.find.Skip = n
	return cp
}

// Select returns only these fields (_id always comes back unless omitted).
func (r *Repo[D]) Select(fields ...string) *Repo[D] {
	cp := r.clone()
	projection := bson.M{}
	for _, field := range fields {
		projection[field] = 1
	}
	cp.find.Projection = projection
	return cp
}

// Omit returns everything but these fields — how a password hash stays out of a
// list response.
func (r *Repo[D]) Omit(fields ...string) *Repo[D] {
	cp := r.clone()
	projection := bson.M{}
	for _, field := range fields {
		projection[field] = 0
	}
	cp.find.Projection = projection
	return cp
}

// Hint forces an index when the planner picks the wrong one.
func (r *Repo[D]) Hint(hint any) *Repo[D] {
	cp := r.clone()
	cp.find.Hint = hint
	return cp
}

// Options replaces the accumulated find options wholesale, for the fields the
// chain does not cover (Collation, MaxTime, BatchSize).
func (r *Repo[D]) Options(opts core.MongoFindOptions) *Repo[D] {
	cp := r.clone()
	cp.find = opts
	return cp
}

// ---------------------------------------------------------------------------
// Read finishers
// ---------------------------------------------------------------------------

// FindOne returns the first match, or a NOT_FOUND error wrapping
// core.ErrDocumentNotFound. An extra filter is ANDed onto the chain.
func (r *Repo[D]) FindOne(filter ...bson.M) (*D, core.IError) {
	scope := r.withExtra(filter)
	d := new(D)
	if err := r.db.FindOne(d, r.collection, scope.filter, scope.find); err != nil {
		return nil, err
	}
	return d, nil
}

// First returns the oldest match — the lowest _id — regardless of the chain's
// sort, mirroring the SQL repository's First.
func (r *Repo[D]) First(filter ...bson.M) (*D, core.IError) {
	return r.withExtra(filter).firstBy("_id")
}

// Last returns the newest match — the highest _id.
func (r *Repo[D]) Last(filter ...bson.M) (*D, core.IError) {
	return r.withExtra(filter).firstBy("-_id")
}

// firstBy reads one document in a fixed order, replacing whatever the chain
// asked for: First means first by id, whatever else was being sorted on.
func (r *Repo[D]) firstBy(sort string) (*D, core.IError) {
	scope := r.clone()
	scope.find.Sort = []string{sort}
	d := new(D)
	if err := r.db.FindOne(d, r.collection, scope.filter, scope.find); err != nil {
		return nil, err
	}
	return d, nil
}

// FindAll returns every match. Without a Limit that is the whole result set —
// use Pagination or Each for anything unbounded.
func (r *Repo[D]) FindAll(filter ...bson.M) ([]D, core.IError) {
	scope := r.withExtra(filter)
	list := make([]D, 0)
	if err := r.db.Find(&list, r.collection, scope.filter, scope.find); err != nil {
		return nil, err
	}
	return list, nil
}

// Each streams the matches one at a time, so a result set larger than memory can
// be processed. Returning an error from fn stops the iteration.
func (r *Repo[D]) Each(fn func(D) error) core.IError {
	cur, err := r.db.FindCursor(r.collection, r.filter, r.find)
	if err != nil {
		return err
	}
	return core.MongoEach(r.db.Context(), cur, fn)
}

// Count returns how many documents match.
func (r *Repo[D]) Count() (int64, core.IError) {
	return r.db.Count(r.collection, r.filter)
}

// Exists reports whether anything matches.
func (r *Repo[D]) Exists() (bool, core.IError) {
	return r.db.Exists(r.collection, r.filter)
}

// Distinct reads the distinct values of a field into dest (a pointer to a slice).
func (r *Repo[D]) Distinct(field string, dest any) core.IError {
	return r.db.Distinct(dest, r.collection, field, r.filter)
}

// Pluck reads one field of every match into dest (a pointer to a slice) —
// gathering ids to hand to another query, without decoding whole documents.
//
// The field may be a dotted path. A path that crosses an array yields one value
// per element, so plucking "items.sku" from ten orders of three items each
// gives thirty skus.
func (r *Repo[D]) Pluck(field string, dest any) core.IError {
	var rows []bson.M
	scope := r.clone()
	scope.find.Projection = bson.M{field: 1}
	if err := r.db.Find(&rows, r.collection, scope.filter, scope.find); err != nil {
		return err
	}

	values := make([]any, 0, len(rows))
	for _, row := range rows {
		values = append(values, lookupPath(row, field)...)
	}
	return decodeInto(values, dest)
}

// Pagination returns a page of the current scope, counted with the same filter
// — or from the collection's metadata when the chain carries no filter at all,
// which is what keeps an unfiltered first page off a full count.
func (r *Repo[D]) Pagination(opts *core.PageOptions) (*core.Page[D], core.IError) {
	if opts == nil {
		opts = &core.PageOptions{}
	}
	// a Sort on the chain is the default ordering; an explicit OrderBy wins
	if len(opts.OrderBy) == 0 && len(r.find.Sort) > 0 {
		clone := *opts
		clone.OrderBy = r.find.Sort
		opts = &clone
	}
	return core.MongoPaginate[D](r.db, r.collection, r.filter, opts)
}

// Aggregate runs a pipeline over this collection with the chain's filter as a
// leading $match, so a repository scope and a pipeline compose.
func (r *Repo[D]) Aggregate(dest any, pipeline []bson.M, opts ...core.MongoAggregateOptions) core.IError {
	stages := make([]bson.M, 0, len(pipeline)+1)
	if len(r.filter) > 0 {
		stages = append(stages, bson.M{"$match": r.filter})
	}
	stages = append(stages, pipeline...)
	return r.db.Aggregate(dest, r.collection, stages, opts...)
}

// ---------------------------------------------------------------------------
// Write finishers
// ---------------------------------------------------------------------------

// Create inserts d and writes the generated id back into its "_id" field, so the
// caller can use it without a second read.
func (r *Repo[D]) Create(d *D) core.IError {
	id, err := r.db.InsertOne(r.collection, d)
	if err != nil {
		return err
	}
	setDocumentID(d, id)
	return nil
}

// CreateMany inserts many documents and returns their ids, in order.
func (r *Repo[D]) CreateMany(docs []D) ([]string, core.IError) {
	if len(docs) == 0 {
		return nil, nil
	}
	values := make([]any, 0, len(docs))
	for i := range docs {
		values = append(values, docs[i])
	}
	return r.db.InsertMany(r.collection, values)
}

// Update sets a single field on the current scope. The field may be a dotted
// path into a subdocument ("profile.city").
func (r *Repo[D]) Update(field string, value any) (core.MongoUpdateResult, core.IError) {
	return r.Updates(bson.M{field: value})
}

// Updates applies $set to the first match of the current scope. Pass a raw
// operator map ({"$inc": …}) and it is sent as it is.
//
// The options carry ArrayFilters, which is what a positional "$[name]" update
// needs to say which array elements it applies to.
func (r *Repo[D]) Updates(values bson.M, opts ...core.MongoUpdateOptions) (core.MongoUpdateResult, core.IError) {
	return r.db.UpdateOne(r.collection, r.filter, updateDocument(values), opts...)
}

// UpdateAll applies the update to every match.
func (r *Repo[D]) UpdateAll(values bson.M, opts ...core.MongoUpdateOptions) (core.MongoUpdateResult, core.IError) {
	return r.db.UpdateMany(r.collection, r.filter, updateDocument(values), opts...)
}

// Upsert applies the update, inserting the document when nothing matches.
func (r *Repo[D]) Upsert(values bson.M, opts ...core.MongoUpdateOptions) (core.MongoUpdateResult, core.IError) {
	opt := core.MongoUpdateOptions{}
	if len(opts) > 0 {
		opt = opts[0]
	}
	opt.Upsert = true
	return r.db.UpdateOne(r.collection, r.filter, updateDocument(values), opt)
}

// Save replaces the whole document matching the scope, inserting it when there
// is none. With no scope it matches on the document's own id.
func (r *Repo[D]) Save(d *D) core.IError {
	filter := r.filter
	if len(filter) == 0 {
		id := documentID(d)
		if id == "" {
			return r.Create(d)
		}
		filter = core.MongoByID(id)
	}
	res, err := r.db.ReplaceOne(r.collection, filter, d, core.MongoUpdateOptions{Upsert: true})
	if err != nil {
		return err
	}
	if res.UpsertedID != "" {
		setDocumentID(d, res.UpsertedID)
	}
	return nil
}

// Delete removes the matches of the current scope and reports how many went.
//
// A scope-less Delete would empty the collection, so it is refused: that is
// almost always a filter that was forgotten rather than one that was meant.
// Use DeleteAll for the deliberate version.
func (r *Repo[D]) Delete() (int64, core.IError) {
	if len(r.filter) == 0 {
		return 0, core.New(400, "UNSCOPED_DELETE",
			"mongorepo: Delete without a filter would empty the collection — use DeleteAll")
	}
	return r.db.DeleteMany(r.collection, r.filter)
}

// DeleteOne removes the first match.
func (r *Repo[D]) DeleteOne() (int64, core.IError) {
	return r.db.DeleteOne(r.collection, r.filter)
}

// DeleteAll removes every match of the current scope, including an empty one.
func (r *Repo[D]) DeleteAll() (int64, core.IError) {
	return r.db.DeleteMany(r.collection, r.filter)
}

// FindOneOrCreate returns the match, or inserts d and returns that.
func (r *Repo[D]) FindOneOrCreate(d *D) (*D, core.IError) {
	found, err := r.FindOne()
	if err == nil {
		return found, nil
	}
	if !errors.Is(err, core.ErrDocumentNotFound) {
		return nil, err
	}
	if createErr := r.Create(d); createErr != nil {
		return nil, createErr
	}
	return d, nil
}

// FindOneAndUpdate updates the first match and returns it, atomically — how a
// queued document is claimed without two workers claiming the same one.
func (r *Repo[D]) FindOneAndUpdate(values bson.M, opts ...core.MongoFindModifyOptions) (*D, core.IError) {
	o := core.MongoFindModifyOptions{ReturnNew: true}
	if len(opts) > 0 {
		o = opts[0]
	}
	if len(o.Sort) == 0 {
		o.Sort = r.find.Sort
	}
	d := new(D)
	if err := r.db.FindOneAndUpdate(d, r.collection, r.filter, updateDocument(values), o); err != nil {
		return nil, err
	}
	return d, nil
}

// FindOneAndDelete removes the first match and returns it.
func (r *Repo[D]) FindOneAndDelete(opts ...core.MongoFindModifyOptions) (*D, core.IError) {
	o := core.MongoFindModifyOptions{}
	if len(opts) > 0 {
		o = opts[0]
	}
	if len(o.Sort) == 0 {
		o.Sort = r.find.Sort
	}
	d := new(D)
	if err := r.db.FindOneAndDelete(d, r.collection, r.filter, o); err != nil {
		return nil, err
	}
	return d, nil
}

// Inc adds to a numeric field atomically — a counter that two requests can
// increment at once without losing one of them.
func (r *Repo[D]) Inc(field string, delta any) (core.MongoUpdateResult, core.IError) {
	return r.Updates(bson.M{"$inc": bson.M{field: delta}})
}

// Push appends to an array field.
func (r *Repo[D]) Push(field string, values ...any) (core.MongoUpdateResult, core.IError) {
	if len(values) == 1 {
		return r.Updates(bson.M{"$push": bson.M{field: values[0]}})
	}
	return r.Updates(bson.M{"$push": bson.M{field: bson.M{"$each": values}}})
}

// AddToSet appends to an array field, skipping values already in it.
func (r *Repo[D]) AddToSet(field string, values ...any) (core.MongoUpdateResult, core.IError) {
	if len(values) == 1 {
		return r.Updates(bson.M{"$addToSet": bson.M{field: values[0]}})
	}
	return r.Updates(bson.M{"$addToSet": bson.M{field: bson.M{"$each": values}}})
}

// Pull removes matching elements from an array field.
func (r *Repo[D]) Pull(field string, value any) (core.MongoUpdateResult, core.IError) {
	return r.Updates(bson.M{"$pull": bson.M{field: value}})
}

// Unset removes fields from the matching document.
func (r *Repo[D]) Unset(fields ...string) (core.MongoUpdateResult, core.IError) {
	unset := bson.M{}
	for _, field := range fields {
		unset[field] = ""
	}
	return r.Updates(bson.M{"$unset": unset})
}

// Transaction runs fn with a repository bound to the transaction, so its writes
// commit or roll back together. Needs a replica set.
func (r *Repo[D]) Transaction(fn func(tx *Repo[D]) error) core.IError {
	return r.db.Transaction(func(tx core.IMongoDB) error {
		return fn(NewIn[D](tx))
	})
}

// Watch opens a change stream on this collection. Close it. Needs a replica set.
func (r *Repo[D]) Watch(stages ...bson.M) (*mongo.ChangeStream, core.IError) {
	pipeline := make([]bson.M, 0, len(stages)+1)
	if len(r.filter) > 0 {
		// a change event nests the document under fullDocument, so the scope
		// has to be re-pointed at it to mean the same thing
		match := bson.M{}
		for key, value := range r.filter {
			match["fullDocument."+key] = value
		}
		pipeline = append(pipeline, bson.M{"$match": match})
	}
	pipeline = append(pipeline, stages...)
	return r.db.Watch(r.collection, pipeline)
}

// BulkWrite sends many writes in one round trip.
func (r *Repo[D]) BulkWrite(models []mongo.WriteModel, ordered bool) (core.MongoBulkResult, core.IError) {
	return r.db.BulkWrite(r.collection, models, ordered)
}

// EnsureIndexes creates this collection's indexes. Call it at startup.
func (r *Repo[D]) EnsureIndexes(indexes ...core.MongoIndex) core.IError {
	return r.db.EnsureIndexes(r.collection, indexes...)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// withExtra ANDs a finisher's inline filter onto the chain without touching the
// receiver, so `repo.FindOne(bson.M{…})` reads like the GORM repository's
// `FindOne(conds…)`.
func (r *Repo[D]) withExtra(filter []bson.M) *Repo[D] {
	if len(filter) == 0 {
		return r
	}
	scope := r
	for _, f := range filter {
		scope = scope.Where(f)
	}
	return scope
}

func cloneFilter(filter bson.M) bson.M {
	out := make(bson.M, len(filter))
	for k, v := range filter {
		out[k] = v
	}
	return out
}

// andFilter composes two filters. Disjoint keys merge into one document, which
// is how Mongo reads an AND; overlapping keys become an explicit $and, because
// merging them would silently drop the first condition.
func andFilter(base, extra bson.M) bson.M {
	if len(extra) == 0 {
		return cloneFilter(base)
	}
	if len(base) == 0 {
		return cloneFilter(extra)
	}

	// "used" includes the keys inside an existing $and, or a third condition on
	// the same field would sit beside the group instead of joining it
	used := usedKeys(base)
	overlaps := false
	for key := range extra {
		if used[key] {
			overlaps = true
			break
		}
	}
	if !overlaps {
		merged := cloneFilter(base)
		for key, value := range extra {
			merged[key] = value
		}
		return merged
	}

	// keep an existing $and flat rather than nesting one inside another
	if existing, ok := base["$and"].([]bson.M); ok {
		merged := cloneFilter(base)
		merged["$and"] = append(append([]bson.M{}, existing...), cloneFilter(extra))
		return merged
	}
	return bson.M{"$and": []bson.M{cloneFilter(base), cloneFilter(extra)}}
}

// usedKeys is every field the filter already constrains, looking one level into
// the $and group this package builds.
func usedKeys(filter bson.M) map[string]bool {
	keys := make(map[string]bool, len(filter))
	for key, value := range filter {
		if key == "$and" {
			if conditions, ok := value.([]bson.M); ok {
				for _, condition := range conditions {
					for inner := range condition {
						keys[inner] = true
					}
				}
				continue
			}
		}
		keys[key] = true
	}
	return keys
}

// updateDocument wraps a plain field map in $set. A map that already speaks in
// operators is passed through, so both spellings work:
//
//	Updates(bson.M{"name": "bob"})                  // $set
//	Updates(bson.M{"$inc": bson.M{"views": 1}})     // as written
func updateDocument(values bson.M) bson.M {
	for key := range values {
		if strings.HasPrefix(key, "$") {
			return values
		}
	}
	return bson.M{"$set": values}
}

// setDocumentID writes a generated id back into the document's "_id" field, the
// way GORM fills a primary key after Create. It handles both spellings — a
// string id and a bson.ObjectID — and leaves an id that was already set
// alone.
func setDocumentID(doc any, id string) {
	if id == "" {
		return
	}
	value := reflect.ValueOf(doc)
	if value.Kind() != reflect.Pointer || value.IsNil() {
		return
	}
	value = value.Elem()
	if value.Kind() != reflect.Struct {
		return
	}

	field, ok := idField(value)
	if !ok || !field.CanSet() {
		return
	}
	switch {
	case field.Kind() == reflect.String:
		if field.String() == "" {
			field.SetString(id)
		}
	case field.Type() == reflect.TypeOf(bson.ObjectID{}):
		current, _ := field.Interface().(bson.ObjectID)
		if current.IsZero() {
			if oid, err := bson.ObjectIDFromHex(id); err == nil {
				field.Set(reflect.ValueOf(oid))
			}
		}
	}
}

// documentID reads the document's "_id", as the string form used everywhere else.
func documentID(doc any) string {
	value := reflect.ValueOf(doc)
	for value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return ""
		}
		value = value.Elem()
	}
	if value.Kind() != reflect.Struct {
		return ""
	}

	field, ok := idField(value)
	if !ok {
		return ""
	}
	switch {
	case field.Kind() == reflect.String:
		return field.String()
	case field.Type() == reflect.TypeOf(bson.ObjectID{}):
		oid, _ := field.Interface().(bson.ObjectID)
		if oid.IsZero() {
			return ""
		}
		return oid.Hex()
	}
	return ""
}

// idField finds the struct field stored as "_id".
func idField(value reflect.Value) (reflect.Value, bool) {
	t := value.Type()
	for i := range t.NumField() {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("bson"), ",")
		if strings.TrimSpace(name) == "_id" {
			return value.Field(i), true
		}
	}
	// no bson tag: the driver lower-cases field names, so "ID" is stored as "id"
	// and only an explicit tag makes it the document's key
	return reflect.Value{}, false
}

// lookupPath reads a dotted field out of a decoded document, following the way
// Mongo itself reads one: a segment applied to an array reaches into every
// element, so "items.sku" on a document with three items yields three values.
func lookupPath(row bson.M, path string) []any {
	current := []any{any(row)}
	for _, segment := range strings.Split(path, ".") {
		next := make([]any, 0, len(current))
		for _, value := range current {
			switch typed := value.(type) {
			case bson.M:
				if found, ok := typed[segment]; ok {
					next = append(next, found)
				}
			case bson.A:
				for _, element := range typed {
					if doc, ok := element.(bson.M); ok {
						if found, ok := doc[segment]; ok {
							next = append(next, found)
						}
					}
				}
			case []any:
				for _, element := range typed {
					if doc, ok := element.(bson.M); ok {
						if found, ok := doc[segment]; ok {
							next = append(next, found)
						}
					}
				}
			}
		}
		current = next
		if len(current) == 0 {
			return nil
		}
	}

	// a final segment that landed on arrays is flattened, so Pluck returns the
	// values themselves rather than the arrays holding them
	out := make([]any, 0, len(current))
	for _, value := range current {
		switch typed := value.(type) {
		case bson.A:
			out = append(out, typed...)
		case []any:
			out = append(out, typed...)
		default:
			out = append(out, value)
		}
	}
	return out
}

// decodeInto moves BSON values into a typed slice through the driver's codec,
// so a caller gets []string rather than []any.
func decodeInto(values []any, dest any) core.IError {
	raw, err := bson.Marshal(bson.M{"v": values})
	if err != nil {
		return core.Wrap(err, "mongorepo: pluck")
	}
	var wrapper struct {
		V bson.RawValue `bson:"v"`
	}
	if err := bson.Unmarshal(raw, &wrapper); err != nil {
		return core.Wrap(err, "mongorepo: pluck")
	}
	if err := wrapper.V.Unmarshal(dest); err != nil {
		return core.Wrap(err, "mongorepo: pluck")
	}
	return nil
}

// regexpQuote escapes the characters that would otherwise turn a user's search
// term into a pattern of their choosing.
func regexpQuote(term string) string {
	var b strings.Builder
	for _, r := range term {
		if strings.ContainsRune(`\.+*?()|[]{}^$`, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}
