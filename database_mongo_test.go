package core

import (
	"github.com/pskclub/mine-core/utils"
	"go.mongodb.org/mongo-driver/bson"
	"testing"
)

func TestRemoveLookup(t *testing.T) {

	pipeline := []bson.M{
		NewMongoHelper().Lookup(&MongoLookupOptions{
			From:         "tags",
			LocalField:   "tags",
			ForeignField: "slug",
			As:           "tags",
		}),
		NewMongoHelper().Lookup(&MongoLookupOptions{
			From:         "categories",
			LocalField:   "categories",
			ForeignField: "slug",
			As:           "categories",
		}),
		NewMongoHelper().Lookup(&MongoLookupOptions{
			From:         "actors",
			LocalField:   "actors",
			ForeignField: "slug",
			As:           "actors",
		}),
		NewMongoHelper().Match(bson.M{
			"id": 5,
		}),
	}

	newPipeline := make([]bson.M, 0)
	for _, m := range pipeline {
		_, ok := m["$lookup"]
		if !ok {
			newPipeline = append(newPipeline, m)
		}
	}

	utils.LogStruct(pipeline)
	utils.LogStruct(newPipeline)
}
