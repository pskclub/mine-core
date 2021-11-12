// +build e2e

package core

import (
	"fmt"
	"github.com/stretchr/testify/assert"
	"go.mongodb.org/mongo-driver/mongo"
	"testing"
)

func TestMongoDB_CreateIndex(t *testing.T) {
	env := NewMockENV()
	assert.NotNil(t, env)

	env.On("Config").Return(&ENVConfig{
		DBMongoHost:     "localhost",
		DBMongoName:     "test",
		DBMongoUserName: "my_username",
		DBMongoPassword: "my_password",
		DBMongoPort:     "27017",
	})

	c := NewDatabaseMongo(env.Config())
	assert.NotNil(t, c)

	mg, err := c.Connect()
	assert.NoError(t, err)
	assert.NotNil(t, mg)

	// Create Collection First
	result, err := mg.Create("test_create_index", map[string]interface{}{
		"name":        "singh",
		"description": "I Love Finema",
		"age":         18,
	})
	assert.NoError(t, err)
	assert.NotNil(t, result)
	defer func() {
		_ = mg.Drop("test_create_index")
	}()

	names, err := mg.CreateIndex("singh", []mongo.IndexModel{
		{
			Keys: map[string]interface{}{
				"description": 1,
			},
		},
	})
	assert.NoError(t, err)
	assert.NotNil(t, names)
}

func TestMongoDB_DropIndex(t *testing.T) {
	env := NewMockENV()
	assert.NotNil(t, env)

	env.On("Config").Return(&ENVConfig{
		DBMongoHost:     "localhost",
		DBMongoName:     "test",
		DBMongoUserName: "my_username",
		DBMongoPassword: "my_password",
		DBMongoPort:     "27017",
	})

	c := NewDatabaseMongo(env.Config())
	assert.NotNil(t, c)

	mg, err := c.Connect()
	assert.NoError(t, err)
	assert.NotNil(t, mg)

	// Create Collection First
	result, err := mg.Create("test_drop_index", map[string]interface{}{
		"name":        "singh",
		"description": "I Love Finema",
		"age":         18,
	})
	assert.NoError(t, err)
	assert.NotNil(t, result)
	defer func() {
		_ = mg.Drop("test_drop_index")
	}()

	names, err := mg.CreateIndex("test_drop_index", []mongo.IndexModel{
		{
			Keys: map[string]interface{}{
				"description": 1,
			},
		},
	})
	assert.NoError(t, err)
	assert.NotNil(t, names)

	results, err := mg.ListIndex("test_drop_index")
	assert.NoError(t, err)
	assert.NotNil(t, results)

	dropResult, err := mg.DropIndex("test_drop_index", "description_1")
	assert.NoError(t, err)
	assert.NotNil(t, dropResult)
	assert.Equal(t, dropResult.DropCount, int64(2))
}

func TestMongoDB_ListIndex(t *testing.T) {
	env := NewMockENV()
	assert.NotNil(t, env)

	env.On("Config").Return(&ENVConfig{
		DBMongoHost:     "localhost",
		DBMongoName:     "test",
		DBMongoUserName: "my_username",
		DBMongoPassword: "my_password",
		DBMongoPort:     "27017",
	})

	c := NewDatabaseMongo(env.Config())
	assert.NotNil(t, c)

	mg, err := c.Connect()
	assert.NoError(t, err)
	assert.NotNil(t, mg)

	// Create Collection First
	result, err := mg.Create("test_drop_index", map[string]interface{}{
		"name":        "singh",
		"description": "I Love Finema",
		"age":         18,
	})
	assert.NoError(t, err)
	assert.NotNil(t, result)
	defer func() {
		_ = mg.Drop("test_drop_index")
	}()

	names, err := mg.CreateIndex("test_drop_index", []mongo.IndexModel{
		{
			Keys: map[string]interface{}{
				"description": 1,
			},
		},
	})
	assert.NoError(t, err)
	assert.NotNil(t, names)

	results, err := mg.ListIndex("test_drop_index")
	assert.NoError(t, err)

	isFound := false
	expectFound := map[string]interface{}{
		"key":   "description",
		"value": int64(1),
	}

	for _, r := range results {
		if r.Key[fmt.Sprintf(`%s`, expectFound["key"])] == expectFound["value"] {
			isFound = true
		}
	}

	assert.NotNil(t, results)
	assert.True(t, isFound)
}

func TestMongoDB_DropAll(t *testing.T) {
	env := NewMockENV()
	assert.NotNil(t, env)

	env.On("Config").Return(&ENVConfig{
		DBMongoHost:     "localhost",
		DBMongoName:     "test",
		DBMongoUserName: "my_username",
		DBMongoPassword: "my_password",
		DBMongoPort:     "27017",
	})

	c := NewDatabaseMongo(env.Config())
	assert.NotNil(t, c)

	mg, err := c.Connect()
	assert.NoError(t, err)
	assert.NotNil(t, mg)

	// Create Collection First
	result, err := mg.Create("test_drop_index", map[string]interface{}{
		"name":        "singh",
		"description": "I Love Finema",
		"age":         18,
	})
	assert.NoError(t, err)
	assert.NotNil(t, result)
	defer func() {
		_ = mg.Drop("test_drop_index")
	}()

	names, err := mg.CreateIndex("test_drop_index", []mongo.IndexModel{
		{
			Keys: map[string]interface{}{
				"description": 1,
			},
		},
	})
	assert.NoError(t, err)
	assert.NotNil(t, names)

	_, err = mg.DropAll("test_drop_index")
	assert.NoError(t, err)

	results, err := mg.ListIndex("test_drop_index")
	assert.NoError(t, err)

	isFound := false
	expectFound := map[string]interface{}{
		"key":   "description",
		"value": 1,
	}

	for _, r := range results {
		if r.Key[fmt.Sprintf(`%s`, expectFound["key"])] == expectFound["value"] {
			isFound = true
		}
	}

	assert.False(t, isFound)
}
