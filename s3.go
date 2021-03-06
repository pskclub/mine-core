package core

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/disintegration/imaging"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"image/jpeg"
	"io"
	"net/http"
	"path"
	"path/filepath"
	"strconv"
)

type S3Config struct {
	Endpoint  string
	AccessKey string
	SecretKey string
	Bucket    string
	IsHTTPS   bool
}

func (r S3Config) Connect() (IS3, error) {
	minioClient, err := minio.New(r.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(r.AccessKey, r.SecretKey, ""),
		Secure: r.IsHTTPS,
	})
	if err != nil {
		return nil, err
	}

	return &s3{client: minioClient}, nil
}

type IS3 interface {
	PutObject(bucketName, objectName string, reader io.Reader, opts minio.PutObjectOptions, uploadOptions *UploadOptions) (*minio.UploadInfo, error)
	PutObjectByURL(bucketName, objectName string, url string, opts minio.PutObjectOptions, uploadOptions *UploadOptions) (*minio.UploadInfo, error)
}

type s3 struct {
	client *minio.Client
}

func NewS3(env *ENVConfig) *S3Config {
	return &S3Config{
		Endpoint:  env.S3Endpoint,
		AccessKey: env.S3AccessKey,
		SecretKey: env.S3SecretKey,
		Bucket:    env.S3Bucket,
		IsHTTPS:   env.S3IsHTTPS,
	}
}

type UploadOptions struct {
	Width   int64
	Height  int64
	Quality int64
}

func (r s3) PutObject(bucketName, objectName string, file io.Reader, opts minio.PutObjectOptions, uploadOptions *UploadOptions) (*minio.UploadInfo, error) {
	err := r.client.MakeBucket(context.Background(), bucketName, minio.MakeBucketOptions{})
	if err != nil {
		isExists, err := r.client.BucketExists(context.Background(), bucketName)
		if err == nil && !isExists {
			return nil, err
		}
	} else {
		policy := fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":["*"]},"Action":["s3:GetBucketLocation","s3:ListBucket","s3:ListBucketMultipartUploads"],"Resource":["arn:aws:s3:::%s"]},{"Effect":"Allow","Principal":{"AWS":["*"]},"Action":["s3:GetObject","s3:ListMultipartUploadParts","s3:PutObject","s3:AbortMultipartUpload","s3:DeleteObject"],"Resource":["arn:aws:s3:::%s/*"]}]}`, bucketName, bucketName)
		err = r.client.SetBucketPolicy(context.Background(), bucketName, policy)
		if err != nil {
			return nil, err
		}
	}

	var reader *bytes.Reader = nil
	if uploadOptions != nil && (uploadOptions.Height != 0 || uploadOptions.Width != 0 || uploadOptions.Quality != 0) {
		img, err := imaging.Decode(file)
		if err != nil {
			return nil, err
		}

		imgSrc := imaging.Fit(img, int(uploadOptions.Width), int(uploadOptions.Height), imaging.Lanczos)
		buf := new(bytes.Buffer)
		err = jpeg.Encode(buf, imgSrc, &jpeg.Options{Quality: int(uploadOptions.Quality)})
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(buf.Bytes())
	}

	var info minio.UploadInfo
	if reader != nil {
		info, err = r.client.PutObject(context.Background(), bucketName, objectName, reader, -1, opts)
		if err != nil {
			return nil, err
		}
	} else {
		info, err = r.client.PutObject(context.Background(), bucketName, objectName, file, -1, opts)
		if err != nil {
			return nil, err
		}
	}

	return &info, nil
}

func (r s3) PutObjectByURL(bucketName, objectName string, url string, opts minio.PutObjectOptions, uploadOptions *UploadOptions) (*minio.UploadInfo, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 300 {
		return nil, errors.New("Something went wrong , status code: " + strconv.Itoa(resp.StatusCode))
	}

	opts.ContentType = resp.Header.Get("Content-type")
	if opts.ContentType == "" {
		opts.ContentType = "application/octet-stream"
	}

	extension := filepath.Ext(path.Base(resp.Request.URL.Path))

	return r.PutObject(bucketName, objectName+extension, resp.Body, opts, uploadOptions)
}
