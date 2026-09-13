package mongorepo

import (
	"testing"

	core "github.com/pskclub/mine-core/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
)

type revenueRow struct {
	Status string  `bson:"_id"`
	Total  float64 `bson:"total"`
}

func TestPipeline_startsFromTheRepositoryScope(t *testing.T) {
	repo := newRepo[user](t).Eq("status", "active")

	stages := Aggregate[revenueRow](repo).
		Group("$status", bson.M{"total": bson.M{"$sum": "$amount"}}).
		Stages()

	require.Len(t, stages, 2)
	assert.Equal(t, bson.M{"$match": bson.M{"status": "active"}}, stages[0],
		"the chain's filter becomes the leading $match")
	assert.Equal(t, bson.M{"$group": bson.M{"_id": "$status", "total": bson.M{"$sum": "$amount"}}},
		stages[1])
}

func TestPipeline_withoutAScopeAddsNoMatch(t *testing.T) {
	stages := Aggregate[revenueRow](newRepo[user](t)).CountAs("n").Stages()
	require.Len(t, stages, 1, "an empty scope must not produce an empty $match")
}

func TestPipeline_isCopyOnWrite(t *testing.T) {
	base := Aggregate[revenueRow](newRepo[user](t)).Match(bson.M{"a": 1})

	left := base.Limit(10)
	right := base.Limit(20)

	assert.Len(t, base.Stages(), 1, "the base is untouched")
	assert.Equal(t, bson.M{"$limit": int64(10)}, left.Stages()[1])
	assert.Equal(t, bson.M{"$limit": int64(20)}, right.Stages()[1],
		"two branches off one base must not contaminate each other")
}

func TestPipeline_lookupSpellings(t *testing.T) {
	simple := Aggregate[revenueRow](newRepo[user](t)).Lookup(Lookup{
		From: "orders", LocalField: "_id", ForeignField: "user_id", As: "orders",
	}).Stages()
	assert.Equal(t, bson.M{"$lookup": bson.M{
		"from": "orders", "localField": "_id", "foreignField": "user_id", "as": "orders",
	}}, simple[0])

	// the correlated form: a join that filters the joined documents
	correlated := Aggregate[revenueRow](newRepo[user](t)).Lookup(Lookup{
		From: "orders",
		Let:  bson.M{"uid": "$_id"},
		Pipeline: []bson.M{
			{"$match": bson.M{"$expr": bson.M{"$eq": []any{"$user_id", "$$uid"}}}},
		},
		As: "orders",
	}).Stages()
	stage := correlated[0]["$lookup"].(bson.M)
	assert.Equal(t, bson.M{"uid": "$_id"}, stage["let"])
	assert.Len(t, stage["pipeline"], 1)
	assert.NotContains(t, stage, "localField", "the two spellings do not mix")
}

func TestPipeline_lookupOneUnwinds(t *testing.T) {
	stages := Aggregate[revenueRow](newRepo[user](t)).LookupOne(Lookup{
		From: "profiles", LocalField: "_id", ForeignField: "user_id", As: "profile",
	}).Stages()

	require.Len(t, stages, 2)
	assert.Equal(t, bson.M{"$unwind": bson.M{
		"path": "$profile", "preserveNullAndEmptyArrays": true,
	}}, stages[1], "a belongs-to join keeps documents that have no match")
}

func TestPipeline_unwindAcceptsEitherSpelling(t *testing.T) {
	withDollar := Aggregate[revenueRow](newRepo[user](t)).Unwind("$items", false).Stages()
	without := Aggregate[revenueRow](newRepo[user](t)).Unwind("items", false).Stages()
	assert.Equal(t, withDollar, without, "a field path is a field path either way")
}

func TestPipeline_sortAndEmptySort(t *testing.T) {
	sorted := Aggregate[revenueRow](newRepo[user](t)).Sort("-total", "name").Stages()
	require.Len(t, sorted, 1)
	assert.Equal(t, bson.M{"$sort": bson.D{{Key: "total", Value: -1}, {Key: "name", Value: 1}}},
		sorted[0])

	assert.Empty(t, Aggregate[revenueRow](newRepo[user](t)).Sort().Stages(),
		"sorting by nothing must not append an empty stage, which Mongo rejects")
}

func TestPipeline_facetAndGraphLookup(t *testing.T) {
	stages := Aggregate[revenueRow](newRepo[user](t)).
		GraphLookup(GraphLookup{
			From: "employees", StartWith: "manager_id",
			ConnectFromField: "manager_id", ConnectToField: "_id",
			As: "chain", MaxDepth: 5, DepthField: "depth",
		}).
		Facet(map[string][]bson.M{
			"rows":  {{"$limit": 10}},
			"total": {{"$count": "n"}},
		}).
		Stages()

	graph := stages[0]["$graphLookup"].(bson.M)
	assert.Equal(t, "$manager_id", graph["startWith"], "startWith is a field path")
	assert.Equal(t, int64(5), graph["maxDepth"])
	assert.Equal(t, "depth", graph["depthField"])

	facet := stages[1]["$facet"].(bson.M)
	assert.Contains(t, facet, "rows")
	assert.Contains(t, facet, "total")
}

func TestPipeline_writeStages(t *testing.T) {
	out := Aggregate[revenueRow](newRepo[user](t)).Out("daily_rollup").Stages()
	assert.Equal(t, bson.M{"$out": "daily_rollup"}, out[0])

	merged := Aggregate[revenueRow](newRepo[user](t)).
		Merge("daily_rollup", []string{"date", "status"}, "merge", "insert").Stages()
	stage := merged[0]["$merge"].(bson.M)
	assert.Equal(t, "daily_rollup", stage["into"])
	assert.Equal(t, []string{"date", "status"}, stage["on"])
	assert.Equal(t, "merge", stage["whenMatched"])

	single := Aggregate[revenueRow](newRepo[user](t)).
		Merge("rollup", []string{"date"}, "", "").Stages()
	assert.Equal(t, "date", single[0]["$merge"].(bson.M)["on"],
		"a single key is sent as a string, which is what Mongo expects")
}

func TestPipeline_options(t *testing.T) {
	p := Aggregate[revenueRow](newRepo[user](t)).AllowDiskUse().Comment("nightly-rollup")
	assert.True(t, p.opts.AllowDiskUse)
	assert.Equal(t, "nightly-rollup", p.opts.Comment)

	replaced := p.Options(core.MongoAggregateOptions{BatchSize: 50})
	assert.Equal(t, int32(50), replaced.opts.BatchSize)
	assert.False(t, replaced.opts.AllowDiskUse, "Options replaces rather than merges")
	assert.True(t, p.opts.AllowDiskUse, "...and leaves the pipeline it branched from alone")
}

func TestPipeline_rawStages(t *testing.T) {
	// nothing is off limits: a stage the builder does not name goes through
	stages := Aggregate[revenueRow](newRepo[user](t)).
		Stage(bson.M{"$setWindowFields": bson.M{"sortBy": bson.M{"date": 1}}}).
		StagesOf(bson.M{"$densify": bson.M{"field": "date"}}, bson.M{"$limit": 5}).
		Stages()

	require.Len(t, stages, 3)
	assert.Contains(t, stages[0], "$setWindowFields")
	assert.Contains(t, stages[1], "$densify")
}

func TestPipeline_finishersFailLoudlyWithoutAConnection(t *testing.T) {
	p := Aggregate[revenueRow](newRepo[user](t)).Match(bson.M{"a": 1})

	_, err := p.All()
	require.Error(t, err)
	assert.Equal(t, "MONGO_DISABLED", err.GetCode())

	_, err = p.Count()
	assert.Error(t, err)
	assert.Error(t, p.Each(func(revenueRow) error { return nil }))
}

func TestPipeline_oneAppendsALimit(t *testing.T) {
	fake := &fakeMongo{}
	_, err := Aggregate[revenueRow](NewIn[user](fake)).Match(bson.M{"a": 1}).One()
	require.Error(t, err, "the fake returns no rows, so One is a miss")
	assert.Equal(t, "NOT_FOUND", err.GetCode())

	stages, ok := fake.pipeline.([]bson.M)
	require.True(t, ok)
	assert.Equal(t, bson.M{"$limit": int64(1)}, stages[len(stages)-1],
		"One must not drag the whole result set back to decode one row")
}

func TestPipeline_pageBuildsAFacet(t *testing.T) {
	fake := &fakeMongo{}
	_, err := Aggregate[revenueRow](NewIn[user](fake)).
		Match(bson.M{"status": "active"}).
		Page(&core.PageOptions{Page: 2, Limit: 10, OrderBy: []string{"-total"}})
	require.NoError(t, err)

	stages, ok := fake.pipeline.([]map[string]any)
	require.True(t, ok)
	require.Len(t, stages, 2, "the caller's stages, then the facet")
	assert.Contains(t, stages[1], "$facet")
}
