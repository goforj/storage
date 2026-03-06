package rclonestorage

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/unknwon/goconfig"
)

func TestMemoryStorage(t *testing.T) {
	data := strings.Join([]string{
		"[one]",
		"key = value",
		"[empty]",
		"",
	}, "\n")

	store, err := newMemoryStorage(data)
	if err != nil {
		t.Fatalf("newMemoryStorage: %v", err)
	}

	sections := store.GetSectionList()
	if !containsString(sections, "one") {
		t.Fatalf("expected section list to include one, got %v", sections)
	}

	if !store.HasSection("one") {
		t.Fatalf("expected section one to exist")
	}
	if store.HasSection("missing") {
		t.Fatalf("expected missing section to be absent")
	}

	keys := store.GetKeyList("one")
	if !containsString(keys, "key") {
		t.Fatalf("expected key list to include key, got %v", keys)
	}

	val, found := store.GetValue("one", "key")
	if !found || val != "value" {
		t.Fatalf("expected key value, got %q (found=%v)", val, found)
	}

	_, found = store.GetValue("one", "missing")
	if found {
		t.Fatalf("expected missing key to be absent")
	}

	store.SetValue("one", "new", "val")
	val, found = store.GetValue("one", "new")
	if !found || val != "val" {
		t.Fatalf("expected new value, got %q (found=%v)", val, found)
	}

	if ok := store.DeleteKey("one", "new"); !ok {
		t.Fatalf("expected DeleteKey to remove existing key")
	}
	if ok := store.DeleteKey("one", "new"); ok {
		t.Fatalf("expected DeleteKey to report missing key")
	}

	store.DeleteSection("empty")
	if store.HasSection("empty") {
		t.Fatalf("expected empty section to be deleted")
	}

	if err := store.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := store.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	serialized, err := store.Serialize()
	if err != nil {
		t.Fatalf("Serialize: %v", err)
	}
	if !strings.Contains(serialized, "[one]") {
		t.Fatalf("Serialize output missing section: %q", serialized)
	}
}

func TestMemoryStorageInvalidData(t *testing.T) {
	if _, err := newMemoryStorage("bad line"); err == nil {
		t.Fatalf("expected error for invalid config data")
	}
}

func TestMemoryStorageSerializeError(t *testing.T) {
	store, err := newMemoryStorage("[one]\nkey = value\n")
	if err != nil {
		t.Fatalf("newMemoryStorage: %v", err)
	}

	orig := saveConfigData
	saveConfigData = func(_ *goconfig.ConfigFile, _ io.Writer) error {
		return errors.New("write failure")
	}
	t.Cleanup(func() {
		saveConfigData = orig
	})

	if _, err := store.Serialize(); err == nil {
		t.Fatalf("expected Serialize to return error")
	}
}

func containsString(values []string, target string) bool {
	for _, v := range values {
		if v == target {
			return true
		}
	}
	return false
}
