package storagetest

import (
	"testing"

	"github.com/goforj/storage"
	"github.com/goforj/storage/driver/memorystorage"
)

// TestFake verifies that the default fake provides an immediately usable in-memory disk.
func TestFake(t *testing.T) {
	store := Fake(t)
	if err := store.Put("photo.jpg", []byte("ok")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := store.Get("photo.jpg")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != "ok" {
		t.Fatalf("Get = %q", got)
	}
}

// TestFakeWithPrefix verifies that configured prefixes remain hidden from logical listings.
func TestFakeWithPrefix(t *testing.T) {
	store := FakeWithPrefix(t, "avatars")
	if err := store.Put("one.jpg", []byte("ok")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	entries, err := store.List("")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 || entries[0].Path != "one.jpg" {
		t.Fatalf("List entries = %+v", entries)
	}
}

// TestFakeManagerDefaults verifies that omitted manager options create one usable default disk.
func TestFakeManagerDefaults(t *testing.T) {
	mgr := FakeManager(t, "", nil)
	store := mgr.Default()
	if err := store.Put("hello.txt", []byte("hello")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	ok, err := store.Exists("hello.txt")
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if !ok {
		t.Fatal("Exists returned false")
	}
}

// TestFakeManagerNamedDisks verifies that named fake disks retain independent prefixed state.
func TestFakeManagerNamedDisks(t *testing.T) {
	mgr := FakeManager(t, "photos", map[storage.DiskName]memorystorage.Config{
		"photos":  {Prefix: "photos"},
		"avatars": {Prefix: "avatars"},
	})

	photos, err := mgr.Disk("photos")
	if err != nil {
		t.Fatalf("photos disk: %v", err)
	}
	avatars, err := mgr.Disk("avatars")
	if err != nil {
		t.Fatalf("avatars disk: %v", err)
	}

	if err := photos.Put("one.jpg", []byte("one")); err != nil {
		t.Fatalf("photos put: %v", err)
	}
	if err := avatars.Put("one.jpg", []byte("two")); err != nil {
		t.Fatalf("avatars put: %v", err)
	}

	photoData, err := photos.Get("one.jpg")
	if err != nil {
		t.Fatalf("photos get: %v", err)
	}
	avatarData, err := avatars.Get("one.jpg")
	if err != nil {
		t.Fatalf("avatars get: %v", err)
	}
	if string(photoData) != "one" || string(avatarData) != "two" {
		t.Fatalf("got photo=%q avatar=%q", photoData, avatarData)
	}
}
