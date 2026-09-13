//go:build integration

package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"
)

// These exercise what only a real server can: decoding, sorting, upserts,
// duplicate-key detection, indexes, aggregation and transactions. The unit tests
// cover the URI, the sort translation and the id rendering without one.
//
//	docker run -p 27017:27017 mongo
//	APP_DB_MONGO_HOST=127.0.0.1 APP_DB_MONGO_NAME=coretest make test-integration
//
// Transactions need a replica set:
//
//	docker run -p 27017:27017 mongo --replSet rs0   # then rs.initiate()
//	APP_DB_MONGO_REPLICA_NAME=rs0 ... make test-integration

type mongoUser struct {
	ID     string `bson:"_id,omitempty"`
	Name   string `bson:"name"`
	Email  string `bson:"email"`
	Age    int    `bson:"age"`
	Status string `bson:"status"`
}

func newTestMongo(t *testing.T) (IMongoDB, string) {
	t.Helper()

	env, err := NewEnvPath(t.TempDir())
	require.NoError(t, err)
	if env.Config().DBMongoName == "" {
		t.Skip("no APP_DB_MONGO_NAME configured")
	}

	m, err := NewMongoDB(env)
	if err != nil {
		t.Skipf("no reachable mongo: %v", err)
	}

	// every test gets its own collection, so a failed run leaves nothing behind
	collection := "coretest_" + strings.ReplaceAll(t.Name(), "/", "_")
	t.Cleanup(func() {
		_, _ = m.DeleteMany(collection, bson.M{})
		_ = m.Close()
	})
	return m, collection
}

func TestRealMongo_insertedIDIsUsableAsAFilter(t *testing.T) {
	// the regression this whole pass started from: InsertOne used to return
	// `ObjectID("507f…")`, and the docs told you to filter with it
	m, coll := newTestMongo(t)

	id, err := m.InsertOne(coll, mongoUser{Name: "ann", Email: "ann@example.com"})
	require.NoError(t, err)
	assert.NotContains(t, id, "ObjectID")
	assert.Len(t, id, 24, "a hex object id")

	var got mongoUser
	require.NoError(t, m.FindOne(&got, coll, MongoByID(id)), "the id must find the document back")
	assert.Equal(t, "ann", got.Name)

	res, err := m.UpdateOne(coll, MongoByID(id), bson.M{"$set": bson.M{"name": "bob"}})
	require.NoError(t, err)
	assert.Equal(t, int64(1), res.Matched, "the update must reach the document, not zero of them")
}

func TestRealMongo_findOneMissIsA404(t *testing.T) {
	m, coll := newTestMongo(t)

	var got mongoUser
	err := m.FindOne(&got, coll, bson.M{"email": "nobody@example.com"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrDocumentNotFound))
	assert.Equal(t, 404, err.GetStatus())
	assert.Equal(t, "NOT_FOUND", err.GetCode())
}

func TestRealMongo_findWithSortLimitSkipAndProjection(t *testing.T) {
	m, coll := newTestMongo(t)
	for i := range 5 {
		_, err := m.InsertOne(coll, mongoUser{
			Name: fmt.Sprintf("user-%d", i), Age: i, Status: "active",
			Email: fmt.Sprintf("u%d@example.com", i),
		})
		require.NoError(t, err)
	}

	var users []mongoUser
	require.NoError(t, m.Find(&users, coll, bson.M{"status": "active"}, MongoFindOptions{
		Sort: []string{"-age"}, Limit: 2,
	}))
	require.Len(t, users, 2)
	assert.Equal(t, 4, users[0].Age, "sorted descending")
	assert.Equal(t, 3, users[1].Age)

	var skipped []mongoUser
	require.NoError(t, m.Find(&skipped, coll, nil, MongoFindOptions{Sort: []string{"age"}, Skip: 3}))
	require.Len(t, skipped, 2)
	assert.Equal(t, 3, skipped[0].Age)

	var hidden []mongoUser
	require.NoError(t, m.Find(&hidden, coll, nil, MongoFindOptions{
		Limit: 1, Projection: bson.M{"email": 0},
	}))
	require.Len(t, hidden, 1)
	assert.Empty(t, hidden[0].Email, "a projected-out field stays out")
	assert.NotEmpty(t, hidden[0].Name)
}

func TestRealMongo_countAndExists(t *testing.T) {
	m, coll := newTestMongo(t)
	for range 3 {
		_, err := m.InsertOne(coll, mongoUser{Status: "active"})
		require.NoError(t, err)
	}

	n, err := m.Count(coll, bson.M{"status": "active"})
	require.NoError(t, err)
	assert.Equal(t, int64(3), n)

	n, err = m.Count(coll, nil)
	require.NoError(t, err)
	assert.Equal(t, int64(3), n, "a nil filter counts the collection")

	ok, err := m.Exists(coll, bson.M{"status": "active"})
	require.NoError(t, err)
	assert.True(t, ok)

	ok, err = m.Exists(coll, bson.M{"status": "deleted"})
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestRealMongo_insertManyAndUpdateMany(t *testing.T) {
	m, coll := newTestMongo(t)

	ids, err := m.InsertMany(coll, []any{
		mongoUser{Name: "a", Status: "new"},
		mongoUser{Name: "b", Status: "new"},
	})
	require.NoError(t, err)
	require.Len(t, ids, 2)
	assert.Len(t, ids[0], 24)

	res, err := m.UpdateMany(coll, bson.M{"status": "new"}, bson.M{"$set": bson.M{"status": "active"}})
	require.NoError(t, err)
	assert.Equal(t, int64(2), res.Matched)
	assert.Equal(t, int64(2), res.Modified)
}

func TestRealMongo_updateReportsWhatItDid(t *testing.T) {
	m, coll := newTestMongo(t)

	res, err := m.UpdateOne(coll, bson.M{"email": "nobody@example.com"},
		bson.M{"$set": bson.M{"name": "x"}})
	require.NoError(t, err, "matching nothing is not a failure")
	assert.Equal(t, int64(0), res.Matched, "...but the caller can tell")

	res, err = m.UpdateOne(coll, bson.M{"email": "new@example.com"},
		bson.M{"$set": bson.M{"name": "new"}}, MongoUpdateOptions{Upsert: true})
	require.NoError(t, err)
	assert.Equal(t, int64(1), res.Upserted)
	assert.Len(t, res.UpsertedID, 24, "the upserted id comes back usable")

	var got mongoUser
	require.NoError(t, m.FindOne(&got, coll, MongoByID(res.UpsertedID)))
	assert.Equal(t, "new", got.Name)
}

func TestRealMongo_deleteReportsCounts(t *testing.T) {
	m, coll := newTestMongo(t)
	for range 3 {
		_, err := m.InsertOne(coll, mongoUser{Status: "stale"})
		require.NoError(t, err)
	}

	n, err := m.DeleteOne(coll, bson.M{"status": "stale"})
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)

	n, err = m.DeleteMany(coll, bson.M{"status": "stale"})
	require.NoError(t, err)
	assert.Equal(t, int64(2), n)

	n, err = m.DeleteMany(coll, bson.M{"status": "stale"})
	require.NoError(t, err)
	assert.Equal(t, int64(0), n, "deleting nothing is not an error")
}

func TestRealMongo_duplicateKeyIsAConflict(t *testing.T) {
	m, coll := newTestMongo(t)
	require.NoError(t, m.EnsureIndex(coll, MongoIndex{Keys: []string{"email"}, Unique: true}))
	require.NoError(t, m.EnsureIndex(coll, MongoIndex{Keys: []string{"email"}, Unique: true}),
		"creating the same index twice is safe, so it can run on every boot")

	_, err := m.InsertOne(coll, mongoUser{Email: "taken@example.com"})
	require.NoError(t, err)

	_, err = m.InsertOne(coll, mongoUser{Email: "taken@example.com"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrDuplicateKey), "a taken email is a conflict to answer")
	assert.Equal(t, 409, err.GetStatus())
	assert.Equal(t, "DUPLICATE_KEY", err.GetCode())
}

func TestRealMongo_ttlIndex(t *testing.T) {
	m, coll := newTestMongo(t)
	require.NoError(t, m.EnsureIndex(coll, MongoIndex{
		Keys: []string{"created_at"}, TTL: time.Hour, Name: "created_at_ttl",
	}))

	var indexes []bson.M
	cur, err := m.Collection(coll).Indexes().List(m.Context())
	require.NoError(t, err)
	require.NoError(t, cur.All(m.Context(), &indexes))

	var found bool
	for _, idx := range indexes {
		if idx["name"] == "created_at_ttl" {
			found = true
			assert.EqualValues(t, 3600, idx["expireAfterSeconds"])
		}
	}
	assert.True(t, found, "the ttl index was created")
}

func TestRealMongo_distinctAndAggregate(t *testing.T) {
	m, coll := newTestMongo(t)
	_, err := m.InsertMany(coll, []any{
		mongoUser{Status: "active", Age: 10},
		mongoUser{Status: "active", Age: 20},
		mongoUser{Status: "banned", Age: 30},
	})
	require.NoError(t, err)

	var statuses []string
	require.NoError(t, m.Distinct(&statuses, coll, "status", nil),
		"distinct decodes into a typed slice, not []any")
	assert.ElementsMatch(t, []string{"active", "banned"}, statuses)

	var totals []struct {
		Status string `bson:"_id"`
		Total  int    `bson:"total"`
	}
	require.NoError(t, m.Aggregate(&totals, coll, []bson.M{
		{"$group": bson.M{"_id": "$status", "total": bson.M{"$sum": "$age"}}},
		{"$sort": bson.M{"_id": 1}},
	}))
	require.Len(t, totals, 2)
	assert.Equal(t, "active", totals[0].Status)
	assert.Equal(t, 30, totals[0].Total)
}

func TestRealMongo_complexAggregation(t *testing.T) {
	// $lookup, $unwind, $facet, $graphLookup and $let in one pipeline: the point
	// is that the stage list is passed through untouched, so nothing is off limits
	m, coll := newTestMongo(t)
	orders := coll + "_orders"
	t.Cleanup(func() { _ = m.DropCollection(orders) })

	uid, err := m.InsertOne(coll, mongoUser{Name: "ann", Status: "active", Age: 30})
	require.NoError(t, err)
	oid, err := MongoObjectID(uid)
	require.NoError(t, err)
	_, err = m.InsertMany(orders, []any{
		bson.M{"user_id": oid, "total": 100, "status": "paid"},
		bson.M{"user_id": oid, "total": 250, "status": "paid"},
		bson.M{"user_id": oid, "total": 999, "status": "cancelled"},
	})
	require.NoError(t, err)

	var result []struct {
		Name    string `bson:"name"`
		Revenue int    `bson:"revenue"`
		Orders  int    `bson:"orders"`
	}
	require.NoError(t, m.Aggregate(&result, coll, []bson.M{
		{"$match": bson.M{"status": "active"}},
		{"$lookup": bson.M{
			"from": orders,
			"let":  bson.M{"uid": "$_id"},
			"pipeline": []bson.M{
				{"$match": bson.M{"$expr": bson.M{"$and": []bson.M{
					{"$eq": []any{"$user_id", "$$uid"}},
					{"$eq": []any{"$status", "paid"}},
				}}}},
			},
			"as": "orders",
		}},
		{"$addFields": bson.M{
			"revenue": bson.M{"$sum": "$orders.total"},
			"orders":  bson.M{"$size": "$orders"},
		}},
		{"$project": bson.M{"name": 1, "revenue": 1, "orders": 1}},
	}, MongoAggregateOptions{AllowDiskUse: true, Comment: "coretest-revenue"}))

	require.Len(t, result, 1)
	assert.Equal(t, "ann", result[0].Name)
	assert.Equal(t, 350, result[0].Revenue, "the cancelled order is excluded by the sub-pipeline")
	assert.Equal(t, 2, result[0].Orders)
}

func TestRealMongo_aggregateFacet(t *testing.T) {
	m, coll := newTestMongo(t)
	_, err := m.InsertMany(coll, []any{
		mongoUser{Status: "active", Age: 20},
		mongoUser{Status: "active", Age: 40},
		mongoUser{Status: "banned", Age: 60},
	})
	require.NoError(t, err)

	var facets []struct {
		ByStatus []struct {
			ID string `bson:"_id"`
			N  int    `bson:"n"`
		} `bson:"by_status"`
		Stats []struct {
			Avg float64 `bson:"avg"`
		} `bson:"stats"`
	}
	require.NoError(t, m.Aggregate(&facets, coll, []bson.M{
		{"$facet": bson.M{
			"by_status": []bson.M{{"$group": bson.M{"_id": "$status", "n": bson.M{"$sum": 1}}}},
			"stats":     []bson.M{{"$group": bson.M{"_id": nil, "avg": bson.M{"$avg": "$age"}}}},
		}},
	}))

	require.Len(t, facets, 1)
	assert.Len(t, facets[0].ByStatus, 2)
	require.Len(t, facets[0].Stats, 1)
	assert.InDelta(t, 40.0, facets[0].Stats[0].Avg, 0.001)
}

func TestRealMongo_aggregateCursorStreams(t *testing.T) {
	m, coll := newTestMongo(t)
	for i := range 50 {
		_, err := m.InsertOne(coll, mongoUser{Age: i, Status: "active"})
		require.NoError(t, err)
	}

	cur, err := m.AggregateCursor(coll, []bson.M{
		{"$match": bson.M{"status": "active"}},
		{"$sort": bson.M{"age": 1}},
	}, MongoAggregateOptions{AllowDiskUse: true, BatchSize: 10})
	require.NoError(t, err)

	var seen int
	require.NoError(t, MongoEach(m.Context(), cur, func(u mongoUser) error {
		assert.Equal(t, seen, u.Age, "documents arrive in order")
		seen++
		return nil
	}))
	assert.Equal(t, 50, seen, "the whole result set is streamed, never held")
}

func TestRealMongo_eachStopsOnError(t *testing.T) {
	m, coll := newTestMongo(t)
	for range 10 {
		_, err := m.InsertOne(coll, mongoUser{Status: "active"})
		require.NoError(t, err)
	}

	cur, err := m.FindCursor(coll, nil)
	require.NoError(t, err)

	stop := errors.New("enough")
	var seen int
	err2 := MongoEach(m.Context(), cur, func(mongoUser) error {
		seen++
		if seen == 3 {
			return stop
		}
		return nil
	})
	require.Error(t, err2)
	assert.Equal(t, 3, seen)
}

func TestRealMongo_aggregatePage(t *testing.T) {
	m, coll := newTestMongo(t)
	for i := range 25 {
		_, err := m.InsertOne(coll, mongoUser{Name: fmt.Sprintf("u%02d", i), Age: i, Status: "active"})
		require.NoError(t, err)
	}

	page, err := MongoAggregatePage[mongoUser](m, coll, []map[string]any{
		{"$match": bson.M{"status": "active"}},
	}, &PageOptions{Page: 2, Limit: 10, OrderBy: []string{"age"}})
	require.NoError(t, err)

	assert.Equal(t, int64(25), page.Total, "the facet counts the whole match, not the page")
	require.Len(t, page.Items, 10)
	assert.Equal(t, 10, page.Items[0].Age)
}

func TestRealMongo_findOneAndUpdateClaimsAtomically(t *testing.T) {
	// the queue-pop shape: a Find followed by an Update is a race
	m, coll := newTestMongo(t)
	_, err := m.InsertMany(coll, []any{
		bson.M{"name": "first", "status": "queued", "seq": 1},
		bson.M{"name": "second", "status": "queued", "seq": 2},
	})
	require.NoError(t, err)

	var claimed struct {
		Name   string `bson:"name"`
		Status string `bson:"status"`
	}
	require.NoError(t, m.FindOneAndUpdate(&claimed, coll,
		bson.M{"status": "queued"},
		bson.M{"$set": bson.M{"status": "claimed"}},
		MongoFindModifyOptions{Sort: []string{"seq"}, ReturnNew: true}))

	assert.Equal(t, "first", claimed.Name, "Sort decides which one is claimed")
	assert.Equal(t, "claimed", claimed.Status, "ReturnNew gives the document after the change")

	n, err := m.Count(coll, bson.M{"status": "queued"})
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)

	var none struct{}
	err2 := m.FindOneAndUpdate(&none, coll, bson.M{"status": "nothing-here"},
		bson.M{"$set": bson.M{"status": "x"}})
	assert.True(t, errors.Is(err2, ErrDocumentNotFound), "claiming nothing is a miss, not a failure")
}

func TestRealMongo_findOneAndDelete(t *testing.T) {
	m, coll := newTestMongo(t)
	_, err := m.InsertOne(coll, mongoUser{Name: "taken", Status: "queued"})
	require.NoError(t, err)

	var got mongoUser
	require.NoError(t, m.FindOneAndDelete(&got, coll, bson.M{"status": "queued"}))
	assert.Equal(t, "taken", got.Name)

	ok, err := m.Exists(coll, bson.M{"status": "queued"})
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestRealMongo_bulkWrite(t *testing.T) {
	m, coll := newTestMongo(t)
	_, err := m.InsertOne(coll, mongoUser{Email: "existing@example.com", Name: "old"})
	require.NoError(t, err)

	res, err := m.BulkWrite(coll, []mongo.WriteModel{
		mongo.NewInsertOneModel().SetDocument(mongoUser{Email: "a@example.com"}),
		mongo.NewUpdateOneModel().
			SetFilter(bson.M{"email": "existing@example.com"}).
			SetUpdate(bson.M{"$set": bson.M{"name": "new"}}),
		mongo.NewUpdateOneModel().
			SetFilter(bson.M{"email": "upserted@example.com"}).
			SetUpdate(bson.M{"$set": bson.M{"name": "upserted"}}).
			SetUpsert(true),
		mongo.NewDeleteOneModel().SetFilter(bson.M{"email": "a@example.com"}),
	}, true)
	require.NoError(t, err)

	assert.Equal(t, int64(1), res.Inserted)
	assert.Equal(t, int64(1), res.Modified)
	assert.Equal(t, int64(1), res.Upserted)
	assert.Equal(t, int64(1), res.Deleted)
	require.Len(t, res.UpsertedIDs, 1)
	for _, id := range res.UpsertedIDs {
		assert.NotContains(t, id, "ObjectID", "bulk upsert ids are usable too")
	}
}

func TestRealMongo_arrayFilters(t *testing.T) {
	m, coll := newTestMongo(t)
	_, err := m.InsertOne(coll, bson.M{
		"name": "ann",
		"scores": []bson.M{
			{"subject": "math", "value": 40},
			{"subject": "art", "value": 90},
		},
	})
	require.NoError(t, err)

	res, err := m.UpdateOne(coll, bson.M{"name": "ann"},
		bson.M{"$set": bson.M{"scores.$[low].value": 50}},
		MongoUpdateOptions{ArrayFilters: []any{bson.M{"low.value": bson.M{"$lt": 50}}}})
	require.NoError(t, err)
	assert.Equal(t, int64(1), res.Modified)

	var got struct {
		Scores []struct {
			Subject string `bson:"subject"`
			Value   int    `bson:"value"`
		} `bson:"scores"`
	}
	require.NoError(t, m.FindOne(&got, coll, bson.M{"name": "ann"}))
	assert.Equal(t, 50, got.Scores[0].Value, "only the matching element changed")
	assert.Equal(t, 90, got.Scores[1].Value)
}

func TestRealMongo_collationMakesAQueryCaseInsensitive(t *testing.T) {
	m, coll := newTestMongo(t)
	_, err := m.InsertOne(coll, mongoUser{Name: "Ann"})
	require.NoError(t, err)

	var got mongoUser
	err = m.FindOne(&got, coll, bson.M{"name": "ann"})
	assert.True(t, errors.Is(err, ErrDocumentNotFound), "without a collation the case matters")

	require.NoError(t, m.FindOne(&got, coll, bson.M{"name": "ann"}, MongoFindOptions{
		Collation: &options.Collation{Locale: "en", Strength: 2},
	}))
	assert.Equal(t, "Ann", got.Name)
}

func TestRealMongo_schemaOperations(t *testing.T) {
	m, coll := newTestMongo(t)
	_, err := m.InsertOne(coll, mongoUser{Name: "x"})
	require.NoError(t, err)

	require.NoError(t, m.EnsureIndexes(coll,
		MongoIndex{Keys: []string{"email"}, Unique: true, Name: "email_unique"},
		MongoIndex{Keys: []string{"status", "-age"}, Name: "status_age"},
	))

	indexes, err := m.ListIndexes(coll)
	require.NoError(t, err)
	names := make([]string, 0, len(indexes))
	for _, idx := range indexes {
		names = append(names, fmt.Sprint(idx["name"]))
	}
	assert.Contains(t, names, "email_unique")
	assert.Contains(t, names, "status_age")

	require.NoError(t, m.DropIndex(coll, "status_age"))
	indexes, err = m.ListIndexes(coll)
	require.NoError(t, err)
	assert.Len(t, indexes, 2, "_id and email_unique are left")

	collections, err := m.ListCollections()
	require.NoError(t, err)
	assert.Contains(t, collections, coll)

	require.NoError(t, m.DropCollection(coll))
	n, err := m.EstimatedCount(coll)
	require.NoError(t, err)
	assert.Equal(t, int64(0), n)
}

func TestRealMongo_estimatedCount(t *testing.T) {
	m, coll := newTestMongo(t)
	for range 4 {
		_, err := m.InsertOne(coll, mongoUser{})
		require.NoError(t, err)
	}

	n, err := m.EstimatedCount(coll)
	require.NoError(t, err)
	assert.Equal(t, int64(4), n)
}

func TestRealMongo_readPreference(t *testing.T) {
	m, coll := newTestMongo(t)
	_, err := m.InsertOne(coll, mongoUser{Name: "ann"})
	require.NoError(t, err)

	// on a standalone the preference is accepted and reads still work; on a
	// replica set this is how a heavy report stays off the primary
	secondary := m.WithReadPreference(readpref.SecondaryPreferred())
	var got mongoUser
	require.NoError(t, secondary.FindOne(&got, coll, bson.M{"name": "ann"}))
	assert.Equal(t, "ann", got.Name)
}

func TestRealMongo_paginate(t *testing.T) {
	m, coll := newTestMongo(t)
	for i := range 25 {
		_, err := m.InsertOne(coll, mongoUser{Name: fmt.Sprintf("u%02d", i), Age: i, Status: "active"})
		require.NoError(t, err)
	}

	page, err := MongoPaginate[mongoUser](m, coll, bson.M{"status": "active"},
		&PageOptions{Page: 2, Limit: 10, OrderBy: []string{"age"}})
	require.NoError(t, err)

	assert.Equal(t, int64(25), page.Total)
	assert.Equal(t, int64(10), page.Count)
	require.Len(t, page.Items, 10)
	assert.Equal(t, 10, page.Items[0].Age, "page 2 starts after the first ten")
}

func TestRealMongo_transactionCommitsAndRollsBack(t *testing.T) {
	m, coll := newTestMongo(t)
	if err := m.Transaction(func(IMongoDB) error { return nil }); err != nil {
		t.Skipf("transactions need a replica set: %v", err)
	}

	require.NoError(t, m.Transaction(func(tx IMongoDB) error {
		if _, err := tx.InsertOne(coll, mongoUser{Name: "committed"}); err != nil {
			return err
		}
		return nil
	}))
	ok, err := m.Exists(coll, bson.M{"name": "committed"})
	require.NoError(t, err)
	assert.True(t, ok)

	failure := errors.New("changed my mind")
	err2 := m.Transaction(func(tx IMongoDB) error {
		if _, err := tx.InsertOne(coll, mongoUser{Name: "rolled-back"}); err != nil {
			return err
		}
		return failure
	})
	require.Error(t, err2)

	ok, err = m.Exists(coll, bson.M{"name": "rolled-back"})
	require.NoError(t, err)
	assert.False(t, ok, "an error must abort the transaction")

	// a panic must abort it too, rather than unwind with it still open
	require.Error(t, m.Transaction(func(tx IMongoDB) error {
		_, _ = tx.InsertOne(coll, mongoUser{Name: "panicked"})
		panic("boom")
	}))
	ok, err = m.Exists(coll, bson.M{"name": "panicked"})
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestRealMongo_cancellationStopsTheQuery(t *testing.T) {
	m, coll := newTestMongo(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := m.WithContext(ctx).InsertOne(coll, mongoUser{Name: "never"})
	require.Error(t, err, "a cancelled request must not keep writing")
}
