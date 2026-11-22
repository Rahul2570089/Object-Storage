package main

import (
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"
)

var (
	ErrNotFound         = errors.New("object not found")
	ErrBucketNotFound   = errors.New("bucket not found")
	ErrInvalidName      = errors.New("invalid bucket name or object name")
	ErrChecksumMismatch = errors.New("checksum mismatch")
	ErrCorrupt          = errors.New("object data does not match its metadata")
	ErrBucketNotEmpty   = errors.New("bucket not empty")
)

type Bucket struct {
	Name    string    `json:"name"`
	Created time.Time `json:"created"`
}

type ObjectStorage struct {
	dataDir     string
	metadataDir string
	tmpDir      string
	locks       [256]sync.RWMutex
	bucketMu    sync.RWMutex
}

type ObjectMetadata struct {
	Key          string    `json:"key"`
	Size         int64     `json:"size"`
	ContentType  string    `json:"content_type"`
	ETag         string    `json:"etag"`
	SHA256       string    `json:"sha256"`
	LastModified time.Time `json:"last_modified"`
}

func NewObjectStorage(baseDir string) *ObjectStorage {
	dataDir := filepath.Join(baseDir, "data")
	metadataDir := filepath.Join(baseDir, "metadata")
	tmpDir := filepath.Join(baseDir, "tmp")

	os.MkdirAll(dataDir, 0755)
	os.MkdirAll(metadataDir, 0755)
	os.MkdirAll(tmpDir, 0755)

	s := &ObjectStorage{
		dataDir:     dataDir,
		metadataDir: metadataDir,
		tmpDir:      tmpDir,
	}

	if entries, err := os.ReadDir(s.tmpDir); err == nil {
		for _, e := range entries {
			os.Remove(filepath.Join(s.tmpDir, e.Name()))
		}
	}
	return s
}

func (storage *ObjectStorage) CreateBucket(bucketName string) error {
	if !validBucket(bucketName) {
		return ErrInvalidName
	}

	bucketDir := filepath.Join(storage.dataDir, bucketName)
	if err := storage.MkdirAll(bucketDir, 0755); err != nil {
		return fmt.Errorf("failed to create Bucket: %w", err)
	}

	bucket := Bucket{
		Name:    bucketName,
		Created: time.Now(),
	}

	return storage.saveBucketMetaData(bucket)
}

func (storage *ObjectStorage) PutObject(bucketName, objectKey string, data io.Reader, contentType string) (*ObjectMetadata, error) {
	if !validBucket(bucketName) || !validKey(objectKey) {
		return nil, ErrInvalidName
	}
	if !storage.bucketExists(bucketName) {
		return nil, ErrBucketNotFound
	}

	objectPath := filepath.Join(storage.dataDir, bucketName, objectKey)

	tempFile, err := os.CreateTemp(storage.tmpDir, "upload-*")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp file: %w", err)
	}
	defer os.Remove(tempFile.Name()) // no-op after a successful rename
	defer tempFile.Close()

	md5h, sha := md5.New(), sha256.New()
	size, err := io.Copy(io.MultiWriter(tempFile, md5h, sha), data)
	if err != nil {
		return nil, fmt.Errorf("failed to write object data: %w", err)
	}
	if err := tempFile.Sync(); err != nil {
		return nil, fmt.Errorf("failed to sync data object data: %w", err)
	}
	if err := tempFile.Close(); err != nil {
		return nil, err
	}

	metadata := &ObjectMetadata{
		Key:          objectKey,
		Size:         size,
		ContentType:  contentType,
		ETag:         hex.EncodeToString(md5h.Sum(nil)),
		SHA256:       hex.EncodeToString(sha.Sum(nil)),
		LastModified: time.Now(),
	}

	storage.bucketMu.RLock()
	defer storage.bucketMu.RUnlock()
	if !storage.bucketExists(bucketName) {
		return nil, ErrBucketNotFound
	}

	mu := storage.lockFor(bucketName, objectKey)
	mu.Lock()
	defer mu.Unlock()

	if err := storage.MkdirAll(filepath.Dir(objectPath), 0755); err != nil {
		return nil, fmt.Errorf("failed to create object directory: %w", err)
	}
	if err := storage.Rename(tempFile.Name(), objectPath); err != nil {
		return nil, fmt.Errorf("failed to finalize object: %w", err)
	}
	if err := syncDir(filepath.Dir(objectPath)); err != nil {
		return nil, err
	}

	if err := storage.saveObjectMetaData(bucketName, metadata); err != nil {
		return nil, fmt.Errorf("failed to save metadata: %w", err)
	}

	return metadata, nil
}

func (storage *ObjectStorage) GetObject(bucketName, objectKey string) (*os.File, *ObjectMetadata, error) {
	if !validBucket(bucketName) || !validKey(objectKey) {
		return nil, nil, ErrInvalidName
	}
	mu := storage.lockFor(bucketName, objectKey)
	mu.RLock()
	defer mu.RUnlock()

	metadata, err := storage.loadObjectMetadata(bucketName, objectKey)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, fmt.Errorf("failed to load metadata: %w", err)
	}

	file, err := storage.Open(filepath.Join(storage.dataDir, bucketName, objectKey))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, fmt.Errorf("failed to open object: %w", err)
	}

	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, nil, err
	}
	if info.Size() != metadata.Size {
		file.Close()
		return nil, nil, ErrCorrupt
	}
	return file, metadata, nil
}

func (storage *ObjectStorage) DeleteBucket(bucketName string) error {
	if !validBucket(bucketName) {
		return ErrInvalidName
	}
	storage.bucketMu.Lock()
	defer storage.bucketMu.Unlock()

	if !storage.bucketExists(bucketName) {
		return ErrBucketNotFound
	}

	bucketDir := filepath.Join(storage.dataDir, bucketName)
	hasFiles := false
	err := filepath.WalkDir(bucketDir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			hasFiles = true
			return fs.SkipAll
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to inspect bucket: %w", err)
	}
	if hasFiles {
		return ErrBucketNotEmpty
	}

	// Data dir first: an orphaned metadata file is harmless, a data dir
	// without metadata would show up in ListBuckets with a fake date.
	if err := os.RemoveAll(bucketDir); err != nil {
		return fmt.Errorf("failed to delete bucket: %w", err)
	}
	if err := os.RemoveAll(filepath.Join(storage.metadataDir, bucketName)); err != nil {
		return fmt.Errorf("failed to delete bucket metadata: %w", err)
	}
	if err := os.Remove(filepath.Join(storage.metadataDir, bucketName+".json")); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to delete bucket metadata: %w", err)
	}
	if err := syncDir(storage.dataDir); err != nil {
		return err
	}
	return syncDir(storage.metadataDir)
}

func (storage *ObjectStorage) Verify(bucketName, objectKey string) error {
	file, metadata, err := storage.GetObject(bucketName, objectKey)
	if err != nil {
		return err
	}
	defer file.Close()
	if metadata.SHA256 == "" {
		return nil
	}
	h := sha256.New()
	if _, err := io.Copy(h, file); err != nil {
		return err
	}
	if hex.EncodeToString(h.Sum(nil)) != metadata.SHA256 {
		return ErrChecksumMismatch
	}
	return nil
}

func (storage *ObjectStorage) DeleteObject(bucketName, objectKey string) error {
	if !validBucket(bucketName) || !validKey(objectKey) {
		return ErrInvalidName
	}
	mu := storage.lockFor(bucketName, objectKey)
	mu.Lock()
	defer mu.Unlock()

	metadataPath := filepath.Join(storage.metadataDir, bucketName, objectKey+".json")
	if err := storage.Remove(metadataPath); err != nil && !storage.IsNotExist(err) {
		return fmt.Errorf("failed to delete metadata: %w", err)
	}
	objectPath := filepath.Join(storage.dataDir, bucketName, objectKey)
	if err := storage.Remove(objectPath); err != nil && !storage.IsNotExist(err) {
		return fmt.Errorf("failed to delete object: %w", err)
	}
	return nil
}

func (storage *ObjectStorage) ListBuckets() ([]Bucket, error) {
	entries, err := storage.ReadDir(storage.dataDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read data directory: %w", err)
	}

	var buckets []Bucket
	for _, entry := range entries {
		if entry.IsDir() {
			bucket, err := storage.loadBucketMetadata(entry.Name())
			if err != nil {
				continue
			}
			buckets = append(buckets, bucket)
		}
	}
	return buckets, nil
}

func (storage *ObjectStorage) ListObjects(bucketName string) ([]ObjectMetadata, error) {
	if !validBucket(bucketName) {
		return nil, ErrInvalidName
	}
	if !storage.bucketExists(bucketName) {
		return nil, ErrBucketNotFound
	}

	bucketPath := filepath.Join(storage.dataDir, bucketName)
	var objects []ObjectMetadata

	err := filepath.Walk(bucketPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if !info.IsDir() {
			relPath, err := filepath.Rel(bucketPath, path)
			if err != nil {
				return err
			}

			metadata, err := storage.loadObjectMetadata(bucketName, relPath)
			if err != nil {
				return nil
			}

			objects = append(objects, *metadata)
		}

		return nil
	})

	return objects, err
}

func (storage *ObjectStorage) saveBucketMetaData(bucket Bucket) error {
	metadataPath := filepath.Join(storage.metadataDir, bucket.Name+".json")
	os.MkdirAll(filepath.Dir(metadataPath), 0755)

	data, err := json.MarshalIndent(bucket, "", "	")
	if err != nil {
		return err
	}

	return storage.writeFileAtomic(metadataPath, data)
}

func (storage *ObjectStorage) saveObjectMetaData(bucketName string, metadata *ObjectMetadata) error {
	metadataPath := filepath.Join(storage.metadataDir, bucketName, metadata.Key+".json")
	os.MkdirAll(filepath.Dir(metadataPath), 0755)

	data, err := json.MarshalIndent(metadata, "", "	")
	if err != nil {
		return err
	}

	return storage.writeFileAtomic(metadataPath, data)
}

func (storage *ObjectStorage) loadObjectMetadata(bucketName string, objectKey string) (*ObjectMetadata, error) {
	metadataPath := filepath.Join(storage.metadataDir, bucketName, objectKey+".json")

	data, err := storage.ReadFile(metadataPath)
	if err != nil {
		return nil, err
	}

	var metadata ObjectMetadata
	err = json.Unmarshal(data, &metadata)
	if err != nil {
		return nil, err
	}
	return &metadata, nil
}

func (storage *ObjectStorage) loadBucketMetadata(bucketName string) (Bucket, error) {
	metadataPath := filepath.Join(storage.metadataDir, bucketName+".json")

	data, err := storage.ReadFile(metadataPath)
	if err != nil {
		return Bucket{Name: bucketName, Created: time.Now()}, nil
	}

	var bucket Bucket
	err = json.Unmarshal(data, &bucket)
	return bucket, err
}

func (storage *ObjectStorage) ReadDir(path string) ([]os.DirEntry, error) {
	return os.ReadDir(path)
}

func (storage *ObjectStorage) IsNotExist(err error) bool {
	return os.IsNotExist(err)
}

func (storage *ObjectStorage) MkdirAll(path string, perm os.FileMode) error {
	return os.MkdirAll(path, perm)
}

func (storage *ObjectStorage) Remove(path string) error {
	return os.Remove(path)
}

func (storage *ObjectStorage) Rename(oldpath, newpath string) error {
	return os.Rename(oldpath, newpath)
}

func (storage *ObjectStorage) Stat(path string) (os.FileInfo, error) {
	return os.Stat(path)
}

func (storage *ObjectStorage) Open(path string) (*os.File, error) {
	return os.Open(path)
}

func (storage *ObjectStorage) WriteFile(path string, data []byte, perm os.FileMode) error {
	return os.WriteFile(path, data, perm)
}

func (storage *ObjectStorage) ReadFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}
