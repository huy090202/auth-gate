// Package avatar stores profile pictures: in S3 through presigned POSTs, or in a
// local directory for development.
package avatar

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// MaxBytes is the largest avatar, 1 MiB.
const MaxBytes = 1 << 20

const presignTTL = 5 * time.Minute

// PostForm is what the browser needs to upload: POST a multipart form with
// Fields first and the file last, to URL.
type PostForm struct {
	URL    string
	Fields map[string]string
}

// Store presigns uploads and inspects or deletes stored avatars.
type Store interface {
	PresignPost(ctx context.Context, key, contentType string) (PostForm, error)
	Head(ctx context.Context, key string) (size int64, contentType string, err error)
	Delete(ctx context.Context, key string) error
}

var allowedTypes = map[string]bool{"image/png": true, "image/jpeg": true, "image/webp": true}

func checkType(ct string) error {
	if !allowedTypes[ct] {
		return fmt.Errorf("avatar: content type %q is not allowed", ct)
	}
	return nil
}

// S3API is the part of the S3 client the store uses.
type S3API interface {
	HeadObject(ctx context.Context, in *s3.HeadObjectInput, optFns ...func(*s3.Options)) (*s3.HeadObjectOutput, error)
	DeleteObject(ctx context.Context, in *s3.DeleteObjectInput, optFns ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
}

// Presigner is the part of the S3 presign client the store uses.
type Presigner interface {
	PresignPostObject(ctx context.Context, in *s3.PutObjectInput, optFns ...func(*s3.PresignPostOptions)) (*s3.PresignedPostRequest, error)
}

// S3 is the production store.
type S3 struct {
	API    S3API
	Signer Presigner
	Bucket string
}

func NewS3(api S3API, signer Presigner, bucket string) *S3 {
	return &S3{API: api, Signer: signer, Bucket: bucket}
}

// PresignPost returns a POST form limited to 1..MaxBytes bytes and the exact content type.
func (s *S3) PresignPost(ctx context.Context, key, contentType string) (PostForm, error) {
	if err := checkType(contentType); err != nil {
		return PostForm{}, err
	}
	p, err := s.Signer.PresignPostObject(ctx,
		&s3.PutObjectInput{Bucket: aws.String(s.Bucket), Key: aws.String(key), ContentType: aws.String(contentType)},
		func(o *s3.PresignPostOptions) {
			o.Expires = presignTTL
			o.Conditions = []any{
				[]any{"content-length-range", 1, MaxBytes},
				map[string]string{"Content-Type": contentType},
			}
		})
	if err != nil {
		return PostForm{}, fmt.Errorf("avatar: presign: %w", err)
	}
	fields := make(map[string]string, len(p.Values)+1)
	for k, v := range p.Values {
		fields[k] = v
	}
	fields["Content-Type"] = contentType
	return PostForm{URL: p.URL, Fields: fields}, nil
}

func (s *S3) Head(ctx context.Context, key string) (int64, string, error) {
	out, err := s.API.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(s.Bucket), Key: aws.String(key)})
	if err != nil {
		return 0, "", fmt.Errorf("avatar: head: %w", err)
	}
	return aws.ToInt64(out.ContentLength), aws.ToString(out.ContentType), nil
}

func (s *S3) Delete(ctx context.Context, key string) error {
	if _, err := s.API.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(s.Bucket), Key: aws.String(key)}); err != nil {
		return fmt.Errorf("avatar: delete: %w", err)
	}
	return nil
}

// Dir is the local store. Its content type comes from sniffing the bytes, since
// a file system keeps none.
type Dir struct {
	root      string
	uploadURL string
}

// NewDir creates the directory. uploadURL is the Portal dev route that receives
// uploads (Dir.Save does the writing there).
func NewDir(root, uploadURL string) (*Dir, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("avatar: dir: %w", err)
	}
	return &Dir{root: root, uploadURL: uploadURL}, nil
}

func (d *Dir) path(key string) (string, error) {
	if !fs.ValidPath(key) || key == "." || strings.Contains(key, `\`) {
		return "", fmt.Errorf("avatar: invalid key %q", key)
	}
	return filepath.Join(d.root, filepath.FromSlash(key)), nil
}

// PresignPost returns the dev route and the same field names the S3 form uses.
func (d *Dir) PresignPost(_ context.Context, key, contentType string) (PostForm, error) {
	if err := checkType(contentType); err != nil {
		return PostForm{}, err
	}
	if _, err := d.path(key); err != nil {
		return PostForm{}, err
	}
	return PostForm{URL: d.uploadURL, Fields: map[string]string{"key": key, "Content-Type": contentType}}, nil
}

// Save writes an upload, refusing more than MaxBytes or a type other than the allowed ones.
func (d *Dir) Save(_ context.Context, key string, r io.Reader) error {
	p, err := d.path(key)
	if err != nil {
		return err
	}
	data, err := io.ReadAll(io.LimitReader(r, MaxBytes+1))
	if err != nil {
		return fmt.Errorf("avatar: read upload: %w", err)
	}
	if len(data) == 0 || len(data) > MaxBytes {
		return fmt.Errorf("avatar: upload must be 1..%d bytes", MaxBytes)
	}
	if err := checkType(http.DetectContentType(data)); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	return os.WriteFile(p, data, 0o600)
}

func (d *Dir) Head(_ context.Context, key string) (int64, string, error) {
	p, err := d.path(key)
	if err != nil {
		return 0, "", err
	}
	f, err := os.Open(p) //nolint:gosec // p is built by path() from a validated relative key
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return 0, "", err
	}
	buf := make([]byte, 512)
	n, _ := io.ReadFull(f, buf)
	return info.Size(), http.DetectContentType(buf[:n]), nil
}

// Delete removes key; a missing key is not an error.
func (d *Dir) Delete(_ context.Context, key string) error {
	p, err := d.path(key)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
