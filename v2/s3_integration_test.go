//go:build integration

package core

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These exercise what only a real server can: signing, multipart uploads, the
// batch delete, paginated listing, server-side copy and presigned URLs that
// something can actually serve. The unit tests cover the same contract against
// the in-memory backend.
//
// Against MinIO:
//
//	docker run -p 9000:9000 -e MINIO_ROOT_USER=minioadmin \
//	  -e MINIO_ROOT_PASSWORD=minioadmin minio/minio server /data
//	APP_S3_ENDPOINT=127.0.0.1:9000 APP_S3_ACCESS_KEY=minioadmin \
//	  APP_S3_SECRET_KEY=minioadmin APP_S3_BUCKET=coretest \
//	  APP_S3_FORCE_PATH_STYLE=true make test-integration
func newRealTestStorage(t *testing.T) IStorage {
	t.Helper()

	env, err := NewEnvPath(t.TempDir())
	require.NoError(t, err)
	if env.Config().S3Bucket == "" {
		t.Skip("no APP_S3_BUCKET configured")
	}

	// every test gets its own prefix, so a failed run leaves nothing behind
	prefix := fmt.Sprintf("coretest/%s", strings.ReplaceAll(t.Name(), "/", "_"))
	s, err := NewStorage(env, WithStoragePrefix(prefix))
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if pingErr := s.WithContext(ctx).Ping(); pingErr != nil {
		t.Skipf("no reachable bucket: %v", pingErr)
	}

	t.Cleanup(func() { _, _ = s.DeleteByPrefix("") })
	return s
}

func TestRealStorage_putGetStatDelete(t *testing.T) {
	s := newRealTestStorage(t)

	require.NoError(t, s.PutBytes("docs/1.pdf", []byte("hello"), StoragePutOptions{
		Metadata: map[string]string{"owner": "u-1"},
	}))

	info, err := s.Stat("docs/1.pdf")
	require.NoError(t, err)
	assert.Equal(t, int64(5), info.Size)
	assert.Equal(t, "application/pdf", info.ContentType)
	assert.Equal(t, "u-1", info.Metadata["owner"])
	assert.NotEmpty(t, info.ETag)

	data, err := s.GetBytes("docs/1.pdf")
	require.NoError(t, err)
	assert.Equal(t, "hello", string(data))

	require.NoError(t, s.Delete("docs/1.pdf"))
	_, err = s.Get("docs/1.pdf")
	assert.True(t, errors.Is(err, ErrObjectNotFound))
}

func TestRealStorage_missIsNotAServerError(t *testing.T) {
	s := newRealTestStorage(t)

	_, err := s.Get("nope")
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrObjectNotFound), "GetObject's 404 must be recognised")
	assert.Equal(t, 404, err.GetStatus())

	// HeadObject answers with no body, so its 404 arrives as a different type
	_, statErr := s.Stat("nope")
	assert.True(t, errors.Is(statErr, ErrObjectNotFound), "HeadObject's 404 must be recognised too")

	ok, existsErr := s.Exists("nope")
	require.NoError(t, existsErr)
	assert.False(t, ok)
}

func TestRealStorage_multipartUpload(t *testing.T) {
	// larger than uploadPartSize, so this takes the transfer manager's multipart path
	s := newRealTestStorage(t)
	body := strings.NewReader(strings.Repeat("0123456789", 700_000)) // 7MB

	require.NoError(t, s.Put("big.bin", body))

	info, err := s.Stat("big.bin")
	require.NoError(t, err)
	assert.Equal(t, int64(7_000_000), info.Size)
}

func TestRealStorage_listPagesThroughEverything(t *testing.T) {
	s := newRealTestStorage(t)
	for i := range 12 {
		require.NoError(t, s.PutBytes(fmt.Sprintf("many/%02d.txt", i), []byte("x")))
	}

	objects, err := s.List("many/")
	require.NoError(t, err)
	assert.Len(t, objects, 12)
	assert.Equal(t, "many/00.txt", objects[0].Key, "keys come back without the prefix, in order")

	limited, err := s.List("many/", StorageListOptions{Limit: 5})
	require.NoError(t, err)
	assert.Len(t, limited, 5)
}

func TestRealStorage_batchDeleteAndDeleteByPrefix(t *testing.T) {
	s := newRealTestStorage(t)
	keys := make([]string, 0, 10)
	for i := range 10 {
		key := fmt.Sprintf("batch/%d.txt", i)
		require.NoError(t, s.PutBytes(key, []byte("x")))
		keys = append(keys, key)
	}

	require.NoError(t, s.Delete(append(keys[:5], "never-existed")...))
	remaining, err := s.List("batch/")
	require.NoError(t, err)
	assert.Len(t, remaining, 5)

	n, err := s.DeleteByPrefix("batch/")
	require.NoError(t, err)
	assert.Equal(t, int64(5), n)
}

func TestRealStorage_copyAndMove(t *testing.T) {
	s := newRealTestStorage(t)
	require.NoError(t, s.PutBytes("tmp/my file+1.png", []byte("data")))

	// a key with a space and a plus is exactly what a naive copy source breaks on
	require.NoError(t, s.Copy("tmp/my file+1.png", "avatars/u1.png"))
	copied, err := s.GetBytes("avatars/u1.png")
	require.NoError(t, err)
	assert.Equal(t, "data", string(copied))

	require.NoError(t, s.Move("tmp/my file+1.png", "avatars/u2.png"))
	ok, err := s.Exists("tmp/my file+1.png")
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestRealStorage_presignedGetIsServable(t *testing.T) {
	s := newRealTestStorage(t)
	require.NoError(t, s.PutBytes("docs/1.pdf", []byte("hello")))

	link, err := s.PresignGet("docs/1.pdf", 5*time.Minute, StoragePresignOptions{Attachment: "report.pdf"})
	require.NoError(t, err)

	resp, httpErr := http.Get(link) //nolint:gosec,noctx // the URL is the thing under test
	require.NoError(t, httpErr)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusOK, resp.StatusCode, "the signed link must actually work")
	assert.Contains(t, resp.Header.Get("Content-Disposition"), "report.pdf")
}

func TestRealStorage_presignedPutAcceptsAnUpload(t *testing.T) {
	s := newRealTestStorage(t)

	link, err := s.PresignPut("uploads/new.png", 5*time.Minute)
	require.NoError(t, err)

	req, reqErr := http.NewRequest(http.MethodPut, link, strings.NewReader("png-bytes")) //nolint:noctx // see above
	require.NoError(t, reqErr)
	req.Header.Set("Content-Type", "image/png") // must match what was signed

	resp, doErr := http.DefaultClient.Do(req)
	require.NoError(t, doErr)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	data, err := s.GetBytes("uploads/new.png")
	require.NoError(t, err)
	assert.Equal(t, "png-bytes", string(data))
}

func TestRealStorage_requestCancellationStopsTheCall(t *testing.T) {
	s := newRealTestStorage(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := s.WithContext(ctx).PutBytes("cancelled.txt", []byte("x"))
	require.Error(t, err, "a cancelled request must not keep uploading")
}
