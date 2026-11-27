package redisstorage

import (
	"context"
	"encoding/base64"
	"fmt"
	"slices"
	"strings"

	"github.com/goforj/storage/storagecore"
	"github.com/redis/go-redis/v9"
)

type indexSchema uint8

const (
	legacyIndex indexSchema = iota
	versionedIndex
)

type indexedMemberRecord struct {
	member string
	schema indexSchema
}

type indexedCandidate struct {
	key   string
	isDir bool
}

// dirChildrenKey maps a directory to the released mixed-schema child set.
func (d *driver) dirChildrenKey(key string) string {
	return d.namespace + ":dir:" + key + ":children"
}

// dirObjectsKey maps a directory to the released mixed-schema descendant set.
func (d *driver) dirObjectsKey(key string) string {
	return d.namespace + ":dir:" + key + ":objects"
}

// versionedDirChildrenKey isolates typed child metadata from ambiguous released legacy sets.
func (d *driver) versionedDirChildrenKey(key string) string {
	return d.namespace + ":index:v2:dir:" + base64.RawURLEncoding.EncodeToString([]byte(key)) + ":children"
}

// versionedDirObjectsKey isolates typed descendant metadata from raw legacy object names.
func (d *driver) versionedDirObjectsKey(key string) string {
	return d.namespace + ":index:v2:dir:" + base64.RawURLEncoding.EncodeToString([]byte(key)) + ":objects"
}

// children merges both index generations and drops links whose backing state no longer exists.
func (d *driver) children(ctx context.Context, key string) ([]string, error) {
	return d.childrenSeen(ctx, key, make(map[string]struct{}))
}

// childrenSeen shares recursion state while validating directory child links.
func (d *driver) childrenSeen(ctx context.Context, key string, seen map[string]struct{}) ([]string, error) {
	sources, err := d.childMembersWithClient(ctx, d.client, key)
	if err != nil {
		return nil, err
	}
	children := make([]string, 0, len(sources))
	for _, child := range sources {
		isDir, childKey, err := parseChildEntry(child)
		if err != nil || childKey == key || parentDir(childKey) != key {
			continue
		}
		valid := false
		if isDir {
			valid, err = d.dirExistsSeen(ctx, childKey, seen)
		} else {
			var size int64
			size, err = d.objectSize(ctx, childKey)
			valid = size >= 0
		}
		if err != nil {
			return nil, err
		}
		if valid {
			children = append(children, child)
		}
	}
	return children, nil
}

// childMembersWithClient returns the sorted union of released and v2 child links without trusting either.
func (d *driver) childMembersWithClient(ctx context.Context, client redis.Cmdable, key string) ([]string, error) {
	pipe := client.Pipeline()
	legacy := pipe.SMembers(ctx, d.dirChildrenKey(key))
	versioned := pipe.SMembers(ctx, d.versionedDirChildrenKey(key))
	if _, err := pipe.Exec(ctx); err != nil {
		return nil, fmt.Errorf("storage: redis list: %w", err)
	}
	sources := map[string]struct{}{}
	for _, child := range legacy.Val() {
		sources[child] = struct{}{}
	}
	for _, child := range versioned.Val() {
		sources[child] = struct{}{}
	}
	children := make([]string, 0, len(sources))
	for child := range sources {
		children = append(children, child)
	}
	slices.Sort(children)
	return children, nil
}

// listEntries converts verified child members into sorted public entries.
func (d *driver) listEntries(ctx context.Context, children []string) ([]storagecore.Entry, error) {
	entries := make([]storagecore.Entry, 0, len(children))
	for _, child := range children {
		isDir, key, err := parseChildEntry(child)
		if err != nil {
			return nil, err
		}
		entry := storagecore.Entry{Path: d.stripPrefix(key), IsDir: isDir}
		if isDir {
			exists, err := d.dirExists(ctx, key)
			if err != nil {
				return nil, err
			}
			if !exists {
				continue
			}
		} else {
			size, err := d.objectSize(ctx, key)
			if err != nil {
				return nil, err
			}
			if size < 0 {
				continue
			}
			entry.Size = size
		}
		entries = append(entries, entry)
	}
	slices.SortFunc(entries, func(a, b storagecore.Entry) int {
		return strings.Compare(a.Path, b.Path)
	})
	return entries, nil
}

// walkEntries emits only verified descendants, so stale indexes cannot synthesize ghost parents.
func (d *driver) walkEntries(ctx context.Context, records []indexedMemberRecord, key string) ([]storagecore.Entry, bool, error) {
	if d.stripPrefix(key) != "" {
		size, err := d.objectSize(ctx, key)
		if err != nil {
			return nil, false, err
		}
		if size >= 0 {
			return []storagecore.Entry{{Path: d.stripPrefix(key), Size: size, IsDir: false}}, true, nil
		}
	}
	found := d.stripPrefix(key) == ""
	if !found {
		var err error
		found, err = d.dirExists(ctx, key)
		if err != nil {
			return nil, false, err
		}
	}

	seenDirs := map[string]struct{}{}
	seenObjects := map[string]struct{}{}
	entries := make([]storagecore.Entry, 0, len(records))
	requested := d.stripPrefix(key)
	for _, record := range records {
		candidates, err := d.resolveIndexedMember(ctx, record)
		if err != nil {
			return nil, false, err
		}
		for _, candidate := range candidates {
			existing := candidate.key
			if key != "" && existing != key && !strings.HasPrefix(existing, key+"/") {
				continue
			}
			if candidate.isDir {
				dir := d.stripPrefix(existing)
				if dir == "" || dir == requested {
					continue
				}
				for _, parent := range recursiveParentDirs(dir + "/marker") {
					if parent == requested || (requested != "" && !strings.HasPrefix(parent, requested+"/")) {
						continue
					}
					fullDir := storagecore.JoinPrefix(d.prefix, parent)
					if _, ok := seenDirs[fullDir]; ok {
						continue
					}
					seenDirs[fullDir] = struct{}{}
					entries = append(entries, storagecore.Entry{Path: parent, IsDir: true})
				}
				continue
			}
			if _, ok := seenObjects[existing]; ok {
				continue
			}
			seenObjects[existing] = struct{}{}
			for _, dir := range recursiveParentDirs(d.stripPrefix(existing)) {
				if dir == requested || (requested != "" && !strings.HasPrefix(dir, requested+"/")) {
					continue
				}
				fullDir := storagecore.JoinPrefix(d.prefix, dir)
				if _, ok := seenDirs[fullDir]; ok {
					continue
				}
				seenDirs[fullDir] = struct{}{}
				entries = append(entries, storagecore.Entry{Path: dir, IsDir: true})
			}
			size, err := d.objectSize(ctx, existing)
			if err != nil {
				return nil, false, err
			}
			if size < 0 {
				continue
			}
			entries = append(entries, storagecore.Entry{Path: d.stripPrefix(existing), Size: size})
		}
	}
	slices.SortFunc(entries, func(a, b storagecore.Entry) int {
		return strings.Compare(a.Path, b.Path)
	})
	return entries, found, nil
}

// descendants returns records with schema provenance so ambiguous legacy values can be resolved safely.
func (d *driver) descendants(ctx context.Context, key string) ([]indexedMemberRecord, error) {
	return d.descendantsWithClient(ctx, d.client, key)
}

// descendantsWithClient reads both schema generations through one client or watched transaction.
func (d *driver) descendantsWithClient(ctx context.Context, client redis.Cmdable, key string) ([]indexedMemberRecord, error) {
	pipe := client.Pipeline()
	legacy := pipe.SMembers(ctx, d.dirObjectsKey(key))
	versioned := pipe.SMembers(ctx, d.versionedDirObjectsKey(key))
	if _, err := pipe.Exec(ctx); err != nil {
		return nil, fmt.Errorf("storage: redis walk: %w", err)
	}
	records := make([]indexedMemberRecord, 0, len(legacy.Val())+len(versioned.Val()))
	for _, member := range legacy.Val() {
		records = append(records, indexedMemberRecord{member: member, schema: legacyIndex})
	}
	for _, member := range versioned.Val() {
		records = append(records, indexedMemberRecord{member: member, schema: versionedIndex})
	}
	slices.SortFunc(records, func(a, b indexedMemberRecord) int {
		if order := strings.Compare(a.member, b.member); order != 0 {
			return order
		}
		return int(a.schema) - int(b.schema)
	})
	return records, nil
}

// dirExists accepts only markers or descendants whose backing state validates.
func (d *driver) dirExists(ctx context.Context, key string) (bool, error) {
	return d.dirExistsSeen(ctx, key, make(map[string]struct{}))
}

// dirExistsSeen bounds recursive child-only visibility when Redis metadata is corrupt or adversarial.
func (d *driver) dirExistsSeen(ctx context.Context, key string, seen map[string]struct{}) (bool, error) {
	if _, ok := seen[key]; ok {
		return false, nil
	}
	if len(seen) >= 1024 {
		return false, nil
	}
	seen[key] = struct{}{}
	defer delete(seen, key)

	records, err := d.descendants(ctx, key)
	if err != nil {
		return false, err
	}
	exists, _, err := d.directoryStateFromRecords(ctx, d.client, key, records)
	if err != nil || exists {
		return exists, err
	}
	children, err := d.childrenSeen(ctx, key, seen)
	if err != nil {
		return false, err
	}
	return len(children) != 0, nil
}

// directoryStateInTransaction watches every backing key implied by a stable index snapshot.
func (d *driver) directoryStateInTransaction(ctx context.Context, tx *redis.Tx, key string) (bool, bool, error) {
	return d.directoryStateInTransactionSeen(ctx, tx, key, make(map[string]struct{}))
}

// directoryStateInTransactionSeen mirrors recursive public visibility while rejecting malicious index cycles.
func (d *driver) directoryStateInTransactionSeen(ctx context.Context, tx *redis.Tx, key string, seen map[string]struct{}) (bool, bool, error) {
	if _, ok := seen[key]; ok {
		return false, false, fmt.Errorf("%w: cyclic directory child index at %q", storagecore.ErrForbidden, d.stripPrefix(key))
	}
	if len(seen) >= 1024 {
		return false, false, fmt.Errorf("%w: directory child index exceeds maximum validation depth", storagecore.ErrForbidden)
	}
	seen[key] = struct{}{}
	defer delete(seen, key)

	exists, hasContents, err := d.directoryRecordsStateInTransaction(ctx, tx, key)
	if err != nil {
		return false, false, err
	}
	children, err := d.watchedChildMembers(ctx, tx, key)
	if err != nil {
		return false, false, err
	}
	for _, child := range children {
		isDir, childKey, err := parseChildEntry(child)
		if err != nil || childKey == key || parentDir(childKey) != key {
			continue
		}
		valid := false
		if isDir {
			valid, _, err = d.directoryStateInTransactionSeen(ctx, tx, childKey, seen)
		} else {
			var size int64
			size, err = d.objectSizeWithClient(ctx, tx, childKey)
			valid = size >= 0
		}
		if err != nil {
			return false, false, err
		}
		if valid {
			exists = true
			hasContents = true
		}
	}
	return exists, hasContents, nil
}

// directoryRecordsStateInTransaction validates descendant records without recursing through child links.
func (d *driver) directoryRecordsStateInTransaction(ctx context.Context, tx *redis.Tx, key string) (bool, bool, error) {
	records, err := d.watchedDescendants(ctx, tx, key)
	if err != nil {
		return false, false, err
	}
	return d.directoryStateFromRecords(ctx, tx, key, records)
}

// watchedChildMembers watches child sets and object hashes before returning a stable link snapshot.
func (d *driver) watchedChildMembers(ctx context.Context, tx *redis.Tx, key string) ([]string, error) {
	indexKeys := []string{d.dirChildrenKey(key), d.versionedDirChildrenKey(key)}
	if err := tx.Watch(ctx, indexKeys...).Err(); err != nil {
		return nil, fmt.Errorf("storage: redis watch directory children: %w", err)
	}
	children, err := d.childMembersWithClient(ctx, tx, key)
	if err != nil {
		return nil, err
	}
	watchKeys := make([]string, 0, len(children))
	for _, child := range children {
		isDir, childKey, err := parseChildEntry(child)
		if err == nil && !isDir && parentDir(childKey) == key {
			watchKeys = append(watchKeys, d.objectKey(childKey))
		}
	}
	if len(watchKeys) != 0 {
		slices.Sort(watchKeys)
		watchKeys = slices.Compact(watchKeys)
		if err := tx.Watch(ctx, watchKeys...).Err(); err != nil {
			return nil, fmt.Errorf("storage: redis watch child objects: %w", err)
		}
	}
	stable, err := d.childMembersWithClient(ctx, tx, key)
	if err != nil {
		return nil, err
	}
	if !slices.Equal(children, stable) {
		return nil, redis.TxFailedErr
	}
	return stable, nil
}

// watchedDescendants closes stale-index races by watching candidate state before validation.
func (d *driver) watchedDescendants(ctx context.Context, tx *redis.Tx, key string) ([]indexedMemberRecord, error) {
	indexKeys := []string{d.dirObjectsKey(key), d.versionedDirObjectsKey(key)}
	if err := tx.Watch(ctx, indexKeys...).Err(); err != nil {
		return nil, fmt.Errorf("storage: redis watch directory indexes: %w", err)
	}
	records, err := d.descendantsWithClient(ctx, tx, key)
	if err != nil {
		return nil, err
	}
	watchKeys := d.indexedMemberWatchKeys(records)
	if len(watchKeys) != 0 {
		if err := tx.Watch(ctx, watchKeys...).Err(); err != nil {
			return nil, fmt.Errorf("storage: redis watch indexed state: %w", err)
		}
	}
	stable, err := d.descendantsWithClient(ctx, tx, key)
	if err != nil {
		return nil, err
	}
	if !slices.Equal(records, stable) {
		return nil, redis.TxFailedErr
	}
	return stable, nil
}

// indexedMemberWatchKeys derives every hash or marker link consulted while resolving records.
func (d *driver) indexedMemberWatchKeys(records []indexedMemberRecord) []string {
	keys := map[string]struct{}{}
	addObject := func(key string) {
		keys[d.objectKey(key)] = struct{}{}
	}
	addMarker := func(schema indexSchema, key string) {
		if schema == versionedIndex {
			keys[d.versionedDirObjectsKey(key)] = struct{}{}
			keys[d.versionedDirChildrenKey(parentDir(key))] = struct{}{}
			return
		}
		keys[d.dirObjectsKey(key)] = struct{}{}
		keys[d.dirChildrenKey(parentDir(key))] = struct{}{}
	}
	for _, record := range records {
		if record.schema == legacyIndex {
			addObject(record.member)
		}
		key, marker, err := parseIndexedMember(record.member)
		if err != nil {
			continue
		}
		if marker {
			addMarker(record.schema, key)
			continue
		}
		addObject(key)
	}
	out := make([]string, 0, len(keys))
	for key := range keys {
		out = append(out, key)
	}
	slices.Sort(out)
	return out
}

// directoryStateFromRecords reports directory existence and whether a verified descendant exists.
func (d *driver) directoryStateFromRecords(ctx context.Context, client redis.Cmdable, key string, records []indexedMemberRecord) (bool, bool, error) {
	exists := key == ""
	hasContents := false
	for _, record := range records {
		candidates, err := d.resolveIndexedMemberWithClient(ctx, client, record)
		if err != nil {
			return false, false, err
		}
		for _, candidate := range candidates {
			descendant := key == "" || strings.HasPrefix(candidate.key, key+"/")
			if descendant {
				exists = true
				hasContents = true
			}
			if candidate.isDir {
				if candidate.key == key {
					exists = true
				}
			}
		}
	}
	return exists, hasContents, nil
}

// resolveIndexedMember disambiguates legacy raw names by checking every possible backing object.
func (d *driver) resolveIndexedMember(ctx context.Context, record indexedMemberRecord) ([]indexedCandidate, error) {
	return d.resolveIndexedMemberWithClient(ctx, d.client, record)
}

// resolveIndexedMemberWithClient validates an index candidate on the transaction connection when required.
func (d *driver) resolveIndexedMemberWithClient(ctx context.Context, client redis.Cmdable, record indexedMemberRecord) ([]indexedCandidate, error) {
	if record.schema == versionedIndex {
		key, marker, err := parseIndexedMember(record.member)
		if err != nil {
			return nil, err
		}
		if marker {
			valid, err := d.versionedDirectoryMarkerValidWithClient(ctx, client, key)
			if err != nil || !valid {
				return nil, err
			}
			return []indexedCandidate{{key: key, isDir: true}}, nil
		}
		size, err := d.objectSizeWithClient(ctx, client, key)
		if err != nil || size < 0 {
			return nil, err
		}
		return []indexedCandidate{{key: key}}, nil
	}

	candidates := make([]indexedCandidate, 0, 2)
	seen := map[indexedCandidate]struct{}{}
	addObject := func(key string) error {
		size, err := d.objectSizeWithClient(ctx, client, key)
		if err != nil {
			return err
		}
		if size >= 0 {
			candidate := indexedCandidate{key: key}
			if _, ok := seen[candidate]; !ok {
				seen[candidate] = struct{}{}
				candidates = append(candidates, candidate)
			}
		}
		return nil
	}
	if err := addObject(record.member); err != nil {
		return nil, err
	}
	key, marker, err := parseIndexedMember(record.member)
	if err != nil {
		if len(candidates) != 0 {
			return candidates, nil
		}
		return nil, nil
	}
	if !marker {
		if err := addObject(key); err != nil {
			return nil, err
		}
		return candidates, nil
	}
	valid, err := d.legacyDirectoryMarkerValidWithClient(ctx, client, key)
	if err != nil {
		return nil, err
	}
	if valid {
		candidate := indexedCandidate{key: key, isDir: true}
		if _, ok := seen[candidate]; !ok {
			candidates = append(candidates, candidate)
		}
	}
	return candidates, nil
}

// legacyDirectoryMarkerValid requires the released schema's self marker and parent link to agree.
func (d *driver) legacyDirectoryMarkerValid(ctx context.Context, key string) (bool, error) {
	return d.legacyDirectoryMarkerValidWithClient(ctx, d.client, key)
}

// legacyDirectoryMarkerValidWithClient treats the exact parent link as the deletion and recreation authority.
func (d *driver) legacyDirectoryMarkerValidWithClient(ctx context.Context, client redis.Cmdable, key string) (bool, error) {
	pipe := client.Pipeline()
	self := pipe.SMembers(ctx, d.dirObjectsKey(key))
	parent := pipe.SIsMember(ctx, d.dirChildrenKey(parentDir(key)), encodeDirChild(key))
	if _, err := pipe.Exec(ctx); err != nil {
		return false, fmt.Errorf("storage: redis stat: %w", err)
	}
	hasSelfMarker := slices.Contains(self.Val(), dirMarkerMember(key)) || slices.Contains(self.Val(), legacyDirMarkerMember(key))
	return hasSelfMarker && parent.Val(), nil
}

// versionedDirectoryMarkerValid requires both links written by the v2 directory transaction.
func (d *driver) versionedDirectoryMarkerValid(ctx context.Context, key string) (bool, error) {
	return d.versionedDirectoryMarkerValidWithClient(ctx, d.client, key)
}

// versionedDirectoryMarkerValidWithClient validates both v2 links on one transaction connection.
func (d *driver) versionedDirectoryMarkerValidWithClient(ctx context.Context, client redis.Cmdable, key string) (bool, error) {
	marker := dirMarkerMember(key)
	pipe := client.Pipeline()
	self := pipe.SIsMember(ctx, d.versionedDirObjectsKey(key), marker)
	parent := pipe.SIsMember(ctx, d.versionedDirChildrenKey(parentDir(key)), encodeDirChild(key))
	if _, err := pipe.Exec(ctx); err != nil {
		return false, fmt.Errorf("storage: redis stat: %w", err)
	}
	return self.Val() && parent.Val(), nil
}

// dirMarkerMember encodes a v1 typed directory member without colliding with ordinary names.
func dirMarkerMember(key string) string {
	return "storage:v1:directory:" + base64.RawURLEncoding.EncodeToString([]byte(key))
}

// legacyDirMarkerMember reproduces the unescaped marker written by early releases.
func legacyDirMarkerMember(key string) string {
	return "dirmarker:" + key
}

// objectMember encodes a typed object member for the v2-only index sets.
func objectMember(key string) string {
	return "storage:v1:object:" + base64.RawURLEncoding.EncodeToString([]byte(key))
}

// parseIndexedMember decodes typed members while treating untagged values as legacy object names.
func parseIndexedMember(member string) (string, bool, error) {
	const (
		objectPrefix    = "storage:v1:object:"
		directoryPrefix = "storage:v1:directory:"
	)
	switch {
	case strings.HasPrefix(member, objectPrefix):
		key, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(member, objectPrefix))
		if err != nil {
			return "", false, fmt.Errorf("storage: redis invalid object index member: %w", err)
		}
		return string(key), false, nil
	case strings.HasPrefix(member, directoryPrefix):
		key, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(member, directoryPrefix))
		if err != nil {
			return "", false, fmt.Errorf("storage: redis invalid directory index member: %w", err)
		}
		return string(key), true, nil
	case strings.HasPrefix(member, "dirmarker:"):
		return strings.TrimPrefix(member, "dirmarker:"), true, nil
	default:
		return member, false, nil
	}
}

// indexPut writes an object's hierarchy only to collision-free v2 index sets.
func (d *driver) indexPut(ctx context.Context, pipe redis.Pipeliner, key string) {
	dirs := objectDirs(key)
	member := objectMember(key)
	pipe.SAdd(ctx, d.versionedDirObjectsKey(""), member)
	if len(dirs) == 0 {
		pipe.SAdd(ctx, d.versionedDirChildrenKey(""), encodeFileChild(key))
		return
	}
	for _, dir := range dirs {
		pipe.SAdd(ctx, d.versionedDirObjectsKey(dir), member)
	}
	pipe.SAdd(ctx, d.versionedDirChildrenKey(""), encodeDirChild(dirs[0]))
	for i := 0; i < len(dirs)-1; i++ {
		pipe.SAdd(ctx, d.versionedDirChildrenKey(dirs[i]), encodeDirChild(dirs[i+1]))
	}
	pipe.SAdd(ctx, d.versionedDirChildrenKey(dirs[len(dirs)-1]), encodeFileChild(key))
}

// indexDir records explicit directories only in v2 so reserved-looking paths remain ordinary names.
func (d *driver) indexDir(ctx context.Context, pipe redis.Pipeliner, key string) {
	marker := dirMarkerMember(key)
	pipe.SAdd(ctx, d.versionedDirObjectsKey(""), marker)
	dirs := objectDirs(key)
	for _, dir := range dirs {
		pipe.SAdd(ctx, d.versionedDirObjectsKey(dir), marker)
	}
	pipe.SAdd(ctx, d.versionedDirObjectsKey(key), marker)
	if len(dirs) == 0 {
		pipe.SAdd(ctx, d.versionedDirChildrenKey(""), encodeDirChild(key))
		return
	}
	pipe.SAdd(ctx, d.versionedDirChildrenKey(""), encodeDirChild(dirs[0]))
	for i := 0; i < len(dirs)-1; i++ {
		pipe.SAdd(ctx, d.versionedDirChildrenKey(dirs[i]), encodeDirChild(dirs[i+1]))
	}
	pipe.SAdd(ctx, d.versionedDirChildrenKey(dirs[len(dirs)-1]), encodeDirChild(key))
}

// queueUnindexDelete removes v2 metadata and only legacy members proven unable to alias typed entries.
func (d *driver) queueUnindexDelete(ctx context.Context, pipe redis.Pipeliner, key string) {
	pipe.Del(ctx, d.objectKey(key))
	pipe.SRem(ctx, d.versionedDirObjectsKey(""), objectMember(key))
	pipe.SRem(ctx, d.versionedDirChildrenKey(parentDir(key)), encodeFileChild(key))
	for _, dir := range objectDirs(key) {
		pipe.SRem(ctx, d.versionedDirObjectsKey(dir), objectMember(key))
	}

	if legacy, ok := unambiguousLegacyObjectMember(key); ok {
		pipe.SRem(ctx, d.dirObjectsKey(""), legacy)
		pipe.SRem(ctx, d.dirChildrenKey(parentDir(key)), encodeFileChild(key))
		for _, dir := range objectDirs(key) {
			pipe.SRem(ctx, d.dirObjectsKey(dir), legacy)
		}
	}
}

// unambiguousLegacyObjectMember excludes names that can also be another entry's typed index member.
func unambiguousLegacyObjectMember(key string) (string, bool) {
	if strings.HasPrefix(key, "storage:v1:object:") ||
		strings.HasPrefix(key, "storage:v1:directory:") ||
		strings.HasPrefix(key, "dirmarker:") {
		return "", false
	}
	return key, true
}

const pruneEmptyDirsScript = `
local arg = 1
for key = 1, #KEYS, 3 do
  if redis.call("SCARD", KEYS[key]) ~= 0 or redis.call("SCARD", KEYS[key + 1]) ~= 0 then
    break
  end
  redis.call("DEL", KEYS[key], KEYS[key + 1])
  redis.call("SREM", KEYS[key + 2], ARGV[arg])
  arg = arg + 1
end
return 1
`

// queuePruneEmptyDirs prunes both schemas without mixing their cardinality decisions.
func (d *driver) queuePruneEmptyDirs(ctx context.Context, pipe redis.Pipeliner, dirs []string) {
	d.queuePruneEmptyDirsForSchema(ctx, pipe, dirs, versionedIndex)
	d.queuePruneEmptyDirsForSchema(ctx, pipe, dirs, legacyIndex)
}

// queuePruneEmptyDirsForSchema keeps parent cleanup within one index generation.
func (d *driver) queuePruneEmptyDirsForSchema(ctx context.Context, pipe redis.Pipeliner, dirs []string, schema indexSchema) {
	if len(dirs) == 0 {
		return
	}
	keys := make([]string, 0, len(dirs)*3)
	args := make([]any, 0, len(dirs))
	for i := len(dirs) - 1; i >= 0; i-- {
		dir := dirs[i]
		objectsKey := d.dirObjectsKey(dir)
		childrenKey := d.dirChildrenKey(dir)
		parentChildrenKey := d.dirChildrenKey(parentDir(dir))
		if schema == versionedIndex {
			objectsKey = d.versionedDirObjectsKey(dir)
			childrenKey = d.versionedDirChildrenKey(dir)
			parentChildrenKey = d.versionedDirChildrenKey(parentDir(dir))
		}
		keys = append(keys, objectsKey, childrenKey, parentChildrenKey)
		args = append(args, encodeDirChild(dir))
	}
	pipe.Eval(ctx, pruneEmptyDirsScript, keys, args...)
}

// recursiveParentDirs returns visible ancestors for a caller-facing object path.
func recursiveParentDirs(p string) []string {
	if p == "" {
		return nil
	}
	parts := strings.Split(p, "/")
	if len(parts) <= 1 {
		return nil
	}
	out := make([]string, 0, len(parts)-1)
	for i := range parts[:len(parts)-1] {
		out = append(out, strings.Join(parts[:i+1], "/"))
	}
	return out
}

// objectDirs returns internal ancestor keys from nearest root through direct parent.
func objectDirs(key string) []string {
	parts := strings.Split(key, "/")
	if len(parts) <= 1 {
		return nil
	}
	out := make([]string, 0, len(parts)-1)
	for i := range parts[:len(parts)-1] {
		out = append(out, strings.Join(parts[:i+1], "/"))
	}
	return out
}

// parentDir returns the direct parent key or the logical root for top-level entries.
func parentDir(key string) string {
	if key == "" {
		return ""
	}
	idx := strings.LastIndexByte(key, '/')
	if idx == -1 {
		return ""
	}
	return key[:idx]
}

// encodeFileChild tags an object link in a directory child set.
func encodeFileChild(key string) string {
	return "f:" + key
}

// encodeDirChild tags a directory link in a directory child set.
func encodeDirChild(key string) string {
	return "d:" + key
}

// parseChildEntry separates a child-set type tag from its internal key.
func parseChildEntry(child string) (bool, string, error) {
	switch {
	case strings.HasPrefix(child, "f:"):
		return false, strings.TrimPrefix(child, "f:"), nil
	case strings.HasPrefix(child, "d:"):
		return true, strings.TrimPrefix(child, "d:"), nil
	default:
		return false, "", fmt.Errorf("storage: redis invalid child entry %q", child)
	}
}
