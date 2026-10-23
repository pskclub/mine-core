package mongorepo

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// "The first match" of no filter is whichever document the server lists first.
// Delete already refuses that; the single-document writes have to as well, or
// a forgotten Where quietly rewrites a stranger's record.
func TestRepo_singleDocumentWritesRefuseAnEmptyScope(t *testing.T) {
	fake := &recordingMongo{}
	repo := NewIn[user](fake)

	_, err := repo.Updates(bson.M{"name": "x"})
	require.NotNil(t, err)
	assert.Equal(t, "UNSCOPED_UPDATE", err.GetCode())
	assert.Nil(t, fake.update, "nothing reached the database")

	_, err = repo.Inc("seats", 1)
	require.NotNil(t, err, "Inc goes through Updates and inherits the guard")
	assert.Equal(t, "UNSCOPED_UPDATE", err.GetCode())

	_, err = repo.DeleteOne()
	require.NotNil(t, err)
	assert.Equal(t, "UNSCOPED_DELETE", err.GetCode())

	// with a scope the same calls go through
	_, err = repo.Eq("_id", "u1").Updates(bson.M{"name": "x"})
	require.Nil(t, err)
	assert.NotNil(t, fake.update)
}
