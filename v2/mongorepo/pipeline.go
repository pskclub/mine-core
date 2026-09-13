package mongorepo

import (
	core "github.com/pskclub/mine-core/v2"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// Pipeline is a typed aggregation builder: D is the document it reads, T is the
// shape each result row decodes into. Every stage returns a new Pipeline, so a
// base can be branched the way a repository chain can.
//
//	type revenue struct {
//	    Status string  `bson:"_id"`
//	    Total  float64 `bson:"total"`
//	}
//
//	rows, err := mongorepo.Aggregate[revenue](users).
//	    Match(bson.M{"status": "active"}).
//	    Lookup(mongorepo.Lookup{
//	        From: "orders", LocalField: "_id", ForeignField: "user_id", As: "orders",
//	    }).
//	    Unwind("$orders", true).
//	    Group("$status", bson.M{"total": bson.M{"$sum": "$orders.total"}}).
//	    Sort("-total").
//	    AllowDiskUse().
//	    All()
//
// Nothing is hidden: Stage takes a raw bson.M for anything the builder does not
// name, and the stages are sent to the driver exactly as built.
type Pipeline[D core.IDocument, T any] struct {
	repo   *Repo[D]
	stages []bson.M
	opts   core.MongoAggregateOptions
}

// Aggregate starts a pipeline over a repository's collection, typed by the row
// it produces. The repository's own chain becomes the leading $match, so a scope
// and a pipeline compose:
//
//	mongorepo.Aggregate[row](users.Eq("status", "active"))
func Aggregate[T any, D core.IDocument](repo *Repo[D]) *Pipeline[D, T] {
	p := &Pipeline[D, T]{repo: repo}
	if len(repo.filter) > 0 {
		p.stages = append(p.stages, bson.M{"$match": repo.Filter()})
	}
	return p
}

func (p *Pipeline[D, T]) with(stage bson.M) *Pipeline[D, T] {
	cp := *p
	cp.stages = append(append([]bson.M{}, p.stages...), stage)
	return &cp
}

// Stages is the pipeline as built — for logging, or for handing to
// core.IMongoDB directly.
func (p *Pipeline[D, T]) Stages() []bson.M {
	return append([]bson.M{}, p.stages...)
}

// ---------------------------------------------------------------------------
// Stages
// ---------------------------------------------------------------------------

// Stage appends a raw stage, for anything this builder does not name
// ($setWindowFields, $densify, Atlas $search).
func (p *Pipeline[D, T]) Stage(stage bson.M) *Pipeline[D, T] { return p.with(stage) }

// Stages appends several raw stages.
func (p *Pipeline[D, T]) StagesOf(stages ...bson.M) *Pipeline[D, T] {
	cp := *p
	cp.stages = append(append([]bson.M{}, p.stages...), stages...)
	return &cp
}

// Match filters. Put it first: a $match before a $lookup or a $group is the
// difference between reading an index and reading the collection.
func (p *Pipeline[D, T]) Match(filter bson.M) *Pipeline[D, T] {
	return p.with(bson.M{"$match": filter})
}

// Group groups by id (a field path like "$status", a bson.M of several fields,
// or nil for the whole collection) and accumulates fields.
func (p *Pipeline[D, T]) Group(id any, fields bson.M) *Pipeline[D, T] {
	group := bson.M{"_id": id}
	for key, value := range fields {
		group[key] = value
	}
	return p.with(bson.M{"$group": group})
}

// Project reshapes each document.
func (p *Pipeline[D, T]) Project(projection bson.M) *Pipeline[D, T] {
	return p.with(bson.M{"$project": projection})
}

// AddFields adds computed fields, keeping everything else.
func (p *Pipeline[D, T]) AddFields(fields bson.M) *Pipeline[D, T] {
	return p.with(bson.M{"$addFields": fields})
}

// Set is $addFields under its newer name.
func (p *Pipeline[D, T]) Set(fields bson.M) *Pipeline[D, T] {
	return p.with(bson.M{"$set": fields})
}

// Unset removes fields.
func (p *Pipeline[D, T]) Unset(fields ...string) *Pipeline[D, T] {
	return p.with(bson.M{"$unset": fields})
}

// Sort orders rows: "-total" is descending.
func (p *Pipeline[D, T]) Sort(fields ...string) *Pipeline[D, T] {
	sort := core.MongoSortDocument(fields)
	if sort == nil {
		// an empty stage document is not valid Mongo, so sorting by nothing
		// leaves the pipeline as it was
		cp := *p
		return &cp
	}
	return p.with(bson.M{"$sort": sort})
}

// Limit caps the rows.
func (p *Pipeline[D, T]) Limit(n int64) *Pipeline[D, T] {
	return p.with(bson.M{"$limit": n})
}

// Skip drops the first n rows.
func (p *Pipeline[D, T]) Skip(n int64) *Pipeline[D, T] {
	return p.with(bson.M{"$skip": n})
}

// Unwind turns an array field into one row per element. preserveEmpty keeps the
// documents whose array is empty or missing, which is what a $lookup usually
// wants — without it they vanish and the aggregate silently under-counts.
func (p *Pipeline[D, T]) Unwind(path string, preserveEmpty bool) *Pipeline[D, T] {
	return p.with(bson.M{"$unwind": bson.M{
		"path":                       ensureFieldPath(path),
		"preserveNullAndEmptyArrays": preserveEmpty,
	}})
}

// Lookup describes a join. Use LocalField/ForeignField for the simple case, or
// Let plus Pipeline for a correlated sub-query — a join that filters or shapes
// the joined documents.
type Lookup struct {
	From         string
	LocalField   string
	ForeignField string
	As           string
	// Let declares the variables Pipeline reads with "$$name".
	Let      bson.M
	Pipeline []bson.M
}

// Lookup joins another collection.
func (p *Pipeline[D, T]) Lookup(spec Lookup) *Pipeline[D, T] {
	stage := bson.M{"from": spec.From, "as": spec.As}
	if spec.LocalField != "" {
		stage["localField"] = spec.LocalField
	}
	if spec.ForeignField != "" {
		stage["foreignField"] = spec.ForeignField
	}
	if spec.Let != nil {
		stage["let"] = spec.Let
	}
	if spec.Pipeline != nil {
		stage["pipeline"] = spec.Pipeline
	}
	return p.with(bson.M{"$lookup": stage})
}

// LookupOne is Lookup followed by the unwind that turns a one-element array into
// a single embedded document — the shape a belongs-to join is usually wanted in.
func (p *Pipeline[D, T]) LookupOne(spec Lookup) *Pipeline[D, T] {
	return p.Lookup(spec).Unwind("$"+spec.As, true)
}

// GraphLookup describes a recursive join — an org chart, a category tree, a
// referral chain.
type GraphLookup struct {
	From             string
	StartWith        string
	ConnectFromField string
	ConnectToField   string
	As               string
	MaxDepth         int64
	DepthField       string
	Restrict         bson.M
}

// GraphLookup follows a relation recursively.
func (p *Pipeline[D, T]) GraphLookup(spec GraphLookup) *Pipeline[D, T] {
	stage := bson.M{
		"from":             spec.From,
		"startWith":        ensureFieldPath(spec.StartWith),
		"connectFromField": spec.ConnectFromField,
		"connectToField":   spec.ConnectToField,
		"as":               spec.As,
	}
	if spec.MaxDepth > 0 {
		stage["maxDepth"] = spec.MaxDepth
	}
	if spec.DepthField != "" {
		stage["depthField"] = spec.DepthField
	}
	if spec.Restrict != nil {
		stage["restrictSearchWithMatch"] = spec.Restrict
	}
	return p.with(bson.M{"$graphLookup": stage})
}

// Facet runs several sub-pipelines over the same input in one pass — the way to
// get a list and its aggregate counts without querying twice.
func (p *Pipeline[D, T]) Facet(facets map[string][]bson.M) *Pipeline[D, T] {
	stage := bson.M{}
	for name, stages := range facets {
		stage[name] = stages
	}
	return p.with(bson.M{"$facet": stage})
}

// Bucket groups rows into explicit boundaries.
func (p *Pipeline[D, T]) Bucket(groupBy any, boundaries []any, defaultBucket any, output bson.M) *Pipeline[D, T] {
	stage := bson.M{"groupBy": groupBy, "boundaries": boundaries}
	if defaultBucket != nil {
		stage["default"] = defaultBucket
	}
	if output != nil {
		stage["output"] = output
	}
	return p.with(bson.M{"$bucket": stage})
}

// BucketAuto groups rows into n buckets of its own choosing — a histogram.
func (p *Pipeline[D, T]) BucketAuto(groupBy any, buckets int64, output bson.M) *Pipeline[D, T] {
	stage := bson.M{"groupBy": groupBy, "buckets": buckets}
	if output != nil {
		stage["output"] = output
	}
	return p.with(bson.M{"$bucketAuto": stage})
}

// SortByCount groups by an expression and orders by how often it occurred.
func (p *Pipeline[D, T]) SortByCount(expr any) *Pipeline[D, T] {
	return p.with(bson.M{"$sortByCount": expr})
}

// ReplaceRoot promotes an embedded document to the top level.
func (p *Pipeline[D, T]) ReplaceRoot(expr any) *Pipeline[D, T] {
	return p.with(bson.M{"$replaceRoot": bson.M{"newRoot": expr}})
}

// UnionWith appends another collection's rows to this pipeline's.
func (p *Pipeline[D, T]) UnionWith(collection string, stages ...bson.M) *Pipeline[D, T] {
	stage := bson.M{"coll": collection}
	if len(stages) > 0 {
		stage["pipeline"] = stages
	}
	return p.with(bson.M{"$unionWith": stage})
}

// Sample takes n rows at random.
func (p *Pipeline[D, T]) Sample(n int64) *Pipeline[D, T] {
	return p.with(bson.M{"$sample": bson.M{"size": n}})
}

// CountAs replaces the rows with a single document holding their count.
func (p *Pipeline[D, T]) CountAs(field string) *Pipeline[D, T] {
	return p.with(bson.M{"$count": field})
}

// Out writes the result into a collection, replacing it.
func (p *Pipeline[D, T]) Out(collection string) *Pipeline[D, T] {
	return p.with(bson.M{"$out": collection})
}

// Merge writes the result into a collection, merging with what is there — how a
// rollup or a materialised view is kept up to date.
func (p *Pipeline[D, T]) Merge(into string, on []string, whenMatched, whenNotMatched string) *Pipeline[D, T] {
	stage := bson.M{"into": into}
	if len(on) == 1 {
		stage["on"] = on[0]
	} else if len(on) > 1 {
		stage["on"] = on
	}
	if whenMatched != "" {
		stage["whenMatched"] = whenMatched
	}
	if whenNotMatched != "" {
		stage["whenNotMatched"] = whenNotMatched
	}
	return p.with(bson.M{"$merge": stage})
}

// ---------------------------------------------------------------------------
// Options
// ---------------------------------------------------------------------------

// AllowDiskUse lets $group and $sort spill past the 100MB in-memory limit. Set
// it on anything that aggregates a large collection.
func (p *Pipeline[D, T]) AllowDiskUse() *Pipeline[D, T] {
	cp := *p
	cp.opts.AllowDiskUse = true
	return &cp
}

// Options replaces the aggregation options wholesale (MaxTime, Hint, Let,
// Collation, BatchSize, Comment).
func (p *Pipeline[D, T]) Options(opts core.MongoAggregateOptions) *Pipeline[D, T] {
	cp := *p
	cp.opts = opts
	return &cp
}

// Comment labels the pipeline in the profiler and in currentOp, which is how a
// slow one is identified in production.
func (p *Pipeline[D, T]) Comment(comment string) *Pipeline[D, T] {
	cp := *p
	cp.opts.Comment = comment
	return &cp
}

// ---------------------------------------------------------------------------
// Finishers
// ---------------------------------------------------------------------------

// All runs the pipeline and decodes every row.
func (p *Pipeline[D, T]) All() ([]T, core.IError) {
	rows := make([]T, 0)
	if err := p.repo.db.Aggregate(&rows, p.repo.collection, p.Stages(), p.opts); err != nil {
		return nil, err
	}
	return rows, nil
}

// One runs the pipeline and returns the first row, or a NOT_FOUND error when it
// produced none.
func (p *Pipeline[D, T]) One() (*T, core.IError) {
	rows, err := p.Limit(1).All()
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, core.New(404, "NOT_FOUND", "not found")
	}
	return &rows[0], nil
}

// Each streams the rows, so a pipeline whose output does not fit in memory can
// still be processed. Returning an error from fn stops the iteration.
func (p *Pipeline[D, T]) Each(fn func(T) error) core.IError {
	cur, err := p.repo.db.AggregateCursor(p.repo.collection, p.Stages(), p.opts)
	if err != nil {
		return err
	}
	return core.MongoEach(p.repo.db.Context(), cur, fn)
}

// Into decodes the rows into dest, for a result whose shape is not T — a $facet
// row, most often.
func (p *Pipeline[D, T]) Into(dest any) core.IError {
	return p.repo.db.Aggregate(dest, p.repo.collection, p.Stages(), p.opts)
}

// Count runs the pipeline and reports how many rows it produced, without
// decoding them.
func (p *Pipeline[D, T]) Count() (int64, core.IError) {
	var rows []struct {
		N int64 `bson:"n"`
	}
	if err := p.CountAs("n").Into(&rows); err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, nil
	}
	return rows[0].N, nil
}

// Page returns a page of rows plus the total. Build the pipeline without
// $sort/$skip/$limit — they are appended from the options.
//
// An ordinary page comes from a single pass over the pipeline via $facet; a page
// too large to fit in the one document $facet builds costs a second round trip
// for the count instead.
func (p *Pipeline[D, T]) Page(opts *core.PageOptions) (*core.Page[T], core.IError) {
	return core.MongoAggregatePage[T](p.repo.db, p.repo.collection, toMaps(p.Stages()), opts, p.opts)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// ensureFieldPath accepts "orders" or "$orders": a stage that needs a field path
// gets one either way, rather than silently matching a literal string.
func ensureFieldPath(field string) string {
	if field == "" || field[0] == '$' {
		return field
	}
	return "$" + field
}

func toMaps(stages []bson.M) []map[string]any {
	out := make([]map[string]any, 0, len(stages))
	for _, stage := range stages {
		out = append(out, map[string]any(stage))
	}
	return out
}
