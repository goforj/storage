//go:build ignore
// +build ignore

package main

import (
	"strings"
	"testing"
)

// TestValidateAPI checks that every entry needs an example and a topic group.
func TestValidateAPI(t *testing.T) {
	examples := []Example{{Code: "fmt.Println(1)"}}
	for _, tt := range []struct {
		name string
		doc  FuncDoc
		want string
	}{
		{"complete", FuncDoc{DisplayName: "Build", Group: "Construction", Examples: examples}, ""},
		{"no group", FuncDoc{DisplayName: "Build", Examples: examples}, "Build needs a topic group"},
		{"catch-all group", FuncDoc{DisplayName: "Build", Group: "Other", Examples: examples}, "Build needs a topic group"},
		{"no example", FuncDoc{DisplayName: "Build", Group: "Construction"}, "Build needs an example"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := validateAPI([]*FuncDoc{&tt.doc})
			if tt.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("validateAPI error = %v, want %q", err, tt.want)
			}
		})
	}
}
