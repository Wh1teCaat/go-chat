package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalStorageSaveWritesFileAndReturnsPublicURL(t *testing.T) {
	root := t.TempDir()
	store := NewLocalStorage(root, "/uploads")

	stored, err := store.Save(context.Background(), SaveObjectInput{
		Reader: strings.NewReader("pdf content"),
		Meta: ObjectMeta{
			OriginalName: "abc.pdf",
			ContentType:  "application/pdf",
			Size:         int64(len("pdf content")),
			Extension:    ".pdf",
		},
	})
	if err != nil {
		t.Fatalf("Save returned error: %v", err)
	}

	if stored.Key == "" {
		t.Fatal("expected storage key")
	}
	if stored.StoredName == "abc.pdf" {
		t.Fatal("expected generated stored name instead of original filename")
	}
	if stored.URL != "/uploads/"+stored.Key {
		t.Fatalf("expected public URL to include key, got %q for key %q", stored.URL, stored.Key)
	}
	if stored.SHA256 != sha256Hex("pdf content") {
		t.Fatalf("expected sha256 %q, got %q", sha256Hex("pdf content"), stored.SHA256)
	}

	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(stored.Key)))
	if err != nil {
		t.Fatalf("expected saved file to exist: %v", err)
	}
	if string(data) != "pdf content" {
		t.Fatalf("expected saved content %q, got %q", "pdf content", string(data))
	}
}

func TestLocalStorageOpenReadsSavedFile(t *testing.T) {
	root := t.TempDir()
	store := NewLocalStorage(root, "/uploads")

	stored, err := store.Save(context.Background(), SaveObjectInput{
		Reader: strings.NewReader("download content"),
		Meta: ObjectMeta{
			OriginalName: "download.txt",
			ContentType:  "text/plain",
			Size:         int64(len("download content")),
			Extension:    ".txt",
		},
	})
	if err != nil {
		t.Fatalf("Save returned error: %v", err)
	}

	reader, err := store.Open(context.Background(), stored.Key)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	defer reader.Close()

	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("ReadAll returned error: %v", err)
	}
	if string(data) != "download content" {
		t.Fatalf("expected saved content, got %q", string(data))
	}
}

func TestLocalStorageOpenRangeReadsByteRange(t *testing.T) {
	root := t.TempDir()
	store := NewLocalStorage(root, "/uploads")

	stored, err := store.Save(context.Background(), SaveObjectInput{
		Reader: strings.NewReader("0123456789"),
		Meta: ObjectMeta{
			OriginalName: "range.txt",
			ContentType:  "text/plain",
			Size:         int64(len("0123456789")),
			Extension:    ".txt",
		},
	})
	if err != nil {
		t.Fatalf("Save returned error: %v", err)
	}

	reader, err := store.OpenRange(context.Background(), stored.Key, 2, 5)
	if err != nil {
		t.Fatalf("OpenRange returned error: %v", err)
	}
	defer reader.Close()

	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("ReadAll returned error: %v", err)
	}
	if string(data) != "2345" {
		t.Fatalf("expected range content %q, got %q", "2345", string(data))
	}
}

func TestLocalStorageMultipartUploadCombinesParts(t *testing.T) {
	root := t.TempDir()
	store := NewLocalStorage(root, "/uploads")

	if size, err := store.SavePart(context.Background(), "upload-1", 0, strings.NewReader("hello ")); err != nil || size != int64(len("hello ")) {
		t.Fatalf("SavePart first returned size=%d err=%v", size, err)
	}
	if size, err := store.SavePart(context.Background(), "upload-1", 1, strings.NewReader("world")); err != nil || size != int64(len("world")) {
		t.Fatalf("SavePart second returned size=%d err=%v", size, err)
	}

	stored, err := store.CompleteMultipart(context.Background(), CompleteMultipartInput{
		UploadID:    "upload-1",
		TotalChunks: 2,
		Meta: ObjectMeta{
			OriginalName: "hello.txt",
			ContentType:  "text/plain",
			Size:         int64(len("hello world")),
			Extension:    ".txt",
		},
	})
	if err != nil {
		t.Fatalf("CompleteMultipart returned error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(stored.Key)))
	if err != nil {
		t.Fatalf("expected completed file to exist: %v", err)
	}
	if string(data) != "hello world" {
		t.Fatalf("expected completed content, got %q", string(data))
	}
	if stored.SHA256 != sha256Hex("hello world") {
		t.Fatalf("expected sha256 %q, got %q", sha256Hex("hello world"), stored.SHA256)
	}

	if err := store.DeleteMultipart(context.Background(), "upload-1"); err != nil {
		t.Fatalf("DeleteMultipart returned error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".parts", "upload-1")); !os.IsNotExist(err) {
		t.Fatalf("expected parts directory to be removed, stat err=%v", err)
	}
}

func sha256Hex(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}
