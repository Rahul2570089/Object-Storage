package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

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
	if r.Method != http.MethodGet {
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
	switch {
	case errors.Is(err, ErrInvalidName):
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	case errors.Is(err, ErrBucketNotFound):
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	case err != nil:
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("ETag", metadata.ETag)
	json.NewEncoder(w).Encode(metadata)
}

func (s *StorageServer) handleGetObject(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
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

	file, metadata, err := s.storage.GetObject(bucketName, objectKey)
	switch {
	case errors.Is(err, ErrInvalidName):
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	case errors.Is(err, ErrNotFound):
		http.Error(w, "Object not found", http.StatusNotFound)
		return
	case err != nil:
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer file.Close()

	w.Header().Set("Content-Type", metadata.ContentType)
	w.Header().Set("ETag", metadata.ETag)
	http.ServeContent(w, r, "", metadata.LastModified, file)
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

func (s *StorageServer) handleDeleteObject(w http.ResponseWriter, r *http.Request) {
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/objects/"), "/", 2)
	if len(parts) < 2 {
		http.Error(w, "Bucket and object key required", http.StatusBadRequest)
		return
	}
	err := s.storage.DeleteObject(parts[0], parts[1])
	switch {
	case errors.Is(err, ErrInvalidName):
		http.Error(w, err.Error(), http.StatusBadRequest)
	case err != nil:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}
