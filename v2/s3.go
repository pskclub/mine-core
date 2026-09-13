package core

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	tmtypes "github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

// ErrObjectNotFound is wrapped by the error returned when a key is absent.
// Compare with errors.Is(err, core.ErrObjectNotFound).
var ErrObjectNotFound = errors.New("storage: object not found")

// ErrStorageDisabled is wrapped by the error every operation returns when no
// S3_* configuration is set.
//
// Storage does not degrade the way the cache does. A cache miss is recoverable —
// the value is recomputed — but an upload that is quietly dropped is a file the
// caller believes it saved and nobody can get back, so a service with no bucket
// fails loudly instead.
var ErrStorageDisabled = errors.New("storage: not configured")

// StorageObject is what the store knows about one object.
type StorageObject struct {
	// Key is the object's key without the storage prefix — the same name it was
	// written under.
	Key          string
	Size         int64
	ContentType  string
	ETag         string
	LastModified time.Time
	Metadata     map[string]string
}

// StoragePutOptions describes how an object is stored. Every field is optional; an
// empty ContentType is guessed from the key's extension.
//
// Methods take it variadically so the common call stays short:
//
//	store.Put("avatars/u1.png", r)
//	store.Put("reports/1.pdf", r, core.StoragePutOptions{Attachment: "report.pdf"})
type StoragePutOptions struct {
	// ContentType is what browsers will trust. Guessed from the extension when
	// empty, falling back to application/octet-stream.
	ContentType string
	// Attachment sets Content-Disposition so a download is offered under this
	// filename instead of being rendered inline.
	Attachment string
	// ContentDisposition is set verbatim, overriding Attachment.
	ContentDisposition string
	ContentEncoding    string
	CacheControl       string
	// Metadata becomes x-amz-meta-* headers, returned again by Stat.
	Metadata map[string]string
	// Public marks the object world-readable. It only works on buckets that
	// still allow object ACLs — most modern S3 buckets do not, and serve public
	// objects through a bucket policy or a CDN instead.
	Public bool
}

// StorageListOptions bounds a listing.
type StorageListOptions struct {
	// Limit caps how many objects are returned. 0 means every one, which for a
	// large bucket is many round trips and a large slice — pass a limit unless
	// the prefix is known to be small.
	Limit int
}

// StoragePresignOptions tunes a presigned GET: what the browser is told the object is,
// rather than what it is stored as.
type StoragePresignOptions struct {
	// Attachment makes the link download under this filename.
	Attachment string
	// ContentType overrides the stored type for this link only.
	ContentType string
}

// IStorage is object storage (S3-compatible: AWS S3, MinIO, R2, Spaces).
//
// ctx.Storage() returns a handle bound to the request context, so methods take
// no ctx: a cancelled request stops the upload it started.
type IStorage interface {
	// --- objects ---

	// Put stores body under key, uploading in parts when it is large — so an
	// object of unknown size does not have to fit in memory.
	Put(key string, body io.Reader, opts ...StoragePutOptions) IError
	// PutBytes stores data under key.
	PutBytes(key string, data []byte, opts ...StoragePutOptions) IError
	// Get streams the object. Close it. The error wraps ErrObjectNotFound when
	// the key is absent.
	Get(key string) (io.ReadCloser, IError)
	// GetBytes reads the whole object into memory — for small objects only.
	GetBytes(key string) ([]byte, IError)
	// Stat returns an object's metadata without transferring it.
	Stat(key string) (StorageObject, IError)
	// Exists reports whether key is present. A missing key is not an error.
	Exists(key string) (bool, IError)
	// List returns the objects under a prefix, following pagination.
	List(prefix string, opts ...StorageListOptions) ([]StorageObject, IError)
	// Copy duplicates an object inside the bucket, server-side.
	Copy(srcKey, dstKey string) IError
	// Move copies then deletes: S3 has no rename.
	Move(srcKey, dstKey string) IError
	// Delete removes objects, in batches of 1000. Absent keys are not an error.
	Delete(keys ...string) IError
	// DeleteByPrefix removes everything under a prefix and returns how many
	// objects it deleted.
	DeleteByPrefix(prefix string) (int64, IError)

	// --- links ---

	// PresignGet returns a temporary URL that reads the object, for a client
	// that must not be given credentials.
	PresignGet(key string, ttl time.Duration, opts ...StoragePresignOptions) (string, IError)
	// PresignPut returns a temporary URL that writes the object, so a browser
	// can upload straight to the bucket instead of through the service. The
	// client must send the same Content-Type the link was signed with.
	PresignPut(key string, ttl time.Duration, opts ...StoragePutOptions) (string, IError)
	// PublicURL is the unsigned address of an object, from S3_PUBLIC_URL when
	// set (a CDN) and the endpoint otherwise. It says nothing about whether the
	// object is actually readable — that is the bucket's policy to decide.
	PublicURL(key string) string

	// --- plumbing ---

	// Ping checks the bucket is reachable — for a readiness probe. It needs
	// permission to head the bucket, which object-scoped credentials may lack.
	Ping() IError
	// Enabled reports whether storage is configured.
	Enabled() bool
	// Bucket is the bucket every operation runs against.
	Bucket() string
	// Key is the full key an operation would use: the prefix plus the name.
	Key(key string) string
	// WithPrefix returns a handle that namespaces keys further, e.g.
	// store.WithPrefix("tenants/42") writes "tenants/42/<key>".
	WithPrefix(prefix string) IStorage
	// WithContext returns a handle bound to another context (escape hatch for
	// work that must outlive the request).
	WithContext(ctx context.Context) IStorage
	// S3 exposes the underlying client for anything this interface does not
	// cover. Nil for the memory and disabled backends, and it does not apply the
	// prefix — build keys with Key().
	S3() *s3.Client
}

type storage struct {
	ctx       context.Context
	client    *s3.Client
	presign   *s3.PresignClient
	uploader  *transfermanager.Client
	bucket    string
	prefix    string
	publicURL string
}

var _ IStorage = (*storage)(nil)

// StorageOption tunes a store at construction.
type StorageOption func(*storage)

// WithStoragePrefix namespaces every key. It overrides S3_PREFIX.
func WithStoragePrefix(prefix string) StorageOption {
	return func(s *storage) { s.prefix = normalizeKeyPrefix(prefix) }
}

// deleteBatchSize is what S3's DeleteObjects accepts in one call.
const deleteBatchSize = 1000

// NewStorage builds an S3-compatible storage client from configuration.
//
// Credentials come from S3_ACCESS_KEY/S3_SECRET_KEY when both are set, and
// otherwise from the AWS default chain — which is what lets a deployment on EKS
// or EC2 use an instance role and set no keys at all. (v2.2 always installed
// static credentials, so an empty pair silently overrode the role and every call
// failed to sign.)
func NewStorage(env IENV, opts ...StorageOption) (IStorage, IError) {
	cfg := env.Config()
	if cfg.S3Bucket == "" {
		return nil, New(500, "INVALID_CONFIG", "storage: S3_BUCKET is required")
	}

	loadOpts := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(s3Region(cfg)),
	}
	if cfg.S3AccessKey != "" && cfg.S3SecretKey != "" {
		loadOpts = append(loadOpts, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.S3AccessKey, cfg.S3SecretKey, ""),
		))
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(context.Background(), loadOpts...)
	if err != nil {
		return nil, Wrap(err, "storage: load config")
	}

	endpoint := s3Endpoint(cfg)
	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if endpoint != "" {
			o.BaseEndpoint = aws.String(endpoint)
		}
		// MinIO and most self-hosted gateways serve bucket/key as a path;
		// virtual-host addressing needs DNS per bucket, which they do not have.
		o.UsePathStyle = cfg.S3ForcePathStyle
	})

	s := &storage{
		ctx:       context.Background(),
		client:    client,
		presign:   newPresignClient(client),
		uploader:  newUploader(client),
		bucket:    cfg.S3Bucket,
		prefix:    normalizeKeyPrefix(cfg.S3Prefix),
		publicURL: strings.TrimSuffix(cfg.S3PublicURL, "/"),
	}
	for _, o := range opts {
		o(s)
	}
	return s, nil
}

// newPresignClient turns response checksum validation off for signed links.
//
// The SDK otherwise puts x-amz-checksum-mode into X-Amz-SignedHeaders on a
// presigned GET, and a presigned GET exists precisely to be handed to something
// that will not send that header — a browser, an <img> tag, a curl. The link
// then fails to verify and S3 answers 400. Validation is only skipped for the
// URL, not for GetObject calls this process makes itself.
func newPresignClient(client *s3.Client) *s3.PresignClient {
	return s3.NewPresignClient(client, func(o *s3.PresignOptions) {
		o.ClientOptions = append(o.ClientOptions, func(so *s3.Options) {
			so.ResponseChecksumValidation = aws.ResponseChecksumValidationWhenRequired
		})
	})
}

// uploadPartSize is both the part size and the multipart threshold, which is
// what feature/s3/manager used to do implicitly: it read one part-sized chunk
// and only went multipart when the body did not end there. transfermanager
// splits the two and defaults the threshold to 16MB, so keeping AWS's default
// would have let a 15MB upload buffer three times as much memory as before —
// a change no caller asked for and the one thing this code path promises not
// to do.
const uploadPartSize = 5 * 1024 * 1024

// newUploader wraps the S3 client in the transfer manager. It replaces
// feature/s3/manager, which AWS deprecated in its favour. transfermanager is
// still v0 (v0.3.15 at the time of writing), so it may break API between patch
// releases: read its CHANGELOG before bumping it.
func newUploader(client *s3.Client) *transfermanager.Client {
	return transfermanager.New(client, func(o *transfermanager.Options) {
		o.PartSizeBytes = uploadPartSize
		o.MultipartUploadThreshold = uploadPartSize
	})
}

// s3Region defaults to ap-southeast-1 (Singapore), which is where the services
// built on this framework run and what v1 has always defaulted to — v2 shipping
// us-east-1 was the odd one out.
//
// A default is needed at all because S3-compatible servers ignore the region
// while the signer refuses to sign without one, so an unset region would be a
// confusing failure rather than a missing feature. Since it has to be *some*
// region, it may as well be the one that is usually right: against real AWS S3
// the wrong region fails to sign, and nobody running in Singapore was served by
// having to remember to set it.
func s3Region(cfg *ENVConfig) string {
	if cfg.S3Region != "" {
		return cfg.S3Region
	}
	return "ap-southeast-1"
}

// s3Endpoint adds the scheme S3_HTTPS asks for when the endpoint was given
// without one ("minio:9000"), which is how it is usually written in compose
// files. Until now S3_HTTPS was read from configuration and never used.
func s3Endpoint(cfg *ENVConfig) string {
	endpoint := strings.TrimSpace(cfg.S3Endpoint)
	if endpoint == "" || strings.Contains(endpoint, "://") {
		return endpoint
	}
	if cfg.S3IsHTTPS {
		return "https://" + endpoint
	}
	return "http://" + endpoint
}

// normalizeKeyPrefix makes "tenants/42" and "tenants/42/" mean the same thing.
// Object keys are separated by "/", not by the ":" the cache uses.
func normalizeKeyPrefix(prefix string) string {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" || strings.HasSuffix(prefix, "/") {
		return prefix
	}
	return prefix + "/"
}

// ---------------------------------------------------------------------------
// Handle plumbing
// ---------------------------------------------------------------------------

func (s *storage) WithContext(ctx context.Context) IStorage {
	if ctx == nil {
		ctx = context.Background()
	}
	cp := *s
	cp.ctx = ctx
	return &cp
}

func (s *storage) WithPrefix(prefix string) IStorage {
	cp := *s
	cp.prefix += normalizeKeyPrefix(prefix)
	return &cp
}

func (s *storage) Enabled() bool         { return true }
func (s *storage) Bucket() string        { return s.bucket }
func (s *storage) Key(key string) string { return s.prefix + key }
func (s *storage) S3() *s3.Client        { return s.client }

// unkey is the inverse of Key: callers see the names they wrote, not the
// namespace they were stored under.
func (s *storage) unkey(key string) string { return strings.TrimPrefix(key, s.prefix) }

func (s *storage) Ping() IError {
	_, err := s.client.HeadBucket(s.ctx, &s3.HeadBucketInput{Bucket: aws.String(s.bucket)})
	if err != nil {
		return s.fail("ping", "", err)
	}
	return nil
}

// fail wraps a driver error and leaves a breadcrumb — a failed upload is
// usually what explains the error the user reported a moment later.
func (s *storage) fail(op, key string, err error) IError {
	breadcrumbTo(s.ctx, Breadcrumb{
		Type: "error", Category: "storage." + op, Level: LevelError,
		Message: op + " " + key,
		Data:    map[string]any{"bucket": s.bucket, "error": err.Error()},
	})
	return Wrap(err, "storage: "+op)
}

// ---------------------------------------------------------------------------
// Objects
// ---------------------------------------------------------------------------

func (s *storage) Put(key string, body io.Reader, opts ...StoragePutOptions) IError {
	if body == nil {
		body = bytes.NewReader(nil)
	}
	opt := firstPutOption(opts)
	input := &transfermanager.UploadObjectInput{
		Bucket:      aws.String(s.bucket),
		Key:         aws.String(s.Key(key)),
		Body:        body,
		ContentType: aws.String(contentTypeFor(key, opt.ContentType)),
	}
	if disposition := contentDispositionFor(opt); disposition != "" {
		input.ContentDisposition = aws.String(disposition)
	}
	if opt.ContentEncoding != "" {
		input.ContentEncoding = aws.String(opt.ContentEncoding)
	}
	if opt.CacheControl != "" {
		input.CacheControl = aws.String(opt.CacheControl)
	}
	if len(opt.Metadata) > 0 {
		input.Metadata = opt.Metadata
	}
	if opt.Public {
		input.ACL = tmtypes.ObjectCannedACLPublicRead
	}

	// UploadObject splits a large body into parts and uploads them concurrently,
	// so memory is bounded by the part size rather than by the file.
	if _, err := s.uploader.UploadObject(s.ctx, input); err != nil {
		return s.fail("put", key, err)
	}
	return nil
}

func (s *storage) PutBytes(key string, data []byte, opts ...StoragePutOptions) IError {
	return s.Put(key, bytes.NewReader(data), opts...)
}

func (s *storage) Get(key string) (io.ReadCloser, IError) {
	out, err := s.client.GetObject(s.ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(s.Key(key)),
	})
	if err != nil {
		if isNotFound(err) {
			return nil, objectNotFound(key)
		}
		return nil, s.fail("get", key, err)
	}
	return out.Body, nil
}

func (s *storage) GetBytes(key string) ([]byte, IError) {
	body, err := s.Get(key)
	if err != nil {
		return nil, err
	}
	defer func() { _ = body.Close() }()

	data, readErr := io.ReadAll(body)
	if readErr != nil {
		return nil, s.fail("get", key, readErr)
	}
	return data, nil
}

func (s *storage) Stat(key string) (StorageObject, IError) {
	out, err := s.client.HeadObject(s.ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(s.Key(key)),
	})
	if err != nil {
		if isNotFound(err) {
			return StorageObject{}, objectNotFound(key)
		}
		return StorageObject{}, s.fail("stat", key, err)
	}

	info := StorageObject{
		Key:         key,
		Size:        aws.ToInt64(out.ContentLength),
		ContentType: aws.ToString(out.ContentType),
		ETag:        strings.Trim(aws.ToString(out.ETag), `"`),
		Metadata:    out.Metadata,
	}
	if out.LastModified != nil {
		info.LastModified = *out.LastModified
	}
	return info, nil
}

func (s *storage) Exists(key string) (bool, IError) {
	_, err := s.Stat(key)
	if err != nil {
		if errors.Is(err, ErrObjectNotFound) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (s *storage) List(prefix string, opts ...StorageListOptions) ([]StorageObject, IError) {
	limit := 0
	if len(opts) > 0 {
		limit = opts[0].Limit
	}

	var out []StorageObject
	paginator := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(s.bucket),
		Prefix: aws.String(s.Key(prefix)),
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(s.ctx)
		if err != nil {
			return nil, s.fail("list", prefix, err)
		}
		for _, obj := range page.Contents {
			out = append(out, StorageObject{
				Key:          s.unkey(aws.ToString(obj.Key)),
				Size:         aws.ToInt64(obj.Size),
				ETag:         strings.Trim(aws.ToString(obj.ETag), `"`),
				LastModified: aws.ToTime(obj.LastModified),
			})
			if limit > 0 && len(out) >= limit {
				return out, nil
			}
		}
	}
	return out, nil
}

func (s *storage) Copy(srcKey, dstKey string) IError {
	_, err := s.client.CopyObject(s.ctx, &s3.CopyObjectInput{
		Bucket:     aws.String(s.bucket),
		Key:        aws.String(s.Key(dstKey)),
		CopySource: aws.String(copySource(s.bucket, s.Key(srcKey))),
	})
	if err != nil {
		if isNotFound(err) {
			return objectNotFound(srcKey)
		}
		return s.fail("copy", srcKey, err)
	}
	return nil
}

func (s *storage) Move(srcKey, dstKey string) IError {
	if err := s.Copy(srcKey, dstKey); err != nil {
		return err
	}
	// the copy is the part that can fail halfway; by here the object exists
	// under both names and only the old one has to go
	return s.Delete(srcKey)
}

func (s *storage) Delete(keys ...string) IError {
	if len(keys) == 0 {
		return nil
	}
	for _, chunk := range slicesChunk(keys, deleteBatchSize) {
		objects := make([]types.ObjectIdentifier, 0, len(chunk))
		for _, key := range chunk {
			objects = append(objects, types.ObjectIdentifier{Key: aws.String(s.Key(key))})
		}
		_, err := s.client.DeleteObjects(s.ctx, &s3.DeleteObjectsInput{
			Bucket: aws.String(s.bucket),
			Delete: &types.Delete{Objects: objects, Quiet: aws.Bool(true)},
		})
		if err == nil {
			continue
		}
		// not every S3-compatible gateway implements the batch call
		if !isNotImplemented(err) {
			return s.fail("delete", "", err)
		}
		if delErr := s.deleteOneByOne(chunk); delErr != nil {
			return delErr
		}
	}
	return nil
}

func (s *storage) deleteOneByOne(keys []string) IError {
	for _, key := range keys {
		_, err := s.client.DeleteObject(s.ctx, &s3.DeleteObjectInput{
			Bucket: aws.String(s.bucket),
			Key:    aws.String(s.Key(key)),
		})
		if err != nil && !isNotFound(err) {
			return s.fail("delete", key, err)
		}
	}
	return nil
}

func (s *storage) DeleteByPrefix(prefix string) (int64, IError) {
	var deleted int64
	paginator := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(s.bucket),
		Prefix: aws.String(s.Key(prefix)),
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(s.ctx)
		if err != nil {
			return deleted, s.fail("deletebyprefix", prefix, err)
		}
		if len(page.Contents) == 0 {
			continue
		}
		keys := make([]string, 0, len(page.Contents))
		for _, obj := range page.Contents {
			keys = append(keys, s.unkey(aws.ToString(obj.Key)))
		}
		if delErr := s.Delete(keys...); delErr != nil {
			return deleted, delErr
		}
		deleted += int64(len(keys))
	}
	return deleted, nil
}

// ---------------------------------------------------------------------------
// Links
// ---------------------------------------------------------------------------

func (s *storage) PresignGet(key string, ttl time.Duration, opts ...StoragePresignOptions) (string, IError) {
	input := &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(s.Key(key)),
	}
	if len(opts) > 0 {
		if opts[0].Attachment != "" {
			input.ResponseContentDisposition = aws.String(attachmentHeader(opts[0].Attachment))
		}
		if opts[0].ContentType != "" {
			input.ResponseContentType = aws.String(opts[0].ContentType)
		}
	}

	req, err := s.presign.PresignGetObject(s.ctx, input, s3.WithPresignExpires(ttl))
	if err != nil {
		return "", s.fail("presign", key, err)
	}
	return req.URL, nil
}

func (s *storage) PresignPut(key string, ttl time.Duration, opts ...StoragePutOptions) (string, IError) {
	opt := firstPutOption(opts)
	input := &s3.PutObjectInput{
		Bucket:      aws.String(s.bucket),
		Key:         aws.String(s.Key(key)),
		ContentType: aws.String(contentTypeFor(key, opt.ContentType)),
	}
	if opt.CacheControl != "" {
		input.CacheControl = aws.String(opt.CacheControl)
	}
	if opt.Public {
		input.ACL = types.ObjectCannedACLPublicRead
	}

	req, err := s.presign.PresignPutObject(s.ctx, input, s3.WithPresignExpires(ttl))
	if err != nil {
		return "", s.fail("presign", key, err)
	}
	return req.URL, nil
}

func (s *storage) PublicURL(key string) string {
	full := s.Key(key)
	if s.publicURL != "" {
		return s.publicURL + "/" + full
	}
	if s.client == nil {
		return ""
	}
	if base := aws.ToString(s.client.Options().BaseEndpoint); base != "" {
		return strings.TrimSuffix(base, "/") + "/" + s.bucket + "/" + full
	}
	return fmt.Sprintf("https://%s.s3.%s.amazonaws.com/%s", s.bucket, s.client.Options().Region, full)
}

// ---------------------------------------------------------------------------
// Shared helpers
// ---------------------------------------------------------------------------

func firstPutOption(opts []StoragePutOptions) StoragePutOptions {
	if len(opts) > 0 {
		return opts[0]
	}
	return StoragePutOptions{}
}

// contentTypeFor guesses from the extension when the caller did not say. An
// object stored as application/octet-stream is one browsers download instead of
// showing, which is rarely what an avatar was for.
func contentTypeFor(key, explicit string) string {
	if explicit != "" {
		return explicit
	}
	if guessed := mime.TypeByExtension(path.Ext(key)); guessed != "" {
		return guessed
	}
	return "application/octet-stream"
}

func contentDispositionFor(opt StoragePutOptions) string {
	if opt.ContentDisposition != "" {
		return opt.ContentDisposition
	}
	if opt.Attachment != "" {
		return attachmentHeader(opt.Attachment)
	}
	return ""
}

// attachmentHeader quotes the filename and adds the RFC 5987 form, so a name
// with a space or a Thai character survives the trip.
func attachmentHeader(filename string) string {
	escaped := url.PathEscape(filename)
	return fmt.Sprintf(`attachment; filename="%s"; filename*=UTF-8''%s`,
		strings.ReplaceAll(filename, `"`, ""), escaped)
}

// copySource is the "/bucket/key" a copy reads from, with each segment escaped:
// a key holding a space or a "+" is otherwise copied from the wrong place.
func copySource(bucket, key string) string {
	segments := strings.Split(key, "/")
	for i, segment := range segments {
		segments[i] = url.PathEscape(segment)
	}
	return "/" + bucket + "/" + strings.Join(segments, "/")
}

// slicesChunk yields consecutive chunks of at most size elements.
func slicesChunk[T any](items []T, size int) [][]T {
	if size <= 0 || len(items) == 0 {
		return nil
	}
	chunks := make([][]T, 0, (len(items)+size-1)/size)
	for start := 0; start < len(items); start += size {
		end := min(start+size, len(items))
		chunks = append(chunks, items[start:end])
	}
	return chunks
}

// objectNotFound is the ordinary "it is not there" outcome: a 404 a handler can
// return as it is, not a server error.
func objectNotFound(key string) *Error {
	return &Error{
		Status:  404,
		Code:    "OBJECT_NOT_FOUND",
		Message: "storage: object not found",
		cause:   fmt.Errorf("%w: %s", ErrObjectNotFound, key),
	}
}

func storageDisabled() *Error {
	return &Error{
		Status:  503,
		Code:    "STORAGE_DISABLED",
		Message: "storage: no bucket is configured (set S3_BUCKET)",
		cause:   ErrStorageDisabled,
	}
}

// isNotFound covers the several ways S3 says a key is absent: a typed error from
// GetObject, an untyped 404 from HeadObject (which has no body to type), and
// whatever an S3-compatible gateway decided to send.
func isNotFound(err error) bool {
	var noSuchKey *types.NoSuchKey
	var notFound *types.NotFound
	if errors.As(err, &noSuchKey) || errors.As(err, &notFound) {
		return true
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "NoSuchKey", "NotFound", "404":
			return true
		}
	}
	return false
}

func isNotImplemented(err error) bool {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "NotImplemented", "MethodNotAllowed":
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Disabled storage
// ---------------------------------------------------------------------------

// noopStorage is what ctx.Storage() returns when no bucket is configured. Every
// call fails with the same error, which names the missing configuration.
type noopStorage struct{ prefix string }

var _ IStorage = noopStorage{}

// NewNoopStorage returns storage that refuses every operation. It is what a
// service with no S3_* configuration gets, so the failure is a clear error
// instead of a nil dereference.
func NewNoopStorage() IStorage { return noopStorage{} }

func (n noopStorage) Put(string, io.Reader, ...StoragePutOptions) IError { return storageDisabled() }
func (n noopStorage) PutBytes(string, []byte, ...StoragePutOptions) IError {
	return storageDisabled()
}
func (n noopStorage) Get(string) (io.ReadCloser, IError)    { return nil, storageDisabled() }
func (n noopStorage) GetBytes(string) ([]byte, IError)      { return nil, storageDisabled() }
func (n noopStorage) Stat(string) (StorageObject, IError)   { return StorageObject{}, storageDisabled() }
func (n noopStorage) Exists(string) (bool, IError)          { return false, storageDisabled() }
func (n noopStorage) Copy(string, string) IError            { return storageDisabled() }
func (n noopStorage) Move(string, string) IError            { return storageDisabled() }
func (n noopStorage) Delete(...string) IError               { return storageDisabled() }
func (n noopStorage) DeleteByPrefix(string) (int64, IError) { return 0, storageDisabled() }

func (n noopStorage) List(string, ...StorageListOptions) ([]StorageObject, IError) {
	return nil, storageDisabled()
}

func (n noopStorage) PresignGet(string, time.Duration, ...StoragePresignOptions) (string, IError) {
	return "", storageDisabled()
}

func (n noopStorage) PresignPut(string, time.Duration, ...StoragePutOptions) (string, IError) {
	return "", storageDisabled()
}

func (n noopStorage) PublicURL(string) string { return "" }
func (n noopStorage) Ping() IError            { return storageDisabled() }
func (n noopStorage) Enabled() bool           { return false }
func (n noopStorage) Bucket() string          { return "" }
func (n noopStorage) Key(key string) string   { return n.prefix + key }

func (n noopStorage) WithPrefix(prefix string) IStorage {
	return noopStorage{prefix: n.prefix + normalizeKeyPrefix(prefix)}
}
func (n noopStorage) WithContext(context.Context) IStorage { return n }
func (n noopStorage) S3() *s3.Client                       { return nil }
