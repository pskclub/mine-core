package core

import (
	"context"
	"errors"
	"fmt"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readpref"
	"time"
)

const (
	connectTimeOut = 5 * time.Second
	queryTimeOut   = 5 * time.Second
)

type DatabaseMongo struct {
	Name     string
	Host     string
	UserName string
	Password string
	Port     string
}

func NewDatabaseMongo(env *ENVConfig) *DatabaseMongo {
	return &DatabaseMongo{
		Name:     env.DBMongoName,
		Host:     env.DBMongoHost,
		UserName: env.DBMongoUserName,
		Password: env.DBMongoPassword,
		Port:     env.DBMongoPort,
	}
}

// ConnectDB to connect Database
func (db *DatabaseMongo) Connect() (IMongoDB, error) {
	ctx, cancel := context.WithTimeout(context.Background(), connectTimeOut)
	defer cancel()
	client, err := mongo.Connect(ctx,
		options.Client().ApplyURI(fmt.Sprintf("mongodb://%s:%s@%s:%s", db.UserName, db.Password, db.Host, db.Port)))

	if err != nil {
		return nil, err
	}

	if err := client.Ping(ctx, readpref.Primary()); err != nil {
		return nil, err
	}

	return &MongoDB{database: client.Database(db.Name), databaseClient: client}, nil
}

type IMongoDB interface {
	DB() *mongo.Database
	Create(coll string, document interface{}, opts ...*options.InsertOneOptions) (*mongo.InsertOneResult, error)
	FindAggregate(dest interface{}, coll string, pipeline []bson.M, opts ...*options.AggregateOptions) error
	FindAggregatePagination(dest interface{}, coll string, pipeline []bson.M, pageOptions *PageOptions, opts ...*options.AggregateOptions) (*PageResponse, error)
	FindAggregateOne(dest interface{}, coll string, pipeline []bson.M, opts ...*options.AggregateOptions) error
	Find(dest interface{}, coll string, filter interface{}, opts ...*options.FindOptions) error
	FindPagination(dest interface{}, coll string, filter interface{}, pageOptions *PageOptions, opts ...*options.FindOptions) (*PageResponse, error)
	FindOne(dest interface{}, coll string, filter interface{}, opts ...*options.FindOneOptions) error
	FindOneAndUpdate(dest interface{}, coll string, filter interface{}, update interface{}, opts ...*options.FindOneAndUpdateOptions) error
	UpdateOne(coll string, filter interface{}, update interface{}, opts ...*options.UpdateOptions) (*mongo.UpdateResult, error)
	Count(coll string, filter interface{}, opts ...*options.CountOptions) (int64, error)
	Drop(coll string) error
	DeleteOne(coll string, filter interface{}, opts ...*options.DeleteOptions) (*mongo.DeleteResult, error)
	DeleteMany(coll string, filter interface{}, opts ...*options.DeleteOptions) (*mongo.DeleteResult, error)
	FindOneAndDelete(coll string, filter interface{}, opts ...*options.FindOneAndDeleteOptions) error
	Close()
	Helper() IMongoDBHelper
}

type MongoDB struct {
	database       *mongo.Database
	databaseClient *mongo.Client
}

func (m MongoDB) Helper() IMongoDBHelper {
	return NewMongoHelper()
}

func (m MongoDB) getContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), queryTimeOut)
}

func (m MongoDB) Close() {
	ctx, cancel := m.getContext()
	defer cancel()

	if err := m.databaseClient.Disconnect(ctx); err != nil {
		panic(err)
	}
}

func (m MongoDB) getSkips(pageOptions *PageOptions) int64 {
	return pageOptions.Limit * (pageOptions.Page - 1)
}

func (m MongoDB) FindPagination(dest interface{}, coll string, filter interface{}, pageOptions *PageOptions, opts ...*options.FindOptions) (*PageResponse, error) {
	ctx, cancel := m.getContext()
	defer cancel()

	totalCount, err := m.Count(coll, filter)
	if err != nil {
		return nil, err
	}

	if pageOptions != nil {
		skips := m.getSkips(pageOptions)
		opts = append(opts, options.Find().SetLimit(pageOptions.Limit), options.Find().SetSkip(skips))
	}

	cur, err := m.DB().Collection(coll).Find(ctx, filter, opts...)
	if err != nil && !errors.Is(err, mongo.ErrNoDocuments) {
		return nil, err
	}
	defer cur.Close(ctx)

	var count int64 = 0
	for cur.Next(ctx) {
		count++
	}

	return &PageResponse{
		Total: totalCount,
		Limit: pageOptions.Limit,
		Count: count,
		Page:  pageOptions.Page,
		Q:     pageOptions.Q,
	}, cur.All(ctx, dest)
}

func (m MongoDB) Count(coll string, filter interface{}, opts ...*options.CountOptions) (int64, error) {
	ctx, cancel := m.getContext()
	defer cancel()

	return m.DB().Collection(coll).CountDocuments(ctx, filter, opts...)
}

func (m MongoDB) FindAggregate(dest interface{}, coll string, pipeline []bson.M, opts ...*options.AggregateOptions) error {
	ctx, cancel := m.getContext()
	defer cancel()
	cur, err := m.DB().Collection(coll).Aggregate(ctx, pipeline, opts...)
	if err != nil {
		return err
	}
	defer cur.Close(ctx)

	return cur.All(ctx, dest)
}

func (m MongoDB) FindAggregatePagination(dest interface{}, coll string, pipeline []bson.M, pageOptions *PageOptions, opts ...*options.AggregateOptions) (*PageResponse, error) {
	ctx, cancel := m.getContext()
	defer cancel()
	type Count struct {
		Count int64 `bson:"_count"`
	}
	totalModel := &Count{}

	countPipeline := make([]bson.M, 0)
	for _, m := range pipeline {
		_, ok := m["$lookup"]
		_, ok2 := m["$set"]
		_, ok3 := m["$sort"]

		if !ok && !ok2 && !ok3 {
			countPipeline = append(countPipeline, m)
		}
	}
	countPipeline = append(countPipeline, bson.M{
		"$count": "_count",
	})

	err := m.FindAggregateOne(totalModel, coll, countPipeline)
	if err != nil && !errors.Is(err, mongo.ErrNoDocuments) {
		return nil, err
	}

	if pageOptions != nil {
		skips := m.getSkips(pageOptions)
		pipeline = append(pipeline,
			bson.M{
				"$skip": skips,
			}, bson.M{
				"$limit": pageOptions.Limit,
			})
	}

	cur, err := m.DB().Collection(coll).Aggregate(ctx, pipeline, opts...)
	if err != nil && !errors.Is(err, mongo.ErrNoDocuments) {
		return nil, err
	}
	defer cur.Close(ctx)

	var count int64 = 0
	for cur.Next(ctx) {
		count++
	}

	return &PageResponse{
		Total: totalModel.Count,
		Limit: pageOptions.Limit,
		Count: count,
		Page:  pageOptions.Page,
		Q:     pageOptions.Q,
	}, cur.All(ctx, dest)
}

func (m MongoDB) FindAggregateOne(dest interface{}, coll string, pipeline []bson.M, opts ...*options.AggregateOptions) error {
	ctx, cancel := m.getContext()
	defer cancel()
	cur, err := m.DB().Collection(coll).Aggregate(ctx, pipeline, opts...)
	if err != nil {
		return err
	}

	defer cur.Close(ctx)
	ok := cur.Next(ctx)
	if !ok {
		return mongo.ErrNoDocuments
	}

	return cur.Decode(dest)
}

func (m MongoDB) Find(dest interface{}, coll string, filter interface{}, opts ...*options.FindOptions) error {
	ctx, cancel := m.getContext()
	defer cancel()
	cur, err := m.DB().Collection(coll).Find(ctx, filter, opts...)
	if err != nil {
		return err
	}
	defer cur.Close(ctx)

	return cur.All(ctx, dest)
}

func (m MongoDB) UpdateOne(coll string, filter interface{}, update interface{},
	opts ...*options.UpdateOptions) (*mongo.UpdateResult, error) {

	ctx, cancel := m.getContext()
	defer cancel()

	return m.DB().Collection(coll).UpdateOne(ctx, filter, update, opts...)
}

func (m MongoDB) FindOneAndUpdate(dest interface{}, coll string, filter interface{}, update interface{},
	opts ...*options.FindOneAndUpdateOptions) error {

	ctx, cancel := m.getContext()
	defer cancel()

	cur := m.DB().Collection(coll).FindOneAndUpdate(ctx, filter, update, opts...)
	if cur.Err() != nil {
		return cur.Err()
	}

	return cur.Decode(dest)
}

func (m MongoDB) Drop(coll string) error {
	ctx, cancel := m.getContext()
	defer cancel()

	return m.DB().Collection(coll).Drop(ctx)
}

func (m MongoDB) DeleteOne(coll string, filter interface{}, opts ...*options.DeleteOptions) (*mongo.DeleteResult, error) {
	ctx, cancel := m.getContext()
	defer cancel()

	return m.DB().Collection(coll).DeleteOne(ctx, filter, opts...)
}

func (m MongoDB) FindOneAndDelete(coll string, filter interface{}, opts ...*options.FindOneAndDeleteOptions) error {
	ctx, cancel := m.getContext()
	defer cancel()

	return m.DB().Collection(coll).FindOneAndDelete(ctx, filter, opts...).Err()
}

func (m MongoDB) DeleteMany(coll string, filter interface{}, opts ...*options.DeleteOptions) (*mongo.DeleteResult, error) {
	ctx, cancel := m.getContext()
	defer cancel()

	return m.DB().Collection(coll).DeleteMany(ctx, filter, opts...)
}

func (m MongoDB) FindOne(dest interface{}, coll string, filter interface{}, opts ...*options.FindOneOptions) error {
	ctx, cancel := m.getContext()
	defer cancel()
	cur := m.DB().Collection(coll).FindOne(ctx, filter, opts...)
	if cur.Err() != nil {
		return cur.Err()
	}

	return cur.Decode(dest)
}

func (m MongoDB) Create(coll string, document interface{}, opts ...*options.InsertOneOptions) (*mongo.InsertOneResult, error) {
	ctx, cancel := m.getContext()
	defer cancel()

	return m.DB().Collection(coll).InsertOne(ctx, document, opts...)
}

func (m MongoDB) DB() *mongo.Database {
	return m.database
}
