package redisstorage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/goforj/storage/storagecore"
)

// TestRedisPublicOperations exercises the complete storage surface against an
// in-process Redis server so default tests cover behavior formerly limited to
// integration-tagged container tests.
func TestRedisPublicOperations(t *testing.T) {
	d := newMiniRedisDriver(t, "sandbox")

	if err := d.MakeDir("empty"); err != nil {
		t.Fatalf("MakeDir: %v", err)
	}
	if err := d.Put("docs/readme.txt", []byte("hello")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := d.Get("docs/readme.txt")
	if err != nil || string(got) != "hello" {
		t.Fatalf("Get = %q, %v", got, err)
	}
	if _, err := d.Get(""); !errors.Is(err, storagecore.ErrForbidden) {
		t.Fatalf("Get root error = %v", err)
	}

	entry, err := d.Stat("docs/readme.txt")
	if err != nil || entry.IsDir || entry.Size != 5 {
		t.Fatalf("Stat file = %+v, %v", entry, err)
	}
	entry, err = d.Stat("docs")
	if err != nil || !entry.IsDir {
		t.Fatalf("Stat directory = %+v, %v", entry, err)
	}
	entry, err = d.Stat("")
	if err != nil || !entry.IsDir || entry.Path != "" {
		t.Fatalf("Stat root = %+v, %v", entry, err)
	}
	if _, err := d.Stat("missing"); !errors.Is(err, storagecore.ErrNotFound) {
		t.Fatalf("Stat missing error = %v", err)
	}
	if exists, err := d.Exists("docs/readme.txt"); err != nil || !exists {
		t.Fatalf("Exists file = %v, %v", exists, err)
	}
	if exists, err := d.Exists(""); err != nil || exists {
		t.Fatalf("Exists root = %v, %v", exists, err)
	}

	entries, err := d.List("")
	if err != nil || len(entries) != 2 {
		t.Fatalf("List root = %+v, %v", entries, err)
	}
	page, err := d.ListPage("", 0, 1)
	if err != nil || len(page.Entries) != 1 || !page.HasMore {
		t.Fatalf("ListPage = %+v, %v", page, err)
	}
	if _, err := d.List("docs/readme.txt"); !errors.Is(err, storagecore.ErrNotFound) {
		t.Fatalf("List file error = %v", err)
	}
	if _, err := d.List("missing"); !errors.Is(err, storagecore.ErrNotFound) {
		t.Fatalf("List missing error = %v", err)
	}

	var walked []string
	if err := d.Walk("docs", func(entry storagecore.Entry) error {
		walked = append(walked, entry.Path)
		return nil
	}); err != nil || !slices.Equal(walked, []string{"docs/readme.txt"}) {
		t.Fatalf("Walk = %v, %v", walked, err)
	}
	callbackErr := errors.New("stop")
	if err := d.Walk("", func(storagecore.Entry) error { return callbackErr }); !errors.Is(err, callbackErr) {
		t.Fatalf("Walk callback error = %v", err)
	}
	if err := d.Walk("", nil); !errors.Is(err, storagecore.ErrForbidden) {
		t.Fatalf("Walk nil callback error = %v", err)
	}
	if err := d.Walk("missing", func(storagecore.Entry) error { return nil }); !errors.Is(err, storagecore.ErrNotFound) {
		t.Fatalf("Walk missing error = %v", err)
	}

	if err := d.Copy("docs/readme.txt", "copies/readme.txt"); err != nil {
		t.Fatalf("Copy: %v", err)
	}
	if err := d.Copy("missing", "copies/missing"); !errors.Is(err, storagecore.ErrNotFound) {
		t.Fatalf("Copy missing error = %v", err)
	}
	if err := d.Move("copies/readme.txt", "archive/readme.txt"); err != nil {
		t.Fatalf("Move file: %v", err)
	}
	if err := d.Move("docs", "manual"); err != nil {
		t.Fatalf("Move directory: %v", err)
	}
	if _, err := d.URL("manual/readme.txt"); !errors.Is(err, storagecore.ErrUnsupported) {
		t.Fatalf("URL error = %v", err)
	}
	if modTime, err := d.ModTime(context.Background(), "manual/readme.txt"); err != nil || modTime.IsZero() {
		t.Fatalf("ModTime = %v, %v", modTime, err)
	}
	if _, err := d.ModTime(context.Background(), "missing"); !errors.Is(err, storagecore.ErrNotFound) {
		t.Fatalf("ModTime missing error = %v", err)
	}

	if err := d.Delete("manual/readme.txt"); err != nil {
		t.Fatalf("Delete file: %v", err)
	}
	if err := d.Delete("manual"); err != nil {
		t.Fatalf("Delete directory: %v", err)
	}
	if err := d.Delete("missing"); !errors.Is(err, storagecore.ErrNotFound) {
		t.Fatalf("Delete missing error = %v", err)
	}
}

// TestRedisBackendFailures verifies transport failures remain observable from
// every operation instead of being mistaken for absence or empty results.
func TestRedisBackendFailures(t *testing.T) {
	server := miniredis.RunT(t)
	d := newMiniRedisDriverForServer(t, server, "sandbox")
	server.Close()

	calls := []func() error{
		func() error { _, err := d.Get("file"); return err },
		func() error { return d.Put("file", []byte("data")) },
		func() error { return d.MakeDir("dir") },
		func() error { return d.Delete("file") },
		func() error { _, err := d.Stat("file"); return err },
		func() error { _, err := d.Exists("file"); return err },
		func() error { _, err := d.List(""); return err },
		func() error { _, err := d.ListPage("", 0, 1); return err },
		func() error { return d.Walk("", func(storagecore.Entry) error { return nil }) },
		func() error { return d.Copy("file", "copy") },
		func() error { return d.Move("file", "moved") },
		func() error { _, err := d.URL("file"); return err },
		func() error { _, err := d.ModTime(context.Background(), "file"); return err },
	}
	for index, call := range calls {
		if err := call(); err == nil {
			t.Fatalf("backend failure call %d returned nil error", index)
		}
	}
}

// TestRedisMalformedRecords covers defensive parsing of persisted metadata so
// corrupted Redis values produce errors rather than fabricated entries.
func TestRedisMalformedRecords(t *testing.T) {
	d := newMiniRedisDriver(t, "")
	ctx := context.Background()

	if err := d.client.HSet(ctx, d.objectKey("bad-time"), "data", "ok", "modtime", "not-a-number").Err(); err != nil {
		t.Fatalf("seed bad modtime: %v", err)
	}
	if _, err := d.ModTime(ctx, "bad-time"); err == nil {
		t.Fatal("ModTime malformed value returned nil error")
	}
	if _, err := d.ModTime(ctx, ""); !errors.Is(err, storagecore.ErrForbidden) {
		t.Fatalf("ModTime root error = %v", err)
	}
}

// TestRedisInternalFailureEdges verifies corrupted Redis key types and
// adversarial recursion state fail closed throughout index validation.
func TestRedisInternalFailureEdges(t *testing.T) {
	d := newMiniRedisDriver(t, "")
	ctx := context.Background()
	wrongType := func(key string) {
		t.Helper()
		if err := d.client.Set(ctx, key, "not-a-set-or-hash", 0).Err(); err != nil {
			t.Fatalf("seed wrong type %q: %v", key, err)
		}
	}

	wrongType(d.objectKey("bad-object"))
	if _, err := d.objectSize(ctx, "bad-object"); err == nil {
		t.Fatal("objectSize wrong type returned nil error")
	}
	wrongType(d.dirChildrenKey("bad-children"))
	if _, err := d.children(ctx, "bad-children"); err == nil {
		t.Fatal("children wrong type returned nil error")
	}
	wrongType(d.dirObjectsKey("bad-descendants"))
	if _, err := d.descendants(ctx, "bad-descendants"); err == nil {
		t.Fatal("descendants wrong type returned nil error")
	}
	wrongType(d.dirObjectsKey("bad-legacy-marker"))
	if _, err := d.legacyDirectoryMarkerValid(ctx, "bad-legacy-marker"); err == nil {
		t.Fatal("legacy marker wrong type returned nil error")
	}
	wrongType(d.versionedDirObjectsKey("bad-versioned-marker"))
	if _, err := d.versionedDirectoryMarkerValid(ctx, "bad-versioned-marker"); err == nil {
		t.Fatal("versioned marker wrong type returned nil error")
	}

	if _, err := d.listEntries(ctx, []string{encodeFileChild("bad-object")}); err == nil {
		t.Fatal("listEntries wrong object type returned nil error")
	}
	if _, _, err := d.walkEntries(ctx, []indexedMemberRecord{{member: objectMember("bad-object"), schema: versionedIndex}}, ""); err == nil {
		t.Fatal("walkEntries wrong object type returned nil error")
	}
	invalidRecord := indexedMemberRecord{member: "storage:v1:object:%", schema: versionedIndex}
	if _, err := d.resolveIndexedMember(ctx, invalidRecord); err == nil {
		t.Fatal("resolveIndexedMember invalid value returned nil error")
	}
	if _, _, err := d.directoryStateFromRecords(ctx, d.client, "", []indexedMemberRecord{invalidRecord}); err == nil {
		t.Fatal("directoryStateFromRecords invalid value returned nil error")
	}

	if exists, err := d.dirExistsSeen(ctx, "cycle", map[string]struct{}{"cycle": {}}); err != nil || exists {
		t.Fatalf("dirExistsSeen cycle = %v, %v", exists, err)
	}
	deep := make(map[string]struct{}, 1024)
	for index := 0; index < 1024; index++ {
		deep[fmt.Sprintf("dir-%d", index)] = struct{}{}
	}
	if exists, err := d.dirExistsSeen(ctx, "overflow", deep); err != nil || exists {
		t.Fatalf("dirExistsSeen depth = %v, %v", exists, err)
	}
	if _, _, err := d.directoryStateInTransactionSeen(ctx, nil, "cycle", map[string]struct{}{"cycle": {}}); !errors.Is(err, storagecore.ErrForbidden) {
		t.Fatalf("transaction cycle error = %v", err)
	}
	if _, _, err := d.directoryStateInTransactionSeen(ctx, nil, "overflow", deep); !errors.Is(err, storagecore.ErrForbidden) {
		t.Fatalf("transaction depth error = %v", err)
	}
}

// TestRedisTransactionalIndexHelpers exercises stable watched snapshots and
// stale-member filtering against the same Redis transaction used by deletion.
func TestRedisTransactionalIndexHelpers(t *testing.T) {
	d := newMiniRedisDriver(t, "")
	ctx := context.Background()
	if err := d.MakeDir("empty"); err != nil {
		t.Fatalf("MakeDir empty: %v", err)
	}
	if err := d.Put("parent/file", []byte("data")); err != nil {
		t.Fatalf("Put nested file: %v", err)
	}

	err := d.client.Watch(ctx, func(tx *redis.Tx) error {
		children, err := d.watchedChildMembers(ctx, tx, "")
		if err != nil || len(children) != 2 {
			t.Fatalf("watchedChildMembers = %v, %v", children, err)
		}
		records, err := d.watchedDescendants(ctx, tx, "")
		if err != nil || len(records) != 2 {
			t.Fatalf("watchedDescendants = %v, %v", records, err)
		}
		exists, contents, err := d.directoryRecordsStateInTransaction(ctx, tx, "parent")
		if err != nil || !exists || !contents {
			t.Fatalf("directoryRecordsStateInTransaction parent = %v, %v, %v", exists, contents, err)
		}
		exists, contents, err = d.directoryStateInTransaction(ctx, tx, "empty")
		if err != nil || !exists || contents {
			t.Fatalf("directoryStateInTransaction empty = %v, %v, %v", exists, contents, err)
		}
		return nil
	}, d.dirChildrenKey(""), d.versionedDirChildrenKey(""))
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	entries, err := d.listEntries(ctx, []string{encodeDirChild("ghost"), encodeFileChild("missing")})
	if err != nil || len(entries) != 0 {
		t.Fatalf("listEntries stale members = %+v, %v", entries, err)
	}
	records := []indexedMemberRecord{
		{member: objectMember("outside/file"), schema: versionedIndex},
		{member: objectMember("parent/missing"), schema: versionedIndex},
		{member: dirMarkerMember("parent"), schema: versionedIndex},
	}
	entries, found, err := d.walkEntries(ctx, records, "parent")
	if err != nil || !found || len(entries) != 0 {
		t.Fatalf("walkEntries filtered records = %+v, %v, %v", entries, found, err)
	}

	seedLegacyObject(t, d, "duplicate", "data")
	if err := d.client.SAdd(ctx, d.versionedDirObjectsKey(""), "duplicate").Err(); err != nil {
		t.Fatalf("seed duplicate schema member: %v", err)
	}
	if records, err := d.descendants(ctx, ""); err != nil || len(records) < 2 {
		t.Fatalf("descendants duplicate records = %v, %v", records, err)
	}

	primary := errors.New("primary")
	cleanup := errors.New("cleanup")
	if err := joinCleanup(primary, nil); !errors.Is(err, primary) {
		t.Fatalf("joinCleanup primary = %v", err)
	}
	if err := joinCleanup(nil, cleanup); !errors.Is(err, cleanup) {
		t.Fatalf("joinCleanup cleanup = %v", err)
	}
	if err := joinCleanup(primary, cleanup); !errors.Is(err, primary) || !errors.Is(err, cleanup) {
		t.Fatalf("joinCleanup combined = %v", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := d.watchTransaction(canceled, nil, func(*redis.Tx) error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("watchTransaction cancellation = %v", err)
	}
}

// TestRedisPublicCorruptTypes verifies every public operation propagates Redis
// WRONGTYPE errors from the particular structure it owns.
func TestRedisPublicCorruptTypes(t *testing.T) {
	tests := []struct {
		name string
		seed func(context.Context, *driver) error
		call func(*driver) error
	}{
		{name: "get", seed: func(ctx context.Context, d *driver) error {
			return d.client.Set(ctx, d.objectKey("file"), "bad", 0).Err()
		}, call: func(d *driver) error { _, err := d.Get("file"); return err }},
		{name: "put", seed: func(ctx context.Context, d *driver) error {
			return d.client.Set(ctx, d.objectKey("file"), "bad", 0).Err()
		}, call: func(d *driver) error { return d.Put("file", nil) }},
		{name: "mkdir", seed: func(ctx context.Context, d *driver) error {
			return d.client.Set(ctx, d.objectKey("dir"), "bad", 0).Err()
		}, call: func(d *driver) error { return d.MakeDir("dir") }},
		{name: "delete", seed: func(ctx context.Context, d *driver) error {
			return d.client.Set(ctx, d.objectKey("file"), "bad", 0).Err()
		}, call: func(d *driver) error { return d.Delete("file") }},
		{name: "stat", seed: func(ctx context.Context, d *driver) error {
			return d.client.Set(ctx, d.objectKey("file"), "bad", 0).Err()
		}, call: func(d *driver) error { _, err := d.Stat("file"); return err }},
		{name: "exists", seed: func(ctx context.Context, d *driver) error {
			return d.client.Set(ctx, d.objectKey("file"), "bad", 0).Err()
		}, call: func(d *driver) error { _, err := d.Exists("file"); return err }},
		{name: "list", seed: func(ctx context.Context, d *driver) error {
			return d.client.Set(ctx, d.dirChildrenKey(""), "bad", 0).Err()
		}, call: func(d *driver) error { _, err := d.List(""); return err }},
		{name: "walk", seed: func(ctx context.Context, d *driver) error {
			return d.client.Set(ctx, d.dirObjectsKey(""), "bad", 0).Err()
		}, call: func(d *driver) error { return d.Walk("", func(storagecore.Entry) error { return nil }) }},
		{name: "copy", seed: func(ctx context.Context, d *driver) error {
			return d.client.Set(ctx, d.objectKey("source"), "bad", 0).Err()
		}, call: func(d *driver) error { return d.Copy("source", "copy") }},
		{name: "move", seed: func(ctx context.Context, d *driver) error {
			return d.client.Set(ctx, d.objectKey("source"), "bad", 0).Err()
		}, call: func(d *driver) error { return d.Move("source", "moved") }},
		{name: "url", seed: func(ctx context.Context, d *driver) error {
			return d.client.Set(ctx, d.objectKey("file"), "bad", 0).Err()
		}, call: func(d *driver) error { _, err := d.URL("file"); return err }},
		{name: "modtime", seed: func(ctx context.Context, d *driver) error {
			return d.client.Set(ctx, d.objectKey("file"), "bad", 0).Err()
		}, call: func(d *driver) error { _, err := d.ModTime(context.Background(), "file"); return err }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			d := newMiniRedisDriver(t, "")
			if err := test.seed(context.Background(), d); err != nil {
				t.Fatalf("seed: %v", err)
			}
			if err := test.call(d); err == nil {
				t.Fatal("operation returned nil error")
			}
		})
	}
}

// TestRedisIndexedMembersDoNotCollide verifies that directory metadata cannot
// hide a valid object whose name matches the legacy marker namespace.
func TestRedisIndexedMembersDoNotCollide(t *testing.T) {
	d := newMiniRedisDriver(t, "")

	if err := d.MakeDir("x"); err != nil {
		t.Fatalf("MakeDir: %v", err)
	}
	if err := d.Put("dirmarker:x", []byte("payload")); err != nil {
		t.Fatalf("Put adversarial object: %v", err)
	}

	var got []storagecore.Entry
	if err := d.Walk("", func(entry storagecore.Entry) error {
		got = append(got, entry)
		return nil
	}); err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(got) != 2 || got[0].Path != "dirmarker:x" || got[0].IsDir || got[1].Path != "x" || !got[1].IsDir {
		t.Fatalf("Walk entries = %+v", got)
	}

	if err := d.Delete("dirmarker:x"); err != nil {
		t.Fatalf("Delete adversarial object: %v", err)
	}
	entry, err := d.Stat("x")
	if err != nil || !entry.IsDir {
		t.Fatalf("Stat directory after object delete = %+v err=%v", entry, err)
	}
}

// TestRedisReservedObjectNamesCannotRemoveOtherIndexes protects typed members from legacy cleanup collisions.
func TestRedisReservedObjectNamesCannotRemoveOtherIndexes(t *testing.T) {
	d := newMiniRedisDriver(t, "")
	for _, tc := range []struct {
		name        string
		victim      string
		adversary   string
		victimIsDir bool
	}{
		{name: "object member", victim: "victim-object", adversary: objectMember("victim-object")},
		{name: "directory member", victim: "victim-directory", adversary: dirMarkerMember("victim-directory"), victimIsDir: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.victimIsDir {
				if err := d.MakeDir(tc.victim); err != nil {
					t.Fatalf("MakeDir victim: %v", err)
				}
			} else if err := d.Put(tc.victim, []byte("victim")); err != nil {
				t.Fatalf("Put victim: %v", err)
			}
			if err := d.Put(tc.adversary, []byte("adversary")); err != nil {
				t.Fatalf("Put adversary: %v", err)
			}
			assertRedisEntryExists(t, d, tc.victim, tc.victimIsDir)
			assertRedisEntryExists(t, d, tc.adversary, false)
			if err := d.Delete(tc.adversary); err != nil {
				t.Fatalf("Delete adversary: %v", err)
			}
			assertRedisEntryExists(t, d, tc.victim, tc.victimIsDir)
		})
	}
}

// TestRedisNewObjectSurvivesLegacyDirectoryMarkerRemoval verifies new typed objects are independent of old marker strings.
func TestRedisNewObjectSurvivesLegacyDirectoryMarkerRemoval(t *testing.T) {
	d := newMiniRedisDriver(t, "")
	if err := d.MakeDir("x"); err != nil {
		t.Fatalf("MakeDir: %v", err)
	}
	if err := d.Put("dirmarker:x", []byte("payload")); err != nil {
		t.Fatalf("Put marker-shaped object: %v", err)
	}
	if err := d.Delete("x"); err != nil {
		t.Fatalf("Delete directory: %v", err)
	}
	assertRedisEntryExists(t, d, "dirmarker:x", false)
}

// TestRedisPreUpgradeReservedNamesRemainDistinct verifies one legacy SET member can safely represent both interpretations.
func TestRedisPreUpgradeReservedNamesRemainDistinct(t *testing.T) {
	t.Run("typed object member and raw object", func(t *testing.T) {
		d := newMiniRedisDriver(t, "")
		victim := "victim"
		rawName := objectMember(victim)
		seedLegacyObject(t, d, rawName, "raw")
		seedLegacyTypedObject(t, d, victim, "typed")
		assertRedisEntryExists(t, d, rawName, false)
		assertRedisEntryExists(t, d, victim, false)
		if err := d.Delete(rawName); err != nil {
			t.Fatalf("Delete raw reserved name: %v", err)
		}
		assertRedisEntryExists(t, d, victim, false)
	})

	for _, tc := range []struct {
		name      string
		directory string
		rawName   func(string) string
		marker    func(string) string
	}{
		{name: "typed directory member and raw object", directory: "typed-dir", rawName: dirMarkerMember, marker: dirMarkerMember},
		{name: "legacy directory member and raw object", directory: "legacy-dir", rawName: legacyDirMarkerMember, marker: legacyDirMarkerMember},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newMiniRedisDriver(t, "")
			rawName := tc.rawName(tc.directory)
			seedLegacyObject(t, d, rawName, "raw")
			seedLegacyDirectory(t, d, tc.directory, tc.marker(tc.directory))
			assertRedisEntryExists(t, d, rawName, false)
			assertRedisEntryExists(t, d, tc.directory, true)
			if err := d.Delete(tc.directory); err != nil {
				t.Fatalf("Delete colliding directory: %v", err)
			}
			assertRedisEntryExists(t, d, rawName, false)
			if _, err := d.Stat(tc.directory); !errors.Is(err, storagecore.ErrNotFound) {
				t.Fatalf("Stat deleted directory error = %v", err)
			}
		})
	}
}

// TestRedisReleasedClientCanRecreateDeletedDirectory proves exact self and parent links supersede stale ancestors.
func TestRedisReleasedClientCanRecreateDeletedDirectory(t *testing.T) {
	d := newMiniRedisDriver(t, "tenant")
	key, err := d.key("recreated")
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	if err := makeDirWithReleasedSchema(context.Background(), d, key); err != nil {
		t.Fatalf("released MakeDir before delete: %v", err)
	}
	if err := d.Delete("recreated"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := d.Stat("recreated"); !errors.Is(err, storagecore.ErrNotFound) {
		t.Fatalf("Stat deleted directory error = %v", err)
	}
	if err := makeDirWithReleasedSchema(context.Background(), d, key); err != nil {
		t.Fatalf("released MakeDir after delete: %v", err)
	}
	entry, err := d.Stat("recreated")
	if err != nil || !entry.IsDir {
		t.Fatalf("Stat recreated directory = %+v err=%v", entry, err)
	}
	entries, err := d.List("")
	if err != nil || len(entries) != 1 || entries[0].Path != "recreated" || !entries[0].IsDir {
		t.Fatalf("List recreated directory = %+v err=%v", entries, err)
	}
}

// TestRedisFiltersPartialLegacyDelete verifies stale indexes stay invisible without unsafe read-time mutation.
func TestRedisFiltersPartialLegacyDelete(t *testing.T) {
	d := newMiniRedisDriver(t, "")
	ctx := context.Background()
	key := "ghost/leaf.txt"
	seedLegacyObject(t, d, key, "payload")
	setKeys := []string{d.dirObjectsKey(""), d.dirObjectsKey("ghost"), d.dirChildrenKey(""), d.dirChildrenKey("ghost")}
	counts := make(map[string]int64, len(setKeys))
	for _, setKey := range setKeys {
		count, err := d.client.SCard(ctx, setKey).Result()
		if err != nil {
			t.Fatalf("count seeded set %q: %v", setKey, err)
		}
		counts[setKey] = count
	}
	if err := d.client.Del(ctx, d.objectKey(key)).Err(); err != nil {
		t.Fatalf("simulate partial delete: %v", err)
	}
	var entries []storagecore.Entry
	if err := d.Walk("", func(entry storagecore.Entry) error {
		entries = append(entries, entry)
		return nil
	}); err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("Walk emitted stale entries: %+v", entries)
	}
	if _, err := d.Stat("ghost"); !errors.Is(err, storagecore.ErrNotFound) {
		t.Fatalf("Stat ghost parent error = %v", err)
	}
	if err := d.Delete("ghost"); !errors.Is(err, storagecore.ErrNotFound) {
		t.Fatalf("Delete ghost parent error = %v", err)
	}
	for _, setKey := range setKeys {
		if count, err := d.client.SCard(ctx, setKey).Result(); err != nil || count != counts[setKey] {
			t.Fatalf("stale set %q count=%d err=%v want %d", setKey, count, err, counts[setKey])
		}
	}
}

// TestRedisStaleChildObservationCannotRemoveConcurrentPut pauses after stale validation to cover the former repair race.
func TestRedisStaleChildObservationCannotRemoveConcurrentPut(t *testing.T) {
	server := miniredis.RunT(t)
	reader := newMiniRedisDriverForServer(t, server, "tenant")
	writer := newMiniRedisDriverForServer(t, server, "tenant")
	ctx := context.Background()
	key, err := reader.key("fresh.txt")
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	child := encodeFileChild(key)
	childrenKey := reader.versionedDirChildrenKey(reader.prefix)
	if err := reader.client.SAdd(ctx, childrenKey, child).Err(); err != nil {
		t.Fatalf("seed stale child: %v", err)
	}
	evaluated, resume := blockNextObjectSize(reader)
	result := make(chan error, 1)
	go func() {
		_, err := reader.List("")
		result <- err
	}()
	waitForObjectSizeEvaluation(t, evaluated)
	if err := writer.Put("fresh.txt", []byte("payload")); err != nil {
		t.Fatalf("concurrent Put: %v", err)
	}
	close(resume)
	if err := <-result; err != nil {
		t.Fatalf("List with stale observation: %v", err)
	}
	if member, err := reader.client.SIsMember(ctx, childrenKey, child).Result(); err != nil || !member {
		t.Fatalf("fresh child link retained = %v err=%v", member, err)
	}
	entries, err := reader.List("")
	if err != nil || len(entries) != 1 || entries[0].Path != "fresh.txt" {
		t.Fatalf("List after concurrent Put = %+v err=%v", entries, err)
	}
}

// TestRedisStaleDescendantObservationCannotRemoveConcurrentPut covers the corresponding Walk index race.
func TestRedisStaleDescendantObservationCannotRemoveConcurrentPut(t *testing.T) {
	server := miniredis.RunT(t)
	reader := newMiniRedisDriverForServer(t, server, "tenant")
	writer := newMiniRedisDriverForServer(t, server, "tenant")
	ctx := context.Background()
	key, err := reader.key("fresh.txt")
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	member := objectMember(key)
	objectsKey := reader.versionedDirObjectsKey(reader.prefix)
	if err := reader.client.SAdd(ctx, objectsKey, member).Err(); err != nil {
		t.Fatalf("seed stale descendant: %v", err)
	}
	evaluated, resume := blockNextObjectSize(reader)
	result := make(chan error, 1)
	go func() {
		result <- reader.Walk("", func(storagecore.Entry) error { return nil })
	}()
	waitForObjectSizeEvaluation(t, evaluated)
	if err := writer.Put("fresh.txt", []byte("payload")); err != nil {
		t.Fatalf("concurrent Put: %v", err)
	}
	close(resume)
	if err := <-result; err != nil {
		t.Fatalf("Walk with stale observation: %v", err)
	}
	if retained, err := reader.client.SIsMember(ctx, objectsKey, member).Result(); err != nil || !retained {
		t.Fatalf("fresh descendant link retained = %v err=%v", retained, err)
	}
	var entries []storagecore.Entry
	if err := reader.Walk("", func(entry storagecore.Entry) error {
		entries = append(entries, entry)
		return nil
	}); err != nil || len(entries) != 1 || entries[0].Path != "fresh.txt" {
		t.Fatalf("Walk after concurrent Put = %+v err=%v", entries, err)
	}
}

// TestRedisDirectoryDeleteRetriesForReleasedWriter covers legacy no-op SADD writes during empty checks.
func TestRedisDirectoryDeleteRetriesForReleasedWriter(t *testing.T) {
	server := miniredis.RunT(t)
	deleter := newMiniRedisDriverForServer(t, server, "tenant")
	writer := newMiniRedisDriverForServer(t, server, "tenant")
	if err := deleter.MakeDir("parent"); err != nil {
		t.Fatalf("MakeDir parent: %v", err)
	}
	childKey, err := deleter.key("parent/child.txt")
	if err != nil {
		t.Fatalf("child key: %v", err)
	}
	seedLegacyObjectIndexesWithoutHash(t, deleter, childKey)
	evaluated, resume := blockObjectSizeCall(deleter, 2)
	result := make(chan error, 1)
	go func() {
		result <- deleter.Delete("parent")
	}()
	waitForObjectSizeEvaluation(t, evaluated)
	if err := putWithReleasedSchema(context.Background(), writer, childKey, []byte("payload")); err != nil {
		t.Fatalf("released concurrent child Put: %v", err)
	}
	close(resume)
	if err := <-result; !errors.Is(err, storagecore.ErrForbidden) {
		t.Fatalf("Delete after concurrent child Put error = %v", err)
	}
	entries, err := deleter.List("parent")
	if err != nil || len(entries) != 1 || entries[0].Path != "parent/child.txt" {
		t.Fatalf("List preserved parent = %+v err=%v", entries, err)
	}
}

// TestRedisObjectPutRetriesForReleasedWriter prevents a mixed-version parent and child object collision.
func TestRedisObjectPutRetriesForReleasedWriter(t *testing.T) {
	server := miniredis.RunT(t)
	parentWriter := newMiniRedisDriverForServer(t, server, "tenant")
	childWriter := newMiniRedisDriverForServer(t, server, "tenant")
	childKey, err := parentWriter.key("parent/child.txt")
	if err != nil {
		t.Fatalf("child key: %v", err)
	}
	seedLegacyObjectIndexesWithoutHash(t, parentWriter, childKey)
	evaluated, resume := blockObjectSizeCall(parentWriter, 2)
	result := make(chan error, 1)
	go func() {
		result <- parentWriter.Put("parent", []byte("parent"))
	}()
	waitForObjectSizeEvaluation(t, evaluated)
	if err := putWithReleasedSchema(context.Background(), childWriter, childKey, []byte("child")); err != nil {
		t.Fatalf("released concurrent child Put: %v", err)
	}
	close(resume)
	if err := <-result; !errors.Is(err, storagecore.ErrForbidden) {
		t.Fatalf("parent Put after concurrent child error = %v", err)
	}
	if data, err := parentWriter.Get("parent/child.txt"); err != nil || string(data) != "child" {
		t.Fatalf("Get preserved child = %q err=%v", data, err)
	}
	entry, err := parentWriter.Stat("parent")
	if err != nil || !entry.IsDir {
		t.Fatalf("Stat implicit parent = %+v err=%v", entry, err)
	}
}

// TestRedisDeleteRejectsChildLinkWithBackingObject protects partial legacy indexes that List can still verify.
func TestRedisDeleteRejectsChildLinkWithBackingObject(t *testing.T) {
	d := newMiniRedisDriver(t, "tenant")
	ctx := context.Background()
	parentKey, err := d.key("parent")
	if err != nil {
		t.Fatalf("parent key: %v", err)
	}
	childKey, err := d.key("parent/child.txt")
	if err != nil {
		t.Fatalf("child key: %v", err)
	}
	if err := d.client.HSet(ctx, d.objectKey(childKey), map[string]any{"data": "payload", "modtime": "0"}).Err(); err != nil {
		t.Fatalf("seed child hash: %v", err)
	}
	if err := d.client.SAdd(ctx, d.dirChildrenKey(parentKey), encodeFileChild(childKey)).Err(); err != nil {
		t.Fatalf("seed child-only link: %v", err)
	}
	entry, err := d.Stat("parent")
	if err != nil || !entry.IsDir {
		t.Fatalf("Stat child-only parent = %+v err=%v", entry, err)
	}
	if err := d.Delete("parent"); !errors.Is(err, storagecore.ErrForbidden) {
		t.Fatalf("Delete child-only parent error = %v", err)
	}
	entries, err := d.List("parent")
	if err != nil || len(entries) != 1 || entries[0].Path != "parent/child.txt" {
		t.Fatalf("List child-only parent = %+v err=%v", entries, err)
	}
}

// TestRedisDeleteRejectsChildLinkWithDirectoryMarker keeps a visible child directory from being orphaned.
func TestRedisDeleteRejectsChildLinkWithDirectoryMarker(t *testing.T) {
	d := newMiniRedisDriver(t, "tenant")
	if err := d.MakeDir("parent"); err != nil {
		t.Fatalf("MakeDir parent: %v", err)
	}
	ctx := context.Background()
	parentKey, err := d.key("parent")
	if err != nil {
		t.Fatalf("parent key: %v", err)
	}
	childKey, err := d.key("parent/child")
	if err != nil {
		t.Fatalf("child key: %v", err)
	}
	marker := dirMarkerMember(childKey)
	if err := d.client.SAdd(ctx, d.versionedDirObjectsKey(childKey), marker).Err(); err != nil {
		t.Fatalf("seed child self marker: %v", err)
	}
	if err := d.client.SAdd(ctx, d.versionedDirChildrenKey(parentKey), encodeDirChild(childKey)).Err(); err != nil {
		t.Fatalf("seed child parent link: %v", err)
	}
	entries, err := d.List("parent")
	if err != nil || len(entries) != 1 || entries[0].Path != "parent/child" || !entries[0].IsDir {
		t.Fatalf("List marker-only child = %+v err=%v", entries, err)
	}
	if err := d.Delete("parent"); !errors.Is(err, storagecore.ErrForbidden) {
		t.Fatalf("Delete parent with marker-only child error = %v", err)
	}
}

// TestRedisDeleteRejectsRecursivelyVisibleChild mirrors List visibility through a partial directory chain.
func TestRedisDeleteRejectsRecursivelyVisibleChild(t *testing.T) {
	d := newMiniRedisDriver(t, "tenant")
	if err := d.MakeDir("parent"); err != nil {
		t.Fatalf("MakeDir parent: %v", err)
	}
	ctx := context.Background()
	parentKey, err := d.key("parent")
	if err != nil {
		t.Fatalf("parent key: %v", err)
	}
	childKey, err := d.key("parent/child")
	if err != nil {
		t.Fatalf("child key: %v", err)
	}
	grandchildKey, err := d.key("parent/child/grand.txt")
	if err != nil {
		t.Fatalf("grandchild key: %v", err)
	}
	if err := d.client.SAdd(ctx, d.versionedDirChildrenKey(parentKey), encodeDirChild(childKey)).Err(); err != nil {
		t.Fatalf("seed child directory link: %v", err)
	}
	if err := d.client.SAdd(ctx, d.versionedDirChildrenKey(childKey), encodeFileChild(grandchildKey)).Err(); err != nil {
		t.Fatalf("seed grandchild link: %v", err)
	}
	if err := d.client.HSet(ctx, d.objectKey(grandchildKey), map[string]any{"data": "payload", "modtime": "0"}).Err(); err != nil {
		t.Fatalf("seed grandchild hash: %v", err)
	}
	entries, err := d.List("parent")
	if err != nil || len(entries) != 1 || entries[0].Path != "parent/child" || !entries[0].IsDir {
		t.Fatalf("List recursively visible child = %+v err=%v", entries, err)
	}
	if err := d.Delete("parent"); !errors.Is(err, storagecore.ErrForbidden) {
		t.Fatalf("Delete parent with recursively visible child error = %v", err)
	}
}

// TestRedisPublicVisibilityRejectsRootSelfCycle filters a corrupt child link instead of recursing forever.
func TestRedisPublicVisibilityRejectsRootSelfCycle(t *testing.T) {
	d := newMiniRedisDriver(t, "")
	if err := d.client.SAdd(context.Background(), d.versionedDirChildrenKey(""), encodeDirChild("")).Err(); err != nil {
		t.Fatalf("seed root self-cycle: %v", err)
	}
	entries, err := d.List("")
	if err != nil || len(entries) != 0 {
		t.Fatalf("List corrupt root = %+v err=%v", entries, err)
	}
}

// TestRedisPublicVisibilityBoundsChildDepth filters adversarial chains before exhausting the Go stack.
func TestRedisPublicVisibilityBoundsChildDepth(t *testing.T) {
	d := newMiniRedisDriver(t, "")
	ctx := context.Background()
	pipe := d.client.TxPipeline()
	parent := ""
	for i := 0; i < 1025; i++ {
		child := storagecore.JoinPrefix(parent, fmt.Sprintf("d%d", i))
		pipe.SAdd(ctx, d.versionedDirChildrenKey(parent), encodeDirChild(child))
		parent = child
	}
	leaf := parent + "/leaf.txt"
	pipe.SAdd(ctx, d.versionedDirChildrenKey(parent), encodeFileChild(leaf))
	pipe.HSet(ctx, d.objectKey(leaf), map[string]any{"data": "payload", "modtime": "0"})
	if _, err := pipe.Exec(ctx); err != nil {
		t.Fatalf("seed deep child chain: %v", err)
	}
	entries, err := d.List("")
	if err != nil || len(entries) != 0 {
		t.Fatalf("List deep child chain = %+v err=%v", entries, err)
	}
}

// TestRedisSiblingDeletePreservesChildOnlyLink ensures pruning respects verified child-set state.
func TestRedisSiblingDeletePreservesChildOnlyLink(t *testing.T) {
	for _, tc := range []struct {
		name  string
		path  string
		setup func(*driver) error
	}{
		{name: "object", path: "a/sibling.txt", setup: func(d *driver) error {
			return d.Put("a/sibling.txt", []byte("sibling"))
		}},
		{name: "directory", path: "a/sibling", setup: func(d *driver) error {
			return d.MakeDir("a/sibling")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newMiniRedisDriver(t, "tenant")
			ctx := context.Background()
			parentKey, err := d.key("a")
			if err != nil {
				t.Fatalf("parent key: %v", err)
			}
			childKey, err := d.key("a/child.txt")
			if err != nil {
				t.Fatalf("child key: %v", err)
			}
			if err := d.client.HSet(ctx, d.objectKey(childKey), map[string]any{"data": "child", "modtime": "0"}).Err(); err != nil {
				t.Fatalf("seed child hash: %v", err)
			}
			if err := d.client.SAdd(ctx, d.versionedDirChildrenKey(parentKey), encodeFileChild(childKey)).Err(); err != nil {
				t.Fatalf("seed child-only link: %v", err)
			}
			if err := tc.setup(d); err != nil {
				t.Fatalf("setup sibling: %v", err)
			}
			if err := d.Delete(tc.path); err != nil {
				t.Fatalf("Delete sibling: %v", err)
			}
			entries, err := d.List("a")
			if err != nil || len(entries) != 1 || entries[0].Path != "a/child.txt" {
				t.Fatalf("List after sibling delete = %+v err=%v", entries, err)
			}
			if data, err := d.Get("a/child.txt"); err != nil || string(data) != "child" {
				t.Fatalf("Get child after sibling delete = %q err=%v", data, err)
			}
		})
	}
}

// TestRedisLegacyObjectMembersRemainReadable covers the forward migration path
// for indexes written before members gained explicit type tags.
func TestRedisLegacyObjectMembersRemainReadable(t *testing.T) {
	d := newMiniRedisDriver(t, "")
	ctx := context.Background()
	key := "legacy/file.txt"
	if err := d.client.HSet(ctx, d.objectKey(key), map[string]any{"data": "legacy", "modtime": "0"}).Err(); err != nil {
		t.Fatalf("seed object: %v", err)
	}
	if err := d.client.SAdd(ctx, d.dirObjectsKey(""), key).Err(); err != nil {
		t.Fatalf("seed legacy index: %v", err)
	}

	var paths []string
	if err := d.Walk("", func(entry storagecore.Entry) error {
		paths = append(paths, entry.Path)
		return nil
	}); err != nil {
		t.Fatalf("Walk legacy index: %v", err)
	}
	if !slices.Contains(paths, key) {
		t.Fatalf("Walk paths = %v", paths)
	}

	if err := d.Delete(key); err != nil {
		t.Fatalf("Delete legacy object: %v", err)
	}
	members, err := d.client.SMembers(ctx, d.dirObjectsKey("")).Result()
	if err != nil {
		t.Fatalf("SMembers: %v", err)
	}
	if len(members) != 0 {
		t.Fatalf("legacy members remain after delete: %v", members)
	}
}

// TestRedisLegacyOverwriteWritesV2WithoutDuplicates verifies dual-read migration leaves one logical entry.
func TestRedisLegacyOverwriteWritesV2WithoutDuplicates(t *testing.T) {
	d := newMiniRedisDriver(t, "")
	ctx := context.Background()
	key := "legacy/file.txt"
	if err := d.client.HSet(ctx, d.objectKey(key), map[string]any{"data": "old", "modtime": "0"}).Err(); err != nil {
		t.Fatalf("seed object: %v", err)
	}
	for _, dir := range append([]string{""}, objectDirs(key)...) {
		if err := d.client.SAdd(ctx, d.dirObjectsKey(dir), key).Err(); err != nil {
			t.Fatalf("seed legacy index %q: %v", dir, err)
		}
	}
	if err := d.Put(key, []byte("new")); err != nil {
		t.Fatalf("Put overwrite: %v", err)
	}
	members, err := d.client.SMembers(ctx, d.dirObjectsKey("")).Result()
	if err != nil {
		t.Fatalf("SMembers: %v", err)
	}
	if len(members) != 1 || members[0] != key {
		t.Fatalf("legacy root members = %v", members)
	}
	versioned, err := d.client.SMembers(ctx, d.versionedDirObjectsKey("")).Result()
	if err != nil {
		t.Fatalf("SMembers v2: %v", err)
	}
	if len(versioned) != 1 || versioned[0] != objectMember(key) {
		t.Fatalf("v2 root members = %v", versioned)
	}
	var paths []string
	if err := d.Walk("", func(entry storagecore.Entry) error {
		if !entry.IsDir {
			paths = append(paths, entry.Path)
		}
		return nil
	}); err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if len(paths) != 1 || paths[0] != key {
		t.Fatalf("Walk object paths = %v", paths)
	}
}

// TestRedisMovesExplicitEmptyDirectories exercises marker preservation and the
// shared directory-move implementation without requiring Docker.
func TestRedisMovesExplicitEmptyDirectories(t *testing.T) {
	d := newMiniRedisDriver(t, "itest")
	if err := d.MakeDir("source/nested"); err != nil {
		t.Fatalf("MakeDir: %v", err)
	}
	if err := d.Move("source", "destination"); err != nil {
		t.Fatalf("Move: %v", err)
	}
	entry, err := d.Stat("destination/nested")
	if err != nil || !entry.IsDir {
		t.Fatalf("Stat destination nested = %+v err=%v", entry, err)
	}
	if _, err := d.Stat("source"); !errors.Is(err, storagecore.ErrNotFound) {
		t.Fatalf("Stat source error = %v", err)
	}
}

// TestRedisRejectsLogicalRootMutations ensures a configured prefix remains a
// namespace boundary rather than becoming a user-addressable object.
func TestRedisRejectsLogicalRootMutations(t *testing.T) {
	d := newMiniRedisDriver(t, "itest")
	if err := d.Put("file.txt", []byte("payload")); err != nil {
		t.Fatalf("Put seed: %v", err)
	}
	for name, err := range map[string]error{
		"put":         d.Put("", []byte("root")),
		"copy target": d.Copy("file.txt", ""),
		"move target": d.Move("file.txt", ""),
		"delete":      d.Delete(""),
	} {
		if !errors.Is(err, storagecore.ErrForbidden) {
			t.Errorf("%s root error = %v", name, err)
		}
	}
	if data, err := d.Get("file.txt"); err != nil || string(data) != "payload" {
		t.Fatalf("seed after rejected mutations = %q err=%v", data, err)
	}
}

// TestRedisObjectSizeHandlesEmptyAndLargePayloads protects the HSTRLEN-based
// metadata path from confusing a missing field with a valid empty object.
func TestRedisObjectSizeHandlesEmptyAndLargePayloads(t *testing.T) {
	d := newMiniRedisDriver(t, "itest")
	large := bytes.Repeat([]byte("x"), 4<<20)
	for name, payload := range map[string][]byte{
		"empty.bin": nil,
		"large.bin": large,
	} {
		if err := d.Put(name, payload); err != nil {
			t.Fatalf("Put %s: %v", name, err)
		}
		entry, err := d.Stat(name)
		if err != nil {
			t.Fatalf("Stat %s: %v", name, err)
		}
		if entry.Size != int64(len(payload)) {
			t.Fatalf("Stat %s size = %d want %d", name, entry.Size, len(payload))
		}
		exists, err := d.Exists(name)
		if err != nil || !exists {
			t.Fatalf("Exists %s = %v err=%v", name, exists, err)
		}
	}
}

// TestRedisObjectSizeIsAtomic verifies metadata never combines existence from one version with another version's length.
func TestRedisObjectSizeIsAtomic(t *testing.T) {
	d := newMiniRedisDriver(t, "itest")
	const iterations = 200
	errs := make(chan error, iterations*3)
	var writers sync.WaitGroup
	writers.Add(1)
	go func() {
		defer writers.Done()
		for i := 0; i < iterations; i++ {
			payload := []byte("x")
			if i%2 != 0 {
				payload = []byte("payload")
			}
			if err := d.Put("flap", payload); err != nil {
				errs <- fmt.Errorf("Put: %w", err)
			}
			if err := d.Delete("flap"); err != nil && !errors.Is(err, storagecore.ErrNotFound) {
				errs <- fmt.Errorf("Delete: %w", err)
			}
		}
	}()
	for i := 0; i < iterations; i++ {
		entry, err := d.Stat("flap")
		if errors.Is(err, storagecore.ErrNotFound) {
			continue
		}
		if err != nil {
			errs <- fmt.Errorf("Stat: %w", err)
			continue
		}
		if entry.Size != int64(len("x")) && entry.Size != int64(len("payload")) {
			errs <- fmt.Errorf("Stat size = %d", entry.Size)
		}
	}
	writers.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

// TestRedisRejectsFileDirectoryCollisions keeps listings unambiguous and
// verifies a same-path move remains a no-op rather than deleting its source.
func TestRedisRejectsFileDirectoryCollisions(t *testing.T) {
	d := newMiniRedisDriver(t, "itest")
	if err := d.MakeDir("directory"); err != nil {
		t.Fatalf("MakeDir directory: %v", err)
	}
	if err := d.Put("directory", []byte("file")); !errors.Is(err, storagecore.ErrForbidden) {
		t.Fatalf("Put over directory error = %v", err)
	}
	if err := d.Put("file", []byte("payload")); err != nil {
		t.Fatalf("Put file: %v", err)
	}
	if err := d.MakeDir("file"); !errors.Is(err, storagecore.ErrForbidden) {
		t.Fatalf("MakeDir over file error = %v", err)
	}
	if err := d.Put("file/child", []byte("payload")); !errors.Is(err, storagecore.ErrForbidden) {
		t.Fatalf("Put below file error = %v", err)
	}
	if err := d.Copy("file", "directory"); !errors.Is(err, storagecore.ErrForbidden) {
		t.Fatalf("Copy over directory error = %v", err)
	}
	if err := d.Move("file", "file"); err != nil {
		t.Fatalf("Move same path: %v", err)
	}
	if data, err := d.Get("file"); err != nil || string(data) != "payload" {
		t.Fatalf("Get after same-path move = %q err=%v", data, err)
	}
}

// TestRedisCollisionChecksAreAtomicAcrossClients races independent drivers to
// prove WATCH prevents a path from becoming both an object and a directory.
func TestRedisCollisionChecksAreAtomicAcrossClients(t *testing.T) {
	server := miniredis.RunT(t)
	newDriver := func() *driver {
		store, err := New(Config{Addr: server.Addr(), Prefix: "tenant"})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		d := store.(*driver)
		t.Cleanup(func() {
			if err := d.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
		})
		return d
	}
	objectClient := newDriver()
	directoryClient := newDriver()

	for i := 0; i < 100; i++ {
		name := fmt.Sprintf("race-%03d", i)
		start := make(chan struct{})
		results := make(chan error, 2)
		var ready sync.WaitGroup
		ready.Add(2)
		go func() {
			ready.Done()
			<-start
			results <- objectClient.Put(name, []byte("payload"))
		}()
		go func() {
			ready.Done()
			<-start
			results <- directoryClient.MakeDir(name)
		}()
		ready.Wait()
		close(start)
		first, second := <-results, <-results
		successes := 0
		for _, err := range []error{first, second} {
			if err == nil {
				successes++
				continue
			}
			if !errors.Is(err, storagecore.ErrForbidden) {
				t.Fatalf("race %s error = %v", name, err)
			}
		}
		if successes != 1 {
			t.Fatalf("race %s successes = %d errors=(%v, %v)", name, successes, first, second)
		}
	}
}

// TestRedisParentObjectCollisionIsAtomicAcrossClients verifies parent and child writes cannot both commit.
func TestRedisParentObjectCollisionIsAtomicAcrossClients(t *testing.T) {
	server := miniredis.RunT(t)
	parentClient := newMiniRedisDriverForServer(t, server, "tenant")
	childClient := newMiniRedisDriverForServer(t, server, "tenant")
	for i := 0; i < 100; i++ {
		parent := fmt.Sprintf("distributed-%03d", i)
		start := make(chan struct{})
		results := make(chan error, 2)
		go func() {
			<-start
			results <- parentClient.Put(parent, []byte("parent"))
		}()
		go func() {
			<-start
			results <- childClient.Put(parent+"/child", []byte("child"))
		}()
		close(start)
		first, second := <-results, <-results
		successes := 0
		for _, err := range []error{first, second} {
			if err == nil {
				successes++
				continue
			}
			if !errors.Is(err, storagecore.ErrForbidden) {
				t.Fatalf("collision %s error = %v", parent, err)
			}
		}
		if successes != 1 {
			t.Fatalf("collision %s successes=%d errors=(%v, %v)", parent, successes, first, second)
		}
	}
}

// TestRedisSamePathAliasesAreValidatedNoOps verifies normalization cannot bypass source validation or rewrite metadata.
func TestRedisSamePathAliasesAreValidatedNoOps(t *testing.T) {
	d := newMiniRedisDriver(t, "tenant")
	if err := d.Put("folder/file.txt", []byte("payload")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	before, err := d.ModTime(context.Background(), "folder/file.txt")
	if err != nil {
		t.Fatalf("ModTime before: %v", err)
	}
	if err := d.Copy("folder/./file.txt", "folder/other/../file.txt"); err != nil {
		t.Fatalf("Copy alias: %v", err)
	}
	if err := d.Move("folder/./file.txt", "folder/other/../file.txt"); err != nil {
		t.Fatalf("Move alias: %v", err)
	}
	after, err := d.ModTime(context.Background(), "folder/file.txt")
	if err != nil || !after.Equal(before) {
		t.Fatalf("ModTime after aliases = %v err=%v want %v", after, err, before)
	}
	if err := d.Copy("missing/../absent", "absent"); !errors.Is(err, storagecore.ErrNotFound) {
		t.Fatalf("Copy missing alias error = %v", err)
	}
	if err := d.Move("missing/../absent", "absent"); !errors.Is(err, storagecore.ErrNotFound) {
		t.Fatalf("Move missing alias error = %v", err)
	}
}

// TestRedisCloseIsTerminalAcrossOperations verifies every entry point fails before using the released client.
func TestRedisCloseIsTerminalAcrossOperations(t *testing.T) {
	d := newMiniRedisDriver(t, "tenant")
	if err := d.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	operations := map[string]func() error{
		"get":      func() error { _, err := d.Get("file"); return err },
		"put":      func() error { return d.Put("file", nil) },
		"make dir": func() error { return d.MakeDir("dir") },
		"delete":   func() error { return d.Delete("file") },
		"stat":     func() error { _, err := d.Stat("file"); return err },
		"exists":   func() error { _, err := d.Exists("file"); return err },
		"list":     func() error { _, err := d.List(""); return err },
		"walk":     func() error { return d.Walk("", func(storagecore.Entry) error { return nil }) },
		"copy":     func() error { return d.Copy("file", "copy") },
		"move":     func() error { return d.Move("file", "moved") },
		"url":      func() error { _, err := d.URL("file"); return err },
		"mod time": func() error { _, err := d.ModTime(context.Background(), "file"); return err },
	}
	for name, operation := range operations {
		if err := operation(); !errors.Is(err, fs.ErrClosed) {
			t.Errorf("%s after Close error = %v", name, err)
		}
	}
}

// TestRedisEmptyDirectoryDeleteIsAtomicAcrossClients verifies a concurrent child cannot be orphaned from its parent indexes.
func TestRedisEmptyDirectoryDeleteIsAtomicAcrossClients(t *testing.T) {
	server := miniredis.RunT(t)
	first := newMiniRedisDriverForServer(t, server, "tenant")
	second := newMiniRedisDriverForServer(t, server, "tenant")
	for i := 0; i < 100; i++ {
		parent := fmt.Sprintf("parent-%03d", i)
		child := parent + "/child.txt"
		if err := first.MakeDir(parent); err != nil {
			t.Fatalf("MakeDir %s: %v", parent, err)
		}
		start := make(chan struct{})
		results := make(chan error, 2)
		go func() {
			<-start
			results <- first.Delete(parent)
		}()
		go func() {
			<-start
			results <- second.Put(child, []byte("payload"))
		}()
		close(start)
		deleteErr, putErr := <-results, <-results
		if putErr != nil && deleteErr == nil {
			deleteErr, putErr = putErr, deleteErr
		}
		if putErr != nil {
			t.Fatalf("Put %s error = %v (other error %v)", child, putErr, deleteErr)
		}
		if deleteErr != nil && !errors.Is(deleteErr, storagecore.ErrForbidden) {
			t.Fatalf("Delete %s error = %v", parent, deleteErr)
		}
		if data, err := first.Get(child); err != nil || string(data) != "payload" {
			t.Fatalf("Get %s = %q err=%v", child, data, err)
		}
		entries, err := first.List(parent)
		if err != nil || len(entries) != 1 || entries[0].Path != child {
			t.Fatalf("List %s = %+v err=%v", parent, entries, err)
		}
	}
}

// TestRedisConfiguredPrefixHidesExactBackendObject verifies the logical root cannot alias a foreign object.
func TestRedisConfiguredPrefixHidesExactBackendObject(t *testing.T) {
	server := miniredis.RunT(t)
	foreign := newMiniRedisDriverForServer(t, server, "")
	prefixed := newMiniRedisDriverForServer(t, server, "scratch")
	if err := foreign.Put("scratch", []byte("foreign")); err != nil {
		t.Fatalf("Put foreign object: %v", err)
	}
	if _, err := prefixed.Get(""); !errors.Is(err, storagecore.ErrForbidden) {
		t.Fatalf("Get root error = %v", err)
	}
	entry, err := prefixed.Stat("")
	if err != nil || entry.Path != "" || !entry.IsDir {
		t.Fatalf("Stat root = %+v err=%v", entry, err)
	}
	if exists, err := prefixed.Exists(""); err != nil || exists {
		t.Fatalf("Exists root = %v err=%v", exists, err)
	}
	if entries, err := prefixed.List(""); err != nil || len(entries) != 0 {
		t.Fatalf("List root = %+v err=%v", entries, err)
	}
	var walked []storagecore.Entry
	if err := prefixed.Walk("", func(entry storagecore.Entry) error {
		walked = append(walked, entry)
		return nil
	}); err != nil || len(walked) != 0 {
		t.Fatalf("Walk root = %+v err=%v", walked, err)
	}
	if err := prefixed.Copy("", "copy"); !errors.Is(err, storagecore.ErrForbidden) {
		t.Fatalf("Copy source root error = %v", err)
	}
	if _, err := prefixed.ModTime(context.Background(), ""); !errors.Is(err, storagecore.ErrForbidden) {
		t.Fatalf("ModTime root error = %v", err)
	}
}

// newMiniRedisDriver creates an isolated in-process Redis driver for ordinary
// unit tests so index invariants do not depend on Docker availability.
func newMiniRedisDriver(t *testing.T, prefix string) *driver {
	t.Helper()
	server := miniredis.RunT(t)
	return newMiniRedisDriverForServer(t, server, prefix)
}

// newMiniRedisDriverForServer creates a driver sharing one in-process Redis server for concurrency tests.
func newMiniRedisDriverForServer(t *testing.T, server *miniredis.Miniredis, prefix string) *driver {
	t.Helper()
	store, err := New(Config{Addr: server.Addr(), Prefix: prefix})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	d, ok := store.(*driver)
	if !ok {
		t.Fatalf("store type = %T", store)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	})
	return d
}

// assertRedisEntryExists verifies both direct metadata and root traversal retain an indexed entry.
func assertRedisEntryExists(t *testing.T, d *driver, name string, wantDir bool) {
	t.Helper()
	entry, err := d.Stat(name)
	if err != nil || entry.IsDir != wantDir {
		t.Fatalf("Stat %q = %+v err=%v", name, entry, err)
	}
	var matches int
	if err := d.Walk("", func(entry storagecore.Entry) error {
		if entry.Path == name && entry.IsDir == wantDir {
			matches++
		}
		return nil
	}); err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if matches != 1 {
		t.Fatalf("Walk matches for %q = %d", name, matches)
	}
}

// seedLegacyObject writes the raw index shape used before typed members were introduced.
func seedLegacyObject(t *testing.T, d *driver, key, data string) {
	t.Helper()
	ctx := context.Background()
	if err := d.client.HSet(ctx, d.objectKey(key), map[string]any{"data": data, "modtime": "0"}).Err(); err != nil {
		t.Fatalf("seed legacy object %q: %v", key, err)
	}
	if err := d.client.SAdd(ctx, d.dirObjectsKey(""), key).Err(); err != nil {
		t.Fatalf("seed legacy root index %q: %v", key, err)
	}
	dirs := objectDirs(key)
	for _, dir := range dirs {
		if err := d.client.SAdd(ctx, d.dirObjectsKey(dir), key).Err(); err != nil {
			t.Fatalf("seed legacy directory index %q: %v", dir, err)
		}
	}
	if len(dirs) == 0 {
		if err := d.client.SAdd(ctx, d.dirChildrenKey(""), encodeFileChild(key)).Err(); err != nil {
			t.Fatalf("seed legacy child %q: %v", key, err)
		}
		return
	}
	if err := d.client.SAdd(ctx, d.dirChildrenKey(""), encodeDirChild(dirs[0])).Err(); err != nil {
		t.Fatalf("seed legacy root directory %q: %v", dirs[0], err)
	}
	for i := 0; i < len(dirs)-1; i++ {
		if err := d.client.SAdd(ctx, d.dirChildrenKey(dirs[i]), encodeDirChild(dirs[i+1])).Err(); err != nil {
			t.Fatalf("seed legacy nested directory: %v", err)
		}
	}
	if err := d.client.SAdd(ctx, d.dirChildrenKey(dirs[len(dirs)-1]), encodeFileChild(key)).Err(); err != nil {
		t.Fatalf("seed legacy file child: %v", err)
	}
}

// seedLegacyTypedObject writes the typed-v1 member into the released mixed SET.
func seedLegacyTypedObject(t *testing.T, d *driver, key, data string) {
	t.Helper()
	ctx := context.Background()
	if err := d.client.HSet(ctx, d.objectKey(key), map[string]any{"data": data, "modtime": "0"}).Err(); err != nil {
		t.Fatalf("seed typed object %q: %v", key, err)
	}
	if err := d.client.SAdd(ctx, d.dirObjectsKey(""), objectMember(key)).Err(); err != nil {
		t.Fatalf("seed typed root index %q: %v", key, err)
	}
	if err := d.client.SAdd(ctx, d.dirChildrenKey(""), encodeFileChild(key)).Err(); err != nil {
		t.Fatalf("seed typed child %q: %v", key, err)
	}
}

// seedLegacyDirectory writes one explicit directory marker into the released mixed schema.
func seedLegacyDirectory(t *testing.T, d *driver, key, marker string) {
	t.Helper()
	ctx := context.Background()
	if err := d.client.SAdd(ctx, d.dirObjectsKey(""), marker).Err(); err != nil {
		t.Fatalf("seed directory root marker %q: %v", key, err)
	}
	if err := d.client.SAdd(ctx, d.dirObjectsKey(key), marker).Err(); err != nil {
		t.Fatalf("seed directory self marker %q: %v", key, err)
	}
	if err := d.client.SAdd(ctx, d.dirChildrenKey(parentDir(key)), encodeDirChild(key)).Err(); err != nil {
		t.Fatalf("seed directory parent link %q: %v", key, err)
	}
}

type blockingObjectSizeHook struct {
	evaluated chan struct{}
	resume    chan struct{}
	blockOn   int
	calls     int
	mu        sync.Mutex
	once      sync.Once
}

// DialHook leaves connection establishment untouched while satisfying redis.Hook.
func (h *blockingObjectSizeHook) DialHook(next redis.DialHook) redis.DialHook {
	return next
}

// ProcessHook pauses a selected object-size command after Redis has fixed its result.
func (h *blockingObjectSizeHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		err := next(ctx, cmd)
		args := cmd.Args()
		if len(args) < 2 || cmd.Name() != "eval" || args[1] != objectSizeScript {
			return err
		}
		h.mu.Lock()
		h.calls++
		block := h.calls == h.blockOn
		h.mu.Unlock()
		if block {
			h.once.Do(func() {
				close(h.evaluated)
				<-h.resume
			})
		}
		return err
	}
}

// ProcessPipelineHook leaves unrelated pipelines untouched while satisfying redis.Hook.
func (h *blockingObjectSizeHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

// blockNextObjectSize installs the deterministic interleaving used by stale-read race tests.
func blockNextObjectSize(d *driver) (<-chan struct{}, chan<- struct{}) {
	return blockObjectSizeCall(d, 1)
}

// blockObjectSizeCall pauses a selected validation after Redis has fixed its stale result.
func blockObjectSizeCall(d *driver, call int) (<-chan struct{}, chan<- struct{}) {
	evaluated := make(chan struct{})
	resume := make(chan struct{})
	d.client.AddHook(&blockingObjectSizeHook{
		evaluated: evaluated,
		resume:    resume,
		blockOn:   call,
	})
	return evaluated, resume
}

// waitForObjectSizeEvaluation prevents a broken test seam from hanging the suite indefinitely.
func waitForObjectSizeEvaluation(t *testing.T, evaluated <-chan struct{}) {
	t.Helper()
	select {
	case <-evaluated:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for stale object-size observation")
	}
}

// seedLegacyObjectIndexesWithoutHash models a partially written released-schema object.
func seedLegacyObjectIndexesWithoutHash(t *testing.T, d *driver, key string) {
	t.Helper()
	ctx := context.Background()
	pipe := d.client.TxPipeline()
	queueReleasedObjectIndexes(ctx, d, pipe, key)
	if _, err := pipe.Exec(ctx); err != nil {
		t.Fatalf("seed released indexes: %v", err)
	}
}

// putWithReleasedSchema reproduces the v0.4.6 object write without any v2 metadata.
func putWithReleasedSchema(ctx context.Context, d *driver, key string, contents []byte) error {
	pipe := d.client.TxPipeline()
	pipe.HSet(ctx, d.objectKey(key), map[string]any{
		"data":    string(contents),
		"modtime": time.Now().UTC().UnixNano(),
	})
	queueReleasedObjectIndexes(ctx, d, pipe, key)
	_, err := pipe.Exec(ctx)
	return err
}

// queueReleasedObjectIndexes writes the raw member and child links used by v0.4.6.
func queueReleasedObjectIndexes(ctx context.Context, d *driver, pipe redis.Pipeliner, key string) {
	dirs := objectDirs(key)
	pipe.SAdd(ctx, d.dirObjectsKey(""), key)
	if len(dirs) == 0 {
		pipe.SAdd(ctx, d.dirChildrenKey(""), encodeFileChild(key))
		return
	}
	for _, dir := range dirs {
		pipe.SAdd(ctx, d.dirObjectsKey(dir), key)
	}
	pipe.SAdd(ctx, d.dirChildrenKey(""), encodeDirChild(dirs[0]))
	for i := 0; i < len(dirs)-1; i++ {
		pipe.SAdd(ctx, d.dirChildrenKey(dirs[i]), encodeDirChild(dirs[i+1]))
	}
	pipe.SAdd(ctx, d.dirChildrenKey(dirs[len(dirs)-1]), encodeFileChild(key))
}

// makeDirWithReleasedSchema reproduces the v0.4.6 empty-directory transaction.
func makeDirWithReleasedSchema(ctx context.Context, d *driver, key string) error {
	dirs := objectDirs(key)
	marker := legacyDirMarkerMember(key)
	pipe := d.client.TxPipeline()
	if len(dirs) == 0 {
		pipe.SAdd(ctx, d.dirChildrenKey(""), encodeDirChild(key))
		pipe.SAdd(ctx, d.dirObjectsKey(key), marker)
	} else {
		pipe.SAdd(ctx, d.dirChildrenKey(""), encodeDirChild(dirs[0]))
		for i := 0; i < len(dirs)-1; i++ {
			pipe.SAdd(ctx, d.dirChildrenKey(dirs[i]), encodeDirChild(dirs[i+1]))
			pipe.SAdd(ctx, d.dirObjectsKey(dirs[i]), marker)
		}
		parent := dirs[len(dirs)-1]
		pipe.SAdd(ctx, d.dirChildrenKey(parent), encodeDirChild(key))
		pipe.SAdd(ctx, d.dirObjectsKey(parent), marker)
		pipe.SAdd(ctx, d.dirObjectsKey(key), marker)
	}
	_, err := pipe.Exec(ctx)
	return err
}
