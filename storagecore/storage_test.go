package storagecore

import (
	"errors"
	"math"
	"testing"
)

// TestPaginateEntriesClampsBounds verifies untrusted page values cannot overflow slice bounds.
func TestPaginateEntriesClampsBounds(t *testing.T) {
	entries := []Entry{{Path: "a"}, {Path: "b"}, {Path: "c"}}
	tests := []struct {
		name       string
		offset     int
		limit      int
		wantOffset int
		wantLimit  int
		wantLen    int
	}{
		{name: "maximum limit", offset: 1, limit: math.MaxInt, wantOffset: 1, wantLimit: math.MaxInt, wantLen: 2},
		{name: "negative values use defaults", offset: -1, limit: -1, wantOffset: 0, wantLimit: 100, wantLen: 3},
		{name: "oversized offset", offset: math.MaxInt, limit: 1, wantOffset: 3, wantLimit: 1, wantLen: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			page := PaginateEntries(entries, tt.offset, tt.limit)
			if page.Offset != tt.wantOffset || page.Limit != tt.wantLimit || len(page.Entries) != tt.wantLen {
				t.Fatalf("PaginateEntries = %+v, entries=%d", page, len(page.Entries))
			}
		})
	}
}

// TestNormalizePathRejectsNUL keeps logical names portable across local and remote drivers.
func TestNormalizePathRejectsNUL(t *testing.T) {
	if _, err := NormalizePath("safe/\x00/object"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("NormalizePath NUL error = %v", err)
	}
}
