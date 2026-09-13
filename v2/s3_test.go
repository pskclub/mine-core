package core

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestStorage(t *testing.T) IStorage {
	t.Helper()
	return NewMemoryStorage()
}

func TestStorage_putGetRoundTrip(t *testing.T) {
	s := newTestStorage(t)

	require.NoError(t, s.Put("avatars/u1.png", strings.NewReader("binary")))

	body, err := s.Get("avatars/u1.png")
	require.NoError(t, err)
	defer func() { _ = body.Close() }()

	data, readErr := io.ReadAll(body)
	require.NoError(t, readErr)
	assert.Equal(t, "binary", string(data))
}

func TestStorage_contentTypeIsGuessedFromTheKey(t *testing.T) {
	s := newTestStorage(t)

	require.NoError(t, s.PutBytes("avatars/u1.png", []byte("x")))
	info, err := s.Stat("avatars/u1.png")
	require.NoError(t, err)
	assert.Equal(t, "image/png", info.ContentType, "an avatar must not be served as octet-stream")

	require.NoError(t, s.PutBytes("blob", []byte("x")))
	info, err = s.Stat("blob")
	require.NoError(t, err)
	assert.Equal(t, "application/octet-stream", info.ContentType, "no extension, no guess")

	require.NoError(t, s.PutBytes("weird.png", []byte("x"), StoragePutOptions{ContentType: "text/plain"}))
	info, err = s.Stat("weird.png")
	require.NoError(t, err)
	assert.Equal(t, "text/plain", info.ContentType, "an explicit type wins")
}

func TestStorage_missIsIdentifiable(t *testing.T) {
	s := newTestStorage(t)

	_, err := s.Get("nope")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrObjectNotFound), "absence must be tellable from a failure")
	assert.Equal(t, 404, err.GetStatus())
	assert.Equal(t, "OBJECT_NOT_FOUND", err.GetCode())

	_, statErr := s.Stat("nope")
	assert.True(t, errors.Is(statErr, ErrObjectNotFound))

	ok, existsErr := s.Exists("nope")
	require.NoError(t, existsErr, "a missing object is not an error for Exists")
	assert.False(t, ok)
}

func TestStorage_statReportsWhatWasStored(t *testing.T) {
	s := newTestStorage(t)
	require.NoError(t, s.PutBytes("docs/1.pdf", []byte("hello"), StoragePutOptions{
		Metadata: map[string]string{"owner": "u-1"},
	}))

	info, err := s.Stat("docs/1.pdf")
	require.NoError(t, err)
	assert.Equal(t, "docs/1.pdf", info.Key)
	assert.Equal(t, int64(5), info.Size)
	assert.Equal(t, "application/pdf", info.ContentType)
	assert.Equal(t, "u-1", info.Metadata["owner"])
	assert.False(t, info.LastModified.IsZero())
}

func TestStorage_list(t *testing.T) {
	s := newTestStorage(t)
	require.NoError(t, s.PutBytes("users/1/avatar.png", []byte("a")))
	require.NoError(t, s.PutBytes("users/2/avatar.png", []byte("b")))
	require.NoError(t, s.PutBytes("reports/1.pdf", []byte("c")))

	objects, err := s.List("users/")
	require.NoError(t, err)
	require.Len(t, objects, 2)
	assert.Equal(t, "users/1/avatar.png", objects[0].Key, "listings are ordered")
	assert.Equal(t, "users/2/avatar.png", objects[1].Key)

	limited, err := s.List("users/", StorageListOptions{Limit: 1})
	require.NoError(t, err)
	assert.Len(t, limited, 1)

	all, err := s.List("")
	require.NoError(t, err)
	assert.Len(t, all, 3)
}

func TestStorage_copyAndMove(t *testing.T) {
	s := newTestStorage(t)
	require.NoError(t, s.PutBytes("tmp/upload.png", []byte("data")))

	require.NoError(t, s.Copy("tmp/upload.png", "avatars/u1.png"))
	copied, err := s.GetBytes("avatars/u1.png")
	require.NoError(t, err)
	assert.Equal(t, "data", string(copied))

	ok, err := s.Exists("tmp/upload.png")
	require.NoError(t, err)
	assert.True(t, ok, "a copy leaves the source alone")

	require.NoError(t, s.Move("tmp/upload.png", "avatars/u2.png"))
	ok, err = s.Exists("tmp/upload.png")
	require.NoError(t, err)
	assert.False(t, ok, "a move does not")

	moved, err := s.GetBytes("avatars/u2.png")
	require.NoError(t, err)
	assert.Equal(t, "data", string(moved))
}

func TestStorage_copyOfAMissingObject(t *testing.T) {
	s := newTestStorage(t)
	err := s.Copy("nope", "somewhere")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrObjectNotFound))
}

func TestStorage_deleteAndDeleteByPrefix(t *testing.T) {
	s := newTestStorage(t)
	require.NoError(t, s.PutBytes("users/1/a.png", []byte("a")))
	require.NoError(t, s.PutBytes("users/1/b.png", []byte("b")))
	require.NoError(t, s.PutBytes("users/2/c.png", []byte("c")))

	require.NoError(t, s.Delete("users/1/a.png", "never-existed"), "an absent key is not an error")

	n, err := s.DeleteByPrefix("users/1/")
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)

	ok, err := s.Exists("users/2/c.png")
	require.NoError(t, err)
	assert.True(t, ok, "objects outside the prefix survive")
}

func TestStorage_prefixNamespacesKeys(t *testing.T) {
	s := newTestStorage(t)
	tenant := s.WithPrefix("tenants/42")

	assert.Equal(t, "tenants/42/avatar.png", tenant.Key("avatar.png"), "a missing separator is added")
	require.NoError(t, tenant.PutBytes("avatar.png", []byte("x")))

	_, err := s.Get("avatar.png")
	assert.True(t, errors.Is(err, ErrObjectNotFound), "the unprefixed handle must not see it")

	ok, err := s.Exists("tenants/42/avatar.png")
	require.NoError(t, err)
	assert.True(t, ok, "the object really is stored under the prefix")

	objects, err := tenant.List("")
	require.NoError(t, err)
	require.Len(t, objects, 1)
	assert.Equal(t, "avatar.png", objects[0].Key, "listings report the caller's own keys")
}

func TestStorage_presignedLinksCarryTheirOptions(t *testing.T) {
	s := newTestStorage(t)

	link, err := s.PresignGet("docs/1.pdf", 15*time.Minute, StoragePresignOptions{Attachment: "report.pdf"})
	require.NoError(t, err)
	assert.Contains(t, link, "docs/1.pdf")
	assert.Contains(t, link, "report.pdf")

	upload, err := s.PresignPut("uploads/new.png", time.Minute)
	require.NoError(t, err)
	assert.Contains(t, upload, "image%2Fpng", "the link is signed for the type the client must send")
}

func TestStorage_disabledFailsLoudly(t *testing.T) {
	// unlike the cache, storage must not degrade quietly: an upload that was
	// dropped is a file nobody can get back
	ctx := newTestApp(t).NewContext(t.Context())
	s := ctx.Storage()

	require.NotNil(t, s, "never nil, so there is no panic to debug")
	assert.False(t, s.Enabled())

	err := s.PutBytes("k", []byte("x"))
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrStorageDisabled))
	assert.Equal(t, "STORAGE_DISABLED", err.GetCode())
	assert.Equal(t, 503, err.GetStatus())

	_, getErr := s.Get("k")
	assert.Error(t, getErr)
	_, listErr := s.List("")
	assert.Error(t, listErr)
	assert.Empty(t, s.PublicURL("k"))
}

func TestStorage_isBoundToTheRequest(t *testing.T) {
	app := newTestApp(t, WithStorage(NewMemoryStorage()))
	ctx := app.NewContext(context.Background())

	require.True(t, ctx.Storage().Enabled())
	require.NoError(t, ctx.Storage().PutBytes("k", []byte("x")))

	// the App's handle and the request's handle share one store
	data, err := app.Storage().GetBytes("k")
	require.NoError(t, err)
	assert.Equal(t, "x", string(data))
}

// --- configuration -------------------------------------------------------

func TestNewStorage_requiresABucket(t *testing.T) {
	env := mustEnv(t, map[string]string{"ENV": "test"})
	_, err := NewStorage(env)
	require.Error(t, err, "a missing bucket is a boot error, not a surprise on the first upload")
	assert.Equal(t, "INVALID_CONFIG", err.GetCode())
}

func TestNewStorage_worksWithoutStaticCredentials(t *testing.T) {
	// an instance role sets no keys; v2.2 installed empty static credentials
	// here and every signed request failed
	env := mustEnv(t, map[string]string{
		"ENV": "test", "S3_BUCKET": "b", "S3_REGION": "ap-southeast-1",
	})
	s, err := NewStorage(env)
	require.NoError(t, err)
	assert.Equal(t, "b", s.Bucket())
	assert.True(t, s.Enabled())
}

func TestNewStorage_presignedGetSignsOnlyTheHost(t *testing.T) {
	// A presigned GET exists to be handed to something that sends no AWS
	// headers — a browser, an <img> tag, a curl. aws-sdk-go-v2 v1.43 started
	// putting x-amz-checksum-mode into X-Amz-SignedHeaders by default, and every
	// such link began answering 400 because the signature covered a header the
	// caller never sends. Nothing in the memory backend can catch that.
	env := mustEnv(t, map[string]string{
		"ENV": "test", "S3_BUCKET": "b", "S3_REGION": "ap-southeast-1",
		"S3_ACCESS_KEY": "key", "S3_SECRET_KEY": "secret",
	})
	s, err := NewStorage(env)
	require.NoError(t, err)

	link, presignErr := s.PresignGet("docs/1.pdf", time.Minute)
	require.NoError(t, presignErr)

	parsed, parseErr := url.Parse(link)
	require.NoError(t, parseErr)
	assert.Equal(t, "host", parsed.Query().Get("X-Amz-SignedHeaders"),
		"anything beyond host is a header the browser following this link will not send")
}

func TestNewStorage_prefixFromConfig(t *testing.T) {
	env := mustEnv(t, map[string]string{
		"ENV": "test", "S3_BUCKET": "b", "S3_PREFIX": "svc",
	})
	s, err := NewStorage(env)
	require.NoError(t, err)
	assert.Equal(t, "svc/avatar.png", s.Key("avatar.png"))
}

func TestS3Region_defaultsToSingapore(t *testing.T) {
	// the services on this framework run in ap-southeast-1, and so does v1's
	// default — a bucket there signs correctly with S3_REGION left unset
	assert.Equal(t, "ap-southeast-1", s3Region(&ENVConfig{}))
	assert.Equal(t, "eu-west-1", s3Region(&ENVConfig{S3Region: "eu-west-1"}),
		"a configured region always wins; the default is only for an unset one")
}

func TestS3Endpoint_addsTheSchemeConfigAsksFor(t *testing.T) {
	// S3_HTTPS was read from configuration and never used before this
	assert.Equal(t, "http://minio:9000", s3Endpoint(&ENVConfig{S3Endpoint: "minio:9000"}))
	assert.Equal(t, "https://minio:9000",
		s3Endpoint(&ENVConfig{S3Endpoint: "minio:9000", S3IsHTTPS: true}))
	assert.Equal(t, "https://s3.example.com",
		s3Endpoint(&ENVConfig{S3Endpoint: "https://s3.example.com"}), "an explicit scheme is left alone")
	assert.Empty(t, s3Endpoint(&ENVConfig{}))
}

func TestPublicURL_usesTheCDNWhenThereIsOne(t *testing.T) {
	env := mustEnv(t, map[string]string{
		"ENV": "test", "S3_BUCKET": "b", "S3_REGION": "ap-southeast-1",
		"S3_PUBLIC_URL": "https://cdn.example.com/",
	})
	s, err := NewStorage(env)
	require.NoError(t, err)
	assert.Equal(t, "https://cdn.example.com/avatars/u1.png", s.PublicURL("avatars/u1.png"))
}

func TestPublicURL_fallsBackToTheBucketAddress(t *testing.T) {
	env := mustEnv(t, map[string]string{
		"ENV": "test", "S3_BUCKET": "b", "S3_REGION": "ap-southeast-1",
	})
	s, err := NewStorage(env)
	require.NoError(t, err)
	assert.Equal(t, "https://b.s3.ap-southeast-1.amazonaws.com/avatars/u1.png",
		s.PublicURL("avatars/u1.png"))
}

func TestPublicURL_usesTheEndpointForACompatibleServer(t *testing.T) {
	env := mustEnv(t, map[string]string{
		"ENV": "test", "S3_BUCKET": "b", "S3_ENDPOINT": "minio:9000",
		"S3_FORCE_PATH_STYLE": "true",
	})
	s, err := NewStorage(env)
	require.NoError(t, err)
	assert.Equal(t, "http://minio:9000/b/avatars/u1.png", s.PublicURL("avatars/u1.png"))
}

// --- helpers -------------------------------------------------------------

func TestCopySource_escapesEachSegment(t *testing.T) {
	assert.Equal(t, "/bucket/a/b.png", copySource("bucket", "a/b.png"))
	assert.Equal(t, "/bucket/a/my%20file+1.png", copySource("bucket", "a/my file+1.png"),
		"a space must be escaped and the slashes must not be")
}

func TestAttachmentHeader_survivesANonASCIIName(t *testing.T) {
	header := attachmentHeader("รายงาน 2026.pdf")
	assert.Contains(t, header, `filename="`)
	assert.Contains(t, header, "filename*=UTF-8''")
	assert.NotContains(t, header, "\n")
}

func TestSlicesChunk(t *testing.T) {
	assert.Nil(t, slicesChunk([]string{}, 2))
	assert.Equal(t, [][]int{{1, 2}, {3, 4}, {5}}, slicesChunk([]int{1, 2, 3, 4, 5}, 2))
	assert.Equal(t, [][]int{{1, 2, 3}}, slicesChunk([]int{1, 2, 3}, 10))
}

func TestIsNotFound(t *testing.T) {
	assert.True(t, isNotFound(&types.NoSuchKey{}))
	assert.True(t, isNotFound(&types.NotFound{}))
	assert.False(t, isNotFound(errors.New("connection refused")))
	assert.False(t, isNotFound(nil))
}

func TestStorage_putNilBodyStoresAnEmptyObject(t *testing.T) {
	s := newTestStorage(t)
	require.NoError(t, s.Put("empty", nil))

	info, err := s.Stat("empty")
	require.NoError(t, err)
	assert.Equal(t, int64(0), info.Size)
}

func TestStorage_getBytesIsACopy(t *testing.T) {
	// the memory backend must not hand out the slice it keeps, or a caller
	// mutating what it read would silently rewrite the stored object
	s := newTestStorage(t)
	require.NoError(t, s.Put("k", bytes.NewReader([]byte("original"))))

	first, err := s.GetBytes("k")
	require.NoError(t, err)
	copy(first, "MUTATED!")

	second, err := s.GetBytes("k")
	require.NoError(t, err)
	assert.Equal(t, "original", string(second))
}
