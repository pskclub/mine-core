package core

import "go.mongodb.org/mongo-driver/bson"

type mongoDBHelper struct {
}

type IMongoDBHelper interface {
	Lookup(options *MongoLookupOptions) bson.M
	Set(options bson.M) bson.M
	Project(options bson.M) bson.M
	Size(expression string) bson.M
	Filter(options *MongoFilterOptions) bson.M
	Match(options bson.M) bson.M
	Unwind(field string) bson.M
	ReplaceRoot(options interface{}) bson.M
	Or(options []bson.M) bson.M
}

func NewMongoHelper() IMongoDBHelper {
	return &mongoDBHelper{}
}

type MongoLookupOptions struct {
	From         string
	LocalField   string
	ForeignField string
	As           string
}

type MongoFilterOptions struct {
	Input     string
	As        string
	Condition bson.M
}

func (m mongoDBHelper) Set(options bson.M) bson.M {
	return bson.M{
		"$set": options,
	}
}

func (m mongoDBHelper) Filter(options *MongoFilterOptions) bson.M {
	return bson.M{
		"$filter": bson.M{
			"input": options.Input,
			"as":    options.As,
			"cond":  options.Condition,
		},
	}
}

func (m mongoDBHelper) Project(options bson.M) bson.M {
	return bson.M{
		"$project": options,
	}
}

func (m mongoDBHelper) Lookup(options *MongoLookupOptions) bson.M {
	return bson.M{
		"$lookup": bson.M{
			"from":         options.From,
			"localField":   options.LocalField,
			"foreignField": options.ForeignField,
			"as":           options.As,
		},
	}
}

func (m mongoDBHelper) Size(expression string) bson.M {
	return bson.M{
		"$size": expression,
	}
}

func (m mongoDBHelper) Match(options bson.M) bson.M {
	return bson.M{
		"$match": options,
	}
}

func (m mongoDBHelper) Unwind(field string) bson.M {
	return bson.M{
		"$unwind": field,
	}
}

func (m mongoDBHelper) ReplaceRoot(options interface{}) bson.M {
	return bson.M{
		"$replaceRoot": bson.M{
			"newRoot": options,
		},
	}
}

func (m mongoDBHelper) Or(options []bson.M) bson.M {
	return bson.M{
		"$or": options,
	}
}
