package store

import (
	"context"
	"errors"
	"io"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

// maxSinglePut is the largest object a single PutObject call may send. Objects
// above it go through the multipart uploader.
const maxSinglePut = 5 << 30

// defaultRegion is used when no region is configured, which is what
// S3-compatible services expect.
const defaultRegion = "us-east-1"

// S3 stores archives in a bucket, addressed as path style when a custom
// endpoint is configured.
type S3 struct {
	bucket   string
	client   *s3.Client
	uploader *manager.Uploader
}

func NewS3(cfg S3Config) (*S3, error) {
	if cfg.AccessKey == "" || cfg.SecretKey == "" {
		return nil, errors.New("s3 access key and secret key are required")
	}

	region := cfg.Region
	if region == "" {
		// S3-compatible services accept any region but signing still needs one.
		region = defaultRegion
	}

	awsCfg := aws.Config{
		Region:      region,
		Credentials: credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, cfg.SessionToken),
	}

	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if cfg.Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
		}
		o.UsePathStyle = cfg.ForcePathStyle
	})

	return &S3{
		bucket: cfg.Bucket,
		client: client,
		uploader: manager.NewUploader(client, func(u *manager.Uploader) {
			u.PartSize = 64 << 20
			u.Concurrency = 4
		}),
	}, nil
}

func (s *S3) List(ctx context.Context, prefix string) ([]Object, error) {
	var objects []Object
	paginator := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(s.bucket),
		Prefix: aws.String(prefix),
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, obj := range page.Contents {
			objects = append(objects, Object{
				Key:          aws.ToString(obj.Key),
				Size:         aws.ToInt64(obj.Size),
				LastModified: aws.ToTime(obj.LastModified),
			})
		}
	}
	return objects, nil
}

func (s *S3) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		var noSuchKey *s3types.NoSuchKey
		if errors.As(err, &noSuchKey) || errorCode(err) == "NoSuchKey" {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return out.Body, nil
}

func (s *S3) Put(ctx context.Context, key string, body io.Reader, size int64, ifAbsent bool) error {
	if ifAbsent {
		// Not every S3-compatible backend enforces If-None-Match, so probe first
		// and keep the conditional header as the atomic backstop.
		if _, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
			Bucket: aws.String(s.bucket),
			Key:    aws.String(key),
		}); err == nil {
			return ErrAlreadyExists
		} else if !isNotFound(err) {
			return err
		}
	}

	if size > maxSinglePut {
		_, err := s.uploader.Upload(ctx, &s3.PutObjectInput{
			Bucket: aws.String(s.bucket),
			Key:    aws.String(key),
			Body:   body,
		})
		return err
	}

	in := &s3.PutObjectInput{
		Bucket:        aws.String(s.bucket),
		Key:           aws.String(key),
		Body:          body,
		ContentLength: aws.Int64(size),
	}
	if ifAbsent {
		in.IfNoneMatch = aws.String("*")
	}
	_, err := s.client.PutObject(ctx, in)
	if errorCode(err) == "PreconditionFailed" {
		return ErrAlreadyExists
	}
	return err
}

func errorCode(err error) string {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		return apiErr.ErrorCode()
	}
	return ""
}

func isNotFound(err error) bool {
	code := errorCode(err)
	return code == "NoSuchKey" || code == "NotFound"
}
