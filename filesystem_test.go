package storage

import (
	"testing"
)

func TestNormalizePath(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{"empty", "", "", false},
		{"trims and cleans", " /foo//bar ", "foo/bar", false},
		{"normalizes backslashes", `foo\\bar\\baz.txt`, "foo/bar/baz.txt", false},
		{"normalizes leading backslash root", `\\foo\\bar`, "foo/bar", false},
		{"root slash becomes empty", "/", "", false},
		{"single dot", ".", "", false},
		{"reject parent traversal", "..", "", true},
		{"reject backslash parent traversal", `..\\foo`, "", true},
		{"reject prefixed parent", "../foo", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizePath(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("NormalizePath error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("NormalizePath got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestJoinPrefix(t *testing.T) {
	tests := []struct {
		prefix string
		p      string
		want   string
	}{
		{"", "", ""},
		{"", "file.txt", "file.txt"},
		{"base", "", "base"},
		{"base", "file.txt", "base/file.txt"},
		{"nested/base", "sub/file.txt", "nested/base/sub/file.txt"},
	}
	for _, tt := range tests {
		got := JoinPrefix(tt.prefix, tt.p)
		if got != tt.want {
			t.Fatalf("JoinPrefix(%q,%q)=%q want %q", tt.prefix, tt.p, got, tt.want)
		}
	}
}
