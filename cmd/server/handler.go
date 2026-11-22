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

func (s *StorageServer) handleBucket(w http.ResponseWriter, r *http.Request) {
	bucketName := strings.TrimPrefix(r.URL.Path, "/buckets/")
	if bucketName == "" {
		http.Error(w, "Bucket name required", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodPut:
		err := s.storage.CreateBucket(bucketName)
		switch {
		case errors.Is(err, ErrInvalidName):
			http.Error(w, err.Error(), http.StatusBadRequest)
		case err != nil:
			http.Error(w, err.Error(), http.StatusInternalServerError)
		default:
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]string{"status": "bucket created"})
		}
	case http.MethodDelete:
		err := s.storage.DeleteBucket(bucketName)
		switch {
		case errors.Is(err, ErrInvalidName):
			http.Error(w, err.Error(), http.StatusBadRequest)
		case errors.Is(err, ErrBucketNotFound):
			http.Error(w, err.Error(), http.StatusNotFound)
		case errors.Is(err, ErrBucketNotEmpty):
			http.Error(w, err.Error(), http.StatusConflict)
		case err != nil:
			http.Error(w, err.Error(), http.StatusInternalServerError)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	default:
		w.Header().Set("Allow", "PUT, DELETE")
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
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

	w.Header().Set("ETag", metadata.ETag)
	if metadata.SHA256 != "" {
		w.Header().Set("X-Content-SHA256", metadata.SHA256)
	}
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
	if objects == nil {
		objects = []ObjectMetadata{}
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
