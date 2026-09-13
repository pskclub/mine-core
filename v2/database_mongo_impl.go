package core

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/event"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"
)

type mongoDB struct {
	ctx    context.Context
	client *mongo.Client
	dbName string
	// session is set inside Transaction: every operation on that handle has to
	// run on the session's context or it silently lands outside the transaction.
	// Driver v2 dropped mongo.SessionContext — the session travels on an ordinary
	// context now, which is what WithTransaction hands its callback.
	session context.Context
	// readPref sends reads somewhere other than the primary when asked.
	readPref *readpref.ReadPref
}

var _ IMongoDB = (*mongoDB)(nil)

const (
	defaultMongoPort           = "27017"
	defaultMongoConnectTimeout = 10 * time.Second
	defaultMongoCloseTimeout   = 5 * time.Second
)

// MongoOption configures a Mongo connection.
type MongoOption func(*mongoOptions)

type mongoOptions struct {
	log              ILogger
	logLevel         MongoLogLevel
	logLevelSet      bool
	slowQuery        time.Duration
	monitor          *event.CommandMonitor
	monitorSet       bool
	poolMonitor      *event.PoolMonitor
	poolMonitorSet   bool
	serverMonitor    *event.ServerMonitor
	serverMonitorSet bool
}

// WithMongoLogLevel overrides how much of the traffic to Mongo is logged,
// independently of LOG_LEVEL: MongoLogInfo logs every command, MongoLogWarn only
// slow ones and failures, MongoLogSilent nothing.
//
//	m, _ := core.NewMongoDB(env, core.WithMongoLogLevel(core.MongoLogInfo))
func WithMongoLogLevel(level MongoLogLevel) MongoOption {
	return func(o *mongoOptions) { o.logLevel = level; o.logLevelSet = true }
}

// WithMongoSlowQuery sets the duration above which a command is logged as slow
// (default 200ms).
func WithMongoSlowQuery(d time.Duration) MongoOption {
	return func(o *mongoOptions) { o.slowQuery = d }
}

// WithMongoLogger sends command logs to a specific logger instead of one built
// from configuration.
func WithMongoLogger(l ILogger) MongoOption { return func(o *mongoOptions) { o.log = l } }

// WithMongoMonitor replaces the command monitor entirely — for tracing, metrics,
// or a logger of your own. Pass nil to record nothing.
func WithMongoMonitor(monitor *event.CommandMonitor) MongoOption {
	return func(o *mongoOptions) { o.monitor = monitor; o.monitorSet = true }
}

// WithMongoPoolMonitor replaces the connection-pool monitor. Pass nil to give up
// the only warning there is that the pool is exhausted.
func WithMongoPoolMonitor(monitor *event.PoolMonitor) MongoOption {
	return func(o *mongoOptions) { o.poolMonitor = monitor; o.poolMonitorSet = true }
}

// WithMongoServerMonitor replaces the topology monitor. Pass nil to record
// nothing about members going away or the primary moving.
func WithMongoServerMonitor(monitor *event.ServerMonitor) MongoOption {
	return func(o *mongoOptions) { o.serverMonitor = monitor; o.serverMonitorSet = true }
}

// NewMongoDB connects to MongoDB using configuration and returns an IMongoDB.
// It accepts a full URI (DB_MONGO_CONNECTION_STRING) or the discrete
// DB_MONGO_HOST/PORT/USERNAME/PASSWORD fields, which now also honour
// DB_MONGO_REPLICA_NAME and DB_MONGO_TLS.
//
// Every command is logged the way SQL statements are — failures, then slow
// commands, then everything at debug — carrying the request the command belongs
// to. See DB_MONGO_LOG_LEVEL. Alongside the commands it logs the two things the
// commands cannot show: a connection checkout that failed, and the primary
// moving. See NewMongoPoolMonitor and NewMongoServerMonitor.
//
// It fails fast: the database name is required and the connection is verified
// with a ping, so a misconfigured Mongo is a boot error rather than a surprise
// on the first query.
func NewMongoDB(env IENV, opts ...MongoOption) (IMongoDB, IError) {
	cfg := env.Config()
	if cfg.DBMongoName == "" {
		return nil, New(500, "INVALID_CONFIG",
			"mongo: DB_MONGO_NAME is required (it is not taken from the connection string)")
	}

	o := &mongoOptions{}
	for _, opt := range opts {
		opt(o)
	}
	if !o.logLevelSet {
		// LOG_LEVEL=debug means "show me everything", and that has to include
		// the queries — a database nobody can watch is one nobody can debug
		o.logLevel = mongoLogLevelFrom(env)
	}
	if o.log == nil {
		o.log = NewLogger(env)
	}
	if !o.monitorSet {
		o.monitor = NewMongoMonitor(o.log, o.logLevel, o.slowQuery)
	}
	if !o.poolMonitorSet {
		o.poolMonitor = NewMongoPoolMonitor(o.log, o.logLevel)
	}
	if !o.serverMonitorSet {
		o.serverMonitor = NewMongoServerMonitor(o.log, o.logLevel)
	}

	clientOpts := options.Client().ApplyURI(mongoURI(cfg))
	if o.monitor != nil {
		clientOpts.SetMonitor(o.monitor)
	}
	if o.poolMonitor != nil {
		clientOpts.SetPoolMonitor(o.poolMonitor)
	}
	if o.serverMonitor != nil {
		clientOpts.SetServerMonitor(o.serverMonitor)
	}
	if cfg.DBMongoMaxPoolSize > 0 {
		clientOpts.SetMaxPoolSize(uint64(cfg.DBMongoMaxPoolSize))
	}
	if cfg.DBMongoMinPoolSize > 0 {
		clientOpts.SetMinPoolSize(uint64(cfg.DBMongoMinPoolSize))
	}
	if cfg.DBMongoTimeout > 0 {
		// the driver's own deadline for a whole operation, so a query on a
		// context with no deadline of its own cannot hang forever
		clientOpts.SetTimeout(time.Duration(cfg.DBMongoTimeout) * time.Second)
	}

	ctx, cancel := context.WithTimeout(context.Background(), defaultMongoConnectTimeout)
	defer cancel()

	// driver v2 no longer dials here, so Connect takes no context — the deadline
	// above is what bounds the Ping, which is where a bad host actually surfaces
	client, err := mongo.Connect(clientOpts)
	if err != nil {
		return nil, Wrap(err, "mongo: connect")
	}
	if pingErr := client.Ping(ctx, nil); pingErr != nil {
		_ = client.Disconnect(context.Background())
		return nil, Wrap(pingErr, "mongo: ping")
	}
	return &mongoDB{ctx: context.Background(), client: client, dbName: cfg.DBMongoName}, nil
}

// mongoURI builds the dial URI, preferring an explicit connection string.
//
// The discrete form percent-encodes the credentials — a password holding "@" or
// "/" used to produce a URI that parsed as a different host — and leaves them
// out entirely when there is no username, which is what a local Mongo without
// auth needs.
func mongoURI(cfg *ENVConfig) string {
	if cfg.DBMongoConnectionString != "" {
		return cfg.DBMongoConnectionString
	}

	var b strings.Builder
	b.WriteString("mongodb://")
	if cfg.DBMongoUserName != "" {
		b.WriteString(url.UserPassword(cfg.DBMongoUserName, cfg.DBMongoPassword).String())
		b.WriteByte('@')
	}
	b.WriteString(mongoHosts(cfg))

	query := url.Values{}
	if cfg.DBMongoReplicaName != "" {
		query.Set("replicaSet", cfg.DBMongoReplicaName)
	}
	if cfg.DBMongoTLS {
		query.Set("tls", "true")
	}
	if len(query) > 0 {
		b.WriteString("/?")
		b.WriteString(query.Encode())
	}
	return b.String()
}

// mongoHosts allows DB_MONGO_HOST to be a comma-separated list, which is how a
// replica set is addressed ("a:27017,b:27017").
func mongoHosts(cfg *ENVConfig) string {
	host := cfg.DBMongoHost
	if host == "" {
		host = "127.0.0.1"
	}
	port := cfg.DBMongoPort
	if port == "" {
		port = defaultMongoPort
	}

	parts := strings.Split(host, ",")
	hosts := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if strings.Contains(part, ":") {
			hosts = append(hosts, part) // the entry carries its own port
			continue
		}
		hosts = append(hosts, net.JoinHostPort(part, port))
	}
	return strings.Join(hosts, ",")
}

// ---------------------------------------------------------------------------
// Handle plumbing
// ---------------------------------------------------------------------------

func (m *mongoDB) WithContext(ctx context.Context) IMongoDB {
	if ctx == nil {
		ctx = context.Background()
	}
	cp := *m
	cp.ctx = ctx
	return &cp
}

func (m *mongoDB) Enabled() bool { return true }
func (m *mongoDB) Name() string  { return m.dbName }

// Context is the session's context inside a transaction and the bound one
// outside it, so a driver call made through Collection joins the transaction
// the caller thinks it is in.
func (m *mongoDB) Context() context.Context {
	if m.session != nil {
		return m.session
	}
	return m.ctx
}

// timedContext bounds one operation. Driver v2 removed SetMaxTime from every
// operation's options: a context deadline is what limits an operation now, and
// the driver sends it on to the server as maxTimeMS — so a slow query is still
// killed there rather than only abandoned here.
//
// The cancel is a no-op when there is no deadline to set, so callers can defer
// it unconditionally.
func (m *mongoDB) timedContext(maxTime time.Duration) (context.Context, context.CancelFunc) {
	if maxTime <= 0 {
		return m.Context(), func() {}
	}
	return context.WithTimeout(m.Context(), maxTime)
}

func (m *mongoDB) Database() *mongo.Database {
	if m.readPref != nil {
		return m.client.Database(m.dbName, options.Database().SetReadPreference(m.readPref))
	}
	return m.client.Database(m.dbName)
}

func (m *mongoDB) Client() *mongo.Client { return m.client }

func (m *mongoDB) Collection(name string) *mongo.Collection {
	return m.Database().Collection(name)
}

// WithReadPreference is how a heavy report is kept off the primary. Writes are
// unaffected: the driver always routes those to the primary whatever a read
// preference says.
func (m *mongoDB) WithReadPreference(pref *readpref.ReadPref) IMongoDB {
	cp := *m
	cp.readPref = pref
	return &cp
}

func (m *mongoDB) Ping() IError {
	if err := m.client.Ping(m.Context(), nil); err != nil {
		return m.fail("ping", "", err)
	}
	return nil
}

func (m *mongoDB) Close() IError {
	ctx, cancel := context.WithTimeout(context.Background(), defaultMongoCloseTimeout)
	defer cancel()
	if err := m.client.Disconnect(ctx); err != nil {
		return Wrap(err, "mongo: close")
	}
	return nil
}

// fail wraps a driver error, translating the two outcomes that are answers
// rather than failures, and leaves a breadcrumb for the rest.
func (m *mongoDB) fail(op, collection string, err error) IError {
	if mongo.IsDuplicateKeyError(err) {
		return duplicateKey(collection, err)
	}
	if errors.Is(err, mongo.ErrNoDocuments) {
		return documentNotFound(collection)
	}
	breadcrumbTo(m.ctx, Breadcrumb{
		Type: "error", Category: "mongo." + op, Level: LevelError,
		Message: op + " " + collection,
		Data:    map[string]any{"db": m.dbName, "error": err.Error()},
	})
	return Wrap(err, "mongo: "+op)
}

// ---------------------------------------------------------------------------
// Reads
// ---------------------------------------------------------------------------

func (m *mongoDB) FindOne(dest any, collection string, filter any, opts ...MongoFindOptions) IError {
	findOpts := options.FindOne()
	var maxTime time.Duration
	if len(opts) > 0 {
		o := opts[0]
		if sort := mongoSort(o.Sort); sort != nil {
			findOpts.SetSort(sort)
		}
		if o.Skip > 0 {
			findOpts.SetSkip(o.Skip)
		}
		if o.Projection != nil {
			findOpts.SetProjection(o.Projection)
		}
		if o.Hint != nil {
			findOpts.SetHint(o.Hint)
		}
		if o.Collation != nil {
			findOpts.SetCollation(o.Collation)
		}
		maxTime = o.MaxTime
	}

	ctx, cancel := m.timedContext(maxTime)
	defer cancel()

	res := m.Collection(collection).FindOne(ctx, orEmptyFilter(filter), findOpts)
	if err := res.Decode(dest); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return documentNotFound(collection)
		}
		return m.fail("findOne", collection, err)
	}
	return nil
}

func (m *mongoDB) Find(dest any, collection string, filter any, opts ...MongoFindOptions) IError {
	cur, err := m.FindCursor(collection, filter, opts...)
	if err != nil {
		return err
	}
	if allErr := cur.All(m.Context(), dest); allErr != nil {
		return m.fail("find", collection, allErr)
	}
	return nil
}

func (m *mongoDB) FindCursor(collection string, filter any, opts ...MongoFindOptions) (*mongo.Cursor, IError) {
	findOpts := options.Find()
	var maxTime time.Duration
	if len(opts) > 0 {
		o := opts[0]
		if sort := mongoSort(o.Sort); sort != nil {
			findOpts.SetSort(sort)
		}
		if o.Limit > 0 {
			findOpts.SetLimit(o.Limit)
		}
		if o.Skip > 0 {
			findOpts.SetSkip(o.Skip)
		}
		if o.Projection != nil {
			findOpts.SetProjection(o.Projection)
		}
		if o.Hint != nil {
			findOpts.SetHint(o.Hint)
		}
		if o.Collation != nil {
			findOpts.SetCollation(o.Collation)
		}
		maxTime = o.MaxTime
		if o.BatchSize > 0 {
			findOpts.SetBatchSize(o.BatchSize)
		}
	}

	// the cursor outlives this call, so the deadline has to as well: cancelling
	// here would close it before the caller reads a single document. The timer
	// releases it, and MaxTime bounds how long that takes.
	ctx, cancel := m.timedContext(maxTime)

	cur, err := m.Collection(collection).Find(ctx, orEmptyFilter(filter), findOpts)
	if err != nil {
		cancel()
		return nil, m.fail("find", collection, err)
	}
	return cur, nil
}

func (m *mongoDB) Count(collection string, filter any) (int64, IError) {
	n, err := m.Collection(collection).CountDocuments(m.Context(), orEmptyFilter(filter))
	if err != nil {
		return 0, m.fail("count", collection, err)
	}
	return n, nil
}

// EstimatedCount reads the collection's metadata instead of scanning, which is
// what makes it instant on a large collection — and approximate after an
// unclean shutdown, and unable to take a filter.
func (m *mongoDB) EstimatedCount(collection string) (int64, IError) {
	n, err := m.Collection(collection).EstimatedDocumentCount(m.Context())
	if err != nil {
		return 0, m.fail("estimatedCount", collection, err)
	}
	return n, nil
}

func (m *mongoDB) Exists(collection string, filter any) (bool, IError) {
	// limit 1: existence does not need the whole count, and on a large
	// collection the difference is a scan
	n, err := m.Collection(collection).CountDocuments(m.Context(), orEmptyFilter(filter),
		options.Count().SetLimit(1))
	if err != nil {
		return false, m.fail("exists", collection, err)
	}
	return n > 0, nil
}

func (m *mongoDB) Distinct(dest any, collection, field string, filter any) IError {
	// driver v2 returns a decodable result rather than []any, so dest can be a
	// typed slice ([]string) without the round-trip through BSON this used to do
	res := m.Collection(collection).Distinct(m.Context(), field, orEmptyFilter(filter))
	if err := res.Err(); err != nil {
		return m.fail("distinct", collection, err)
	}
	if err := res.Decode(dest); err != nil {
		return m.fail("distinct", collection, err)
	}
	return nil
}

func (m *mongoDB) Aggregate(dest any, collection string, pipeline any, opts ...MongoAggregateOptions) IError {
	cur, err := m.AggregateCursor(collection, pipeline, opts...)
	if err != nil {
		return err
	}
	if allErr := cur.All(m.Context(), dest); allErr != nil {
		return m.fail("aggregate", collection, allErr)
	}
	return nil
}

func (m *mongoDB) AggregateCursor(collection string, pipeline any, opts ...MongoAggregateOptions) (*mongo.Cursor, IError) {
	if pipeline == nil {
		pipeline = mongo.Pipeline{}
	}
	aggOpts := options.Aggregate()
	var maxTime time.Duration
	if len(opts) > 0 {
		o := opts[0]
		if o.AllowDiskUse {
			aggOpts.SetAllowDiskUse(true)
		}
		maxTime = o.MaxTime
		if o.BatchSize > 0 {
			aggOpts.SetBatchSize(o.BatchSize)
		}
		if o.Hint != nil {
			aggOpts.SetHint(o.Hint)
		}
		if o.Let != nil {
			aggOpts.SetLet(o.Let)
		}
		if o.Collation != nil {
			aggOpts.SetCollation(o.Collation)
		}
		if o.Comment != "" {
			aggOpts.SetComment(o.Comment)
		}
	}

	// as in FindCursor: the cursor outlives this call, so the deadline does too
	ctx, cancel := m.timedContext(maxTime)

	cur, err := m.Collection(collection).Aggregate(ctx, pipeline, aggOpts)
	if err != nil {
		cancel()
		return nil, m.fail("aggregate", collection, err)
	}
	return cur, nil
}

// ---------------------------------------------------------------------------
// Writes
// ---------------------------------------------------------------------------

func (m *mongoDB) InsertOne(collection string, document any) (string, IError) {
	res, err := m.Collection(collection).InsertOne(m.Context(), document)
	if err != nil {
		return "", m.fail("insertOne", collection, err)
	}
	return mongoIDString(res.InsertedID), nil
}

func (m *mongoDB) InsertMany(collection string, documents []any) ([]string, IError) {
	if len(documents) == 0 {
		return nil, nil
	}
	res, err := m.Collection(collection).InsertMany(m.Context(), documents)
	if err != nil {
		return nil, m.fail("insertMany", collection, err)
	}
	ids := make([]string, 0, len(res.InsertedIDs))
	for _, id := range res.InsertedIDs {
		ids = append(ids, mongoIDString(id))
	}
	return ids, nil
}

func (m *mongoDB) UpdateOne(collection string, filter, update any, opts ...MongoUpdateOptions) (MongoUpdateResult, IError) {
	res, err := m.Collection(collection).UpdateOne(m.Context(), orEmptyFilter(filter), update, mongoUpdateOneOptions(opts))
	if err != nil {
		return MongoUpdateResult{}, m.fail("updateOne", collection, err)
	}
	return updateResultOf(res), nil
}

func (m *mongoDB) UpdateMany(collection string, filter, update any, opts ...MongoUpdateOptions) (MongoUpdateResult, IError) {
	res, err := m.Collection(collection).UpdateMany(m.Context(), orEmptyFilter(filter), update, mongoUpdateManyOptions(opts))
	if err != nil {
		return MongoUpdateResult{}, m.fail("updateMany", collection, err)
	}
	return updateResultOf(res), nil
}

func (m *mongoDB) ReplaceOne(collection string, filter, document any, opts ...MongoUpdateOptions) (MongoUpdateResult, IError) {
	replaceOpts := options.Replace()
	if len(opts) > 0 && opts[0].Upsert {
		replaceOpts.SetUpsert(true)
	}
	res, err := m.Collection(collection).ReplaceOne(m.Context(), orEmptyFilter(filter), document, replaceOpts)
	if err != nil {
		return MongoUpdateResult{}, m.fail("replaceOne", collection, err)
	}
	return updateResultOf(res), nil
}

func (m *mongoDB) DeleteOne(collection string, filter any) (int64, IError) {
	res, err := m.Collection(collection).DeleteOne(m.Context(), orEmptyFilter(filter))
	if err != nil {
		return 0, m.fail("deleteOne", collection, err)
	}
	return res.DeletedCount, nil
}

func (m *mongoDB) DeleteMany(collection string, filter any) (int64, IError) {
	res, err := m.Collection(collection).DeleteMany(m.Context(), orEmptyFilter(filter))
	if err != nil {
		return 0, m.fail("deleteMany", collection, err)
	}
	return res.DeletedCount, nil
}

func (m *mongoDB) BulkWrite(collection string, models []mongo.WriteModel, ordered bool) (MongoBulkResult, IError) {
	if len(models) == 0 {
		return MongoBulkResult{}, nil
	}
	res, err := m.Collection(collection).BulkWrite(m.Context(), models,
		options.BulkWrite().SetOrdered(ordered))
	if err != nil {
		return MongoBulkResult{}, m.fail("bulkWrite", collection, err)
	}

	out := MongoBulkResult{
		Inserted: res.InsertedCount,
		Matched:  res.MatchedCount,
		Modified: res.ModifiedCount,
		Deleted:  res.DeletedCount,
		Upserted: res.UpsertedCount,
	}
	if len(res.UpsertedIDs) > 0 {
		out.UpsertedIDs = make(map[int64]string, len(res.UpsertedIDs))
		for index, id := range res.UpsertedIDs {
			out.UpsertedIDs[index] = mongoIDString(id)
		}
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Atomic find-and-modify
// ---------------------------------------------------------------------------

func (m *mongoDB) FindOneAndUpdate(dest any, collection string, filter, update any, opts ...MongoFindModifyOptions) IError {
	o := firstFindModify(opts)
	updateOpts := options.FindOneAndUpdate()
	if o.Upsert {
		updateOpts.SetUpsert(true)
	}
	if o.ReturnNew {
		updateOpts.SetReturnDocument(options.After)
	}
	if sort := mongoSort(o.Sort); sort != nil {
		updateOpts.SetSort(sort)
	}
	if o.Projection != nil {
		updateOpts.SetProjection(o.Projection)
	}
	if len(o.ArrayFilters) > 0 {
		// driver v2 takes the filters directly; the options.ArrayFilters wrapper
		// (which also carried a registry) is gone
		updateOpts.SetArrayFilters(o.ArrayFilters)
	}
	if o.Collation != nil {
		updateOpts.SetCollation(o.Collation)
	}

	ctx, cancel := m.timedContext(o.MaxTime)
	defer cancel()

	res := m.Collection(collection).FindOneAndUpdate(ctx, orEmptyFilter(filter), update, updateOpts)
	return m.decodeSingle(res, dest, "findOneAndUpdate", collection)
}

func (m *mongoDB) FindOneAndReplace(dest any, collection string, filter, document any, opts ...MongoFindModifyOptions) IError {
	o := firstFindModify(opts)
	replaceOpts := options.FindOneAndReplace()
	if o.Upsert {
		replaceOpts.SetUpsert(true)
	}
	if o.ReturnNew {
		replaceOpts.SetReturnDocument(options.After)
	}
	if sort := mongoSort(o.Sort); sort != nil {
		replaceOpts.SetSort(sort)
	}
	if o.Projection != nil {
		replaceOpts.SetProjection(o.Projection)
	}
	if o.Collation != nil {
		replaceOpts.SetCollation(o.Collation)
	}

	ctx, cancel := m.timedContext(o.MaxTime)
	defer cancel()

	res := m.Collection(collection).FindOneAndReplace(ctx, orEmptyFilter(filter), document, replaceOpts)
	return m.decodeSingle(res, dest, "findOneAndReplace", collection)
}

func (m *mongoDB) FindOneAndDelete(dest any, collection string, filter any, opts ...MongoFindModifyOptions) IError {
	o := firstFindModify(opts)
	deleteOpts := options.FindOneAndDelete()
	if sort := mongoSort(o.Sort); sort != nil {
		deleteOpts.SetSort(sort)
	}
	if o.Projection != nil {
		deleteOpts.SetProjection(o.Projection)
	}
	if o.Collation != nil {
		deleteOpts.SetCollation(o.Collation)
	}

	ctx, cancel := m.timedContext(o.MaxTime)
	defer cancel()

	res := m.Collection(collection).FindOneAndDelete(ctx, orEmptyFilter(filter), deleteOpts)
	return m.decodeSingle(res, dest, "findOneAndDelete", collection)
}

// decodeSingle decodes a SingleResult, allowing dest to be nil for a caller that
// only wanted the modification to happen.
func (m *mongoDB) decodeSingle(res *mongo.SingleResult, dest any, op, collection string) IError {
	if dest == nil {
		if err := res.Err(); err != nil {
			if errors.Is(err, mongo.ErrNoDocuments) {
				return documentNotFound(collection)
			}
			return m.fail(op, collection, err)
		}
		return nil
	}
	if err := res.Decode(dest); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return documentNotFound(collection)
		}
		return m.fail(op, collection, err)
	}
	return nil
}

func firstFindModify(opts []MongoFindModifyOptions) MongoFindModifyOptions {
	if len(opts) > 0 {
		return opts[0]
	}
	return MongoFindModifyOptions{}
}

// ---------------------------------------------------------------------------
// Schema and sessions
// ---------------------------------------------------------------------------

func (m *mongoDB) EnsureIndex(collection string, index MongoIndex) IError {
	return m.EnsureIndexes(collection, index)
}

func (m *mongoDB) EnsureIndexes(collection string, indexes ...MongoIndex) IError {
	if len(indexes) == 0 {
		return nil
	}
	models := make([]mongo.IndexModel, 0, len(indexes))
	for _, index := range indexes {
		model, err := mongoIndexModel(index)
		if err != nil {
			return err
		}
		models = append(models, model)
	}

	if _, err := m.Collection(collection).Indexes().CreateMany(m.Context(), models); err != nil {
		return m.fail("ensureIndex", collection, err)
	}
	return nil
}

func mongoIndexModel(index MongoIndex) (mongo.IndexModel, IError) {
	keys := mongoIndexKeys(index.Keys)
	if keys == nil {
		return mongo.IndexModel{}, New(400, "INVALID_INDEX", "mongo: an index needs at least one key")
	}

	model := mongo.IndexModel{Keys: keys, Options: options.Index()}
	if index.Name != "" {
		model.Options.SetName(index.Name)
	}
	if index.Unique {
		model.Options.SetUnique(true)
	}
	if index.Sparse {
		model.Options.SetSparse(true)
	}
	if index.TTL > 0 {
		model.Options.SetExpireAfterSeconds(int32(index.TTL.Seconds()))
	}
	if index.Partial != nil {
		model.Options.SetPartialFilterExpression(index.Partial)
	}
	if index.Collation != nil {
		model.Options.SetCollation(index.Collation)
	}
	if index.Weights != nil {
		model.Options.SetWeights(index.Weights)
	}
	return model, nil
}

func (m *mongoDB) DropIndex(collection, name string) IError {
	if err := m.Collection(collection).Indexes().DropOne(m.Context(), name); err != nil {
		return m.fail("dropIndex", collection, err)
	}
	return nil
}

func (m *mongoDB) ListIndexes(collection string) ([]map[string]any, IError) {
	cur, err := m.Collection(collection).Indexes().List(m.Context())
	if err != nil {
		return nil, m.fail("listIndexes", collection, err)
	}
	var indexes []map[string]any
	if allErr := cur.All(m.Context(), &indexes); allErr != nil {
		return nil, m.fail("listIndexes", collection, allErr)
	}
	return indexes, nil
}

func (m *mongoDB) DropCollection(collection string) IError {
	if err := m.Collection(collection).Drop(m.Context()); err != nil {
		return m.fail("dropCollection", collection, err)
	}
	return nil
}

func (m *mongoDB) ListCollections() ([]string, IError) {
	names, err := m.Database().ListCollectionNames(m.Context(), bson.M{})
	if err != nil {
		return nil, m.fail("listCollections", "", err)
	}
	return names, nil
}

// Watch opens a change stream on one collection, or on the whole database when
// collection is empty.
func (m *mongoDB) Watch(collection string, pipeline any) (*mongo.ChangeStream, IError) {
	if pipeline == nil {
		pipeline = mongo.Pipeline{}
	}
	var (
		stream *mongo.ChangeStream
		err    error
	)
	if collection == "" {
		stream, err = m.Database().Watch(m.Context(), pipeline)
	} else {
		stream, err = m.Collection(collection).Watch(m.Context(), pipeline)
	}
	if err != nil {
		return nil, m.fail("watch", collection, err)
	}
	return stream, nil
}

func (m *mongoDB) Transaction(fn func(tx IMongoDB) error) IError {
	if m.session != nil {
		// Mongo has no nested transactions; joining the outer one is what the
		// caller means, and is what a nested SQL transaction would do here too
		return From(fn(m))
	}

	session, err := m.client.StartSession()
	if err != nil {
		return m.fail("transaction", "", err)
	}
	defer session.EndSession(m.ctx)

	_, err = session.WithTransaction(m.ctx, func(sessCtx context.Context) (any, error) {
		tx := *m
		tx.session = sessCtx

		var runErr error
		func() {
			// a panic inside the callback must abort the transaction rather
			// than unwind past the driver with it still open
			defer Recover(&runErr)
			runErr = fn(&tx)
		}()
		return nil, runErr
	})
	if err != nil {
		return m.fail("transaction", "", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// orEmptyFilter turns a nil filter into "match everything", which is what a
// caller passing nil means. The driver would return an error instead.
func orEmptyFilter(filter any) any {
	if filter == nil {
		return bson.M{}
	}
	return filter
}

// mongoFilterMatchesEverything reports whether a filter selects the whole
// collection, so a counter can read the collection's metadata instead of
// walking it — instant rather than a full scan on a large collection, at the
// price of a total that can drift after an unclean shutdown or on a sharded
// collection still holding orphans. Approximate is the right trade for a page
// header; an exact number wants Count with a filter that narrows something.
//
// A type switch rather than reflection: these are the shapes a filter actually
// arrives in, and anything else falls through to the exact count, which is the
// safe answer.
func mongoFilterMatchesEverything(filter any) bool {
	switch f := filter.(type) {
	case nil:
		return true
	case bson.M:
		return len(f) == 0
	case bson.D:
		return len(f) == 0
	case map[string]any:
		return len(f) == 0
	default:
		return false
	}
}

// mongoSort turns []string{"-created_at", "name"} into the driver's sort
// document. Order is preserved, which is what makes a compound sort — and a
// compound index — mean what it says.
func mongoSort(fields []string) bson.D {
	sort := bson.D{}
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		direction := 1
		switch field[0] {
		case '-':
			direction, field = -1, field[1:]
		case '+':
			field = field[1:]
		}
		// "created_at desc", the SQL spelling PageOptions.OrderBy also accepts
		if name, order, found := strings.Cut(field, " "); found {
			field = name
			if strings.EqualFold(strings.TrimSpace(order), "desc") {
				direction = -1
			}
		}
		if field != "" {
			sort = append(sort, bson.E{Key: field, Value: direction})
		}
	}
	if len(sort) == 0 {
		return nil
	}
	return sort
}

// MongoSortDocument turns the "-created_at" spelling used by PageOptions and
// MongoFindOptions into the driver's sort document, for code building a $sort stage
// by hand.
func MongoSortDocument(fields []string) bson.D { return mongoSort(fields) }

// mongoIndexKeys is mongoSort plus the non-numeric index types: an entry of
// "name:text" or "location:2dsphere" builds that kind of index, which a
// direction alone cannot express.
func mongoIndexKeys(fields []string) bson.D {
	keys := bson.D{}
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		if name, kind, found := strings.Cut(field, ":"); found {
			if name = strings.TrimSpace(name); name != "" {
				keys = append(keys, bson.E{Key: name, Value: strings.TrimSpace(kind)})
			}
			continue
		}
		if entry := mongoSort([]string{field}); entry != nil {
			keys = append(keys, entry...)
		}
	}
	if len(keys) == 0 {
		return nil
	}
	return keys
}

// Driver v2 split UpdateOptions into one builder per operation, so the shared
// helper became two.
func mongoUpdateOneOptions(opts []MongoUpdateOptions) *options.UpdateOneOptionsBuilder {
	updateOpts := options.UpdateOne()
	if len(opts) > 0 && opts[0].Upsert {
		updateOpts.SetUpsert(true)
	}
	return updateOpts
}

func mongoUpdateManyOptions(opts []MongoUpdateOptions) *options.UpdateManyOptionsBuilder {
	updateOpts := options.UpdateMany()
	if len(opts) > 0 && opts[0].Upsert {
		updateOpts.SetUpsert(true)
	}
	return updateOpts
}

func updateResultOf(res *mongo.UpdateResult) MongoUpdateResult {
	if res == nil {
		return MongoUpdateResult{}
	}
	return MongoUpdateResult{
		Matched:    res.MatchedCount,
		Modified:   res.ModifiedCount,
		Upserted:   res.UpsertedCount,
		UpsertedID: mongoIDString(res.UpsertedID),
	}
}

// mongoIDString renders an id as something that can be handed back to a filter.
// fmt.Sprintf on an ObjectID gives `ObjectID("507f…")`, which matches nothing —
// the bug this replaces.
func mongoIDString(id any) string {
	switch v := id.(type) {
	case nil:
		return ""
	case bson.ObjectID:
		return v.Hex()
	case string:
		return v
	case fmt.Stringer:
		return v.String()
	default:
		return fmt.Sprintf("%v", v)
	}
}

// MongoObjectID parses a hex id. A filter built with the string instead of the
// ObjectID matches nothing, silently, which is the single most common way to
// lose an afternoon with Mongo.
func MongoObjectID(id string) (bson.ObjectID, IError) {
	oid, err := bson.ObjectIDFromHex(id)
	if err != nil {
		return bson.NilObjectID, New(400, "INVALID_ID", "mongo: invalid object id").WithCause(err)
	}
	return oid, nil
}

// MongoByID is the _id filter for a hex id, falling back to the raw value for a
// collection whose ids are strings of your own making:
//
//	err := ctx.DBMongo().FindOne(&user, "users", core.MongoByID(id))
func MongoByID(id string) bson.M {
	if oid, err := bson.ObjectIDFromHex(id); err == nil {
		return bson.M{"_id": oid}
	}
	return bson.M{"_id": id}
}
