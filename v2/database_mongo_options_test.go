package core

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// The repository test proves the options reach the IMongoDB; this proves the
// real implementation hands them to the driver. A positional "$[name]" update
// with its ArrayFilters dropped is a server error every time, and Hint and
// Collation silently ignored are a query plan nobody asked for.
func TestMongoUpdateOptions_reachTheDriverBuilders(t *testing.T) {
	in := MongoUpdateOptions{
		Upsert:       true,
		ArrayFilters: []any{bson.M{"low.value": bson.M{"$lt": 50}}},
		Hint:         "idx_score",
		Collation:    &options.Collation{Locale: "en", Strength: 2},
	}

	one := options.UpdateOneOptions{}
	for _, apply := range mongoUpdateOneOptions([]MongoUpdateOptions{in}).Opts {
		require.NoError(t, apply(&one))
	}
	assert.True(t, *one.Upsert)
	assert.Len(t, one.ArrayFilters, 1)
	assert.Equal(t, "idx_score", one.Hint)
	assert.Equal(t, "en", one.Collation.Locale)

	many := options.UpdateManyOptions{}
	for _, apply := range mongoUpdateManyOptions([]MongoUpdateOptions{in}).Opts {
		require.NoError(t, apply(&many))
	}
	assert.True(t, *many.Upsert)
	assert.Len(t, many.ArrayFilters, 1)
	assert.Equal(t, "idx_score", many.Hint)
	assert.Equal(t, "en", many.Collation.Locale)

	// no options at all is still a valid builder, with nothing set
	empty := options.UpdateOneOptions{}
	for _, apply := range mongoUpdateOneOptions(nil).Opts {
		require.NoError(t, apply(&empty))
	}
	assert.Nil(t, empty.Upsert)
}
