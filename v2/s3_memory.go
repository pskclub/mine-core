package core

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// memoryStorage is an in-process IStorage. A test that uploads a file should
// not need MinIO running, and a dev box should not need one either.
//
// It keeps objects in memory with their metadata, so content types, sizes,
// prefixes, listings and not-found errors behave as they do against S3. What it
// cannot do is outlive the process or be seen by another one — and its presigned
// URLs are fake, since there is no server to present them to.
type memoryStorage struct {
	ctx    context.Context
	store  *memoryObjectStore
	prefix string
	bucket string
}

var _ IStorage = (*memoryStorage)(nil)

type memoryObjectStore struct {
	mu      sync.RWMutex
	objects map[string]memoryObject
}

type memoryObject struct {
	data []byte
	info StorageObject
}

// NewMemoryStorage returns in-process storage. Wire it like any other:
//
//	app, _ := core.NewApp(env, core.WithStorage(core.NewMemoryStorage()))
func NewMemoryStorage() IStorage {
	return &memoryStorage{
		ctx:    context.Background(),
		store:  &memoryObjectStore{objects: map[string]memoryObject{}},
		bucket: "memory",
	}
}

// ---------------------------------------------------------------------------
// Handle plumbing
// ---------------------------------------------------------------------------

func (m *memoryStorage) WithContext(ctx context.Context) IStorage {
	if ctx == nil {
		ctx = context.Background()
	}
	cp := *m
	cp.ctx = ctx
	return &cp
}

func (m *memoryStorage) WithPrefix(prefix string) IStorage {
	cp := *m
	cp.prefix += normalizeKeyPrefix(prefix)
	return &cp
}

func (m *memoryStorage) Enabled() bool         { return true }
func (m *memoryStorage) Bucket() string        { return m.bucket }
func (m *memoryStorage) Key(key string) string { return m.prefix + key }
func (m *memoryStorage) Ping() IError          { return nil }
func (m *memoryStorage) S3() *s3.Client        { return nil }

func (m *memoryStorage) unkey(key string) string { return strings.TrimPrefix(key, m.prefix) }

// ---------------------------------------------------------------------------
// Objects
// ---------------------------------------------------------------------------

func (m *memoryStorage) Put(key string, body io.Reader, opts ...StoragePutOptions) IError {
	var data []byte
	if body != nil {
		read, err := io.ReadAll(body)
		if err != nil {
			return Wrap(err, "storage: put")
		}
		data = read
	}
	return m.PutBytes(key, data, opts...)
}

func (m *memoryStorage) PutBytes(key string, data []byte, opts ...StoragePutOptions) IError {
	opt := firstPutOption(opts)
	stored := append([]byte(nil), data...)

	// S3's ETag for a single-part upload is the MD5 of the body, so a test
	// comparing two uploads by ETag sees the same answer it would get from S3;
	// a copy of the metadata, so the caller's map is not shared with the store
	var metadata map[string]string
	if len(opt.Metadata) > 0 {
		metadata = make(map[string]string, len(opt.Metadata))
		for k, v := range opt.Metadata {
			metadata[k] = v
		}
	}

	m.store.mu.Lock()
	defer m.store.mu.Unlock()
	m.store.objects[m.Key(key)] = memoryObject{
		data: stored,
		info: StorageObject{
			Key:          key,
			Size:         int64(len(stored)),
			ContentType:  contentTypeFor(key, opt.ContentType),
			ETag:         fmt.Sprintf("%x", md5.Sum(stored)),
			LastModified: time.Now().UTC(),
			Metadata:     metadata,
		},
	}
	return nil
}

func (m *memoryStorage) Get(key string) (io.ReadCloser, IError) {
	data, err := m.GetBytes(key)
	if err != nil {
		return nil, err
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (m *memoryStorage) GetBytes(key string) ([]byte, IError) {
	m.store.mu.RLock()
	defer m.store.mu.RUnlock()
	obj, ok := m.store.objects[m.Key(key)]
	if !ok {
		return nil, objectNotFound(key)
	}
	return append([]byte(nil), obj.data...), nil
}

func (m *memoryStorage) Stat(key string) (StorageObject, IError) {
	m.store.mu.RLock()
	defer m.store.mu.RUnlock()
	obj, ok := m.store.objects[m.Key(key)]
	if !ok {
		return StorageObject{}, objectNotFound(key)
	}
	info := obj.info
	info.Key = key
	return info, nil
}

func (m *memoryStorage) Exists(key string) (bool, IError) {
	_, err := m.Stat(key)
	if err != nil {
		if errors.Is(err, ErrObjectNotFound) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (m *memoryStorage) List(prefix string, opts ...StorageListOptions) ([]StorageObject, IError) {
	limit := 0
	if len(opts) > 0 {
		limit = opts[0].Limit
	}
	full := m.Key(prefix)

	m.store.mu.RLock()
	keys := make([]string, 0, len(m.store.objects))
	for key := range m.store.objects {
		if strings.HasPrefix(key, full) {
			keys = append(keys, key)
		}
	}
	m.store.mu.RUnlock()

	// S3 lists in lexicographic order, and code that pages through a listing
	// depends on that; a map's order would make such a test pass by luck
	sort.Strings(keys)

	out := make([]StorageObject, 0, len(keys))
	m.store.mu.RLock()
	defer m.store.mu.RUnlock()
	for _, key := range keys {
		obj, ok := m.store.objects[key]
		if !ok {
			continue
		}
		info := obj.info
		info.Key = m.unkey(key)
		out = append(out, info)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (m *memoryStorage) Copy(srcKey, dstKey string) IError {
	m.store.mu.Lock()
	defer m.store.mu.Unlock()
	src, ok := m.store.objects[m.Key(srcKey)]
	if !ok {
		return objectNotFound(srcKey)
	}
	copied := src
	copied.data = append([]byte(nil), src.data...)
	copied.info.Key = dstKey
	copied.info.LastModified = time.Now().UTC()
	m.store.objects[m.Key(dstKey)] = copied
	return nil
}

func (m *memoryStorage) Move(srcKey, dstKey string) IError {
	if err := m.Copy(srcKey, dstKey); err != nil {
		return err
	}
	return m.Delete(srcKey)
}

func (m *memoryStorage) Delete(keys ...string) IError {
	m.store.mu.Lock()
	defer m.store.mu.Unlock()
	for _, key := range keys {
		delete(m.store.objects, m.Key(key))
	}
	return nil
}

func (m *memoryStorage) DeleteByPrefix(prefix string) (int64, IError) {
	full := m.Key(prefix)
	m.store.mu.Lock()
	defer m.store.mu.Unlock()
	var deleted int64
	for key := range m.store.objects {
		if strings.HasPrefix(key, full) {
			delete(m.store.objects, key)
			deleted++
		}
	}
	return deleted, nil
}

// ---------------------------------------------------------------------------
// Links
// ---------------------------------------------------------------------------

// PresignGet returns a URL nothing can serve. It is here so code that builds a
// link compiles and can be asserted on in a test; a browser given one gets
// nowhere, which is the honest outcome for a store with no server.
func (m *memoryStorage) PresignGet(key string, ttl time.Duration, opts ...StoragePresignOptions) (string, IError) {
	query := url.Values{"expires": {fmt.Sprint(int(ttl.Seconds()))}}
	if len(opts) > 0 && opts[0].Attachment != "" {
		query.Set("filename", opts[0].Attachment)
	}
	return m.presignedURL("get", key, query), nil
}

func (m *memoryStorage) PresignPut(key string, ttl time.Duration, opts ...StoragePutOptions) (string, IError) {
	query := url.Values{
		"expires":      {fmt.Sprint(int(ttl.Seconds()))},
		"content-type": {contentTypeFor(key, firstPutOption(opts).ContentType)},
	}
	return m.presignedURL("put", key, query), nil
}

// PresignPost answers the same shape the real storage does. The policy is
// written into the "policy" field as plain JSON rather than signed, so a test
// can read back what the upload would have been held to.
func (m *memoryStorage) PresignPost(key string, ttl time.Duration, opts ...StoragePostOptions) (*StoragePostForm, IError) {
	opt := firstPostOption(opts)
	full := m.Key(key)
	fields := map[string]string{
		"key":                   full,
		"x-amz-expires":         fmt.Sprint(int(ttl.Seconds())),
		"success_action_status": "201",
	}
	if opt.KeyPrefix {
		fields["key"] = full + "${filename}"
	}
	if opt.ContentType != "" {
		fields["Content-Type"] = opt.ContentType
	}
	if opt.CacheControl != "" {
		fields["Cache-Control"] = opt.CacheControl
	}
	if opt.Public {
		fields["acl"] = "public-read"
	}
	if opt.SuccessRedirect != "" {
		delete(fields, "success_action_status")
		fields["success_action_redirect"] = opt.SuccessRedirect
	}
	for k, v := range opt.Metadata {
		fields["x-amz-meta-"+k] = v
	}
	policy, err := json.Marshal(map[string]any{"conditions": postConditions(full, opt)})
	if err != nil {
		return nil, Wrap(err, "storage: presign post")
	}
	fields["policy"] = string(policy)
	return &StoragePostForm{URL: "memory://" + m.bucket + "?op=post", Fields: fields}, nil
}

func (m *memoryStorage) presignedURL(op, key string, query url.Values) string {
	query.Set("op", op)
	return "memory://" + m.bucket + "/" + m.Key(key) + "?" + query.Encode()
}

func (m *memoryStorage) PublicURL(key string) string {
	return "memory://" + m.bucket + "/" + m.Key(key)
}
