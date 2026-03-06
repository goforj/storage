//go:build ignore
// +build ignore

package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	apiStart = "<!-- api:embed:start -->"
	apiEnd   = "<!-- api:embed:end -->"
)

func main() {
	if err := run(); err != nil {
		fmt.Println("Error:", err)
		os.Exit(1)
	}
	fmt.Println("✔ API section updated in README.md")
}

func run() error {
	root, err := findRoot()
	if err != nil {
		return err
	}

	funcs, err := parseFuncs(root)
	if err != nil {
		return err
	}

	api := renderAPI(funcs)

	readmePath := filepath.Join(root, "README.md")
	data, err := os.ReadFile(readmePath)
	if err != nil {
		return err
	}

	out, err := replaceAPISection(string(data), api)
	if err != nil {
		return err
	}

	return os.WriteFile(readmePath, []byte(out), 0o644)
}

type FuncDoc struct {
	Name        string
	DisplayName string
	Anchor      string
	Group       string
	Description string
	Examples    []Example
}

type Example struct {
	Label string
	Code  string
	Line  int
}

var (
	groupHeader   = regexp.MustCompile(`(?i)^\s*@group\s+(.+)$`)
	exampleHeader = regexp.MustCompile(`(?i)^\s*Example:\s*(.*)$`)
)

func parseFuncs(root string) ([]*FuncDoc, error) {
	funcs := map[string]*FuncDoc{}

	if err := parseFuncsInDir(funcs, root); err != nil {
		return nil, err
	}
	for _, rel := range []string{
		"driver/localstorage",
		"driver/s3storage",
		"driver/gcsstorage",
		"driver/sftpstorage",
		"driver/ftpstorage",
		"driver/dropboxstorage",
		"driver/rclonestorage",
	} {
		if err := parseFuncsInDir(funcs, filepath.Join(root, rel)); err != nil {
			return nil, err
		}
	}

	out := make([]*FuncDoc, 0, len(funcs))
	for _, fd := range funcs {
		sort.Slice(fd.Examples, func(i, j int) bool {
			return fd.Examples[i].Line < fd.Examples[j].Line
		})
		out = append(out, fd)
	}

	return out, nil
}

func parseFuncsInDir(funcs map[string]*FuncDoc, dir string) error {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(
		fset,
		dir,
		func(info os.FileInfo) bool { return !strings.HasSuffix(info.Name(), "_test.go") },
		parser.ParseComments,
	)
	if err != nil {
		return err
	}

	pkgName, err := selectPackage(pkgs)
	if err != nil {
		return err
	}
	pkg, ok := pkgs[pkgName]
	if !ok {
		return fmt.Errorf(`package %q not found`, pkgName)
	}

	for _, file := range pkg.Files {
		for _, decl := range file.Decls {
			if gen, ok := decl.(*ast.GenDecl); ok && gen.Doc != nil && gen.Tok == token.TYPE {
				for _, spec := range gen.Specs {
					ts, ok := spec.(*ast.TypeSpec)
					if !ok || !ast.IsExported(ts.Name.Name) {
						continue
					}
					displayName := ts.Name.Name
					anchor := strings.ToLower(ts.Name.Name)
					key := displayName
					if pkgName != "storage" {
						displayName = pkgName + "." + ts.Name.Name
						anchor = strings.ToLower(pkgName + "-" + ts.Name.Name)
						key = displayName
					}
					fd := &FuncDoc{
						Name:        ts.Name.Name,
						DisplayName: displayName,
						Anchor:      anchor,
						Group:       extractGroup(gen.Doc),
						Description: extractDescription(gen.Doc),
						Examples:    extractExamplesFromGroup(fset, gen.Doc),
					}
					if existing, ok := funcs[key]; ok {
						existing.Examples = append(existing.Examples, fd.Examples...)
					} else {
						funcs[key] = fd
					}

					if iface, ok := ts.Type.(*ast.InterfaceType); ok {
						typeGroup := extractGroup(gen.Doc)
						for _, field := range iface.Methods.List {
							if len(field.Names) == 0 || field.Doc == nil {
								continue
							}
							name := field.Names[0].Name
							if !ast.IsExported(name) {
								continue
							}
							displayName := displayName + "." + name
							anchor := strings.ToLower(ts.Name.Name + "-" + name)
							key := displayName
							group := extractGroupWithDefault(field.Doc, typeGroup)
							fd := &FuncDoc{
								Name:        name,
								DisplayName: displayName,
								Anchor:      anchor,
								Group:       group,
								Description: extractDescription(field.Doc),
								Examples:    extractExamplesFromGroup(fset, field.Doc),
							}
							if existing, ok := funcs[key]; ok {
								existing.Examples = append(existing.Examples, fd.Examples...)
							} else {
								funcs[key] = fd
							}
						}
					}
				}
			}

			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Doc == nil || !ast.IsExported(fn.Name.Name) {
				continue
			}
			if recv := extractReceiverName(fn); recv != "" && !ast.IsExported(recv) {
				continue
			}

			displayName := fn.Name.Name
			anchor := strings.ToLower(fn.Name.Name)
			key := displayName

			if recv := extractReceiverName(fn); recv != "" {
				displayName = recv + "." + fn.Name.Name
				anchor = strings.ToLower(recv + "-" + fn.Name.Name)
				key = displayName
			} else if pkgName != "storage" {
				displayName = pkgName + "." + fn.Name.Name
				anchor = strings.ToLower(pkgName + "-" + fn.Name.Name)
				key = displayName
			}

			fd := &FuncDoc{
				Name:        fn.Name.Name,
				DisplayName: displayName,
				Anchor:      anchor,
				Group:       extractGroup(fn.Doc),
				Description: extractDescription(fn.Doc),
				Examples:    extractExamples(fset, fn),
			}

			if existing, ok := funcs[key]; ok {
				existing.Examples = append(existing.Examples, fd.Examples...)
			} else {
				funcs[key] = fd
			}
		}
	}

	return nil
}

func extractGroup(group *ast.CommentGroup) string {
	for _, c := range group.List {
		line := strings.TrimSpace(strings.TrimPrefix(c.Text, "//"))
		if m := groupHeader.FindStringSubmatch(line); m != nil {
			return strings.TrimSpace(m[1])
		}
	}
	return "Other"
}

func extractGroupWithDefault(group *ast.CommentGroup, fallback string) string {
	if group == nil {
		return fallback
	}
	for _, c := range group.List {
		line := strings.TrimSpace(strings.TrimPrefix(c.Text, "//"))
		if m := groupHeader.FindStringSubmatch(line); m != nil {
			return strings.TrimSpace(m[1])
		}
	}
	return fallback
}

func extractDescription(group *ast.CommentGroup) string {
	var lines []string
	for _, c := range group.List {
		line := strings.TrimSpace(strings.TrimPrefix(c.Text, "//"))
		if exampleHeader.MatchString(line) || groupHeader.MatchString(line) {
			break
		}
		if len(lines) == 0 && line == "" {
			continue
		}
		lines = append(lines, line)
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func extractExamples(fset *token.FileSet, fn *ast.FuncDecl) []Example {
	return extractExamplesFromGroup(fset, fn.Doc)
}

func extractExamplesFromGroup(fset *token.FileSet, group *ast.CommentGroup) []Example {
	var out []Example
	var current []string
	var label string
	var start int
	inExample := false

	flush := func() {
		if len(current) == 0 {
			return
		}
		out = append(out, Example{
			Label: label,
			Code:  strings.Join(normalizeIndent(current), "\n"),
			Line:  start,
		})
		current = nil
		label = ""
		inExample = false
	}

	for _, c := range group.List {
		raw := strings.TrimPrefix(c.Text, "//")
		line := strings.TrimSpace(raw)
		if m := exampleHeader.FindStringSubmatch(line); m != nil {
			flush()
			inExample = true
			label = strings.TrimSpace(m[1])
			start = fset.Position(c.Slash).Line
			continue
		}
		if !inExample {
			continue
		}
		current = append(current, raw)
	}

	flush()
	return out
}

func extractReceiverName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return ""
	}
	return recvTypeName(fn.Recv.List[0].Type)
}

func recvTypeName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return recvTypeName(t.X)
	case *ast.IndexExpr:
		return recvTypeName(t.X)
	case *ast.IndexListExpr:
		return recvTypeName(t.X)
	case *ast.SelectorExpr:
		return t.Sel.Name
	default:
		return ""
	}
}

func selectPackage(pkgs map[string]*ast.Package) (string, error) {
	if len(pkgs) == 0 {
		return "", fmt.Errorf("no packages found")
	}
	if len(pkgs) == 1 {
		for name := range pkgs {
			return name, nil
		}
	}
	type candidate struct {
		name  string
		count int
	}
	candidates := make([]candidate, 0, len(pkgs))
	for name, pkg := range pkgs {
		candidates = append(candidates, candidate{name: name, count: len(pkg.Files)})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].count == candidates[j].count {
			return candidates[i].name < candidates[j].name
		}
		return candidates[i].count > candidates[j].count
	})
	for _, cand := range candidates {
		if cand.name != "main" {
			return cand.name, nil
		}
	}
	return candidates[0].name, nil
}

func renderAPI(funcs []*FuncDoc) string {
	byGroup := map[string][]*FuncDoc{}
	for _, fd := range funcs {
		byGroup[fd.Group] = append(byGroup[fd.Group], fd)
	}

	groupNames := make([]string, 0, len(byGroup))
	for g := range byGroup {
		groupNames = append(groupNames, g)
	}
	sort.Strings(groupNames)

	var buf bytes.Buffer
	buf.WriteString("## API Index\n\n")
	buf.WriteString("| Group | Functions |\n")
	buf.WriteString("|------:|-----------|\n")

	for _, group := range groupNames {
		sort.Slice(byGroup[group], func(i, j int) bool {
			return byGroup[group][i].DisplayName < byGroup[group][j].DisplayName
		})
		var links []string
		for _, fn := range byGroup[group] {
			links = append(links, fmt.Sprintf("[%s](#%s)", fn.DisplayName, fn.Anchor))
		}
		buf.WriteString(fmt.Sprintf("| **%s** | %s |\n", group, strings.Join(links, " ")))
	}

	buf.WriteString("\n\n")
	for _, group := range groupNames {
		buf.WriteString("## " + group + "\n\n")
		for _, fn := range byGroup[group] {
			buf.WriteString(fmt.Sprintf("### <a id=\"%s\"></a>%s\n\n", fn.Anchor, fn.DisplayName))
			if fn.Description != "" {
				buf.WriteString(fn.Description + "\n\n")
			}
			for _, ex := range fn.Examples {
				if ex.Label != "" && len(fn.Examples) > 1 {
					buf.WriteString(fmt.Sprintf("_Example: %s_\n\n", ex.Label))
				}
				buf.WriteString("```go\n")
				buf.WriteString(strings.TrimSpace(ex.Code))
				buf.WriteString("\n```\n\n")
			}
		}
	}

	return strings.TrimRight(buf.String(), "\n")
}

func replaceAPISection(readme, api string) (string, error) {
	start := strings.Index(readme, apiStart)
	end := strings.Index(readme, apiEnd)
	if start == -1 || end == -1 || end < start {
		return readme, fmt.Errorf("API anchors not found or malformed")
	}
	var out bytes.Buffer
	out.WriteString(readme[:start+len(apiStart)])
	out.WriteString("\n\n")
	out.WriteString(api)
	out.WriteString("\n")
	out.WriteString(readme[end:])
	return out.String(), nil
}

func findRoot() (string, error) {
	wd, _ := os.Getwd()
	for _, c := range []string{wd, filepath.Join(wd, ".."), filepath.Join(wd, "..", "..")} {
		c = filepath.Clean(c)
		if fileExists(filepath.Join(c, "go.mod")) && fileExists(filepath.Join(c, "README.md")) && fileExists(filepath.Join(c, "storage.go")) {
			return c, nil
		}
	}
	return "", fmt.Errorf("could not find project root")
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func normalizeIndent(lines []string) []string {
	min := -1
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		n := len(l) - len(strings.TrimLeft(l, " \t"))
		if min == -1 || n < min {
			min = n
		}
	}
	if min <= 0 {
		return lines
	}
	out := make([]string, len(lines))
	for i, l := range lines {
		if len(l) >= min {
			out[i] = l[min:]
		} else {
			out[i] = strings.TrimLeft(l, " \t")
		}
	}
	return out
}
