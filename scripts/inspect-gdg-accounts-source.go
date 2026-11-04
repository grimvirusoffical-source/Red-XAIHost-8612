// Read-only metadata inspection. This program never imports/builds/runs the app.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const sourceDirectory = "/root/gdg-signing-api-20261028/RedX/cmd/redx-gdg-accounts"
const storageDirectory = "/root/gdg-signing-api-20261028/RedX/internal/gdgaccounts"
const maxFiles = 64
const maxFileBytes = 1024 * 1024
const maxTotalBytes = 8 * 1024 * 1024
const maxNodes = 150000
const maxDeclarations = 4000
const maxCalls = 8000
const maxOutputBytes = 1024 * 1024

var filenamePattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*\.go$`)
var importPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*(/[a-z0-9][a-z0-9._~-]*)*$`)
var envNamePattern = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,127}$`)

// Reviewed account-contract words only. Unknown routes are counted, not emitted.
// No arbitrary suffixes, query, credentials, fragments, hostnames or %-encoding.
var routePattern = regexp.MustCompile(`^((GET|HEAD|POST|PUT|PATCH|DELETE|OPTIONS) )?/(api|v1)/accounts/(signup|register|login|login/2fa|me|logout|activate|verification|verification/resend|resend|resend-verification|mfa|mfa/enroll|mfa/verify|mfa/disable|sessions|sessions/\{id\}|password|password/reset|password/forgot)/?$`)
var focusPattern = regexp.MustCompile(`(?i)(close|shutdown|signal|notify|listen|serve|storage|store|database|checkpoint|backup|restore|flush|sync|lock|opendb|openfile)`)

type fieldMetadata struct {
	Names     []string `json:"names,omitempty"`
	TypeNames []string `json:"typeNames,omitempty"`
}
type declaration struct {
	Kind          string          `json:"kind"`
	Name          string          `json:"name"`
	Line          int             `json:"line"`
	ReceiverNames []string        `json:"receiverNames,omitempty"`
	TypeKind      string          `json:"typeKind,omitempty"`
	TypeNames     []string        `json:"typeNames,omitempty"`
	Fields        []fieldMetadata `json:"fields,omitempty"`
	Parameters    []fieldMetadata `json:"parameters,omitempty"`
	Results       []fieldMetadata `json:"results,omitempty"`
}
type imported struct {
	Path      string `json:"path"`
	Alias     string `json:"alias,omitempty"`
	LocalLead string `json:"localRepoRelativeLead,omitempty"`
}
type callMetadata struct {
	Callee               string   `json:"callee"`
	Line                 int      `json:"line"`
	Column               int      `json:"column"`
	Function             string   `json:"function"`
	FunctionLiteralDepth int      `json:"functionLiteralDepth,omitempty"`
	LexicalContexts      []string `json:"lexicalContexts,omitempty"`
	DirectlyDeferred     bool     `json:"directlyDeferred,omitempty"`
	DirectlyGo           bool     `json:"directlyGo,omitempty"`
	ReceiverDynamic      bool     `json:"receiverDynamic,omitempty"`
	FocusHint            bool     `json:"focusHint,omitempty"`
	ReviewedRoute        string   `json:"reviewedRoute,omitempty"`
	EnvironmentName      string   `json:"environmentName,omitempty"`
}
type fileMetadata struct {
	Name                         string         `json:"name"`
	Bytes                        int64          `json:"bytes"`
	SHA256                       string         `json:"sha256"`
	Package                      string         `json:"package"`
	Imports                      []imported     `json:"imports"`
	Declarations                 []declaration  `json:"declarations"`
	Calls                        []callMetadata `json:"calls"`
	WithheldImports              int            `json:"withheldImports,omitempty"`
	WithheldRoutes               int            `json:"withheldRouteArguments,omitempty"`
	WithheldEnvironmentArguments int            `json:"withheldEnvironmentArguments,omitempty"`
}
type report struct {
	OK                        bool           `json:"ok"`
	Schema                    int            `json:"schema"`
	Kind                      string         `json:"kind"`
	Directory                 string         `json:"directory"`
	Files                     []fileMetadata `json:"files"`
	LocalPackageLeads         []string       `json:"localPackageLeads"`
	SourceBinaryParityProven  bool           `json:"sourceBinaryParityProven"`
	BuildConstraintsEvaluated bool           `json:"buildConstraintsEvaluated"`
	ReferenceDeployedModule   string         `json:"referenceOnlyDeployedModule"`
	ReferenceVCSBase          string         `json:"referenceOnlyVcsBase"`
	ReferenceVCSModified      bool           `json:"referenceOnlyVcsModified"`
	LiteralPolicy             string         `json:"literalPolicy"`
	CallOrder                 string         `json:"callOrder"`
}
type refusal string

func (r refusal) Error() string { return string(r) }
func identifier(s string) bool  { return len(s) > 0 && len(s) <= 128 && token.IsIdentifier(s) }

func validImport(s string) bool {
	if len(s) > 240 || !importPattern.MatchString(s) {
		return false
	}
	for _, part := range strings.Split(s, "/") {
		if part == "." || part == ".." || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".") {
			return false
		}
	}
	return true
}
func symbol(expr ast.Expr) (string, bool) {
	switch v := expr.(type) {
	case *ast.Ident:
		if identifier(v.Name) {
			return v.Name, false
		}
	case *ast.SelectorExpr:
		if !identifier(v.Sel.Name) {
			return "", true
		}
		left, dynamic := symbol(v.X)
		if left == "" {
			return v.Sel.Name, true
		}
		if len(left)+len(v.Sel.Name)+1 <= 240 {
			return left + "." + v.Sel.Name, dynamic
		}
	case *ast.ParenExpr:
		return symbol(v.X)
	case *ast.StarExpr:
		return symbol(v.X)
	case *ast.IndexExpr:
		return symbol(v.X)
	case *ast.IndexListExpr:
		return symbol(v.X)
	}
	return "", true
}
func typeNames(expr ast.Expr) []string {
	seen := map[string]bool{}
	values := []string{}
	if expr == nil {
		return values
	}
	ast.Inspect(expr, func(n ast.Node) bool {
		if n == nil {
			return true
		}
		if len(values) >= 128 {
			return false
		}
		switch v := n.(type) {
		case *ast.BasicLit, *ast.CallExpr:
			return false
		case *ast.SelectorExpr:
			name, _ := symbol(v)
			if name != "" && !seen[name] {
				seen[name] = true
				values = append(values, name)
			}
			return false
		case *ast.Ident:
			if identifier(v.Name) && !seen[v.Name] {
				seen[v.Name] = true
				values = append(values, v.Name)
			}
		}
		return len(values) < 128
	})
	return values
}
func fields(list *ast.FieldList) []fieldMetadata {
	result := []fieldMetadata{}
	if list == nil {
		return result
	}
	for _, f := range list.List {
		names := []string{}
		for _, n := range f.Names {
			if identifier(n.Name) {
				names = append(names, n.Name)
			}
		}
		result = append(result, fieldMetadata{Names: names, TypeNames: typeNames(f.Type)})
	}
	return result
}
func typeKind(expr ast.Expr) string {
	switch expr.(type) {
	case *ast.StructType:
		return "struct"
	case *ast.InterfaceType:
		return "interface"
	case *ast.FuncType:
		return "function"
	case *ast.MapType:
		return "map"
	case *ast.ArrayType:
		return "array_or_slice"
	case *ast.ChanType:
		return "channel"
	default:
		return "reference_or_alias"
	}
}
func declarations(file *ast.File, fset *token.FileSet) []declaration {
	result := []declaration{}
	for _, d := range file.Decls {
		switch v := d.(type) {
		case *ast.FuncDecl:
			if identifier(v.Name.Name) {
				receiver := []string{}
				if v.Recv != nil {
					for _, f := range v.Recv.List {
						receiver = append(receiver, typeNames(f.Type)...)
					}
				}
				result = append(result, declaration{Kind: "function", Name: v.Name.Name, Line: fset.Position(v.Pos()).Line, ReceiverNames: receiver, Parameters: fields(v.Type.Params), Results: fields(v.Type.Results)})
			}
		case *ast.GenDecl:
			for _, spec := range v.Specs {
				switch s := spec.(type) {
				case *ast.ValueSpec:
					for _, name := range s.Names {
						if identifier(name.Name) {
							result = append(result, declaration{Kind: strings.ToLower(v.Tok.String()), Name: name.Name, Line: fset.Position(name.Pos()).Line, TypeNames: typeNames(s.Type)})
						}
					}
				case *ast.TypeSpec:
					if !identifier(s.Name.Name) {
						continue
					}
					value := declaration{Kind: "type", Name: s.Name.Name, Line: fset.Position(s.Pos()).Line, TypeKind: typeKind(s.Type)}
					switch t := s.Type.(type) {
					case *ast.StructType:
						value.Fields = fields(t.Fields)
					case *ast.InterfaceType:
						value.Fields = fields(t.Methods)
					default:
						value.TypeNames = typeNames(s.Type)
					}
					result = append(result, value)
				}
			}
		}
	}
	return result
}
func stringArgument(call *ast.CallExpr) (string, bool) {
	if len(call.Args) < 1 {
		return "", false
	}
	literal, ok := call.Args[0].(*ast.BasicLit)
	if !ok || literal.Kind != token.STRING || len(literal.Value) > 512 {
		return "", false
	}
	value, err := strconv.Unquote(literal.Value)
	return value, err == nil
}
func inspectAST(ctx context.Context, file *ast.File, fset *token.FileSet, metadata *fileMetadata, nodes *int) error {
	aliases := map[string]string{}
	for _, item := range file.Imports {
		value, err := strconv.Unquote(item.Path.Value)
		if err != nil || !validImport(value) {
			metadata.WithheldImports++
			continue
		}
		alias := pathBase(value)
		if item.Name != nil {
			alias = item.Name.Name
		}
		if alias != "_" && alias != "." && !identifier(alias) {
			metadata.WithheldImports++
			continue
		}
		lead := ""
		if strings.HasPrefix(value, "redx/") {
			lead = strings.TrimPrefix(value, "redx/")
		}
		metadata.Imports = append(metadata.Imports, imported{Path: value, Alias: alias, LocalLead: lead})
		aliases[alias] = value
	}
	metadata.Declarations = declarations(file, fset)
	if len(metadata.Declarations) > maxDeclarations {
		return refusal("declaration_bound_exceeded")
	}
	stack := []ast.Node{}
	var failure error
	ast.Inspect(file, func(n ast.Node) bool {
		if n == nil {
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
			return true
		}
		*nodes++
		if *nodes > maxNodes {
			failure = refusal("node_bound_exceeded")
			return false
		}
		if ctx.Err() != nil {
			failure = refusal("time_bound_exceeded")
			return false
		}
		stack = append(stack, n)
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		callee, dynamic := symbol(call.Fun)
		if callee == "" {
			return true
		}
		position := fset.Position(call.Pos())
		value := callMetadata{Callee: callee, Line: position.Line, Column: position.Column, Function: "package_initializer", ReceiverDynamic: dynamic, FocusHint: focusPattern.MatchString(callee)}
		for _, ancestor := range stack {
			switch a := ancestor.(type) {
			case *ast.FuncDecl:
				if identifier(a.Name.Name) {
					value.Function = a.Name.Name
				}
			case *ast.FuncLit:
				value.FunctionLiteralDepth++
			case *ast.DeferStmt:
				value.LexicalContexts = append(value.LexicalContexts, "defer")
				value.DirectlyDeferred = value.DirectlyDeferred || a.Call == call
			case *ast.GoStmt:
				value.LexicalContexts = append(value.LexicalContexts, "go")
				value.DirectlyGo = value.DirectlyGo || a.Call == call
			}
		}
		if selectExpr, ok := call.Fun.(*ast.SelectorExpr); ok {
			if selectExpr.Sel.Name == "Handle" || selectExpr.Sel.Name == "HandleFunc" {
				route, ok := stringArgument(call)
				if ok && routePattern.MatchString(route) {
					value.ReviewedRoute = route
				} else {
					metadata.WithheldRoutes++
				}
			}
			if root, ok := selectExpr.X.(*ast.Ident); ok && aliases[root.Name] == "os" && selectExpr.Sel.Name == "Getenv" {
				name, ok := stringArgument(call)
				if ok && envNamePattern.MatchString(name) {
					value.EnvironmentName = name
				} else {
					metadata.WithheldEnvironmentArguments++
				}
			}
		}
		metadata.Calls = append(metadata.Calls, value)
		if len(metadata.Calls) > maxCalls {
			failure = refusal("call_bound_exceeded")
			return false
		}
		return true
	})
	return failure
}
func pathBase(s string) string { p := strings.Split(s, "/"); return p[len(p)-1] }
func inspectDirectory(ctx context.Context, directory string) (*report, error) {
	return inspectPackage(ctx, directory, "main")
}
func inspectPackage(ctx context.Context, directory, expectedPackage string) (*report, error) {
	clean, err := filepath.Abs(directory)
	if err != nil {
		return nil, refusal("source_path_refused")
	}
	resolved, err := filepath.EvalSymlinks(clean)
	if err != nil || resolved != clean {
		return nil, refusal("source_path_refused")
	}
	dir, err := os.Lstat(clean)
	if err != nil || !dir.IsDir() || dir.Mode()&os.ModeSymlink != 0 {
		return nil, refusal("source_directory_refused")
	}
	listing, err := os.Open(clean)
	if err != nil {
		return nil, refusal("source_inventory_failed")
	}
	entries, listErr := listing.ReadDir(257)
	listing.Close()
	if listErr != nil && listErr != io.EOF {
		return nil, refusal("source_inventory_failed")
	}
	if len(entries) > 256 {
		return nil, refusal("inventory_bound_exceeded")
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	result := &report{OK: true, Schema: 1, Kind: "go-ast-source-metadata-only", Directory: clean, Files: []fileMetadata{}, LocalPackageLeads: []string{}, ReferenceDeployedModule: "redx/cmd/redx-gdg-accounts", ReferenceVCSBase: "b3cfa4ff2bac9789b1132d80cc07e34f26ccf9e3", ReferenceVCSModified: true, LiteralPolicy: "reviewed account Handle/HandleFunc routes and validated os.Getenv names only; all other values/comments/body text withheld", CallOrder: "source position; lexical defer/go contexts do not establish execution order"}
	var total int64
	nodes := 0
	declarationsTotal := 0
	callsTotal := 0
	leads := map[string]bool{}
	for _, entry := range entries {
		if ctx.Err() != nil {
			return nil, refusal("time_bound_exceeded")
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		if !filenamePattern.MatchString(name) {
			return nil, refusal("source_filename_refused")
		}
		if len(result.Files) >= maxFiles {
			return nil, refusal("file_count_bound_exceeded")
		}
		full := filepath.Join(clean, name)
		before, err := os.Lstat(full)
		if err != nil || !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 || before.Size() > maxFileBytes {
			return nil, refusal("source_file_refused")
		}
		total += before.Size()
		if total > maxTotalBytes {
			return nil, refusal("total_size_bound_exceeded")
		}
		fd, err := os.Open(full)
		if err != nil {
			return nil, refusal("source_read_failed")
		}
		opened, statErr := fd.Stat()
		if statErr != nil || !os.SameFile(before, opened) {
			fd.Close()
			return nil, refusal("source_changed_during_read")
		}
		bytes, readErr := io.ReadAll(io.LimitReader(fd, maxFileBytes+1))
		fd.Close()
		after, afterErr := os.Lstat(full)
		if readErr != nil || afterErr != nil || !os.SameFile(before, after) || after.Mode()&os.ModeSymlink != 0 || !before.ModTime().Equal(after.ModTime()) || before.Size() != after.Size() || int64(len(bytes)) != before.Size() || len(bytes) > maxFileBytes {
			return nil, refusal("source_changed_during_read")
		}
		fset := token.NewFileSet()
		parsed, parseErr := parser.ParseFile(fset, name, bytes, parser.SkipObjectResolution)
		if parseErr != nil {
			return nil, refusal("source_parse_failed")
		}
		if parsed.Name.Name != expectedPackage {
			return nil, refusal("source_package_refused")
		}
		digest := sha256.Sum256(bytes)
		metadata := fileMetadata{Name: name, Bytes: int64(len(bytes)), SHA256: hex.EncodeToString(digest[:]), Package: parsed.Name.Name, Imports: []imported{}, Declarations: []declaration{}, Calls: []callMetadata{}}
		if err := inspectAST(ctx, parsed, fset, &metadata, &nodes); err != nil {
			return nil, err
		}
		declarationsTotal += len(metadata.Declarations)
		callsTotal += len(metadata.Calls)
		if declarationsTotal > maxDeclarations || callsTotal > maxCalls {
			return nil, refusal("aggregate_metadata_bound_exceeded")
		}
		for _, item := range metadata.Imports {
			if item.LocalLead != "" {
				leads[item.LocalLead] = true
			}
		}
		result.Files = append(result.Files, metadata)
	}
	if len(result.Files) == 0 {
		return nil, refusal("no_source_files")
	}
	for lead := range leads {
		result.LocalPackageLeads = append(result.LocalPackageLeads, lead)
	}
	sort.Strings(result.LocalPackageLeads)
	return result, nil
}
func emitFailure(code string) {
	bytes, _ := json.Marshal(map[string]any{"ok": false, "reason": code, "privateOutputWithheld": true})
	os.Stdout.Write(append(bytes, '\n'))
}
func safelyInspect(ctx context.Context, directory, expectedPackage string) (value *report, err error) {
	defer func() {
		if recover() != nil {
			value = nil
			err = refusal("inspection_failed")
		}
	}()
	return inspectPackage(ctx, directory, expectedPackage)
}
func main() {
	directory, expectedPackage := sourceDirectory, "main"
	if len(os.Args) == 2 && os.Args[1] == "--retained-store-package" {
		directory, expectedPackage = storageDirectory, "gdgaccounts"
	} else if len(os.Args) != 1 {
		emitFailure("arguments_refused")
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	type outcome struct {
		value *report
		err   error
	}
	done := make(chan outcome, 1)
	go func() { value, err := safelyInspect(ctx, directory, expectedPackage); done <- outcome{value, err} }()
	select {
	case <-ctx.Done():
		emitFailure("time_bound_exceeded")
		os.Exit(1)
	case result := <-done:
		if result.err != nil {
			code := "inspection_failed"
			if safe, ok := result.err.(refusal); ok {
				code = string(safe)
			}
			emitFailure(code)
			os.Exit(1)
		}
		bytes, err := json.Marshal(result.value)
		if err != nil || len(bytes) > maxOutputBytes {
			emitFailure("output_bound_exceeded")
			os.Exit(1)
		}
		os.Stdout.Write(append(bytes, '\n'))
	}
}
