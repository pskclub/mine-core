package core

import (
	"bytes"
	"errors"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	ss3 "github.com/aws/aws-sdk-go/service/s3"
	"github.com/disintegration/imaging"
	"github.com/pskclub/mine-core/utils"
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
	Region    string
	Bucket    string
	IsHTTPS   bool
}

func (r *S3Config) Connect() (IS3, error) {
	sess, err := session.NewSession(&aws.Config{
		Region:      aws.String(r.Region),
		Credentials: credentials.NewStaticCredentials(r.AccessKey, r.SecretKey, ""),
	})
	if err != nil {
		return nil, err
	}

	svc := ss3.New(sess)
	return &s3{client: svc, config: r}, nil
}

type IS3 interface {
	GetObject(path string, opts *ss3.GetObjectInput) (*ss3.GetObjectOutput, error)
	PutObject(objectName string, file io.ReadSeeker, opts *ss3.PutObjectInput, uploadOptions *UploadOptions) (*ss3.PutObjectOutput, error)
	PutObjectByURL(objectName string, url string, opts *ss3.PutObjectInput, uploadOptions *UploadOptions) (*ss3.PutObjectOutput, error)
}

type s3 struct {
	client *ss3.S3
	config *S3Config
}

func NewS3(env *ENVConfig) *S3Config {
	return &S3Config{
		Endpoint:  env.S3Endpoint,
		AccessKey: env.S3AccessKey,
		SecretKey: env.S3SecretKey,
		Bucket:    env.S3Bucket,
		Region:    env.S3Region,
		IsHTTPS:   env.S3IsHTTPS,
	}
}

type UploadOptions struct {
	Width   int64
	Height  int64
	Quality int64
}

func (r s3) PutObject(objectName string, file io.ReadSeeker, opts *ss3.PutObjectInput, uploadOptions *UploadOptions) (*ss3.PutObjectOutput, error) {
	var reader = file
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

	if opts == nil {
		opts = &ss3.PutObjectInput{}
	}

	opts.Bucket = aws.String(r.config.Bucket)
	opts.Key = aws.String(objectName)
	opts.Body = reader

	req, err := r.client.PutObject(opts)
	if err != nil {
		return nil, err
	}

	return req, nil
}

func (r s3) GetObject(path string, opts *ss3.GetObjectInput) (*ss3.GetObjectOutput, error) {
	if opts == nil {
		opts = &ss3.GetObjectInput{}
	}

	opts.Bucket = aws.String(r.config.Bucket)
	opts.Key = aws.String(path)
	result, err := r.client.GetObject(opts)
	if err != nil {
		return nil, err
	}

	return result, nil
}

func (r s3) PutObjectByURL(objectName string, url string, opts *ss3.PutObjectInput, uploadOptions *UploadOptions) (*ss3.PutObjectOutput, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 300 {
		return nil, errors.New("Something went wrong , status code: " + strconv.Itoa(resp.StatusCode))
	}

	opts.ContentType = aws.String(resp.Header.Get("Content-type"))
	if utils.GetString(opts.ContentType) == "" {
		opts.ContentType = aws.String("application/octet-stream")
	}

	extension := filepath.Ext(path.Base(resp.Request.URL.Path))

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	return r.PutObject(objectName+extension, bytes.NewReader(body), opts, uploadOptions)
}
