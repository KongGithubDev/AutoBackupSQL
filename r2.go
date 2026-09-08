package main

import (
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// r2Store is a thin wrapper around the S3-compatible Cloudflare R2 API.
type r2Store struct {
	client *minio.Client
	bucket string
	prefix string
}

func newR2Store(cfg R2Config, objectPrefix string) (*r2Store, error) {
	endpoint := strings.TrimSpace(cfg.Endpoint)
	secure := true
	if strings.HasPrefix(endpoint, "http://") {
		secure = false
	}
	host := strings.TrimPrefix(strings.TrimPrefix(endpoint, "https://"), "http://")
	if host == "" || cfg.AccessKeyID == "" || cfg.SecretAccessKey == "" || cfg.Bucket == "" {
		return nil, fmt.Errorf("cloudflareR2 is incomplete: endpoint, accessKeyId, secretAccessKey and bucket must all be set")
	}
	client, err := minio.New(host, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKeyID, cfg.SecretAccessKey, ""),
		Secure: secure,
		Region: cfg.Region,
	})
	if err != nil {
		return nil, fmt.Errorf("invalid R2 client: %w", err)
	}
	return &r2Store{client: client, bucket: cfg.Bucket, prefix: objectPrefix}, nil
}

// ping verifies connectivity by checking the bucket exists.
func (s *r2Store) ping(ctx context.Context) error {
	ok, err := s.client.BucketExists(ctx, s.bucket)
	if err != nil {
		return fmt.Errorf("R2 connection failed (bucket %q): %w", s.bucket, err)
	}
	if !ok {
		return fmt.Errorf("R2 bucket %q does not exist (or no permission to see it)", s.bucket)
	}
	return nil
}

// objectKey builds the destination key: <prefix>/<db>/<stamp>.sql[.gz]
func (s *r2Store) objectKey(dbName string, t time.Time, gzipped bool) string {
	stamp := t.Format("2006-01-02_15-04-05")
	ext := ".sql"
	if gzipped {
		ext = ".sql.gz"
	}
	safeDB := sanitizeName(dbName)
	return fmt.Sprintf("%s/%s/%s%s", s.prefix, safeDB, stamp, ext)
}

// tableObjectKey builds the per-table destination key:
// <prefix>/<db>/<stamp>/<table>.sql[.gz]
func (s *r2Store) tableObjectKey(dbName, tableName string, t time.Time, gzipped bool) string {
	stamp := t.Format("2006-01-02_15-04-05")
	ext := ".sql"
	if gzipped {
		ext = ".sql.gz"
	}
	return fmt.Sprintf("%s/%s/%s/%s%s", s.prefix, sanitizeName(dbName), stamp, sanitizeName(tableName), ext)
}

// sanitizeName keeps object keys safe regardless of the database name.
func sanitizeName(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := b.String()
	if out == "" {
		return "db"
	}
	return out
}

// upload sends a finished backup file to R2.
func (s *r2Store) upload(ctx context.Context, objectName, filePath string) error {
	opts := minio.PutObjectOptions{ContentType: "application/gzip"}
	if !strings.HasSuffix(objectName, ".gz") {
		opts.ContentType = "application/octet-stream"
	}
	info, err := s.client.FPutObject(ctx, s.bucket, objectName, filePath, opts)
	if err != nil {
		return fmt.Errorf("R2 upload failed for %q: %w", objectName, err)
	}
	if info.Size < 0 {
		return fmt.Errorf("R2 upload finished without size confirmation for %q", objectName)
	}
	return nil
}

// deleteOlderThan removes old .sql / .sql.gz objects under the prefix.
// Returns the number of deleted objects.
func (s *r2Store) deleteOlderThan(ctx context.Context, olderThan time.Time, log *Logger) (int, error) {
	removed := 0
	opts := minio.ListObjectsOptions{Prefix: s.prefix, Recursive: true}
	for obj := range s.client.ListObjects(ctx, s.bucket, opts) {
		if obj.Err != nil {
			return removed, fmt.Errorf("R2 listing failed: %w", obj.Err)
		}
		if !strings.HasSuffix(obj.Key, ".sql") && !strings.HasSuffix(obj.Key, ".sql.gz") {
			continue
		}
		if obj.LastModified.IsZero() || obj.LastModified.After(olderThan) {
			continue
		}
		if err := s.client.RemoveObject(ctx, s.bucket, obj.Key, minio.RemoveObjectOptions{}); err != nil {
			log.Error("retention: cannot delete %s: %v", obj.Key, err)
			continue
		}
		removed++
		log.Info("retention: deleted old object %s (from %s)", obj.Key, obj.LastModified.Format("2006-01-02 15:04:05"))
	}
	return removed, nil
}

// dirSize returns the total size of files under dir (used for reporting).
func dirSize(dir string) (int64, error) {
	var total int64
	err := filepath.Walk(dir, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	return total, err
}

// gzipFile compresses src into dst with gzip.
func gzipFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	gz, err := gzip.NewWriterLevel(out, gzip.BestCompression)
	if err != nil {
		return err
	}
	if _, err := io.Copy(gz, in); err != nil {
		gz.Close()
		return err
	}
	return gz.Close()
}
