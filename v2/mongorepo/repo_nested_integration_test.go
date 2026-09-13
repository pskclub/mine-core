//go:build integration

package mongorepo

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	core "github.com/pskclub/mine-core/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// Nested documents and arrays against a real server: everything the nested-field
// section of docs/mongo-repository.md claims, verified.

type item struct {
	SKU string `bson:"sku"`
	Qty int    `bson:"qty"`
}

type profile struct {
	City string `bson:"city"`
	Age  int    `bson:"age"`
}

type account struct {
	ID      string   `bson:"_id,omitempty"`
	Name    string   `bson:"name"`
	Profile profile  `bson:"profile"`
	Items   []item   `bson:"items"`
	Tags    []string `bson:"tags"`
}

var accountCollection = "coretest_accounts"

func (account) CollectionName() string { return accountCollection }

func newAccountRepo(t *testing.T) *Repo[account] {
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

	accountCollection = "coretest_acct_" + strings.ReplaceAll(t.Name(), "/", "_")
	repo := NewIn[account](db)
	t.Cleanup(func() {
		_, _ = repo.DeleteAll()
		_ = db.Close()
	})
	return repo
}

func TestNested_filters(t *testing.T) {
	repo := newAccountRepo(t)
	a := account{
		Name:    "ann",
		Profile: profile{City: "BKK", Age: 30},
		Items:   []item{{SKU: "A-1", Qty: 1}, {SKU: "B-2", Qty: 5}},
		Tags:    []string{"beta", "vip"},
	}
	require.NoError(t, repo.Create(&a))

	found, err := repo.Eq("profile.city", "BKK").FindOne()
	require.NoError(t, err)
	assert.Equal(t, 30, found.Profile.Age)

	_, err = repo.Gte("profile.age", 40).FindOne()
	assert.True(t, errors.Is(err, core.ErrDocumentNotFound))

	// an array field matches when any element does
	found, err = repo.Eq("items.sku", "B-2").FindOne()
	require.NoError(t, err)
	assert.Equal(t, "ann", found.Name)

	found, err = repo.Eq("tags", "vip").FindOne()
	require.NoError(t, err, "a scalar array contains the value")
	assert.Equal(t, "ann", found.Name)
}

func TestNested_elemMatch(t *testing.T) {
	// the trap the docs warn about, verified: two dotted conditions are met by
	// two different elements, $elemMatch demands one element meet both
	repo := newAccountRepo(t)
	a := account{Name: "ann", Items: []item{{SKU: "A-1", Qty: 1}, {SKU: "B-2", Qty: 5}}}
	require.NoError(t, repo.Create(&a))

	loose, err := repo.Eq("items.sku", "A-1").Eq("items.qty", 5).FindAll()
	require.NoError(t, err)
	assert.Len(t, loose, 1, "satisfied across two different elements")

	strict, err := repo.ElemMatch("items", bson.M{"sku": "A-1", "qty": 5}).FindAll()
	require.NoError(t, err)
	assert.Empty(t, strict, "no single element is both")

	match, err := repo.ElemMatch("items", bson.M{"sku": "A-1", "qty": 1}).FindAll()
	require.NoError(t, err)
	assert.Len(t, match, 1)
}

func TestNested_arrayConditions(t *testing.T) {
	repo := newAccountRepo(t)
	a := account{Name: "ann", Tags: []string{"beta", "vip", "th"}}
	require.NoError(t, repo.Create(&a))

	all, err := repo.HasAll("tags", "beta", "vip").FindAll()
	require.NoError(t, err)
	assert.Len(t, all, 1)

	none, err := repo.HasAll("tags", "beta", "missing").FindAll()
	require.NoError(t, err)
	assert.Empty(t, none)

	sized, err := repo.HasSize("tags", 3).FindAll()
	require.NoError(t, err)
	assert.Len(t, sized, 1)

	present, err := repo.HasField("profile.city", true).FindAll()
	require.NoError(t, err)
	assert.Len(t, present, 1)
}

func TestNested_updates(t *testing.T) {
	repo := newAccountRepo(t)
	a := account{
		Name:    "ann",
		Profile: profile{City: "BKK", Age: 30},
		Items:   []item{{SKU: "A-1", Qty: 0}, {SKU: "B-2", Qty: 5}},
	}
	require.NoError(t, repo.Create(&a))

	// a nested $set touches only that field
	_, err := repo.ByID(a.ID).Update("profile.city", "CNX")
	require.NoError(t, err)
	got, err := repo.ByID(a.ID).FindOne()
	require.NoError(t, err)
	assert.Equal(t, "CNX", got.Profile.City)
	assert.Equal(t, 30, got.Profile.Age, "the sibling field survives")

	// array filters: only the elements below the threshold
	_, err = repo.ByID(a.ID).Updates(
		bson.M{"items.$[low].qty": 1},
		core.MongoUpdateOptions{ArrayFilters: []any{bson.M{"low.qty": bson.M{"$lt": 1}}}},
	)
	require.NoError(t, err)
	got, err = repo.ByID(a.ID).FindOne()
	require.NoError(t, err)
	assert.Equal(t, 1, got.Items[0].Qty)
	assert.Equal(t, 5, got.Items[1].Qty, "the element above the threshold is untouched")

	// the positional operator updates the element the query matched
	_, err = repo.ByID(a.ID).ElemMatch("items", bson.M{"sku": "B-2"}).
		Updates(bson.M{"items.$.qty": 99})
	require.NoError(t, err)
	got, err = repo.ByID(a.ID).FindOne()
	require.NoError(t, err)
	assert.Equal(t, 99, got.Items[1].Qty)

	// whole-array operations
	_, err = repo.ByID(a.ID).Push("items", item{SKU: "C-3", Qty: 2})
	require.NoError(t, err)
	_, err = repo.ByID(a.ID).AddToSet("tags", "beta")
	require.NoError(t, err)
	_, err = repo.ByID(a.ID).AddToSet("tags", "beta")
	require.NoError(t, err)
	_, err = repo.ByID(a.ID).Unset("profile.city")
	require.NoError(t, err)

	got, err = repo.ByID(a.ID).FindOne()
	require.NoError(t, err)
	assert.Len(t, got.Items, 3)
	assert.Equal(t, []string{"beta"}, got.Tags, "AddToSet does not duplicate")
	assert.Empty(t, got.Profile.City, "Unset removed the nested field")

	_, err = repo.ByID(a.ID).Pull("items", bson.M{"sku": "C-3"})
	require.NoError(t, err)
	got, err = repo.ByID(a.ID).FindOne()
	require.NoError(t, err)
	assert.Len(t, got.Items, 2)
}

func TestNested_pluck(t *testing.T) {
	repo := newAccountRepo(t)
	first := account{
		Name: "ann", Profile: profile{City: "BKK"},
		Items: []item{{SKU: "A-1"}, {SKU: "B-2"}},
	}
	second := account{
		Name: "bob", Profile: profile{City: "CNX"},
		Items: []item{{SKU: "C-3"}},
	}
	require.NoError(t, repo.Create(&first))
	require.NoError(t, repo.Create(&second))

	var cities []string
	require.NoError(t, repo.Sort("name").Pluck("profile.city", &cities))
	assert.Equal(t, []string{"BKK", "CNX"}, cities)

	var skus []string
	require.NoError(t, repo.Sort("name").Pluck("items.sku", &skus))
	assert.Equal(t, []string{"A-1", "B-2", "C-3"}, skus,
		"a path across an array yields one value per element")
}

func TestNested_unwindInAPipeline(t *testing.T) {
	repo := newAccountRepo(t)
	require.NoError(t, repo.Create(&account{
		Name: "ann", Items: []item{{SKU: "A-1", Qty: 2}, {SKU: "B-2", Qty: 3}},
	}))
	require.NoError(t, repo.Create(&account{
		Name: "bob", Items: []item{{SKU: "A-1", Qty: 5}},
	}))
	require.NoError(t, repo.Create(&account{Name: "carl"})) // no items at all

	type skuTotal struct {
		SKU   string `bson:"_id"`
		Total int    `bson:"total"`
	}
	rows, err := Aggregate[skuTotal](repo).
		Unwind("items", false).
		Group("$items.sku", bson.M{"total": bson.M{"$sum": "$items.qty"}}).
		Sort("-total").
		All()
	require.NoError(t, err)

	require.Len(t, rows, 2, "the account with no items contributes nothing")
	assert.Equal(t, "A-1", rows[0].SKU)
	assert.Equal(t, 7, rows[0].Total)
	assert.Equal(t, 3, rows[1].Total)

	// preserving empties keeps the third account in the result
	kept, err := Aggregate[bson.M](repo).Unwind("items", true).All()
	require.NoError(t, err)
	assert.Len(t, kept, 4, "2 + 1 items, plus the account that has none")
}

func TestNested_indexes(t *testing.T) {
	repo := newAccountRepo(t)
	require.NoError(t, repo.Create(&account{Name: "ann", Profile: profile{City: "BKK"}}))

	require.NoError(t, repo.EnsureIndexes(
		core.MongoIndex{Keys: []string{"profile.city"}, Name: "profile_city"},
		core.MongoIndex{Keys: []string{"items.sku"}, Name: "items_sku"},
		core.MongoIndex{Keys: []string{"profile.city", "-profile.age"}, Name: "city_age"},
	))

	indexes, err := repo.DB().ListIndexes(repo.CollectionName())
	require.NoError(t, err)
	names := make([]string, 0, len(indexes))
	for _, idx := range indexes {
		names = append(names, fmt.Sprint(idx["name"]))
	}
	assert.Contains(t, names, "profile_city")
	assert.Contains(t, names, "items_sku")
	assert.Contains(t, names, "city_age")
}
