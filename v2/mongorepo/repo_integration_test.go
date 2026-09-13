//go:build integration

package mongorepo

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	core "github.com/pskclub/mine-core/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

// The repository against a real server. Run it the same way as the core Mongo
// suite:
//
//	docker run -p 27017:27017 mongo
//	APP_DB_MONGO_HOST=127.0.0.1 APP_DB_MONGO_NAME=coretest make test-integration

// person is scoped to one test run by its collection name, so a failed run
// leaves nothing behind for the next one.
type person struct {
	ID     string    `bson:"_id,omitempty"`
	Email  string    `bson:"email"`
	Name   string    `bson:"name"`
	Age    int       `bson:"age"`
	Status string    `bson:"status"`
	Joined time.Time `bson:"joined"`
}

var personCollection = "coretest_people"

func (person) CollectionName() string { return personCollection }

func newTestRepo(t *testing.T) *Repo[person] {
	t.Helper()

	env, err := core.NewEnvPath(t.TempDir())
	require.NoError(t, err)
	if env.Config().DBMongoName == "" {
		t.Skip("no APP_DB_MONGO_NAME configured")
	}
	db, err := core.NewMongoDB(env)
	if err != nil {
		t.Skipf("no reachable mongo: %v", err)
	}

	personCollection = "coretest_" + strings.ReplaceAll(t.Name(), "/", "_")
	repo := NewIn[person](db)
	t.Cleanup(func() {
		_, _ = repo.DeleteAll()
		_ = db.Close()
	})
	return repo
}

func seed(t *testing.T, repo *Repo[person], people ...person) []string {
	t.Helper()
	ids := make([]string, 0, len(people))
	for i := range people {
		require.NoError(t, repo.Create(&people[i]))
		ids = append(ids, people[i].ID)
	}
	return ids
}

func TestRepo_createFillsTheID(t *testing.T) {
	repo := newTestRepo(t)

	p := person{Email: "ann@example.com", Name: "ann"}
	require.NoError(t, repo.Create(&p))

	require.NotEmpty(t, p.ID, "Create fills the id, the way GORM fills a primary key")
	assert.NotContains(t, p.ID, "ObjectID")

	found, err := repo.ByID(p.ID).FindOne()
	require.NoError(t, err, "the filled id must find the document back")
	assert.Equal(t, "ann", found.Name)
}

func TestRepo_findOneAndFindAll(t *testing.T) {
	repo := newTestRepo(t)
	seed(t, repo,
		person{Name: "ann", Age: 30, Status: "active"},
		person{Name: "bob", Age: 20, Status: "active"},
		person{Name: "carl", Age: 40, Status: "banned"},
	)

	ann, err := repo.Eq("name", "ann").FindOne()
	require.NoError(t, err)
	assert.Equal(t, 30, ann.Age)

	_, err = repo.Eq("name", "nobody").FindOne()
	assert.True(t, errors.Is(err, core.ErrDocumentNotFound), "a miss is a 404")

	active, err := repo.Eq("status", "active").Sort("age").FindAll()
	require.NoError(t, err)
	require.Len(t, active, 2)
	assert.Equal(t, "bob", active[0].Name, "sorted ascending by age")

	// an inline filter reads like the GORM repository's FindAll(conds…)
	adults, err := repo.FindAll(bson.M{"age": bson.M{"$gte": 30}})
	require.NoError(t, err)
	assert.Len(t, adults, 2)
}

func TestRepo_chainComposesOnTheServer(t *testing.T) {
	repo := newTestRepo(t)
	seed(t, repo,
		person{Name: "ann", Age: 30, Status: "active"},
		person{Name: "bob", Age: 20, Status: "active"},
		person{Name: "carl", Age: 70, Status: "active"},
	)

	// the same-field AND the unit tests build must also mean this on the server
	people, err := repo.Eq("status", "active").Gte("age", 18).Lte("age", 65).Sort("age").FindAll()
	require.NoError(t, err)
	require.Len(t, people, 2)
	assert.Equal(t, "bob", people[0].Name)
	assert.Equal(t, "ann", people[1].Name)
}

func TestRepo_searchMatchesSubstringsCaseInsensitively(t *testing.T) {
	repo := newTestRepo(t)
	seed(t, repo,
		person{Name: "Ann Smith", Email: "ann@example.com"},
		person{Name: "Bob Jones", Email: "bob@example.com"},
	)

	found, err := repo.Search("ANN", "name", "email").FindAll()
	require.NoError(t, err)
	require.Len(t, found, 1)
	assert.Equal(t, "Ann Smith", found[0].Name)

	// a term full of metacharacters must not become a pattern of its own
	none, err := repo.Search(".*", "name").FindAll()
	require.NoError(t, err)
	assert.Empty(t, none, "the search term is escaped, so it matches nothing")
}

func TestRepo_projection(t *testing.T) {
	repo := newTestRepo(t)
	seed(t, repo, person{Name: "ann", Email: "ann@example.com", Age: 30})

	only, err := repo.Select("name").FindOne()
	require.NoError(t, err)
	assert.Equal(t, "ann", only.Name)
	assert.Empty(t, only.Email, "a field left out of Select does not come back")

	without, err := repo.Omit("email").FindOne()
	require.NoError(t, err)
	assert.Equal(t, "ann", without.Name)
	assert.Empty(t, without.Email)
	assert.Equal(t, 30, without.Age, "everything else does")
}

func TestRepo_countExistsAndPluck(t *testing.T) {
	repo := newTestRepo(t)
	seed(t, repo,
		person{Name: "ann", Status: "active"},
		person{Name: "bob", Status: "active"},
		person{Name: "carl", Status: "banned"},
	)

	n, err := repo.Eq("status", "active").Count()
	require.NoError(t, err)
	assert.Equal(t, int64(2), n)

	ok, err := repo.Eq("status", "banned").Exists()
	require.NoError(t, err)
	assert.True(t, ok)

	ok, err = repo.Eq("status", "nothing").Exists()
	require.NoError(t, err)
	assert.False(t, ok)

	var names []string
	require.NoError(t, repo.Eq("status", "active").Sort("name").Pluck("name", &names))
	assert.Equal(t, []string{"ann", "bob"}, names, "Pluck decodes into a typed slice")

	var statuses []string
	require.NoError(t, repo.Distinct("status", &statuses))
	assert.ElementsMatch(t, []string{"active", "banned"}, statuses)
}

func TestRepo_pagination(t *testing.T) {
	repo := newTestRepo(t)
	for i := range 25 {
		p := person{Name: fmt.Sprintf("p%02d", i), Age: i, Status: "active"}
		require.NoError(t, repo.Create(&p))
	}

	page, err := repo.Eq("status", "active").Sort("age").
		Pagination(&core.PageOptions{Page: 2, Limit: 10})
	require.NoError(t, err)

	assert.Equal(t, int64(25), page.Total)
	assert.Equal(t, int64(10), page.Count)
	require.Len(t, page.Items, 10)
	assert.Equal(t, 10, page.Items[0].Age, "the chain's Sort is the default ordering")
}

func TestRepo_each(t *testing.T) {
	repo := newTestRepo(t)
	for i := range 30 {
		p := person{Age: i, Status: "active"}
		require.NoError(t, repo.Create(&p))
	}

	var seen int
	require.NoError(t, repo.Eq("status", "active").Sort("age").Each(func(p person) error {
		assert.Equal(t, seen, p.Age)
		seen++
		return nil
	}))
	assert.Equal(t, 30, seen)
}

func TestRepo_updates(t *testing.T) {
	repo := newTestRepo(t)
	seed(t, repo,
		person{Name: "ann", Status: "new"},
		person{Name: "bob", Status: "new"},
	)

	res, err := repo.Eq("name", "ann").Updates(bson.M{"status": "active"})
	require.NoError(t, err)
	assert.Equal(t, int64(1), res.Matched)

	res, err = repo.Eq("status", "new").UpdateAll(bson.M{"status": "active"})
	require.NoError(t, err)
	assert.Equal(t, int64(1), res.Modified)

	// an operator map is passed through rather than wrapped in $set
	_, err = repo.Eq("name", "ann").Updates(bson.M{"$inc": bson.M{"age": 5}})
	require.NoError(t, err)
	ann, err := repo.Eq("name", "ann").FindOne()
	require.NoError(t, err)
	assert.Equal(t, 5, ann.Age)

	res, err = repo.Eq("name", "nobody").Updates(bson.M{"status": "x"})
	require.NoError(t, err, "matching nothing is not a failure")
	assert.Equal(t, int64(0), res.Matched, "...but the caller can tell")
}

func TestRepo_upsertAndSave(t *testing.T) {
	repo := newTestRepo(t)

	res, err := repo.Eq("email", "new@example.com").Upsert(bson.M{"name": "new"})
	require.NoError(t, err)
	assert.Equal(t, int64(1), res.Upserted)

	found, err := repo.Eq("email", "new@example.com").FindOne()
	require.NoError(t, err)
	assert.Equal(t, "new", found.Name)

	found.Name = "renamed"
	require.NoError(t, repo.Save(found), "Save matches on the document's own id")

	again, err := repo.ByID(found.ID).FindOne()
	require.NoError(t, err)
	assert.Equal(t, "renamed", again.Name)

	// a document with no id yet is inserted
	fresh := &person{Email: "fresh@example.com"}
	require.NoError(t, repo.Save(fresh))
	assert.NotEmpty(t, fresh.ID)
}

func TestRepo_delete(t *testing.T) {
	repo := newTestRepo(t)
	seed(t, repo,
		person{Name: "ann", Status: "stale"},
		person{Name: "bob", Status: "stale"},
		person{Name: "carl", Status: "active"},
	)

	n, err := repo.Eq("name", "ann").DeleteOne()
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)

	n, err = repo.Eq("status", "stale").Delete()
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)

	// the guard is not theoretical: without it this would empty the collection
	_, err = repo.Delete()
	require.Error(t, err)
	assert.Equal(t, "UNSCOPED_DELETE", err.GetCode())

	remaining, err := repo.Count()
	require.NoError(t, err)
	assert.Equal(t, int64(1), remaining)
}

func TestRepo_findOneOrCreate(t *testing.T) {
	repo := newTestRepo(t)

	created, err := repo.Eq("email", "ann@example.com").
		FindOneOrCreate(&person{Email: "ann@example.com", Name: "ann"})
	require.NoError(t, err)
	assert.NotEmpty(t, created.ID)

	found, err := repo.Eq("email", "ann@example.com").
		FindOneOrCreate(&person{Email: "ann@example.com", Name: "other"})
	require.NoError(t, err)
	assert.Equal(t, "ann", found.Name, "the second call finds rather than inserts")

	n, err := repo.Count()
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)
}

func TestRepo_findOneAndUpdateClaims(t *testing.T) {
	repo := newTestRepo(t)
	seed(t, repo,
		person{Name: "first", Status: "queued", Age: 1},
		person{Name: "second", Status: "queued", Age: 2},
	)

	claimed, err := repo.Eq("status", "queued").Sort("age").
		FindOneAndUpdate(bson.M{"status": "claimed"})
	require.NoError(t, err)
	assert.Equal(t, "first", claimed.Name, "Sort decides which one is claimed")
	assert.Equal(t, "claimed", claimed.Status, "the default is the document after the change")

	n, err := repo.Eq("status", "queued").Count()
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)

	popped, err := repo.Eq("status", "queued").FindOneAndDelete()
	require.NoError(t, err)
	assert.Equal(t, "second", popped.Name)
}

func TestRepo_aggregate(t *testing.T) {
	repo := newTestRepo(t)
	seed(t, repo,
		person{Status: "active", Age: 10},
		person{Status: "active", Age: 30},
		person{Status: "banned", Age: 99},
	)

	var stats []struct {
		ID  any     `bson:"_id"`
		Avg float64 `bson:"avg"`
	}
	require.NoError(t, repo.Eq("status", "active").Aggregate(&stats, []bson.M{
		{"$group": bson.M{"_id": nil, "avg": bson.M{"$avg": "$age"}}},
	}, core.MongoAggregateOptions{AllowDiskUse: true}))

	require.Len(t, stats, 1)
	assert.InDelta(t, 20.0, stats[0].Avg, 0.001, "the chain's filter scoped the pipeline")
}

func TestRepo_bulkWriteAndIndexes(t *testing.T) {
	repo := newTestRepo(t)
	require.NoError(t, repo.EnsureIndexes(
		core.MongoIndex{Keys: []string{"email"}, Unique: true, Name: "email_unique"},
	))

	res, err := repo.BulkWrite([]mongo.WriteModel{
		mongo.NewInsertOneModel().SetDocument(person{Email: "a@example.com"}),
		mongo.NewInsertOneModel().SetDocument(person{Email: "b@example.com"}),
	}, true)
	require.NoError(t, err)
	assert.Equal(t, int64(2), res.Inserted)

	_, err = repo.BulkWrite([]mongo.WriteModel{
		mongo.NewInsertOneModel().SetDocument(person{Email: "a@example.com"}),
	}, true)
	require.Error(t, err, "the unique index is real")
}

func TestRepo_insideATransaction(t *testing.T) {
	repo := newTestRepo(t)
	db := repo.DB()
	if err := db.Transaction(func(core.IMongoDB) error { return nil }); err != nil {
		t.Skipf("transactions need a replica set: %v", err)
	}

	err := db.Transaction(func(tx core.IMongoDB) error {
		// the repository is rebound to the transaction handle, so its writes
		// are part of it
		txRepo := NewIn[person](tx)
		if err := txRepo.Create(&person{Name: "rolled-back"}); err != nil {
			return err
		}
		return assert.AnError
	})
	require.Error(t, err)

	ok, err := repo.Eq("name", "rolled-back").Exists()
	require.NoError(t, err)
	assert.False(t, ok, "the repository's write was rolled back with the transaction")
}

// --- typed aggregation ---------------------------------------------------

type revenue struct {
	Status string  `bson:"_id"`
	Total  float64 `bson:"total"`
	Count  int     `bson:"count"`
}

func TestPipeline_groupAndSortOnTheServer(t *testing.T) {
	repo := newTestRepo(t)
	seed(t, repo,
		person{Status: "active", Age: 10},
		person{Status: "active", Age: 30},
		person{Status: "banned", Age: 100},
	)

	rows, err := Aggregate[revenue](repo).
		Group("$status", bson.M{
			"total": bson.M{"$sum": "$age"},
			"count": bson.M{"$sum": 1},
		}).
		Sort("-total").
		AllowDiskUse().
		Comment("coretest-revenue").
		All()
	require.NoError(t, err)

	require.Len(t, rows, 2)
	assert.Equal(t, "banned", rows[0].Status, "sorted by total, descending")
	assert.EqualValues(t, 100, rows[0].Total)
	assert.EqualValues(t, 40, rows[1].Total)
	assert.Equal(t, 2, rows[1].Count)
}

func TestPipeline_scopeBecomesTheMatch(t *testing.T) {
	repo := newTestRepo(t)
	seed(t, repo,
		person{Status: "active", Age: 10},
		person{Status: "banned", Age: 100},
	)

	rows, err := Aggregate[revenue](repo.Eq("status", "active")).
		Group("$status", bson.M{"total": bson.M{"$sum": "$age"}}).
		All()
	require.NoError(t, err)
	require.Len(t, rows, 1, "the repository scope filtered the pipeline")
	assert.EqualValues(t, 10, rows[0].Total)
}

func TestPipeline_correlatedLookup(t *testing.T) {
	repo := newTestRepo(t)
	orders := personCollection + "_orders"
	t.Cleanup(func() { _ = repo.DB().DropCollection(orders) })

	p := person{Name: "ann", Status: "active"}
	require.NoError(t, repo.Create(&p))
	oid, err := core.MongoObjectID(p.ID)
	require.NoError(t, err)
	_, err = repo.DB().InsertMany(orders, []any{
		bson.M{"user_id": oid, "total": 100, "status": "paid"},
		bson.M{"user_id": oid, "total": 250, "status": "paid"},
		bson.M{"user_id": oid, "total": 999, "status": "cancelled"},
	})
	require.NoError(t, err)

	type row struct {
		Name    string  `bson:"name"`
		Revenue float64 `bson:"revenue"`
	}
	rows, err := Aggregate[row](repo.Eq("status", "active")).
		Lookup(Lookup{
			From: orders,
			Let:  bson.M{"uid": "$_id"},
			Pipeline: []bson.M{{"$match": bson.M{"$expr": bson.M{"$and": []bson.M{
				{"$eq": []any{"$user_id", "$$uid"}},
				{"$eq": []any{"$status", "paid"}},
			}}}}},
			As: "orders",
		}).
		AddFields(bson.M{"revenue": bson.M{"$sum": "$orders.total"}}).
		Project(bson.M{"name": 1, "revenue": 1}).
		All()
	require.NoError(t, err)

	require.Len(t, rows, 1)
	assert.Equal(t, "ann", rows[0].Name)
	assert.EqualValues(t, 350, rows[0].Revenue, "the sub-pipeline excluded the cancelled order")
}

func TestPipeline_lookupOneEmbedsASingleDocument(t *testing.T) {
	repo := newTestRepo(t)
	profiles := personCollection + "_profiles"
	t.Cleanup(func() { _ = repo.DB().DropCollection(profiles) })

	withProfile := person{Name: "ann"}
	require.NoError(t, repo.Create(&withProfile))
	without := person{Name: "bob"}
	require.NoError(t, repo.Create(&without))

	oid, err := core.MongoObjectID(withProfile.ID)
	require.NoError(t, err)
	_, err = repo.DB().InsertOne(profiles, bson.M{"user_id": oid, "bio": "hello"})
	require.NoError(t, err)

	type row struct {
		Name    string `bson:"name"`
		Profile struct {
			Bio string `bson:"bio"`
		} `bson:"profile"`
	}
	rows, err := Aggregate[row](repo).
		LookupOne(Lookup{From: profiles, LocalField: "_id", ForeignField: "user_id", As: "profile"}).
		Sort("name").
		All()
	require.NoError(t, err)

	require.Len(t, rows, 2, "the person with no profile is kept, not dropped")
	assert.Equal(t, "hello", rows[0].Profile.Bio)
	assert.Empty(t, rows[1].Profile.Bio)
}

func TestPipeline_facetCountsAndLists(t *testing.T) {
	repo := newTestRepo(t)
	for i := range 12 {
		p := person{Age: i, Status: "active"}
		require.NoError(t, repo.Create(&p))
	}

	var facets []struct {
		Rows  []person `bson:"rows"`
		Total []struct {
			N int64 `bson:"n"`
		} `bson:"total"`
	}
	require.NoError(t, Aggregate[person](repo.Eq("status", "active")).
		Facet(map[string][]bson.M{
			"rows":  {{"$sort": bson.M{"age": 1}}, {"$limit": 3}},
			"total": {{"$count": "n"}},
		}).
		Into(&facets))

	require.Len(t, facets, 1)
	assert.Len(t, facets[0].Rows, 3)
	require.Len(t, facets[0].Total, 1)
	assert.Equal(t, int64(12), facets[0].Total[0].N)
}

func TestPipeline_pageCountAndEach(t *testing.T) {
	repo := newTestRepo(t)
	for i := range 25 {
		p := person{Age: i, Status: "active"}
		require.NoError(t, repo.Create(&p))
	}
	scoped := Aggregate[person](repo.Eq("status", "active"))

	n, err := scoped.Count()
	require.NoError(t, err)
	assert.Equal(t, int64(25), n)

	page, err := scoped.Page(&core.PageOptions{Page: 2, Limit: 10, OrderBy: []string{"age"}})
	require.NoError(t, err)
	assert.Equal(t, int64(25), page.Total)
	require.Len(t, page.Items, 10)
	assert.Equal(t, 10, page.Items[0].Age)

	var seen int
	require.NoError(t, scoped.Sort("age").Each(func(p person) error {
		assert.Equal(t, seen, p.Age)
		seen++
		return nil
	}))
	assert.Equal(t, 25, seen)

	first, err := scoped.Sort("-age").One()
	require.NoError(t, err)
	assert.Equal(t, 24, first.Age)
}

func TestPipeline_graphLookupFollowsAChain(t *testing.T) {
	repo := newTestRepo(t)
	boss := person{Name: "boss"}
	require.NoError(t, repo.Create(&boss))
	lead := person{Name: "lead", Email: boss.ID}
	require.NoError(t, repo.Create(&lead))
	dev := person{Name: "dev", Email: lead.ID}
	require.NoError(t, repo.Create(&dev))

	type row struct {
		Name  string `bson:"name"`
		Chain []struct {
			Name  string `bson:"name"`
			Depth int    `bson:"depth"`
		} `bson:"chain"`
	}
	rows, err := Aggregate[row](repo.Eq("name", "dev")).
		GraphLookup(GraphLookup{
			From: personCollection, StartWith: "email",
			ConnectFromField: "email", ConnectToField: "_id",
			As: "chain", DepthField: "depth",
		}).
		All()
	require.NoError(t, err)

	require.Len(t, rows, 1)
	assert.Len(t, rows[0].Chain, 2, "the whole chain up to the boss")
}

func TestPipeline_mergeMaterialisesARollup(t *testing.T) {
	repo := newTestRepo(t)
	rollup := personCollection + "_rollup"
	t.Cleanup(func() { _ = repo.DB().DropCollection(rollup) })

	seed(t, repo,
		person{Status: "active", Age: 10},
		person{Status: "active", Age: 20},
	)

	_, err := Aggregate[revenue](repo).
		Group("$status", bson.M{"total": bson.M{"$sum": "$age"}}).
		Merge(rollup, []string{"_id"}, "merge", "insert").
		All()
	require.NoError(t, err)

	var stored []revenue
	require.NoError(t, repo.DB().Find(&stored, rollup, bson.M{}))
	require.Len(t, stored, 1)
	assert.EqualValues(t, 30, stored[0].Total)
}
