package core

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readpref"
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

type MongoListIndexResult struct {
	Key     map[string]int64 `json:"key" bson:"key"`
	Name    string           `json:"name" bson:"name"`
	Version int64            `json:"version" bson:"v"`
}

type MongoDropIndexResult struct {
	DropCount int64 `json:"drop_count" bson:"nIndexesWas"`
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

// Connect to connect Database
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
	FindAggregate(dest interface{}, coll string, pipeline interface{}, opts ...*options.AggregateOptions) error
	FindAggregatePagination(dest interface{}, coll string, pipeline interface{}, pageOptions *PageOptions, opts ...*options.AggregateOptions) (*PageResponse, error)
	FindAggregateOne(dest interface{}, coll string, pipeline interface{}, opts ...*options.AggregateOptions) error
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
	CreateIndex(coll string, models []mongo.IndexModel, opts ...*options.CreateIndexesOptions) ([]string, error)
	DropIndex(coll string, name string, opts ...*options.DropIndexesOptions) (*MongoDropIndexResult, error)
	DropAll(coll string, opts ...*options.DropIndexesOptions) (*MongoDropIndexResult, error)
	ListIndex(coll string, opts ...*options.ListIndexesOptions) ([]MongoListIndexResult, error)
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

func (m MongoDB) FindAggregate(dest interface{}, coll string, pipeline interface{}, opts ...*options.AggregateOptions) error {
	ctx, cancel := m.getContext()
	defer cancel()
	cur, err := m.DB().Collection(coll).Aggregate(ctx, pipeline, opts...)
	if err != nil {
		return err
	}
	defer cur.Close(ctx)

	return cur.All(ctx, dest)
}

func (m MongoDB) FindAggregatePagination(dest interface{}, coll string, pipeline interface{}, pageOptions *PageOptions, opts ...*options.AggregateOptions) (*PageResponse, error) {
	ctx, cancel := m.getContext()
	defer cancel()
	type Count struct {
		Count int64 `bson:"_count"`
	}
	totalModel := &Count{}
	countPipeline, ok := pipeline.([]bson.M)
	if !ok {
		return nil, errors.New("pipeline is not []bson.M")
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
		pips, ok := pipeline.([]bson.M)
		if !ok {
			return nil, errors.New("pipeline is not []bson.M")
		}

		pips = append(pips,
			bson.M{
				"$skip": skips,
			}, bson.M{
				"$limit": pageOptions.Limit,
			})

		pipeline = pips
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

func (m MongoDB) FindAggregateOne(dest interface{}, coll string, pipeline interface{}, opts ...*options.AggregateOptions) error {
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

func (m MongoDB) CreateIndex(coll string, models []mongo.IndexModel, opts ...*options.CreateIndexesOptions) ([]string, error) {
	ctx, cancel := m.getContext()
	defer cancel()

	return m.DB().Collection(coll).Indexes().CreateMany(ctx, models, opts...)
}

func (m MongoDB) DropIndex(coll string, name string, opts ...*options.DropIndexesOptions) (*MongoDropIndexResult, error) {
	ctx, cancel := m.getContext()
	defer cancel()

	result := &MongoDropIndexResult{}
	b, err := m.DB().Collection(coll).Indexes().DropOne(ctx, name, opts...)
	if err != nil {
		return nil, err
	}

	err = bson.Unmarshal(b, &result)
	if err != nil {
		return nil, err
	}

	return result, nil
}

func (m MongoDB) DropAll(coll string, opts ...*options.DropIndexesOptions) (*MongoDropIndexResult, error) {
	ctx, cancel := m.getContext()
	defer cancel()

	result := &MongoDropIndexResult{}
	b, err := m.DB().Collection(coll).Indexes().DropAll(ctx, opts...)
	if err != nil {
		return nil, err
	}

	err = bson.Unmarshal(b, &result)
	if err != nil {
		return nil, err
	}

	return result, nil
}

func (m MongoDB) ListIndex(coll string, opts ...*options.ListIndexesOptions) ([]MongoListIndexResult, error) {
	ctx, cancel := m.getContext()
	defer cancel()

	cursor, err := m.DB().Collection(coll).Indexes().List(ctx, opts...)
	if err != nil {
		return nil, err
	}

	results := make([]MongoListIndexResult, 0)
	if err := cursor.All(ctx, &results); err != nil {
		return nil, err
	}

	return results, nil
}

func (m MongoDB) DB() *mongo.Database {
	return m.database
}
