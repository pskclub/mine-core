//go:build e2e
// +build e2e

package core

import (
	"github.com/stretchr/testify/assert"
	"gorm.io/gorm"
	"testing"
)

type testDBSetSearchModel struct {
}

func TestConnect(t *testing.T) {
	env := NewEnv()
	db := NewDatabase(env.Config())

	_, err := db.Connect()
	assert.NoError(t, err)
}

func TestNewDatabaseWithConfig(t *testing.T) {
	env := NewEnv()
	dbWithConsFalse := NewDatabase(env.Config())
	dbWithConsTrue := NewDatabaseWithConfig(env.Config(), &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,
	})

	assert.False(t, dbWithConsFalse.config.DisableForeignKeyConstraintWhenMigrating)
	assert.True(t, dbWithConsTrue.config.DisableForeignKeyConstraintWhenMigrating)
}

func (t *testDBSetSearchModel) TableName() string {
	return "test_table"
}

func TestSetSearchExpectCorrectValueNo1(t *testing.T) {
	db := NewMockDatabase().Gorm
	expectSQLQuery := "SELECT * FROM `test_table` WHERE (id = ? AND is_active = ?) AND (title_th LIKE ? OR title_en LIKE ? OR title_jp LIKE ? OR title_cn LIKE ?)"

	db = SetSearch(db, NewKeywordAndCondition([]KeywordOptions{
		*NewKeywordMustMatchOption("id", "p011"),
		*NewKeywordMustMatchOption("is_active", "true"),
	}))
	db = SetSearch(db,
		NewKeywordOrCondition(NewKeywordWildCardOptions([]string{"title_th", "title_en", "title_jp", "title_cn"}, "singh")),
	)

	result := db.Session(&gorm.Session{DryRun: true}).Find(&testDBSetSearchModel{})
	assert.Equal(t, expectSQLQuery, result.Statement.SQL.String())
}

func TestSetSearchExpectCorrectValueNo2(t *testing.T) {
	db := NewMockDatabase().Gorm
	expectSQLQuery := "SELECT * FROM `test_table` WHERE (id = ? OR name LIKE ?) AND (title_th LIKE ? OR title_en LIKE ?)"

	db = SetSearch(db, NewKeywordOrCondition([]KeywordOptions{
		*NewKeywordMustMatchOption("id", "p011"),
		*NewKeywordWildCardOption("name", "singh"),
	}))
	db = SetSearch(db,
		NewKeywordOrCondition(NewKeywordWildCardOptions([]string{"title_th", "title_en"}, "singh")),
	)

	result := db.Session(&gorm.Session{DryRun: true}).Find(&testDBSetSearchModel{})
	assert.Equal(t, expectSQLQuery, result.Statement.SQL.String())
}

func TestSetSearchExpectCorrectValueNo3(t *testing.T) {
	db := NewMockDatabase().Gorm
	expectSQLQuery := "SELECT * FROM `test_table` WHERE (id LIKE ? AND name LIKE ?) AND (title_th = ? AND title_en = ?)"

	db = SetSearch(db, NewKeywordAndCondition([]KeywordOptions{
		*NewKeywordWildCardOption("id", "p011"),
		*NewKeywordWildCardOption("name", "singh"),
	}))
	db = SetSearch(db,
		NewKeywordAndCondition(
			NewKeywordMustMatchOptions([]string{"title_th", "title_en"}, "singh"),
		),
	)

	result := db.Session(&gorm.Session{DryRun: true}).Find(&testDBSetSearchModel{})
	assert.Equal(t, expectSQLQuery, result.Statement.SQL.String())
}

func TestSetSearchExpectCorrectErrorNo1(t *testing.T) {
	db := NewMockDatabase().Gorm
	expectSQLQuery := "SELECT * FROM `test_table` WHERE (id = ? OR is_active = ?) AND (title_th LIKE ? OR title_en LIKE ? OR title_jp LIKE ? OR title_cn LIKE ?)"

	db = SetSearch(db, NewKeywordAndCondition([]KeywordOptions{
		*NewKeywordMustMatchOption("id", "p011"),
		*NewKeywordMustMatchOption("is_active", "true"),
	}))
	db = SetSearch(db,
		NewKeywordOrCondition(NewKeywordWildCardOptions([]string{"title_th", "title_en", "title_jp", "title_cn"}, "singh")),
	)

	result := db.Session(&gorm.Session{DryRun: true}).Find(&testDBSetSearchModel{})
	assert.NotEqual(t, expectSQLQuery, result.Statement.SQL.String())
}

func TestSetSearchExpectCorrectErrorNo2(t *testing.T) {
	db := NewMockDatabase().Gorm
	expectSQLQuery := "SELECT * FROM `test_table` WHERE (id = ? AND is_active = ?)"

	db = SetSearch(db, NewKeywordAndCondition([]KeywordOptions{
		*NewKeywordWildCardOption("id", "p011"),
		*NewKeywordMustMatchOption("is_active", "true"),
	}))

	result := db.Session(&gorm.Session{DryRun: true}).Find(&testDBSetSearchModel{})
	assert.NotEqual(t, expectSQLQuery, result.Statement.SQL.String())
}

func TestSetSearchSimpleExpectCorrectValueNo1(t *testing.T) {
	db := NewMockDatabase().Gorm
	expectSQLQuery := "SELECT * FROM `test_table` WHERE title LIKE ?"

	db = SetSearchSimple(db, "singh", []string{"title"})

	result := db.Session(&gorm.Session{DryRun: true}).Find(&testDBSetSearchModel{})
	assert.Equal(t, expectSQLQuery, result.Statement.SQL.String())
}

func TestSetSearchSimpleExpectCorrectValueNo2(t *testing.T) {
	db := NewMockDatabase().Gorm
	expectSQLQuery := "SELECT * FROM `test_table` WHERE (title_th LIKE ? OR title_en LIKE ?) AND (name = ? AND age = ?)"

	db = SetSearchSimple(db, "singh", []string{"title_th", "title_en"})
	db = SetSearch(db, NewKeywordAndCondition([]KeywordOptions{
		*NewKeywordMustMatchOption("name", "singh"),
		*NewKeywordMustMatchOption("age", "18"),
	}))

	result := db.Session(&gorm.Session{DryRun: true}).Find(&testDBSetSearchModel{})
	assert.Equal(t, expectSQLQuery, result.Statement.SQL.String())
}

func TestSetSearchWithArgsExpectCorrectValue(t *testing.T) {
	db1 := NewMockDatabase().Gorm
	db2 := NewMockDatabase().Gorm
	db3 := NewMockDatabase().Gorm
	db4 := NewMockDatabase().Gorm
	expectSQLQueries := []string{
		"SELECT * FROM `test_table` WHERE id LIKE ?",
		"SELECT * FROM `test_table` WHERE (title_th LIKE ? OR title_en LIKE ?)",
		"SELECT * FROM `test_table` WHERE id LIKE ? AND (title_th = ? AND title_en = ?)",
	}
	args := [][]interface{}{
		{"%%s01%%"},
		{"%%singh%%", "%%singh%%"},
		{"%%s01%%", "singh", "singh"},
	}
	resultQueries := []string{}

	db1 = SetSearchSimple(db1, "s01", []string{"id"})
	result := db1.Session(&gorm.Session{DryRun: true}).Find(&testDBSetSearchModel{})
	resultQueries = append(resultQueries, db1.Dialector.Explain(result.Statement.SQL.String(), result.Statement.Vars...))

	db2 = SetSearch(db2, NewKeywordOrCondition([]KeywordOptions{
		*NewKeywordWildCardOption("title_th", "singh"),
		*NewKeywordWildCardOption("title_en", "singh"),
	}))
	result = db2.Session(&gorm.Session{DryRun: true}).Find(&testDBSetSearchModel{})
	resultQueries = append(resultQueries, db2.Dialector.Explain(result.Statement.SQL.String(), result.Statement.Vars...))

	db3 = SetSearchSimple(db3, "s01", []string{"id"})
	db3 = SetSearch(db3, NewKeywordAndCondition([]KeywordOptions{
		*NewKeywordMustMatchOption("title_th", "singh"),
		*NewKeywordMustMatchOption("title_en", "singh"),
	}))
	result = db3.Session(&gorm.Session{DryRun: true}).Find(&testDBSetSearchModel{})
	resultQueries = append(resultQueries, db3.Dialector.Explain(result.Statement.SQL.String(), result.Statement.Vars...))

	for i := range resultQueries {
		assert.Equal(t, db4.Dialector.Explain(expectSQLQueries[i], args[i]...), resultQueries[i])
	}
}

func TestSetSearchWithOldWhereExpectCorrectValue(t *testing.T) {
	db := NewMockDatabase().Gorm
	expectSQLQueryBeforeSetSearch := "SELECT * FROM `test_table` WHERE something = ? AND something1 = ?"
	expectSQLQuery := "SELECT * FROM `test_table` WHERE something = ? AND something1 = ? AND (title_th LIKE ? OR title_en LIKE ?)"

	db = db.Where("something = ?", "sometime")
	db = db.Where("something1 = ?", "sometime2")
	result := db.Session(&gorm.Session{DryRun: true}).Find(&testDBSetSearchModel{})
	assert.Equal(t, expectSQLQueryBeforeSetSearch, result.Statement.SQL.String())

	db = SetSearchSimple(db, "singh", []string{"title_th", "title_en"})

	result = db.Session(&gorm.Session{DryRun: true}).Find(&testDBSetSearchModel{})
	assert.Equal(t, expectSQLQuery, result.Statement.SQL.String())
}

func TestPaginateExpectCorrectValue(t *testing.T) {
	db := NewMockDatabase().Gorm

	items := make([]*testDBSetSearchModel, 0)
	_, _ = Paginate(db, &items, &PageOptions{})
}
