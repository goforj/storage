package rclonestorage

import (
	"strings"
	"testing"
)

func TestRenderLocal(t *testing.T) {
	_, err := RenderLocal(LocalRemote{})
	if err == nil {
		t.Fatalf("expected error for missing name")
	}

	out, err := RenderLocal(LocalRemote{Name: "localdisk"})
	if err != nil {
		t.Fatalf("RenderLocal: %v", err)
	}
	if want := "[localdisk]\n"; out[:len(want)] != want {
		t.Fatalf("output missing section header, got %q", out)
	}
	if !containsLine(out, "type = local") {
		t.Fatalf("expected type line in output: %q", out)
	}
}

func TestMustRenderLocalPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatalf("expected panic for invalid config")
		}
	}()
	_ = MustRenderLocal(LocalRemote{Name: ""})
}

func containsLine(body, line string) bool {
	return strings.Contains("\n"+body+"\n", "\n"+line+"\n")
}
