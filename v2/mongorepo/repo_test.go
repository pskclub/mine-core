package mongorepo

import (
	"testing"
	"time"

	core "github.com/pskclub/mine-core/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
)

type user struct {
	ID     string    `bson:"_id,omitempty"`
	Email  string    `bson:"email"`
	Name   string    `bson:"name"`
	Age    int       `bson:"age"`
	Status string    `bson:"status"`
	Joined time.Time `bson:"joined"`
}

func (user) CollectionName() string { return "users" }

type objectIDUser struct {
	ID   bson.ObjectID `bson:"_id,omitempty"`
	Name string        `bson:"name"`
}

func (objectIDUser) CollectionName() string { return "oid_users" }

// newRepo builds a repository with no live connection: the query-building half
// needs none, and every finisher would fail loudly rather than quietly.
func newRepo[D core.IDocument](t *testing.T) *Repo[D] {
	t.Helper()
	return NewIn[D](core.NewNoopMongoDB())
}

func TestRepo_collectionComesFromTheDocument(t *testing.T) {
	assert.Equal(t, "users", newRepo[user](t).CollectionName())
	assert.Equal(t, "oid_users", newRepo[objectIDUser](t).CollectionName())
}

func TestRepo_chainIsCopyOnWrite(t *testing.T) {
	base := newRepo[user](t).Eq("status", "active")

	admins := base.Eq("role", "admin")
	guests := base.Eq("role", "guest")

	assert.Equal(t, bson.M{"status": "active"}, base.Filter(), "the base is untouched")
	assert.Equal(t, bson.M{"status": "active", "role": "admin"}, admins.Filter())
	assert.Equal(t, bson.M{"status": "active", "role": "guest"}, guests.Filter(),
		"two branches off one base must not contaminate each other")
}

func TestRepo_filterHelpers(t *testing.T) {
	repo := newRepo[user](t)

	assert.Equal(t, bson.M{"age": bson.M{"$gte": 18}}, repo.Gte("age", 18).Filter())
	assert.Equal(t, bson.M{"age": bson.M{"$ne": 0}}, repo.Ne("age", 0).Filter())
	assert.Equal(t, bson.M{"status": bson.M{"$in": []any{"a", "b"}}},
		repo.In("status", "a", "b").Filter())
	assert.Equal(t, bson.M{"status": bson.M{"$nin": []any{"x"}}}, repo.NotIn("status", "x").Filter())
	assert.Equal(t, bson.M{"age": bson.M{"$gte": 18, "$lte": 65}}, repo.Between("age", 18, 65).Filter())
	assert.Equal(t, bson.M{"deleted_at": bson.M{"$exists": false}},
		repo.HasField("deleted_at", false).Filter())
}

func TestRepo_whereOnTheSameFieldBecomesAnAnd(t *testing.T) {
	// merging would silently drop the first condition, which is the kind of bug
	// that shows up as "the date filter does nothing"
	filter := newRepo[user](t).
		Gte("age", 18).
		Lte("age", 65).
		Filter()

	conditions, ok := filter["$and"].([]bson.M)
	require.True(t, ok, "got %#v", filter)
	require.Len(t, conditions, 2)
	assert.Equal(t, bson.M{"age": bson.M{"$gte": 18}}, conditions[0])
	assert.Equal(t, bson.M{"age": bson.M{"$lte": 65}}, conditions[1])
}

func TestRepo_andStaysFlat(t *testing.T) {
	filter := newRepo[user](t).
		Eq("age", 1).Eq("age", 2).Eq("age", 3).
		Filter()

	conditions, ok := filter["$and"].([]bson.M)
	require.True(t, ok)
	assert.Len(t, conditions, 3, "a third condition must not nest another $and")
}

func TestRepo_orGroupsAlternatives(t *testing.T) {
	filter := newRepo[user](t).
		Eq("status", "active").
		Or(bson.M{"role": "admin"}, bson.M{"role": "owner"}).
		Filter()

	assert.Equal(t, "active", filter["status"], "the OR does not swallow what came before")
	ors, ok := filter["$or"].([]bson.M)
	require.True(t, ok)
	assert.Len(t, ors, 2)
}

func TestRepo_searchEscapesTheTerm(t *testing.T) {
	// a term like ".*" would otherwise match everything, and "(" would be an
	// invalid pattern the server rejects
	filter := newRepo[user](t).Search("a.b*c(", "name", "email").Filter()

	ors, ok := filter["$or"].([]bson.M)
	require.True(t, ok)
	require.Len(t, ors, 2)

	pattern, ok := ors[0]["name"].(bson.Regex)
	require.True(t, ok)
	assert.Equal(t, `a\.b\*c\(`, pattern.Pattern)
	assert.Equal(t, "i", pattern.Options, "search is case-insensitive")
}

func TestRepo_searchWithNothingToSearchIsANoop(t *testing.T) {
	assert.Empty(t, newRepo[user](t).Search("", "name").Filter())
	assert.Empty(t, newRepo[user](t).Search("ann").Filter())
}

func TestRepo_byID(t *testing.T) {
	oid := bson.NewObjectID()
	assert.Equal(t, bson.M{"_id": oid}, newRepo[user](t).ByID(oid.Hex()).Filter())
	assert.Equal(t, bson.M{"_id": "custom"}, newRepo[user](t).ByID("custom").Filter())
}

func TestRepo_findOptions(t *testing.T) {
	repo := newRepo[user](t).
		Sort("-created_at").Sort("name").
		Limit(10).Skip(20).
		Select("name", "email")

	opts := repo.FindOptions()
	assert.Equal(t, []string{"-created_at", "name"}, opts.Sort, "sorts accumulate, in order")
	assert.Equal(t, int64(10), opts.Limit)
	assert.Equal(t, int64(20), opts.Skip)
	assert.Equal(t, bson.M{"name": 1, "email": 1}, opts.Projection)

	assert.Equal(t, bson.M{"password": 0}, newRepo[user](t).Omit("password").FindOptions().Projection)
}

func TestRepo_sortDoesNotLeakBetweenBranches(t *testing.T) {
	base := newRepo[user](t).Sort("name")
	newest := base.Sort("-created_at")

	assert.Equal(t, []string{"name"}, base.FindOptions().Sort)
	assert.Equal(t, []string{"name", "-created_at"}, newest.FindOptions().Sort)
}

func TestUpdateDocument(t *testing.T) {
	assert.Equal(t, bson.M{"$set": bson.M{"name": "bob"}}, updateDocument(bson.M{"name": "bob"}),
		"a plain field map is wrapped in $set")
	assert.Equal(t, bson.M{"$inc": bson.M{"views": 1}}, updateDocument(bson.M{"$inc": bson.M{"views": 1}}),
		"a map that already speaks in operators is passed through")
}

func TestSetDocumentID_stringID(t *testing.T) {
	// the repository fills the id after Create, the way GORM fills a primary key
	u := &user{Name: "ann"}
	setDocumentID(u, "507f1f77bcf86cd799439011")
	assert.Equal(t, "507f1f77bcf86cd799439011", u.ID)

	existing := &user{ID: "keep-me"}
	setDocumentID(existing, "507f1f77bcf86cd799439011")
	assert.Equal(t, "keep-me", existing.ID, "an id that was already set is left alone")
}

func TestSetDocumentID_objectID(t *testing.T) {
	oid := bson.NewObjectID()
	u := &objectIDUser{Name: "ann"}
	setDocumentID(u, oid.Hex())
	assert.Equal(t, oid, u.ID)
}

func TestSetDocumentID_survivesOddInput(t *testing.T) {
	assert.NotPanics(t, func() {
		setDocumentID(nil, "x")
		setDocumentID((*user)(nil), "x")
		setDocumentID(user{}, "x") // not a pointer: nothing to write to
		setDocumentID(&struct{ Name string }{}, "x")
	})
}

func TestDocumentID(t *testing.T) {
	assert.Equal(t, "abc", documentID(&user{ID: "abc"}))
	assert.Empty(t, documentID(&user{}))

	oid := bson.NewObjectID()
	assert.Equal(t, oid.Hex(), documentID(&objectIDUser{ID: oid}))
	assert.Empty(t, documentID(&objectIDUser{}), "a zero ObjectID is not an id")
}

func TestRepo_deleteWithoutAFilterIsRefused(t *testing.T) {
	// the forgotten-filter case: this would otherwise empty the collection
	_, err := newRepo[user](t).Delete()
	require.Error(t, err)
	assert.Equal(t, "UNSCOPED_DELETE", err.GetCode())

	// the deliberate version says so
	_, err = newRepo[user](t).DeleteAll()
	require.Error(t, err)
	assert.Equal(t, "MONGO_DISABLED", err.GetCode(), "it reached the driver instead of being refused")
}

func TestRepo_finishersFailLoudlyWithoutAConnection(t *testing.T) {
	repo := newRepo[user](t).Eq("status", "active")

	_, err := repo.FindOne()
	require.Error(t, err)
	assert.Equal(t, "MONGO_DISABLED", err.GetCode())

	_, err = repo.Count()
	assert.Error(t, err)
	_, err = repo.Pagination(nil)
	assert.Error(t, err)
	assert.Error(t, repo.Create(&user{}))
}

func TestRepo_aggregatePrependsTheScope(t *testing.T) {
	fake := &fakeMongo{}
	repo := NewIn[user](fake).Eq("status", "active")

	var out []bson.M
	require.NoError(t, repo.Aggregate(&out, []bson.M{{"$group": bson.M{"_id": "$age"}}}))

	stages, ok := fake.pipeline.([]bson.M)
	require.True(t, ok)
	require.Len(t, stages, 2)
	assert.Equal(t, bson.M{"$match": bson.M{"status": "active"}}, stages[0],
		"the chain's filter becomes the leading $match")
	assert.Contains(t, stages[1], "$group")
}

func TestRepo_aggregateWithoutAScopeAddsNoMatch(t *testing.T) {
	fake := &fakeMongo{}
	var out []bson.M
	require.NoError(t, NewIn[user](fake).Aggregate(&out, []bson.M{{"$count": "n"}}))

	stages, _ := fake.pipeline.([]bson.M)
	require.Len(t, stages, 1, "an empty scope must not add an empty $match")
}

func TestRepo_paginationUsesTheChainsSortAsTheDefault(t *testing.T) {
	fake := &fakeMongo{}
	repo := NewIn[user](fake).Eq("status", "active").Sort("-joined")

	_, err := repo.Pagination(&core.PageOptions{Page: 1, Limit: 10})
	require.NoError(t, err)
	assert.Equal(t, []string{"-joined"}, fake.findOpts.Sort)

	// an explicit OrderBy from the request wins over the chain's default
	_, err = repo.Pagination(&core.PageOptions{Page: 1, Limit: 10, OrderBy: []string{"name"}})
	require.NoError(t, err)
	assert.Equal(t, []string{"name"}, fake.findOpts.Sort)
}

// fakeMongo records what the repository asked the driver for. Embedding the
// interface means a method this test does not exercise panics rather than
// quietly returning a zero value.
type fakeMongo struct {
	core.IMongoDB
	pipeline any
	filter   any
	findOpts core.MongoFindOptions
}

func (f *fakeMongo) Aggregate(_ any, _ string, pipeline any, _ ...core.MongoAggregateOptions) core.IError {
	f.pipeline = pipeline
	return nil
}

func (f *fakeMongo) Count(string, any) (int64, core.IError) { return 1, nil }

func (f *fakeMongo) Find(_ any, _ string, filter any, opts ...core.MongoFindOptions) core.IError {
	f.filter = filter
	if len(opts) > 0 {
		f.findOpts = opts[0]
	}
	return nil
}

// --- nested fields -------------------------------------------------------

func TestRepo_nestedFilters(t *testing.T) {
	repo := newRepo[user](t)

	assert.Equal(t, bson.M{"profile.city": "BKK"}, repo.Eq("profile.city", "BKK").Filter(),
		"a dotted path is a field like any other")
	assert.Equal(t, bson.M{"profile.age": bson.M{"$gte": 18}}, repo.Gte("profile.age", 18).Filter())
}

func TestRepo_elemMatchIsNotTheSameAsTwoDottedConditions(t *testing.T) {
	// two dotted conditions can be satisfied by two *different* array elements;
	// $elemMatch demands one element satisfy both
	loose := newRepo[user](t).Eq("items.sku", "A").Eq("items.qty", 2).Filter()
	assert.Equal(t, bson.M{"items.sku": "A", "items.qty": 2}, loose)

	strict := newRepo[user](t).ElemMatch("items", bson.M{"sku": "A", "qty": 2}).Filter()
	assert.Equal(t, bson.M{"items": bson.M{"$elemMatch": bson.M{"sku": "A", "qty": 2}}}, strict)
}

func TestRepo_arrayHelpers(t *testing.T) {
	repo := newRepo[user](t)
	assert.Equal(t, bson.M{"tags": bson.M{"$size": 3}}, repo.HasSize("tags", 3).Filter())
	assert.Equal(t, bson.M{"tags": bson.M{"$all": []any{"a", "b"}}},
		repo.HasAll("tags", "a", "b").Filter())
}

func TestLookupPath_walksIntoSubdocuments(t *testing.T) {
	row := bson.M{"profile": bson.M{"city": "BKK"}}
	assert.Equal(t, []any{"BKK"}, lookupPath(row, "profile.city"))
	assert.Nil(t, lookupPath(row, "profile.zip"), "a missing leaf yields nothing")
	assert.Nil(t, lookupPath(row, "nope.city"))
}

func TestLookupPath_flattensArrays(t *testing.T) {
	// Pluck("items.sku") must reach into every element, the way Mongo does
	row := bson.M{"items": bson.A{
		bson.M{"sku": "A"},
		bson.M{"sku": "B"},
		bson.M{"other": 1},
	}}
	assert.Equal(t, []any{"A", "B"}, lookupPath(row, "items.sku"))

	nested := bson.M{"order": bson.M{"items": bson.A{bson.M{"sku": "A"}}}}
	assert.Equal(t, []any{"A"}, lookupPath(nested, "order.items.sku"))

	// a leaf that is itself an array is flattened, not returned as one value
	tags := bson.M{"tags": bson.A{"a", "b"}}
	assert.Equal(t, []any{"a", "b"}, lookupPath(tags, "tags"))
}

func TestRepo_updateOptionsReachTheDriver(t *testing.T) {
	// a positional "$[name]" update is useless without its array filters
	fake := &recordingMongo{}
	_, err := NewIn[user](fake).Eq("_id", "u1").Updates(
		bson.M{"scores.$[low].value": 50},
		core.MongoUpdateOptions{ArrayFilters: []any{bson.M{"low.value": bson.M{"$lt": 50}}}},
	)
	require.NoError(t, err)

	require.Len(t, fake.updateOpts, 1)
	assert.Len(t, fake.updateOpts[0].ArrayFilters, 1)
	assert.Equal(t, bson.M{"$set": bson.M{"scores.$[low].value": 50}}, fake.update)
}

func TestRepo_upsertKeepsTheCallersOptions(t *testing.T) {
	fake := &recordingMongo{}
	_, err := NewIn[user](fake).Eq("email", "a@b.c").Upsert(
		bson.M{"name": "ann"},
		core.MongoUpdateOptions{Hint: "email_1"},
	)
	require.NoError(t, err)

	require.Len(t, fake.updateOpts, 1)
	assert.True(t, fake.updateOpts[0].Upsert, "Upsert sets its own flag")
	assert.Equal(t, "email_1", fake.updateOpts[0].Hint, "...without discarding the rest")
}

// recordingMongo captures a single write for assertion.
type recordingMongo struct {
	core.IMongoDB
	update     any
	updateOpts []core.MongoUpdateOptions
}

func (f *recordingMongo) UpdateOne(_ string, _, update any, opts ...core.MongoUpdateOptions) (core.MongoUpdateResult, core.IError) {
	f.update = update
	f.updateOpts = opts
	return core.MongoUpdateResult{Matched: 1}, nil
}
