package main

import (
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

const remoteScheme = "s3://"

type BucketInfo struct {
	Name    string    `json:"name"`
	Created time.Time `json:"created"`
}

type ObjectInfo struct {
	Key          string    `json:"key"`
	Size         int64     `json:"size"`
	ContentType  string    `json:"content_type"`
	ETag         string    `json:"etag"`
	SHA256       string    `json:"sha256"`
	LastModified time.Time `json:"last_modified"`
}

type remotePath struct {
	Bucket string
	Key    string // may be empty (bucket root) or a prefix
}

func (r remotePath) String() string {
	if r.Key == "" {
		return r.Bucket
	}
	return r.Bucket + "/" + r.Key
}

func parseRemote(s string) (remotePath, error) {
	s = strings.TrimPrefix(s, remoteScheme)
	parts := strings.SplitN(s, "/", 2)
	if parts[0] == "" {
		return remotePath{}, usagef("path must be in format: bucket[/key]")
	}
	r := remotePath{Bucket: parts[0]}
	if len(parts) == 2 {
		r.Key = parts[1]
	}
	return r, nil
}

func escapeKey(key string) string {
	segs := strings.Split(key, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}

func (c *CLI) bucketURL(bucket string) string {
	return c.config.ServerUrl + "/buckets/" + url.PathEscape(bucket)
}

func (c *CLI) listURL(bucket string) string {
	return c.config.ServerUrl + "/objects/" + url.PathEscape(bucket)
}

func (c *CLI) objectURL(bucket, key string) string {
	return c.listURL(bucket) + "/" + escapeKey(key)
}

func httpError(resp *http.Response, action string) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	msg := strings.TrimSpace(string(b))
	if msg == "" {
		msg = http.StatusText(resp.StatusCode)
	}
	return fmt.Errorf("%s: %s (HTTP %d)", action, msg, resp.StatusCode)
}

func hexSum(h hash.Hash) string { return hex.EncodeToString(h.Sum(nil)) }

// ---- prefix helpers (folder semantics) -------------------------------------
//
// rm -r / cp -r / mv -r / sync treat the prefix as a folder: "logs" matches
// "logs" and "logs/...", but NOT "logs2/...". This is deliberately safer than
// raw string-prefix matching for destructive commands.

func under(key, prefix string) bool {
	p := strings.TrimSuffix(prefix, "/")
	if p == "" {
		return true
	}
	return key == p || strings.HasPrefix(key, p+"/")
}

func relTo(key, prefix string) string {
	p := strings.TrimSuffix(prefix, "/")
	if p == "" {
		return key
	}
	if key == p {
		return path.Base(key)
	}
	return strings.TrimPrefix(key, p+"/")
}

func joinKey(prefix, rel string) string {
	p := strings.TrimSuffix(prefix, "/")
	if p == "" {
		return rel
	}
	return p + "/" + rel
}

func (c *CLI) fetchBuckets() ([]BucketInfo, error) {
	resp, err := c.client.Get(c.config.ServerUrl + "/buckets")
	if err != nil {
		return nil, fmt.Errorf("failed to list buckets: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, httpError(resp, "failed to list buckets")
	}
	var buckets []BucketInfo
	if err := json.NewDecoder(resp.Body).Decode(&buckets); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	return buckets, nil
}

func (c *CLI) fetchObjects(bucket string) ([]ObjectInfo, error) {
	resp, err := c.client.Get(c.listURL(bucket))
	if err != nil {
		return nil, fmt.Errorf("failed to list objects: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("bucket '%s' not found", bucket)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, httpError(resp, "failed to list objects")
	}
	var objects []ObjectInfo
	if err := json.NewDecoder(resp.Body).Decode(&objects); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	return objects, nil
}

func (c *CLI) fetchUnder(r remotePath) ([]ObjectInfo, error) {
	all, err := c.fetchObjects(r.Bucket)
	if err != nil {
		return nil, err
	}
	var out []ObjectInfo
	for _, o := range all {
		if under(o.Key, r.Key) {
			out = append(out, o)
		}
	}
	return out, nil
}

// uploadFile streams a local file with a known Content-Length and checks the
// SHA-256 the server reports back against the bytes that were actually sent.
func (c *CLI) uploadFile(localPath string, dst remotePath) error {
	f, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("failed to open local file: %w", err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("failed to stat local file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("'%s' is not a regular file", localPath)
	}

	h := sha256.New()
	var body io.Reader = io.TeeReader(f, h)
	if info.Size() == 0 {
		// A non-nil body with ContentLength 0 means "unknown length" to net/http.
		body = http.NoBody
	}

	req, err := http.NewRequest(http.MethodPut, c.objectURL(dst.Bucket, dst.Key), body)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	// *os.File isn't one of the types NewRequest sizes automatically, so
	// without this the upload would go out chunked.
	req.ContentLength = info.Size()
	req.Header.Set("Content-Type", getContentType(localPath))

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to upload file: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return httpError(resp, "failed to upload file")
	}

	var meta ObjectInfo
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		return fmt.Errorf("upload not confirmed, bad server response: %w", err)
	}
	if meta.SHA256 != "" && !strings.EqualFold(meta.SHA256, hexSum(h)) {
		return fmt.Errorf("upload verification failed: server stored sha256 %s, local data is %s", meta.SHA256, hexSum(h))
	}
	return nil
}

// downloadObject writes to a temp file next to the destination, verifies the
// X-Content-SHA256 header, and only then renames into place. A failed or
// corrupt download never touches an existing file.
func (c *CLI) downloadObject(src remotePath, localPath string) (int64, error) {
	resp, err := c.client.Get(c.objectURL(src.Bucket, src.Key))
	if err != nil {
		return 0, fmt.Errorf("failed to download file: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return 0, fmt.Errorf("object '%s' not found", src)
	}
	if resp.StatusCode != http.StatusOK {
		return 0, httpError(resp, "failed to download file")
	}
	want := resp.Header.Get("X-Content-SHA256")

	dir := filepath.Dir(localPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, fmt.Errorf("failed to create directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".storage-cli-*")
	if err != nil {
		return 0, fmt.Errorf("failed to create local file: %w", err)
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	defer tmp.Close()

	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), resp.Body)
	if err != nil {
		return 0, fmt.Errorf("failed to write file: %w", err)
	}
	if want != "" {
		if got := hexSum(h); !strings.EqualFold(got, want) {
			return 0, fmt.Errorf("checksum mismatch for '%s': server sha256 %s, downloaded data %s", src, want, got)
		}
	} else {
		c.debugf("warning: server sent no X-Content-SHA256, '%s' not verified\n", src)
	}
	if err := tmp.Close(); err != nil {
		return 0, err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return 0, err
	}
	if err := os.Rename(tmp.Name(), localPath); err != nil {
		return 0, fmt.Errorf("failed to finalize file: %w", err)
	}
	return n, nil
}

// copyRemote streams GET -> PUT through the client (no temp file, constant
// memory). Both the source header and the destination's reported SHA-256 are
// checked, so `mv` never deletes a source that wasn't copied intact.
func (c *CLI) copyRemote(src, dst remotePath) error {
	if src == dst {
		return fmt.Errorf("source and destination are the same object")
	}
	resp, err := c.client.Get(c.objectURL(src.Bucket, src.Key))
	if err != nil {
		return fmt.Errorf("failed to read source: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("object '%s' not found", src)
	}
	if resp.StatusCode != http.StatusOK {
		return httpError(resp, "failed to read source")
	}
	want := resp.Header.Get("X-Content-SHA256")

	h := sha256.New()
	var body io.Reader = io.TeeReader(resp.Body, h)
	if resp.ContentLength == 0 {
		body = http.NoBody
	}
	req, err := http.NewRequest(http.MethodPut, c.objectURL(dst.Bucket, dst.Key), body)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.ContentLength = resp.ContentLength // -1 (unknown) falls back to chunked
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		req.Header.Set("Content-Type", ct)
	}

	presp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to write destination: %w", err)
	}
	defer presp.Body.Close()
	if presp.StatusCode != http.StatusOK {
		return httpError(presp, "failed to write destination")
	}

	var meta ObjectInfo
	if err := json.NewDecoder(presp.Body).Decode(&meta); err != nil {
		return fmt.Errorf("copy not confirmed, bad server response: %w", err)
	}
	got := hexSum(h)
	if (want != "" && !strings.EqualFold(want, got)) ||
		(meta.SHA256 != "" && !strings.EqualFold(meta.SHA256, got)) {
		_ = c.deleteObject(dst)
		return fmt.Errorf("copy verification failed for '%s'", src)
	}
	return nil
}

func (c *CLI) deleteObject(r remotePath) error {
	req, err := http.NewRequest(http.MethodDelete, c.objectURL(r.Bucket, r.Key), nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to delete object: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return httpError(resp, "failed to delete object")
	}
	return nil
}

func md5File(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := md5.New() // ETag is MD5; used for change detection only
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hexSum(h), nil
}

// sameContent compares size first (free) and only hashes the local file when
// the sizes match.
func sameContent(localPath string, localSize int64, ob ObjectInfo) (bool, error) {
	if ob.Size != localSize {
		return false, nil
	}
	etag := strings.Trim(ob.ETag, `"`)
	if etag == "" {
		return false, nil
	}
	sum, err := md5File(localPath)
	if err != nil {
		return false, err
	}
	return strings.EqualFold(sum, etag), nil
}
