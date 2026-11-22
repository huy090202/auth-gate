package avatar

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io/fs"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func newTestS3(t *testing.T) *S3 {
	t.Helper()
	c := s3.New(s3.Options{Region: "eu-west-1", Credentials: credentials.NewStaticCredentialsProvider("AKIATEST", "secret", "")})
	return NewS3(c, s3.NewPresignClient(c), "bkt")
}

func TestAvatarPresignConditions(t *testing.T) {
	s := newTestS3(t)
	form, err := s.PresignPost(context.Background(), "avatars/u1/01ABC.png", "image/png")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(form.URL, "bkt") {
		t.Errorf("url %q", form.URL)
	}
	if form.Fields["key"] != "avatars/u1/01ABC.png" {
		t.Errorf("key field %q", form.Fields["key"])
	}
	raw, err := base64.StdEncoding.DecodeString(form.Fields["policy"])
	if err != nil {
		t.Fatalf("policy: %v (fields %v)", err, form.Fields)
	}
	var pol struct{ Conditions []json.RawMessage }
	if err := json.Unmarshal(raw, &pol); err != nil {
		t.Fatal(err)
	}
	var haveRange, haveType bool
	for _, c := range pol.Conditions {
		var arr []any
		if json.Unmarshal(c, &arr) == nil && len(arr) == 3 && arr[0] == "content-length-range" && arr[1] == float64(1) && arr[2] == float64(1048576) {
			haveRange = true
		}
		var m map[string]string
		if json.Unmarshal(c, &m) == nil && m["Content-Type"] == "image/png" {
			haveType = true
		}
		if json.Unmarshal(c, &arr) == nil && len(arr) == 3 && arr[0] == "eq" && arr[1] == "$Content-Type" && arr[2] == "image/png" {
			haveType = true
		}
	}
	if !haveRange || !haveType {
		t.Errorf("range=%v type=%v policy=%s", haveRange, haveType, raw)
	}
	if form.Fields["Content-Type"] != "image/png" {
		t.Errorf("the form must carry the content type: %v", form.Fields)
	}
}

func TestAvatarRefusesOtherContentTypes(t *testing.T) {
	s := newTestS3(t)
	d, _ := NewDir(t.TempDir(), "/dev/avatars-upload")
	for _, ct := range []string{"image/gif", "image/svg+xml", "text/html", "", "image/png; charset=x"} {
		if _, err := s.PresignPost(context.Background(), "avatars/u/x", ct); err == nil {
			t.Errorf("s3 accepted %q", ct)
		}
		if _, err := d.PresignPost(context.Background(), "avatars/u/x", ct); err == nil {
			t.Errorf("dir accepted %q", ct)
		}
	}
	for _, ct := range []string{"image/png", "image/jpeg", "image/webp"} {
		if _, err := s.PresignPost(context.Background(), "avatars/u/x", ct); err != nil {
			t.Errorf("s3 refused %q: %v", ct, err)
		}
	}
}

type fakeS3 struct {
	size  int64
	ct    string
	err   error
	del   string
	headK string
}

func (f *fakeS3) HeadObject(_ context.Context, in *s3.HeadObjectInput, _ ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	f.headK = *in.Key
	if f.err != nil {
		return nil, f.err
	}
	return &s3.HeadObjectOutput{ContentLength: aws.Int64(f.size), ContentType: aws.String(f.ct)}, nil
}

func (f *fakeS3) DeleteObject(_ context.Context, in *s3.DeleteObjectInput, _ ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	f.del = *in.Key
	return &s3.DeleteObjectOutput{}, nil
}

func TestS3HeadAndDelete(t *testing.T) {
	f := &fakeS3{size: 123, ct: "image/webp"}
	s := &S3{API: f, Bucket: "bkt"}
	size, ct, err := s.Head(context.Background(), "avatars/u/k.webp")
	if err != nil || size != 123 || ct != "image/webp" || f.headK != "avatars/u/k.webp" {
		t.Fatalf("%d %q %v", size, ct, err)
	}
	if err := s.Delete(context.Background(), "avatars/u/k.webp"); err != nil || f.del != "avatars/u/k.webp" {
		t.Fatalf("delete %v %q", err, f.del)
	}
	f.err = errors.New("boom")
	if _, _, err := s.Head(context.Background(), "k"); err == nil {
		t.Error("head error must surface")
	}
}

func TestDirStore(t *testing.T) {
	ctx := context.Background()
	d, err := NewDir(t.TempDir(), "/dev/avatars-upload")
	if err != nil {
		t.Fatal(err)
	}
	form, err := d.PresignPost(ctx, "avatars/u1/01A.png", "image/png")
	if err != nil {
		t.Fatal(err)
	}
	if form.URL != "/dev/avatars-upload" || form.Fields["key"] != "avatars/u1/01A.png" || form.Fields["Content-Type"] != "image/png" {
		t.Errorf("form %+v", form)
	}
	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 64)...)
	if err := d.Save(ctx, "avatars/u1/01A.png", bytes.NewReader(png)); err != nil {
		t.Fatal(err)
	}
	size, ct, err := d.Head(ctx, "avatars/u1/01A.png")
	if err != nil || size != int64(len(png)) || ct != "image/png" {
		t.Fatalf("head %d %q %v", size, ct, err)
	}
	if err := d.Save(ctx, "avatars/u1/big.png", bytes.NewReader(append(png, make([]byte, MaxBytes)...))); err == nil {
		t.Error("an upload over 1 MiB must be refused")
	}
	if _, _, err := d.Head(ctx, "avatars/u1/big.png"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("an oversized upload must leave nothing: %v", err)
	}
	if err := d.Delete(ctx, "avatars/u1/01A.png"); err != nil {
		t.Fatal(err)
	}
	if err := d.Delete(ctx, "avatars/u1/01A.png"); err != nil {
		t.Errorf("delete of a missing key is not an error: %v", err)
	}
	if _, _, err := d.Head(ctx, "avatars/u1/01A.png"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("head after delete: %v", err)
	}
	for _, k := range []string{"../x", "/etc/passwd", "a/../../b", "", "a\\b"} {
		if _, err := d.PresignPost(ctx, k, "image/png"); err == nil {
			t.Errorf("key %q accepted", k)
		}
		if _, _, err := d.Head(ctx, k); err == nil {
			t.Errorf("head key %q accepted", k)
		}
	}
}
