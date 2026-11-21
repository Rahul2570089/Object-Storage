package main

import (
	"hash/fnv"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

func validBucket(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, `/\`)
}

func validKey(key string) bool {
	if key == "" || strings.Contains(key, `\`) {
		return false
	}
	for _, seg := range strings.Split(key, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

func (s *ObjectStorage) lockFor(bucket, key string) *sync.RWMutex {
	h := fnv.New32a()
	h.Write([]byte(bucket + "/" + key))
	return &s.locks[h.Sum32()%uint32(len(s.locks))]
}

func (s *ObjectStorage) bucketExists(bucket string) bool {
	info, err := os.Stat(filepath.Join(s.dataDir, bucket))
	return err == nil && info.IsDir()
}

func syncDir(dir string) error {
	if runtime.GOOS == "windows" {
		// Windows has no fsync for directories, so skip this step there.
		return nil
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}

	defer d.Close()
	return d.Sync()
}

func (s *ObjectStorage) writeFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.tmpDir, "meta-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}
