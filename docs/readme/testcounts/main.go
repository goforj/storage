//go:build ignore
// +build ignore

// Command testcounts reports executed test totals from reproducible repository data.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// main reports executed test totals and propagates errors to the shell.
func main() {
	if err := run(); err != nil {
		fmt.Println("Error:", err)
		os.Exit(1)
	}
}

// run counts executed tests in the root module and shared integration suite.
func run() error {
	root, err := findRoot()
	if err != nil {
		return err
	}

	integrationDir := filepath.Join(root, "integration")

	integrationNames, err := integrationTopLevelTests(integrationDir)
	if err != nil {
		return fmt.Errorf("integration top-level tests: %w", err)
	}

	unitCount, err := countRunEvents(root, []string{"test", "./...", "-run", "Test", "-count=1", "-json"})
	if err != nil {
		return fmt.Errorf("count unit test runs: %w", err)
	}

	integrationCount, err := countIntegrationRunEvents(integrationDir, integrationNames)
	if err != nil {
		return fmt.Errorf("count integration test runs: %w", err)
	}

	fmt.Printf("Root-module tests: %d\nIntegration tests: %d\n", unitCount, integrationCount)
	return nil
}

// countRunEvents executes a Go test command and counts its structured run events.
func countRunEvents(root string, args []string) (int, error) {
	cmd := exec.Command("go", args...)
	cmd.Dir = root

	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	if err := cmd.Run(); err != nil {
		return 0, fmt.Errorf("go %s: %w\n%s", strings.Join(args, " "), err, out.String())
	}

	return countRunEventsFromJSON(out.Bytes(), nil)
}

// countIntegrationRunEvents counts only discovered top-level integration suites and their subtests.
func countIntegrationRunEvents(integrationDir string, integrationNames map[string]struct{}) (int, error) {
	runPattern := buildTopLevelRunPattern(integrationNames)
	if runPattern == "" {
		return 0, nil
	}

	args := []string{"test", "-tags=integration", "./all", "-run", runPattern, "-count=1", "-json"}
	cmd := exec.Command("go", args...)
	cmd.Dir = integrationDir
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	if err := cmd.Run(); err != nil {
		return 0, fmt.Errorf("go %s: %w\n%s", strings.Join(args, " "), err, out.String())
	}

	return countRunEventsFromJSON(out.Bytes(), integrationNames)
}

// integrationTopLevelTests discovers test entry points in integration-tagged source files.
func integrationTopLevelTests(root string) (map[string]struct{}, error) {
	names := map[string]struct{}{}

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if info.Name() == ".git" || info.Name() == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}

		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !hasIntegrationBuildTag(src) {
			return nil
		}

		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			return err
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil {
				continue
			}
			if strings.HasPrefix(fn.Name.Name, "Test") {
				names[fn.Name.Name] = struct{}{}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return names, nil
}

// hasIntegrationBuildTag recognizes either supported spelling of the integration constraint.
func hasIntegrationBuildTag(src []byte) bool {
	text := string(src)
	return strings.Contains(text, "//go:build integration") || strings.Contains(text, "// +build integration")
}

// buildTopLevelRunPattern creates a stable exact filter that also admits nested subtests.
func buildTopLevelRunPattern(names map[string]struct{}) string {
	if len(names) == 0 {
		return ""
	}
	keys := make([]string, 0, len(names))
	for k := range names {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, regexp.QuoteMeta(k))
	}
	return "^(" + strings.Join(parts, "|") + ")(/.*)?$"
}

// countRunEventsFromJSON counts valid run events, optionally restricted to known integration roots.
func countRunEventsFromJSON(data []byte, topLevelNames map[string]struct{}) (int, error) {
	var total int
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var event struct {
			Action string `json:"Action"`
			Test   string `json:"Test"`
		}
		if err := json.Unmarshal(line, &event); err != nil {
			continue
		}
		if event.Action != "run" || event.Test == "" {
			continue
		}
		if topLevelNames == nil {
			total++
			continue
		}
		top := event.Test
		if i := strings.IndexByte(top, '/'); i >= 0 {
			top = top[:i]
		}
		if _, ok := topLevelNames[top]; ok {
			total++
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, err
	}
	return total, nil
}

// findRoot locates the repository from each working directory supported by the command.
func findRoot() (string, error) {
	wd, _ := os.Getwd()
	for _, c := range []string{wd, filepath.Join(wd, ".."), filepath.Join(wd, "..", ".."), filepath.Join(wd, "..", "..", "..")} {
		c = filepath.Clean(c)
		if fileExists(filepath.Join(c, "go.mod")) && fileExists(filepath.Join(c, "README.md")) && fileExists(filepath.Join(c, "storage.go")) {
			return c, nil
		}
	}
	return "", fmt.Errorf("could not find project root")
}

// fileExists reports whether a candidate repository marker is present.
func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
