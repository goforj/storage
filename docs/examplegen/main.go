//go:build ignore
// +build ignore

package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

func main() {
	if err := run(); err != nil {
		fmt.Println("Error:", err)
		os.Exit(1)
	}
	fmt.Println("✔ Examples generated in ./examples/")
}

func run() error {
	root, err := findRoot()
	if err != nil {
		return err
	}

	examplesDir := filepath.Join(root, "examples")
	if err := os.MkdirAll(examplesDir, 0o755); err != nil {
		return err
	}

	modPath, err := modulePath(root)
	if err != nil {
		return err
	}

	funcs := map[string]*FuncDoc{}
	if err := collectExamplesFromDir(funcs, root, modPath, ""); err != nil {
		return err
	}
	for _, rel := range []string{
		"driver/localstorage",
		"driver/memorystorage",
		"driver/redisstorage",
		"driver/s3storage",
		"driver/gcsstorage",
		"driver/sftpstorage",
		"driver/ftpstorage",
		"driver/dropboxstorage",
		"driver/rclonestorage",
	} {
		dir := filepath.Join(root, rel)
		driverModPath, err := modulePath(dir)
		if err != nil {
			return err
		}
		if err := collectExamplesFromDir(funcs, dir, driverModPath, ""); err != nil {
			return err
		}
	}

	for _, fd := range funcs {
		sort.Slice(fd.Examples, func(i, j int) bool {
			return fd.Examples[i].Line < fd.Examples[j].Line
		})
		if err := writeMain(examplesDir, fd, fd.ImportPath); err != nil {
			return err
		}
	}

	return nil
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

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

func modulePath(root string) (string, error) {
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module ")), nil
		}
	}
	return "", fmt.Errorf("module path not found in go.mod")
}

type FuncDoc struct {
	Name        string
	Slug        string
	ImportPath  string
	Group       string
	Description string
	Examples    []Example
}

type Example struct {
	FuncName string
	File     string
	Label    string
	Line     int
	Code     string
}

var exampleHeader = regexp.MustCompile(`(?i)^\s*Example:\s*(.*)$`)
var groupHeader = regexp.MustCompile(`(?i)^\s*@group\s+(.+)$`)

type importPattern struct {
	re  *regexp.Regexp
	imp string
}

type docLine struct {
	text string
	pos  token.Pos
}

func collectExamplesFromDir(funcs map[string]*FuncDoc, dir, importPath, slugPrefix string) error {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, nil, parser.ParseComments)
	if err != nil {
		return err
	}

	pkgName, err := selectPackage(pkgs)
	if err != nil {
		return err
	}
	pkg, ok := pkgs[pkgName]
	if !ok {
		return fmt.Errorf(`package %q not found in %s`, pkgName, dir)
	}

	prefix := slugPrefix
	if prefix == "" && pkgName != "storage" {
		prefix = pkgName + "_"
	}

	for filename, file := range pkg.Files {
		if strings.Contains(filename, "_test.go") {
			continue
		}
		for name, fd := range extractFuncDocs(fset, filename, file) {
			fd.ImportPath = importPath
			if prefix != "" {
				fd.Slug = prefix + strings.ToLower(fd.Slug)
				name = fd.Slug
			}
			if existing, ok := funcs[name]; ok {
				existing.Examples = append(existing.Examples, fd.Examples...)
			} else {
				funcs[name] = fd
			}
		}
	}

	return nil
}

func extractFuncDocs(fset *token.FileSet, filename string, file *ast.File) map[string]*FuncDoc {
	out := map[string]*FuncDoc{}

	for _, decl := range file.Decls {
		if gen, ok := decl.(*ast.GenDecl); ok && gen.Doc != nil && gen.Tok == token.TYPE {
			for _, spec := range gen.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok || !ast.IsExported(ts.Name.Name) {
					continue
				}
				fd := &FuncDoc{
					Name:        ts.Name.Name,
					Slug:        ts.Name.Name,
					Group:       extractGroup(gen.Doc),
					Description: extractFuncDescription(gen.Doc),
					Examples:    extractExamplesFromGroup(fset, filename, ts.Name.Name, gen.Doc),
				}
				out[fd.Slug] = fd

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
						slug := ts.Name.Name + "_" + name
						out[slug] = &FuncDoc{
							Name:        name,
							Slug:        slug,
							Group:       extractGroupWithDefault(field.Doc, typeGroup),
							Description: extractFuncDescription(field.Doc),
							Examples:    extractExamplesFromGroup(fset, filename, name, field.Doc),
						}
					}
				}
			}
		}

		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Doc == nil {
			continue
		}

		name := fn.Name.Name
		if !ast.IsExported(name) {
			continue
		}
		if fn.Recv != nil && len(fn.Recv.List) > 0 {
			if recv := recvTypeName(fn.Recv.List[0].Type); recv != "" && !ast.IsExported(recv) {
				continue
			}
		}

		slug := funcSlug(fn)
		out[slug] = &FuncDoc{
			Name:        name,
			Slug:        slug,
			Group:       extractGroup(fn.Doc),
			Description: extractFuncDescription(fn.Doc),
			Examples:    extractExamplesFromGroup(fset, filename, name, fn.Doc),
		}
	}

	return out
}

func funcSlug(fn *ast.FuncDecl) string {
	name := fn.Name.Name
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return name
	}
	recv := recvTypeName(fn.Recv.List[0].Type)
	if recv == "" {
		return name
	}
	return recv + "_" + name
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

func extractGroup(group *ast.CommentGroup) string {
	lines := docLines(group)
	for _, dl := range lines {
		if m := groupHeader.FindStringSubmatch(strings.TrimSpace(dl.text)); m != nil {
			return strings.TrimSpace(m[1])
		}
	}
	return "Other"
}

func extractGroupWithDefault(group *ast.CommentGroup, fallback string) string {
	if group == nil {
		return fallback
	}
	for _, dl := range docLines(group) {
		if m := groupHeader.FindStringSubmatch(strings.TrimSpace(dl.text)); m != nil {
			return strings.TrimSpace(m[1])
		}
	}
	return fallback
}

func extractFuncDescription(group *ast.CommentGroup) string {
	lines := docLines(group)
	var desc []string

	for _, dl := range lines {
		trimmed := strings.TrimSpace(dl.text)
		if exampleHeader.MatchString(trimmed) || groupHeader.MatchString(trimmed) {
			break
		}
		if len(desc) == 0 && trimmed == "" {
			continue
		}
		desc = append(desc, dl.text)
	}

	for len(desc) > 0 && strings.TrimSpace(desc[len(desc)-1]) == "" {
		desc = desc[:len(desc)-1]
	}

	return strings.Join(desc, "\n")
}

func docLines(group *ast.CommentGroup) []docLine {
	var lines []docLine
	for _, c := range group.List {
		text := c.Text
		if strings.HasPrefix(text, "//") {
			line := strings.TrimPrefix(text, "//")
			if strings.HasPrefix(line, " ") {
				line = line[1:]
			}
			if strings.HasPrefix(line, "\t") {
				line = line[1:]
			}
			lines = append(lines, docLine{text: line, pos: c.Slash})
		}
	}
	return lines
}

func extractExamplesFromGroup(fset *token.FileSet, filename, funcName string, group *ast.CommentGroup) []Example {
	var out []Example
	lines := docLines(group)

	var label string
	var collected []string
	var startLine int
	inExample := false

	flush := func() {
		if len(collected) == 0 {
			return
		}
		out = append(out, Example{
			FuncName: funcName,
			File:     filename,
			Label:    label,
			Line:     startLine,
			Code:     strings.Join(collected, "\n"),
		})
		collected = nil
		label = ""
		inExample = false
	}

	for _, dl := range lines {
		raw := dl.text
		trimmed := strings.TrimSpace(raw)

		if m := exampleHeader.FindStringSubmatch(trimmed); m != nil {
			flush()
			inExample = true
			label = strings.TrimSpace(m[1])
			startLine = fset.Position(dl.pos).Line
			continue
		}
		if !inExample {
			continue
		}
		collected = append(collected, raw)
	}

	flush()
	return out
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

func writeMain(base string, fd *FuncDoc, importPath string) error {
	if len(fd.Examples) == 0 {
		return nil
	}
	if importPath == "" {
		return fmt.Errorf("import path cannot be empty")
	}

	if err := removeGeneratedExampleDirs(base, fd.Slug); err != nil {
		return err
	}

	for i, ex := range fd.Examples {
		slug := strings.ToLower(fd.Slug)
		if len(fd.Examples) > 1 {
			slug += "_" + slugify(ex.Label)
			if ex.Label == "" {
				slug += fmt.Sprintf("_%d", i+1)
			}
		}
		if err := writeExampleMain(base, slug, fd, ex, importPath); err != nil {
			return err
		}
	}

	return nil
}

func writeExampleMain(base, slug string, fd *FuncDoc, ex Example, importPath string) error {
	dir := filepath.Join(base, slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	var buf bytes.Buffer
	buf.WriteString("// Code generated by docs/examplegen/main.go. DO NOT EDIT.\n\n")
	buf.WriteString("package main\n\n")

	imports := map[string]bool{importPath: true}
	patternImports := []importPattern{
		{re: regexp.MustCompile(`\bfmt\.`), imp: "fmt"},
		{re: regexp.MustCompile(`\bbytes\.`), imp: "bytes"},
		{re: regexp.MustCompile(`\berrors\.`), imp: "errors"},
		{re: regexp.MustCompile(`\bstrings\.`), imp: "strings"},
		{re: regexp.MustCompile(`\bio\.`), imp: "io"},
		{re: regexp.MustCompile(`\bos\.`), imp: "os"},
		{re: regexp.MustCompile(`\bhttp\.`), imp: "net/http"},
		{re: regexp.MustCompile(`\bhttptest\.`), imp: "net/http/httptest"},
		{re: regexp.MustCompile(`\bcontext\.`), imp: "context"},
		{re: regexp.MustCompile(`\bregexp\.`), imp: "regexp"},
		{re: regexp.MustCompile(`\btime\.`), imp: "time"},
		{re: regexp.MustCompile(`\bfilepath\.`), imp: "path/filepath"},
		{re: regexp.MustCompile(`\blocalstorage\.`), imp: "github.com/goforj/storage/driver/localstorage"},
		{re: regexp.MustCompile(`\bmemorystorage\.`), imp: "github.com/goforj/storage/driver/memorystorage"},
		{re: regexp.MustCompile(`\bredisstorage\.`), imp: "github.com/goforj/storage/driver/redisstorage"},
		{re: regexp.MustCompile(`\bs3storage\.`), imp: "github.com/goforj/storage/driver/s3storage"},
		{re: regexp.MustCompile(`\bgcsstorage\.`), imp: "github.com/goforj/storage/driver/gcsstorage"},
		{re: regexp.MustCompile(`\bsftpstorage\.`), imp: "github.com/goforj/storage/driver/sftpstorage"},
		{re: regexp.MustCompile(`\bftpstorage\.`), imp: "github.com/goforj/storage/driver/ftpstorage"},
		{re: regexp.MustCompile(`\bdropboxstorage\.`), imp: "github.com/goforj/storage/driver/dropboxstorage"},
		{re: regexp.MustCompile(`\brclonestorage\.`), imp: "github.com/goforj/storage/driver/rclonestorage"},
		{re: regexp.MustCompile(`\bstorage\.`), imp: "github.com/goforj/storage"},
	}
	for _, pattern := range patternImports {
		if pattern.re.MatchString(ex.Code) {
			imports[pattern.imp] = true
		}
	}

	if len(imports) == 1 {
		for imp := range imports {
			buf.WriteString("import " + fmt.Sprintf("%q", imp) + "\n\n")
		}
	} else {
		buf.WriteString("import (\n")
		keys := make([]string, 0, len(imports))
		for k := range imports {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, imp := range keys {
			buf.WriteString("\t\"" + imp + "\"\n")
		}
		buf.WriteString(")\n\n")
	}

	buf.WriteString("func main() {\n")
	if fd.Description != "" {
		for _, line := range strings.Split(fd.Description, "\n") {
			buf.WriteString("\t// " + line + "\n")
		}
		buf.WriteString("\n")
	}
	if ex.Label != "" {
		buf.WriteString("\t// Example: " + ex.Label + "\n")
	}
	for _, line := range strings.Split(strings.TrimLeft(ex.Code, "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			buf.WriteString("\n")
		} else {
			buf.WriteString("\t" + line + "\n")
		}
	}
	buf.WriteString("}\n")

	formatted, err := format.Source(buf.Bytes())
	if err != nil {
		return fmt.Errorf("format example file: %w", err)
	}
	return os.WriteFile(filepath.Join(dir, "main.go"), formatted, 0o644)
}

func removeGeneratedExampleDirs(base, slug string) error {
	patterns := []string{
		filepath.Join(base, strings.ToLower(slug)),
		filepath.Join(base, strings.ToLower(slug)+"_*"),
	}
	for _, pattern := range patterns {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return err
		}
		for _, match := range matches {
			mainPath := filepath.Join(match, "main.go")
			data, err := os.ReadFile(mainPath)
			if err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return err
			}
			if !bytes.HasPrefix(data, []byte("// Code generated by docs/examplegen/main.go. DO NOT EDIT.")) &&
				!bytes.HasPrefix(data, []byte("//go:build ignore")) {
				continue
			}
			if err := os.RemoveAll(match); err != nil {
				return err
			}
		}
	}
	return nil
}

func slugify(label string) string {
	label = strings.TrimSpace(strings.ToLower(label))
	if label == "" {
		return ""
	}
	var b strings.Builder
	lastUnderscore := false
	for _, r := range label {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastUnderscore = false
		default:
			if !lastUnderscore {
				b.WriteByte('_')
				lastUnderscore = true
			}
		}
	}
	out := strings.Trim(b.String(), "_")
	if out == "" {
		return "example"
	}
	return out
}
