package core

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// --- connection string ---------------------------------------------------

func TestMongoURI_connectionStringWins(t *testing.T) {
	uri := mongoURI(&ENVConfig{
		DBMongoConnectionString: "mongodb+srv://u:p@cluster.example.com/?retryWrites=true",
		DBMongoHost:             "ignored",
	})
	assert.Equal(t, "mongodb+srv://u:p@cluster.example.com/?retryWrites=true", uri)
}

func TestMongoURI_escapesCredentials(t *testing.T) {
	// a password holding "@" or "/" used to produce a URI that parsed as a
	// different host, and the failure looked like a network problem
	uri := mongoURI(&ENVConfig{
		DBMongoUserName: "svc user",
		DBMongoPassword: "p@ss/w:rd",
		DBMongoHost:     "mongo",
		DBMongoPort:     "27017",
	})
	assert.Equal(t, "mongodb://svc%20user:p%40ss%2Fw%3Ard@mongo:27017", uri)
}

func TestMongoURI_omitsEmptyCredentials(t *testing.T) {
	// a local Mongo without auth: "mongodb://:@host" is not the same thing
	uri := mongoURI(&ENVConfig{DBMongoHost: "localhost", DBMongoPort: "27017"})
	assert.Equal(t, "mongodb://localhost:27017", uri)
}

func TestMongoURI_defaultsHostAndPort(t *testing.T) {
	assert.Equal(t, "mongodb://127.0.0.1:27017", mongoURI(&ENVConfig{}))
}

func TestMongoURI_replicaSetAndTLS(t *testing.T) {
	// both keys existed in ENVConfig and neither was ever read
	uri := mongoURI(&ENVConfig{
		DBMongoHost:        "a,b,c",
		DBMongoPort:        "27017",
		DBMongoReplicaName: "rs0",
		DBMongoTLS:         true,
	})
	assert.Equal(t, "mongodb://a:27017,b:27017,c:27017/?replicaSet=rs0&tls=true", uri)
}

func TestMongoHosts_keepsAPerEntryPort(t *testing.T) {
	assert.Equal(t, "a:27018,b:27017",
		mongoHosts(&ENVConfig{DBMongoHost: "a:27018, b", DBMongoPort: "27017"}))
}

// --- sorting -------------------------------------------------------------

func TestMongoSort(t *testing.T) {
	assert.Nil(t, mongoSort(nil))
	assert.Nil(t, mongoSort([]string{"", "  "}))

	assert.Equal(t, bson.D{{Key: "name", Value: 1}}, mongoSort([]string{"name"}))
	assert.Equal(t, bson.D{{Key: "created_at", Value: -1}}, mongoSort([]string{"-created_at"}))
	assert.Equal(t, bson.D{{Key: "created_at", Value: -1}}, mongoSort([]string{"created_at desc"}))
	assert.Equal(t, bson.D{{Key: "name", Value: 1}}, mongoSort([]string{"+name"}))

	assert.Equal(t,
		bson.D{{Key: "status", Value: 1}, {Key: "created_at", Value: -1}},
		mongoSort([]string{"status", "-created_at"}),
		"order is preserved, which is what makes a compound sort mean anything")
}

func TestMongoIndexKeys_supportsNonNumericTypes(t *testing.T) {
	// a text or geo index cannot be expressed as a direction
	assert.Equal(t, bson.D{{Key: "name", Value: "text"}}, mongoIndexKeys([]string{"name:text"}))
	assert.Equal(t, bson.D{{Key: "loc", Value: "2dsphere"}}, mongoIndexKeys([]string{"loc:2dsphere"}))
	assert.Equal(t,
		bson.D{{Key: "status", Value: 1}, {Key: "created_at", Value: -1}},
		mongoIndexKeys([]string{"status", "-created_at"}))
	assert.Nil(t, mongoIndexKeys(nil))
}

// indexOptionsOf resolves the builder driver v2 hands back — a list of setters,
// not the options themselves — into what the driver would actually send.
func indexOptionsOf(t *testing.T, b *options.IndexOptionsBuilder) *options.IndexOptions {
	t.Helper()
	out := &options.IndexOptions{}
	for _, set := range b.List() {
		require.NoError(t, set(out))
	}
	return out
}

func TestMongoIndexModel_carriesItsOptions(t *testing.T) {
	model, err := mongoIndexModel(MongoIndex{
		Keys: []string{"email"}, Unique: true, Sparse: true,
		TTL: time.Hour, Name: "email_ttl", Partial: bson.M{"deleted_at": nil},
	})
	require.NoError(t, err)
	assert.Equal(t, bson.D{{Key: "email", Value: 1}}, model.Keys)

	opts := indexOptionsOf(t, model.Options)
	assert.True(t, *opts.Unique)
	assert.True(t, *opts.Sparse)
	assert.EqualValues(t, 3600, *opts.ExpireAfterSeconds)
	assert.Equal(t, "email_ttl", *opts.Name)
	assert.NotNil(t, opts.PartialFilterExpression)

	_, err = mongoIndexModel(MongoIndex{})
	require.Error(t, err, "an index with no keys is a mistake worth catching early")
	assert.Equal(t, "INVALID_INDEX", err.GetCode())
}

// --- ids -----------------------------------------------------------------

func TestMongoIDString_rendersAnObjectIDAsHex(t *testing.T) {
	// the bug: fmt.Sprintf("%v", oid) gives `ObjectID("507f…")`, and a filter
	// built from that string matches nothing — silently
	oid, err := bson.ObjectIDFromHex("507f1f77bcf86cd799439011")
	require.NoError(t, err)

	assert.Equal(t, "507f1f77bcf86cd799439011", mongoIDString(oid))
	assert.NotContains(t, mongoIDString(oid), "ObjectID")
}

func TestMongoIDString_otherKinds(t *testing.T) {
	assert.Equal(t, "my-own-id", mongoIDString("my-own-id"))
	assert.Equal(t, "42", mongoIDString(42))
	assert.Empty(t, mongoIDString(nil))
}

func TestMongoObjectID(t *testing.T) {
	oid, err := MongoObjectID("507f1f77bcf86cd799439011")
	require.NoError(t, err)
	assert.Equal(t, "507f1f77bcf86cd799439011", oid.Hex())

	_, err = MongoObjectID("not-an-id")
	require.Error(t, err)
	assert.Equal(t, "INVALID_ID", err.GetCode())
	assert.Equal(t, 400, err.GetStatus())
}

func TestMongoByID_roundTripsWhatInsertReturned(t *testing.T) {
	// an id from InsertOne must be usable as a filter, which is the whole point
	// of fixing mongoIDString
	oid := bson.NewObjectID()
	id := mongoIDString(oid)

	assert.Equal(t, bson.M{"_id": oid}, MongoByID(id))
	assert.Equal(t, bson.M{"_id": "custom-id"}, MongoByID("custom-id"),
		"a collection with string ids still works")
}

// --- small helpers -------------------------------------------------------

func TestOrEmptyFilter(t *testing.T) {
	assert.Equal(t, bson.M{}, orEmptyFilter(nil), "nil means match everything, not an error")
	assert.Equal(t, bson.M{"a": 1}, orEmptyFilter(bson.M{"a": 1}))
}

func TestUpdateResultOf(t *testing.T) {
	assert.Equal(t, MongoUpdateResult{}, updateResultOf(nil))

	oid := bson.NewObjectID()
	got := updateResultOf(&mongo.UpdateResult{
		MatchedCount: 1, ModifiedCount: 1, UpsertedCount: 0,
	})
	assert.Equal(t, MongoUpdateResult{Matched: 1, Modified: 1}, got)

	upserted := updateResultOf(&mongo.UpdateResult{UpsertedCount: 1, UpsertedID: oid})
	assert.Equal(t, oid.Hex(), upserted.UpsertedID, "an upserted id is usable as a filter too")
}

// --- disabled ------------------------------------------------------------

func TestMongo_disabledFailsLoudly(t *testing.T) {
	// a database that silently reads nothing is worse than one that says it is
	// not there
	ctx := newTestApp(t).NewContext(t.Context())
	m := ctx.DBMongo()

	require.NotNil(t, m, "never nil, so there is no panic to debug")
	assert.False(t, m.Enabled())

	var user struct{}
	err := m.FindOne(&user, "users", bson.M{})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrMongoDisabled))
	assert.Equal(t, "MONGO_DISABLED", err.GetCode())
	assert.Equal(t, 503, err.GetStatus())

	_, insertErr := m.InsertOne("users", bson.M{})
	assert.Error(t, insertErr)
	_, updateErr := m.UpdateOne("users", bson.M{}, bson.M{})
	assert.Error(t, updateErr)
	assert.Error(t, m.Aggregate(&user, "users", nil))
	assert.Error(t, m.FindOneAndUpdate(&user, "users", bson.M{}, bson.M{}))
	assert.Error(t, m.EnsureIndexes("users", MongoIndex{Keys: []string{"a"}}))
	assert.Error(t, m.Transaction(func(IMongoDB) error { return nil }))
	assert.Nil(t, m.Collection("users"))
	assert.Nil(t, m.Database())
	assert.Nil(t, m.Client())
	assert.NoError(t, m.Close(), "closing what was never opened is not a failure")
}

func TestNewMongoDB_requiresADatabaseName(t *testing.T) {
	env := mustEnv(t, map[string]string{"ENV": "test", "DB_MONGO_HOST": "localhost"})
	_, err := NewMongoDB(env)
	require.Error(t, err, "the database name is not in the URI, so it has to be checked")
	assert.Equal(t, "INVALID_CONFIG", err.GetCode())
}

// --- pagination ----------------------------------------------------------

// fakeMongo records what MongoPaginate asked for. Embedding the interface means
// a method this test does not exercise panics rather than quietly returning a
// zero value.
type fakeMongo struct {
	IMongoDB
	total        int64
	items        []map[string]any
	gotFilter    any
	gotOpts      MongoFindOptions
	gotPipeline  any
	gotPipelines []any
	gotAggOpts   []MongoAggregateOptions
	aggregateOut func(dest any)
	counted      int // exact counts asked for
	estimated    int // metadata counts asked for
}

func (f *fakeMongo) Count(string, any) (int64, IError) {
	f.counted++
	return f.total, nil
}

func (f *fakeMongo) EstimatedCount(string) (int64, IError) {
	f.estimated++
	return f.total, nil
}

func (f *fakeMongo) Find(dest any, _ string, filter any, opts ...MongoFindOptions) IError {
	f.gotFilter = filter
	if len(opts) > 0 {
		f.gotOpts = opts[0]
	}
	out := dest.(*[]map[string]any)
	*out = f.items
	return nil
}

func (f *fakeMongo) Aggregate(dest any, _ string, pipeline any, opts ...MongoAggregateOptions) IError {
	f.gotPipeline = pipeline
	f.gotPipelines = append(f.gotPipelines, pipeline)
	f.gotAggOpts = opts
	if f.aggregateOut != nil {
		f.aggregateOut(dest)
	}
	return nil
}

func TestMongoPaginate_translatesPageOptions(t *testing.T) {
	fake := &fakeMongo{
		total: 42,
		items: []map[string]any{{"id": 1}, {"id": 2}},
	}

	page, err := MongoPaginate[map[string]any](fake, "users", bson.M{"status": "active"},
		&PageOptions{Page: 3, Limit: 10, OrderBy: []string{"-created_at"}, Q: "ann"})
	require.NoError(t, err)

	assert.Equal(t, int64(20), fake.gotOpts.Skip, "page 3 of 10 skips the first 20")
	assert.Equal(t, int64(10), fake.gotOpts.Limit)
	assert.Equal(t, []string{"-created_at"}, fake.gotOpts.Sort)
	assert.Equal(t, bson.M{"status": "active"}, fake.gotFilter)

	assert.Equal(t, int64(42), page.Total)
	assert.Equal(t, int64(2), page.Count)
	assert.Equal(t, int64(3), page.Page)
	assert.Equal(t, "ann", page.Q)
	assert.Len(t, page.Items, 2)
}

func TestMongoPaginate_countsAnUnfilteredPageFromMetadata(t *testing.T) {
	// counting a whole collection walks it; the metadata already knows the
	// number, and a page header can live with the drift that costs
	unfiltered := map[string]any{
		"nil":          nil,
		"empty bson.M": bson.M{},
		"empty bson.D": bson.D{},
		"empty map":    map[string]any{},
		"nil bson.M":   bson.M(nil),
	}
	for name, filter := range unfiltered {
		t.Run(name, func(t *testing.T) {
			fake := &fakeMongo{total: 9, items: []map[string]any{{"id": 1}}}

			page, err := MongoPaginate[map[string]any](fake, "users", filter, nil)
			require.NoError(t, err)

			assert.Equal(t, 1, fake.estimated, "nothing narrows the count, so the metadata answers it")
			assert.Equal(t, 0, fake.counted, "no scan for a number the collection already knows")
			assert.Equal(t, int64(9), page.Total)
		})
	}

	t.Run("a filter still counts exactly", func(t *testing.T) {
		fake := &fakeMongo{total: 9, items: []map[string]any{{"id": 1}}}

		_, err := MongoPaginate[map[string]any](fake, "users", bson.M{"status": "active"}, nil)
		require.NoError(t, err)

		assert.Equal(t, 1, fake.counted, "the metadata cannot answer a filtered count")
		assert.Equal(t, 0, fake.estimated)
	})
}

func TestMongoPaginate_clampsAndDefaults(t *testing.T) {
	fake := &fakeMongo{total: 5, items: []map[string]any{{"id": 1}}}

	_, err := MongoPaginate[map[string]any](fake, "users", nil, nil)
	require.NoError(t, err)
	assert.Equal(t, PageLimitDefault, fake.gotOpts.Limit, "no options means the default page size")
	assert.Equal(t, int64(0), fake.gotOpts.Skip)

	_, err = MongoPaginate[map[string]any](fake, "users", nil, &PageOptions{Limit: PageLimitMax * 5, Page: 1})
	require.NoError(t, err)
	assert.Equal(t, PageLimitMax, fake.gotOpts.Limit, "a caller cannot ask for the whole collection")
}

func TestMongoPaginate_skipsTheQueryWhenEmpty(t *testing.T) {
	fake := &fakeMongo{total: 0}

	page, err := MongoPaginate[map[string]any](fake, "users", nil, nil)
	require.NoError(t, err)
	assert.Empty(t, page.Items)
	assert.NotNil(t, page.Items, "an empty page serialises as [] rather than null")
	assert.Nil(t, fake.gotFilter, "nothing to page through, so no second round trip")
}

func TestMongoPaginate_onADisabledHandle(t *testing.T) {
	_, err := MongoPaginate[map[string]any](NewNoopMongoDB(), "users", nil, nil)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrMongoDisabled))
}

func TestMongoAggregatePage_wrapsThePipelineInAFacet(t *testing.T) {
	// one pass, two outputs: counting in a second round trip would count a
	// collection that may have changed in between
	fake := &fakeMongo{}
	fake.aggregateOut = func(dest any) {
		out := dest.(*[]struct {
			Items []map[string]any `bson:"items"`
			Total []struct {
				N int64 `bson:"n"`
			} `bson:"total"`
		})
		*out = append(*out, struct {
			Items []map[string]any `bson:"items"`
			Total []struct {
				N int64 `bson:"n"`
			} `bson:"total"`
		}{
			Items: []map[string]any{{"id": 1}},
			Total: []struct {
				N int64 `bson:"n"`
			}{{N: 7}},
		})
	}

	stages := []map[string]any{
		{"$match": bson.M{"status": "active"}},
		{"$lookup": bson.M{"from": "orders", "as": "orders"}},
	}
	page, err := MongoAggregatePage[map[string]any](fake, "users", stages,
		&PageOptions{Page: 2, Limit: 10, OrderBy: []string{"-created_at"}},
		MongoAggregateOptions{AllowDiskUse: true})
	require.NoError(t, err)

	sent, ok := fake.gotPipeline.([]map[string]any)
	require.True(t, ok)
	require.Len(t, sent, 3, "the caller's stages, then the facet")
	assert.Contains(t, sent[0], "$match")
	assert.Contains(t, sent[1], "$lookup")

	facet, ok := sent[2]["$facet"].(map[string]any)
	require.True(t, ok, "the last stage is a $facet")
	items, ok := facet["items"].([]map[string]any)
	require.True(t, ok)
	require.Len(t, items, 3, "sort, skip, limit")
	assert.Contains(t, items[0], "$sort")
	assert.Equal(t, int64(10), items[1]["$skip"], "page 2 of 10")
	assert.Equal(t, int64(10), items[2]["$limit"])

	require.Len(t, fake.gotAggOpts, 1)
	assert.True(t, fake.gotAggOpts[0].AllowDiskUse, "options reach the driver")

	assert.Equal(t, int64(7), page.Total)
	assert.Len(t, page.Items, 1)
	assert.Equal(t, int64(2), page.Page)
}

func TestMongoAggregatePage_splitsTheCountOutOfALargePage(t *testing.T) {
	// $facet returns everything it produces inside one document, so a page that
	// PageLimitMax now allows but 16MB of BSON may not hold is read on its own
	fake := &fakeMongo{}
	fake.aggregateOut = func(dest any) {
		switch out := dest.(type) {
		case *[]struct {
			N int64 `bson:"n"`
		}:
			*out = append(*out, struct {
				N int64 `bson:"n"`
			}{N: 4200})
		case *[]map[string]any:
			*out = []map[string]any{{"id": 1}, {"id": 2}}
		}
	}

	stages := []map[string]any{{"$match": bson.M{"status": "active"}}}
	page, err := MongoAggregatePage[map[string]any](fake, "users", stages,
		&PageOptions{Page: 2, Limit: 1000, OrderBy: []string{"-created_at"}})
	require.NoError(t, err)

	require.Len(t, fake.gotPipelines, 2, "one round trip for the count, one for the page")

	counting, ok := fake.gotPipelines[0].([]map[string]any)
	require.True(t, ok)
	require.Len(t, counting, 2, "the caller's stage, then $count")
	assert.Contains(t, counting[0], "$match")
	assert.Equal(t, "n", counting[1]["$count"])

	paged, ok := fake.gotPipelines[1].([]map[string]any)
	require.True(t, ok)
	require.Len(t, paged, 4, "the caller's stage, then sort, skip, limit")
	assert.Contains(t, paged[0], "$match")
	assert.Contains(t, paged[1], "$sort")
	assert.Equal(t, int64(1000), paged[2]["$skip"], "page 2 of 1000")
	assert.Equal(t, int64(1000), paged[3]["$limit"])
	for _, stage := range paged {
		assert.NotContains(t, stage, "$facet", "nothing goes through a facet at this size")
	}

	assert.Equal(t, int64(4200), page.Total)
	assert.Len(t, page.Items, 2)
	assert.Equal(t, int64(2), page.Count)
}

func TestMongoAggregatePage_doesNotFetchALargePageThatCannotExist(t *testing.T) {
	fake := &fakeMongo{}

	page, err := MongoAggregatePage[map[string]any](fake, "users",
		[]map[string]any{{"$match": bson.M{"status": "active"}}},
		&PageOptions{Page: 1, Limit: 1000})
	require.NoError(t, err)

	assert.Len(t, fake.gotPipelines, 1, "the count came back empty, so there is no page to ask for")
	assert.Empty(t, page.Items)
	assert.NotNil(t, page.Items, "an empty page serialises as [] rather than null")
	assert.Equal(t, int64(0), page.Total)
}

func TestMongoAggregatePage_leavesTheCallersPipelineAlone(t *testing.T) {
	fake := &fakeMongo{}
	stages := []map[string]any{{"$match": bson.M{"a": 1}}}

	_, err := MongoAggregatePage[map[string]any](fake, "c", stages, &PageOptions{Page: 1, Limit: 5})
	require.NoError(t, err)
	assert.Len(t, stages, 1, "the slice the caller passed must not grow a facet")
}

func TestMongoAggregatePage_emptyResult(t *testing.T) {
	fake := &fakeMongo{} // aggregateOut unset: the pipeline matched nothing

	page, err := MongoAggregatePage[map[string]any](fake, "users", nil, nil)
	require.NoError(t, err)
	assert.Equal(t, int64(0), page.Total)
	assert.NotNil(t, page.Items, "an empty page serialises as [] rather than null")
}

func TestMongoEach_rejectsANilCursor(t *testing.T) {
	err := MongoEach(t.Context(), nil, func(map[string]any) error { return nil })
	require.Error(t, err)
	assert.Equal(t, "MONGO_CURSOR", err.GetCode())
}
