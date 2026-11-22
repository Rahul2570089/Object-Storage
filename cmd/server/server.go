package main

import (
	"log"
	"net/http"
	"strings"
)

func main() {
	storage := NewObjectStorage("./storage")
	server := NewStorageServer(storage)

	http.HandleFunc("/buckets/", server.handleBucket)
	http.HandleFunc("/buckets", server.handleListBuckets)
	http.HandleFunc("/objects/", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/objects/")
		if !strings.Contains(path, "/") {
			server.handleListObjects(w, r)
			return
		}
		switch r.Method {
		case http.MethodPut:
			server.handlePutObject(w, r)
		case http.MethodDelete:
			server.handleDeleteObject(w, r)
		default:
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
	log.Println("  DELETE /buckets/{name} - Delete an empty bucket")
	log.Println("  PUT /objects/{bucket}/{key} - Upload object")
	log.Println("  GET /objects/{bucket}/{key} - Download object")
	log.Println("  GET /objects/{bucket} - List objects in bucket")

	if err := http.ListenAndServe(":8080", nil); err != nil {
		log.Fatal("Server failed to start:", err)
	}
}
