package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("connection reset") }

func newTestStorage(t *testing.T) *ObjectStorage {
	t.Helper()
	s := NewObjectStorage(t.TempDir())
	if err := s.CreateBucket("b"); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestRoundTrip(t *testing.T) {
	s := newTestStorage(t)
	payload := bytes.Repeat([]byte("log line\n"), 1000)
	meta, err := s.PutObject("b", "app/a.log", bytes.NewReader(payload), "text/plain")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(payload)
	if meta.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatal("sha256 mismatch")
	}
	f, _, err := s.GetObject("b", "app/a.log")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	got, _ := io.ReadAll(f)
	if !bytes.Equal(got, payload) {
		t.Fatal("content mismatch")
	}
	if err := s.Verify("b", "app/a.log"); err != nil {
		t.Fatal(err)
	}
}

func TestFailedUploadLeavesNothing(t *testing.T) {
	s := newTestStorage(t)
	_, err := s.PutObject("b", "k.txt",
		io.MultiReader(strings.NewReader("partial"), errReader{}), "text/plain")
	if err == nil {
		t.Fatal("expected an error")
	}
	if _, _, err := s.GetObject("b", "k.txt"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("object should not exist, got %v", err)
	}
	if entries, _ := os.ReadDir(s.tmpDir); len(entries) != 0 {
		t.Fatalf("%d temp files left behind", len(entries))
	}
}

func TestFailedOverwriteKeepsOldObject(t *testing.T) {
	s := newTestStorage(t)
	if _, err := s.PutObject("b", "k", strings.NewReader("version one"), "text/plain"); err != nil {
		t.Fatal(err)
	}
	_, err := s.PutObject("b", "k",
		io.MultiReader(strings.NewReader("ver"), errReader{}), "text/plain")
	if err == nil {
		t.Fatal("expected an error")
	}
	f, _, err := s.GetObject("b", "k")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if got, _ := io.ReadAll(f); string(got) != "version one" {
		t.Fatalf("old object damaged: %q", got)
	}
}

func TestVerifyDetectsCorruption(t *testing.T) {
	s := newTestStorage(t)
	if _, err := s.PutObject("b", "k", strings.NewReader("hello world"), "text/plain"); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(s.dataDir, "b", "k")
	raw, _ := os.ReadFile(p)
	raw[0] ^= 0xFF // flip bits, same size
	os.WriteFile(p, raw, 0644)
	if err := s.Verify("b", "k"); !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("expected checksum mismatch, got %v", err)
	}
}

func TestConcurrentWritersSameKey(t *testing.T) {
	s := newTestStorage(t)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		payload := bytes.Repeat([]byte{byte('a' + i)}, 64*1024)
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.PutObject("b", "same", bytes.NewReader(payload), "text/plain"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if err := s.Verify("b", "same"); err != nil {
		t.Fatalf("data and metadata disagree after concurrent writes: %v", err)
	}
}
