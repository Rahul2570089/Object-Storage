package main

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Bucket struct {
	Name    string    `json:"name"`
	Created time.Time `json:"created"`
}

type ObjectStorage struct {
	dataDir     string
	metadataDir string
}

type ObjectMetadata struct {
	Key          string    `json:"key"`
	Size         int64     `json:"size"`
	ContentType  string    `json:"content_type"`
	ETag         string    `json:"etag"`
	LastModified time.Time `json:"last_modified"`
}

func NewObjectStorage(baseDir string) *ObjectStorage {
	dataDir := filepath.Join(baseDir, "data")
	metadataDir := filepath.Join(baseDir, "metadata")

	os.MkdirAll(dataDir, 0755)
	os.MkdirAll(metadataDir, 0755)

	return &ObjectStorage{
		dataDir:     dataDir,
		metadataDir: metadataDir,
	}
}

func (storage *ObjectStorage) CreateBucket(bucketName string) error {
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
	objectPath := filepath.Join(storage.dataDir, bucketName, objectKey)
	objectDir := filepath.Dir(objectPath)

	if err := storage.MkdirAll(objectDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create object directory: %w", err)
	}

	tempFile, err := os.CreateTemp(objectDir, "upload-*.tmp")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp file: %w", err)
	}
	defer tempFile.Close()

	hash := md5.New()
	multiWriter := io.MultiWriter(tempFile, hash)

	size, err := io.Copy(multiWriter, data)
	if err != nil {
		storage.Remove(tempFile.Name())
		return nil, fmt.Errorf("failed to write object data: %w", err)
	}

	tempFile.Close()

	if err := storage.Rename(tempFile.Name(), objectPath); err != nil {
		storage.Remove(tempFile.Name())
		return nil, fmt.Errorf("failed to finalize object: %w", err)
	}

	metadata := &ObjectMetadata{
		Key:          objectKey,
		Size:         size,
		ContentType:  contentType,
		ETag:         hex.EncodeToString(hash.Sum(nil)),
		LastModified: time.Now(),
	}

	if err := storage.saveObjectMetaData(bucketName, metadata); err != nil {
		return nil, fmt.Errorf("failed to save metadata: %w", err)
	}

	return metadata, nil
}

func (storage *ObjectStorage) GetObject(bucketName, objectKey string) (io.ReadCloser, *ObjectMetadata, error) {
	objectPath := filepath.Join(storage.dataDir, bucketName, objectKey)

	if _, err := storage.Stat(objectPath); storage.IsNotExist(err) {
		return nil, nil, fmt.Errorf("object not found")
	}

	file, err := storage.Open(objectPath)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to open object: %w", err)
	}

	metadata, err := storage.loadObjectMetadata(bucketName, objectKey)
	if err != nil {
		file.Close()
		return nil, nil, fmt.Errorf("failed to load metadata: %w", err)
	}

	return file, metadata, nil
}

func (storage *ObjectStorage) DeleteObject(bucketName, objectKey string) error {
	objectPath := filepath.Join(storage.dataDir, bucketName, objectKey)

	if err := storage.Remove(objectPath); err != nil && !storage.IsNotExist(err) {
		return fmt.Errorf("failed to delete object: %w", err)
	}

	metadataPath := filepath.Join(storage.dataDir, bucketName, objectKey+".json")
	if err := storage.Remove(metadataPath); err != nil && !storage.IsNotExist(err) {
		return fmt.Errorf("failed to delete metadata: %w", err)
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

	return storage.WriteFile(metadataPath, data, 0644)
}

func (storage *ObjectStorage) saveObjectMetaData(bucketName string, metadata *ObjectMetadata) error {
	metadataPath := filepath.Join(storage.metadataDir, bucketName, metadata.Key+".json")
	os.MkdirAll(filepath.Dir(metadataPath), 0755)

	data, err := json.MarshalIndent(metadata, "", "	")
	if err != nil {
		return err
	}

	return storage.WriteFile(metadataPath, data, 0644)
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

type StorageServer struct {
	storage *ObjectStorage
}

func NewStorageServer(storage *ObjectStorage) *StorageServer {
	return &StorageServer{storage: storage}
}

func (s *StorageServer) handleCreateBucket(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	bucketName := strings.TrimPrefix(r.URL.Path, "/buckets/")
	if bucketName == "" {
		http.Error(w, "Bucket name required", http.StatusBadRequest)
		return
	}

	if err := s.storage.CreateBucket(bucketName); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{"status": "bucket created"})
}

func (s *StorageServer) handleListBuckets(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	buckets, err := s.storage.ListBuckets()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(buckets)
}

func (s *StorageServer) handlePutObject(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/objects/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) < 2 {
		http.Error(w, "Bucket and object key required", http.StatusBadRequest)
		return
	}

	bucketName, objectKey := parts[0], parts[1]
	contentType := r.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	metadata, err := s.storage.PutObject(bucketName, objectKey, r.Body, contentType)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("ETag", metadata.ETag)
	json.NewEncoder(w).Encode(metadata)
}

func (s *StorageServer) handleGetObject(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/objects/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) < 2 {
		http.Error(w, "Bucket and object key required", http.StatusBadRequest)
		return
	}

	bucketName, objectKey := parts[0], parts[1]

	reader, metadata, err := s.storage.GetObject(bucketName, objectKey)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			http.Error(w, "Object not found", http.StatusNotFound)
		} else {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}
	defer reader.Close()

	w.Header().Set("Content-Type", metadata.ContentType)
	w.Header().Set("ETag", metadata.ETag)
	w.Header().Set("Last-Modified", metadata.LastModified.Format(http.TimeFormat))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", metadata.Size))

	io.Copy(w, reader)
}

func (s *StorageServer) handleListObjects(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	bucketName := strings.TrimPrefix(r.URL.Path, "/objects/")
	if strings.Contains(bucketName, "/") {
		// This is a specific object request, not a list
		s.handleGetObject(w, r)
		return
	}

	objects, err := s.storage.ListObjects(bucketName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(objects)
}

func main() {
	storage := NewObjectStorage("./storage")
	server := NewStorageServer(storage)

	http.HandleFunc("/buckets/", server.handleCreateBucket)
	http.HandleFunc("/buckets", server.handleListBuckets)
	http.HandleFunc("/objects/", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/objects/")
		if !strings.Contains(path, "/") {
			server.handleListObjects(w, r)
		} else if r.Method == http.MethodPut {
			server.handlePutObject(w, r)
		} else {
			server.handleGetObject(w, r)
		}
	})

	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	log.Println("Object storage server starting on :8080")
	log.Println("API endpoints:")
	log.Println("  PUT /buckets/{name} - Create bucket")
	log.Println("  GET /buckets - List buckets")
	log.Println("  PUT /objects/{bucket}/{key} - Upload object")
	log.Println("  GET /objects/{bucket}/{key} - Download object")
	log.Println("  GET /objects/{bucket} - List objects in bucket")

	if err := http.ListenAndServe(":8080", nil); err != nil {
		log.Fatal("Server failed to start:", err)
	}
}
