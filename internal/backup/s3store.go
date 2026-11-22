package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/kushal1o1/bunker/internal/model"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// Credentials come from the environment only, never from bunker.yaml.
const (
	EnvAccessKey    = "AWS_ACCESS_KEY_ID"
	EnvSecretKey    = "AWS_SECRET_ACCESS_KEY"
	EnvSessionToken = "AWS_SESSION_TOKEN"
)

// s3PartSize is the multipart chunk for streamed uploads. One part per
// upload is held in memory, and S3 allows 10,000 parts, so this caps one
// artifact at 320 GiB while keeping memory at 32 MiB per upload in flight.
const s3PartSize = 32 << 20

// S3Store keeps snapshots in an S3-compatible bucket under
// <prefix><server>/<snapshot>/.
type S3Store struct {
	client *minio.Client
	bucket string
	prefix string
}

// NewS3Store builds a client from the config and the environment. It does not
// contact the bucket; Check does.
func NewS3Store(spec model.S3Spec) (*S3Store, error) {
	return newS3Store(spec, nil)
}

// newS3Store takes an optional transport, which tests use to trust a fake
// server's certificate.
func newS3Store(spec model.S3Spec, transport http.RoundTripper) (*S3Store, error) {
	access, secret := os.Getenv(EnvAccessKey), os.Getenv(EnvSecretKey)
	if access == "" || secret == "" {
		return nil, fmt.Errorf("S3 credentials not found: set %s and %s", EnvAccessKey, EnvSecretKey)
	}
	u, err := url.Parse(spec.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("storage.s3.endpoint: %w", err)
	}
	client, err := minio.New(u.Host, &minio.Options{
		Creds:        credentials.NewStaticV4(access, secret, os.Getenv(EnvSessionToken)),
		Secure:       u.Scheme == "https",
		Region:       spec.Region,
		BucketLookup: minio.BucketLookupAuto,
		Transport:    transport,
	})
	if err != nil {
		return nil, fmt.Errorf("s3 client: %w", err)
	}
	return &S3Store{client: client, bucket: spec.Bucket, prefix: spec.Prefix}, nil
}

func (s *S3Store) Name() string  { return "s3" }
func (s *S3Store) Where() string { return "s3://" + s.bucket + "/" + s.prefix }

// Check confirms the bucket exists and the credentials can reach it, so a
// run fails once, up front, instead of once per server.
func (s *S3Store) Check(ctx context.Context) error {
	ok, err := s.client.BucketExists(ctx, s.bucket)
	if err != nil {
		return fmt.Errorf("s3 bucket %q: %w", s.bucket, err)
	}
	if !ok {
		return fmt.Errorf("s3 bucket %q does not exist (bunker never creates buckets)", s.bucket)
	}
	return nil
}

func (s *S3Store) snapshotPrefix(server, snapshot string) string {
	return s.prefix + server + "/" + snapshot + "/"
}

func (s *S3Store) key(server, snapshot, file string) string {
	return s.snapshotPrefix(server, snapshot) + file
}

// Create streams the artifact as a multipart upload of unknown size: the
// bytes go straight from the pipeline to the bucket with no temp file.
func (s *S3Store) Create(ctx context.Context, server, snapshot, file string) (ArtifactWriter, error) {
	if err := checkNames(server, snapshot); err != nil {
		return nil, err
	}
	key := s.key(server, snapshot, file)
	pr, pw := io.Pipe()
	w := &s3Writer{store: s, key: key, pw: pw, done: make(chan error, 1)}
	go func() {
		_, err := s.client.PutObject(ctx, s.bucket, key, pr, -1, minio.PutObjectOptions{
			ContentType: "application/octet-stream",
			PartSize:    s3PartSize,
		})
		// Unblock a writer stuck in Write if the upload failed early.
		pr.CloseWithError(err)
		w.done <- err
	}()
	return w, nil
}

type s3Writer struct {
	store *S3Store
	key   string
	pw    *io.PipeWriter
	done  chan error
	err   error
	ended bool
}

func (w *s3Writer) Write(p []byte) (int, error) { return w.pw.Write(p) }

func (w *s3Writer) wait() error {
	if !w.ended {
		w.ended = true
		w.err = <-w.done
	}
	return w.err
}

func (w *s3Writer) Commit() error {
	w.pw.Close()
	if err := w.wait(); err != nil {
		return fmt.Errorf("s3 upload %s: %w", w.key, err)
	}
	return nil
}

var errAborted = errors.New("upload aborted")

// Abort fails the upload so minio-go abandons the multipart upload, then
// removes any parts it could not clean up itself (it uses the run's ctx,
// which is already cancelled after Ctrl-C).
func (w *s3Writer) Abort() {
	w.pw.CloseWithError(errAborted)
	w.wait()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	w.store.client.RemoveIncompleteUpload(ctx, w.store.bucket, w.key)
}

// Files an agent writes next to its snapshots. They are not snapshot
// folders, so List ignores them.
const (
	// StatusName is rewritten after every agent run.
	StatusName = "status.json"
	// CheckName is written by `bunker agent check` to prove uploads work.
	CheckName = "check.json"
)

// PutServerFile writes <prefix><server>/<name>, where name is StatusName or
// CheckName.
func (s *S3Store) PutServerFile(ctx context.Context, server, name string, data []byte) error {
	if err := checkServer(server); err != nil {
		return err
	}
	if name != StatusName && name != CheckName {
		return fmt.Errorf("refusing to write %q next to snapshots", name)
	}
	key := s.prefix + server + "/" + name
	_, err := s.client.PutObject(ctx, s.bucket, key, strings.NewReader(string(data)), int64(len(data)),
		minio.PutObjectOptions{ContentType: "application/json"})
	if err != nil {
		return fmt.Errorf("s3 upload %s: %w", key, err)
	}
	return nil
}

func (s *S3Store) PutManifest(ctx context.Context, m *Manifest) error {
	if err := checkNames(m.Server, m.Snapshot); err != nil {
		return err
	}
	data, err := marshalManifest(m)
	if err != nil {
		return err
	}
	key := s.key(m.Server, m.Snapshot, ManifestName)
	_, err = s.client.PutObject(ctx, s.bucket, key, strings.NewReader(string(data)), int64(len(data)),
		minio.PutObjectOptions{ContentType: "application/json"})
	if err != nil {
		return fmt.Errorf("s3 upload %s: %w", key, err)
	}
	return nil
}

func (s *S3Store) Open(ctx context.Context, server, snapshot, file string) (io.ReadCloser, error) {
	key := s.key(server, snapshot, file)
	obj, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, s3Err(key, err)
	}
	// GetObject is lazy; Stat surfaces a missing key now rather than on the
	// first Read, so callers can tell "missing" from "corrupt".
	if _, err := obj.Stat(); err != nil {
		obj.Close()
		return nil, s3Err(key, err)
	}
	return obj, nil
}

// s3Err maps a missing key to fs.ErrNotExist, like a missing local file.
func s3Err(key string, err error) error {
	if code := minio.ToErrorResponse(err).Code; code == minio.NoSuchKey || code == "NotFound" {
		return fmt.Errorf("s3 %s: %w", key, fs.ErrNotExist)
	}
	return fmt.Errorf("s3 %s: %w", key, err)
}

// List finds snapshot "folders" under the server's prefix and reads each
// manifest. Keys that are not snapshot IDs are ignored, as locally.
func (s *S3Store) List(ctx context.Context, server string) ([]SnapshotInfo, error) {
	base := s.prefix + server + "/"
	var out []SnapshotInfo
	for obj := range s.client.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{Prefix: base}) {
		if obj.Err != nil {
			return nil, fmt.Errorf("s3 list %s: %w", base, obj.Err)
		}
		if !strings.HasSuffix(obj.Key, "/") {
			continue
		}
		id := strings.TrimSuffix(strings.TrimPrefix(obj.Key, base), "/")
		created, err := time.Parse(snapshotLayout, id)
		if err != nil {
			continue
		}

		info := SnapshotInfo{ID: id, Created: created}
		man, err := s.readManifest(ctx, server, id)
		switch {
		case err == nil:
			info.Status, info.Manifest = Complete, man
		case errors.Is(err, fs.ErrNotExist):
			info.Status = NoManifest
		default:
			info.Status, info.Err = BadManifest, err
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out, nil
}

func (s *S3Store) Manifest(ctx context.Context, server, snapshot string) (*Manifest, error) {
	m, err := s.readManifest(ctx, server, snapshot)
	if err != nil && errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("read manifest %s: %w", s.Where()+server+"/"+snapshot+"/"+ManifestName, err)
	}
	return m, err
}

func (s *S3Store) readManifest(ctx context.Context, server, snapshot string) (*Manifest, error) {
	r, err := s.Open(ctx, server, snapshot, ManifestName)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, s3Err(s.key(server, snapshot, ManifestName), err)
	}
	return ParseManifest(data, "s3://"+s.bucket+"/"+s.key(server, snapshot, ManifestName))
}

// Delete removes every object of one snapshot.
func (s *S3Store) Delete(ctx context.Context, server, snapshot string) error {
	if err := checkNames(server, snapshot); err != nil {
		return err
	}
	prefix := s.snapshotPrefix(server, snapshot)
	objects := s.client.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{Prefix: prefix, Recursive: true})
	var first error
	// Drain the whole channel: returning early would leave minio-go's
	// sender goroutine blocked.
	for rerr := range s.client.RemoveObjects(ctx, s.bucket, objects, minio.RemoveObjectsOptions{}) {
		if first == nil {
			first = fmt.Errorf("delete s3://%s/%s: %w", s.bucket, rerr.ObjectName, rerr.Err)
		}
	}
	return first
}
