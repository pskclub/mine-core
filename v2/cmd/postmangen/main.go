package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
)

const (
	repoRootMarker = "go.mod"
	layoutGrouped  = "grouped"
	layoutFlat     = "flat"

	// The output formats. Both are built from one parse, so asking for both
	// costs a marshal rather than a second run.
	formatPostman = "postman"
	formatOpenAPI = "openapi"
	formatBoth    = "both"

	// defaultAPIVersion is what info.version says when the project has not said.
	// OpenAPI requires the field, and a stated "0.0.0" reads as "nobody set
	// this", which is exactly what it means.
	defaultAPIVersion = "0.0.0"

	// paginationPrefix matches the context methods that read page/limit off the
	// query — GetPageOptions and its variants such as GetPageOptionsWithAllowed.
	paginationPrefix = "GetPageOptions"
	// handlerWrapper is the optional adapter a route may wrap its handler in.
	// Handlers passed unwrapped are recognised just the same.
	handlerWrapper = "WithHTTPContext"
	// groupMethod is the router method that opens a prefixed route group.
	groupMethod = "Group"

	// The three shapes a request body is sent in, named as Postman names them.
	bodyModeRaw        = "raw"
	bodyModeFormData   = "formdata"
	bodyModeURLEncoded = "urlencoded"
	// contentTypeJSON and contentTypeForm are stated on the request; a multipart
	// body carries no Content-Type here because Postman writes its own, and only
	// Postman knows the boundary it will use.
	contentTypeJSON = "application/json"
	contentTypeForm = "application/x-www-form-urlencoded"

	// maxAliasDepth bounds following one type name to another, so a pair of
	// files that name each other cannot loop forever.
	maxAliasDepth = 8

	// defaultMaxDepth is how many objects deep a rendered body goes when the
	// project has not said.
	//
	// Refusing to expand a type already on the current path stops the recursion
	// but not the growth: a model graph where a province holds districts, a
	// district holds its province, and an address holds both has no cycle along
	// any single path, and every distinct path through it gets written out. One
	// real service produced a 425 MB document that way, nesting 64 levels deep —
	// which nothing reads and no browser opens.
	//
	// Output roughly doubles per level, so this is the setting that decides
	// whether the result is usable. Three keeps a reply describing itself
	// (a page, its items, and what an item holds) without touring the schema.
	defaultMaxDepth = 3

	// The framework answers a rejected request before the handler runs, in a
	// shape it fixes: {code, message, fields:{field:{code, message, in}}}.
	// These are what it puts there.
	invalidParamsCode    = "INVALID_PARAMS"
	invalidParamsMessage = "Invalid parameters"
	unauthorizedCode     = "UNAUTHORIZED"
	unauthorizedMessage  = "unauthorized"
	requiredRule         = "Required"
	requiredCode         = "REQUIRED"
)

// httpStatusCodes are the success statuses a handler writes. Anything else a
// handler can send is an error, which is not what an example shows.
var httpStatusCodes = map[string]int{
	"StatusOK":        http.StatusOK,
	"StatusCreated":   http.StatusCreated,
	"StatusAccepted":  http.StatusAccepted,
	"StatusNoContent": http.StatusNoContent,
}

type repoInfo struct {
	root       string
	moduleName string
}

type goFile struct {
	path        string
	relPath     string
	dirRel      string
	packageName string
	file        *ast.File
	fset        *token.FileSet
	imports     map[string]string
}

type routeInfo struct {
	Method         string
	Path           string
	Folder         string
	Module         string
	HandlerType    string
	HandlerMethod  string
	HandlerPackage string
	NeedsAuth      bool
	RequestType    *typeRef
	UsesPagination bool
	PathParams     []string
	Signals        requestSignals
	Response       *responseInfo
	// Examples are replies the framework writes itself rather than ones read off
	// a handler — the health probes, whose bodies no handler in the project
	// contains.
	Examples []staticExample
}

// staticExample is a saved reply stated here rather than resolved from source.
type staticExample struct {
	Code int
	Body string
}

// requestSignals is what a handler takes off its context directly rather than
// through a bound request struct: c.QueryParam("q"), c.FormValue("is_public"),
// c.FormFile("file"), c.Request().Header.Get("X-Api-Key") and their kin.
//
// A request struct states its inputs in tags, which is what the rest of this
// tool reads. These handlers state theirs in code, and a collection built from
// tags alone would show such an endpoint as taking nothing at all — no query
// string, no upload, no body.
type requestSignals struct {
	Params  []string
	Query   []namedSample
	Form    []namedSample
	Files   []string
	Headers []string
	Cookies []string
	// Multipart is set by c.FormFile or c.MultipartForm: whatever else the
	// handler reads, the body is multipart.
	Multipart bool
	// HasForm is set by c.FormValues, which names no field but does say the
	// payload is a form rather than JSON.
	HasForm bool
}

// namedSample is a parameter the handler asked for by name, with the fallback it
// stated when it used one of the ...Or forms. That fallback is a value the
// server itself would use, so it beats anything guessed from the name; an empty
// one says nothing and is treated as absent.
type namedSample struct {
	Name    string
	Default string
}

type typeRef struct {
	ImportPath string
	TypeName   string
}

type structField struct {
	Name     string
	Tag      string
	Expr     ast.Expr
	Embedded bool
}

// typeCtx is where a declaration was written. A name inside it means whatever
// that file's imports say it means, so every lookup needs this alongside the
// expression itself.
type typeCtx struct {
	ImportPath string
	Imports    map[string]string
}

// structInfo is a struct declaration: its fields, the type parameters it takes
// when generic, and the context those field types resolve in.
type structInfo struct {
	TypeName   string
	TypeParams []string
	Ctx        *typeCtx
	Fields     []structField
}

// funcInfo is what a function, method or interface method hands back, which is
// how a handler's response type is traced through the service it calls.
type funcInfo struct {
	Results []ast.Expr
	Ctx     *typeCtx
}

type controllerMethodInfo struct {
	RequestType    *typeRef
	UsesPagination bool
	PathParams     []string
	Signals        requestSignals
	Response       *responseInfo
}

// responseInfo is the success reply a handler writes: the status it sends and
// the expression it sends, still unresolved, with the local variables and
// context needed to resolve it later.
type responseInfo struct {
	StatusCode int
	Value      ast.Expr
	Locals     map[string]*localValue
	Ctx        *typeCtx
	// RecvName and RecvType are the handler's own receiver — "h UserHandler" —
	// which is what a call written on one of its fields (h.users.Find) has to be
	// resolved through.
	RecvName string
	RecvType string
}

type postmanCollection struct {
	Info     postmanInfo       `json:"info"`
	Variable []postmanVariable `json:"variable"`
	Item     []postmanItem     `json:"item"`
}

type postmanInfo struct {
	PostmanID   string `json:"_postman_id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Schema      string `json:"schema"`
}

type postmanVariable struct {
	Key   string `json:"key"`
	Value string `json:"value"`
	Type  string `json:"type,omitempty"`
}

type postmanItem struct {
	SortKey  string            `json:"-"`
	Name     string            `json:"name"`
	Item     []postmanItem     `json:"item,omitempty"`
	Request  *postmanReq       `json:"request,omitempty"`
	Response []postmanResponse `json:"response,omitempty"`
	Event    []postmanEvent    `json:"event,omitempty"`
}

// postmanResponse is a saved example: the reply Postman shows under a request
// without the request having been sent.
type postmanResponse struct {
	Name            string          `json:"name"`
	OriginalRequest *postmanReq     `json:"originalRequest,omitempty"`
	Status          string          `json:"status"`
	Code            int             `json:"code"`
	PreviewLanguage string          `json:"_postman_previewlanguage,omitempty"`
	Header          []postmanHeader `json:"header"`
	Cookie          []any           `json:"cookie"`
	Body            string          `json:"body"`
}

type postmanReq struct {
	Method string          `json:"method"`
	Header []postmanHeader `json:"header,omitempty"`
	Body   *postmanBody    `json:"body,omitempty"`
	URL    postmanURL      `json:"url"`
}

type postmanHeader struct {
	Key   string `json:"key"`
	Value string `json:"value"`
	Type  string `json:"type,omitempty"`
}

// postmanBody is the payload, in the one of Postman's shapes that matches how
// the handler reads it: raw JSON, a url-encoded form, or multipart form data.
type postmanBody struct {
	Mode       string             `json:"mode"`
	Raw        string             `json:"raw,omitempty"`
	FormData   []postmanFormParam `json:"formdata,omitempty"`
	URLEncoded []postmanFormParam `json:"urlencoded,omitempty"`
}

// postmanFormParam is one entry of a form body. A file entry carries no value:
// Postman asks whoever runs the request to pick the file, which is the only
// thing a generated collection can honestly say about an upload.
type postmanFormParam struct {
	Key         string `json:"key"`
	Value       string `json:"value,omitempty"`
	Type        string `json:"type"`
	Description string `json:"description,omitempty"`
}

type postmanURL struct {
	Raw      string            `json:"raw"`
	Host     []string          `json:"host"`
	Path     []string          `json:"path"`
	Query    []postmanQuery    `json:"query,omitempty"`
	Variable []postmanVariable `json:"variable,omitempty"`
}

type postmanQuery struct {
	Key         string `json:"key"`
	Value       string `json:"value"`
	Description string `json:"description,omitempty"`
}

type postmanEvent struct {
	Listen string        `json:"listen"`
	Script postmanScript `json:"script"`
}

type postmanScript struct {
	Type string   `json:"type"`
	Exec []string `json:"exec"`
}

type folderNode struct {
	Name     string
	SortKey  string
	Folders  map[string]*folderNode
	Requests []postmanItem
}

func main() {
	configPath := flag.String("config", "", "path to "+configFileName+"; defaults to the one at the repository root, if any")
	layout := flag.String("layout", "", "collection layout: grouped or flat")
	output := flag.String("out", "", "where to write the collection, relative to the repository root")
	name := flag.String("name", "", "collection name shown in Postman")
	baseURL := flag.String("base-url", "", "value of the {{baseUrl}} collection variable")
	format := flag.String("format", "", "what to write: postman, openapi or both")
	openAPIOut := flag.String("openapi-out", "", "where to write the OpenAPI document, relative to the repository root")
	apiVersion := flag.String("api-version", "", "info.version of the OpenAPI document")
	maxDepth := flag.Int("max-depth", 0, "how many objects deep a rendered body goes")
	flag.Parse()

	repo, err := discoverRepoInfo()
	if err != nil {
		exitErr(err)
	}

	cfg, err := loadConfig(repo.root, *configPath)
	if err != nil {
		exitErr(err)
	}
	// A flag beats the file, which beats the default: the layout is the one
	// setting a caller routinely wants both ways, and the rest follow the same
	// rule so a one-off run never needs the file edited.
	overrideString(&cfg.Layout, *layout)
	overrideString(&cfg.Output, *output)
	overrideString(&cfg.Name, *name)
	overrideString(&cfg.BaseURL, *baseURL)
	overrideString(&cfg.Format, *format)
	overrideString(&cfg.OpenAPIOutput, *openAPIOut)
	overrideString(&cfg.APIVersion, *apiVersion)
	if *maxDepth > 0 {
		cfg.MaxDepth = *maxDepth
	}

	if err := cfg.validate(); err != nil {
		exitErr(err)
	}

	files, err := loadGoFiles(repo.root, &cfg)
	if err != nil {
		exitErr(err)
	}

	res := newResolver(repo, files, &cfg)
	controllerMethods := buildControllerMethodRegistry(repo, files, &cfg)
	routes := buildRoutes(repo, files, controllerMethods, &cfg)

	if cfg.writesPostman() {
		collection, err := buildCollection(routes, res, repo, &cfg)
		if err != nil {
			exitErr(err)
		}
		if err := writeCollection(repo.root, cfg.Output, collection); err != nil {
			exitErr(err)
		}
		fmt.Printf("generated %s with layout=%s, %d folders and %d routes\n",
			cfg.Output, cfg.Layout, len(collection.Item), countRoutes(collection.Item))
	}

	if cfg.writesOpenAPI() {
		doc, err := buildOpenAPI(routes, res, &cfg)
		if err != nil {
			exitErr(err)
		}
		if err := writeOpenAPI(repo.root, cfg.OpenAPIOutput, doc); err != nil {
			exitErr(err)
		}
		fmt.Printf("generated %s (OpenAPI %s) with %d paths and %d operations\n",
			cfg.OpenAPIOutput, doc.OpenAPI, len(doc.Paths), countOperations(doc))
	}
}

func overrideString(target *string, value string) {
	if value != "" {
		*target = value
	}
}

func discoverRepoInfo() (*repoInfo, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}

	root := cwd
	for {
		goModPath := filepath.Join(root, repoRootMarker)
		data, readErr := os.ReadFile(goModPath)
		if readErr == nil {
			return &repoInfo{
				root:       root,
				moduleName: parseModuleName(string(data)),
			}, nil
		}

		parent := filepath.Dir(root)
		if parent == root {
			return nil, fmt.Errorf("could not locate %s from %s", repoRootMarker, cwd)
		}
		root = parent
	}
}

func parseModuleName(goMod string) string {
	for _, line := range strings.Split(goMod, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module "))
		}
	}
	return ""
}

func loadGoFiles(root string, cfg *Config) ([]*goFile, error) {
	var files []*goFile

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if path == root {
				return nil
			}
			name := d.Name()
			if cfg.isSkippedDir(name) || strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			// One unparsable file should cost its own routes, not every route in
			// the repo — generated or vendored code is the usual culprit.
			fmt.Fprintf(os.Stderr, "warning: skipping %s: %v\n", path, err)
			return nil
		}

		relPath, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relPath = filepath.ToSlash(relPath)
		dirRel := filepath.ToSlash(filepath.Dir(relPath))
		if dirRel == "." {
			dirRel = ""
		}

		files = append(files, &goFile{
			path:        path,
			relPath:     relPath,
			dirRel:      dirRel,
			packageName: file.Name.Name,
			file:        file,
			fset:        fset,
			imports:     buildImportMap(file),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].relPath < files[j].relPath
	})
	return files, nil
}

func buildImportMap(file *ast.File) map[string]string {
	imports := map[string]string{}
	for _, imp := range file.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			continue
		}
		name := packageNameFromImportPath(path)
		if imp.Name != nil && imp.Name.Name != "_" && imp.Name.Name != "." {
			name = imp.Name.Name
		}
		imports[name] = path
	}
	return imports
}

// packageNameFromImportPath guesses the identifier an unaliased import binds to.
// The last segment is right except for a module version suffix, where "/v2" is
// part of the path and never the package name.
func packageNameFromImportPath(path string) string {
	parts := strings.Split(path, "/")
	name := parts[len(parts)-1]
	if len(parts) > 1 && isVersionSegment(name) {
		name = parts[len(parts)-2]
	}
	return name
}

// resolver answers "what is this type" across the repo, pulling in a dependency
// only when a response actually mentions one of its types.
type resolver struct {
	structs map[string]*structInfo
	// aliases holds names that stand for another type — `type Request =
	// pkg.Request` and `type Request pkg.Request` alike. Both compile and both
	// bind identically, so both have to resolve to the same fields.
	aliases map[string]*typeAlias
	funcs   map[string]*funcInfo
	// validators holds each request's Valid/Validate body, where the rules a
	// field must satisfy are stated.
	validators map[string]*validatorInfo
	// consts holds integer constants, so Length(minPasswordLength, 72) is read
	// as the number it stands for.
	consts    map[string]int
	strConsts map[string]string
	// constRefs holds the declarations whose value is not a literal — one
	// constant named in terms of another — so they can be followed on lookup
	// rather than dropped at index time, when the target may not be parsed yet.
	constRefs map[string]*constRef
	// sliceConsts holds the elements of a slice-valued declaration, so a set of
	// allowed values written as `var Kinds = []string{...}` and spread into
	// In(Kinds...) reads as the values it stands for.
	sliceConsts map[string]*constSlice
	// pkgNames maps an import path to the package name it actually declares.
	// The two disagree often enough — a `services/` directory holding `package
	// service` — that guessing from the path alone loses every rule written in
	// terms of that package's constants.
	pkgNames  map[string]string
	ruleCache map[string]map[string]*fieldRules
	loaded    map[string]struct{}
	// maxDepth bounds how many objects deep a rendered body goes. See
	// Config.MaxDepth for why it is a setting rather than a constant.
	maxDepth int
}

// typeAlias is a declaration that names another type instead of declaring its
// own fields. The fields live at the target, and are followed to on lookup.
type typeAlias struct {
	Target ast.Expr
	Ctx    *typeCtx
}

// validatorInfo is a request's Valid/Validate body together with the file it
// was written in. The context has to travel with the body: a request whose
// rules live in a `*.valid.go` sibling resolves `service.KindActive` through
// that file's imports, not through the ones the struct declaration happened to
// need.
type validatorInfo struct {
	Body *ast.BlockStmt
	Ctx  *typeCtx
}

// constSlice is a slice-valued declaration together with the file it was
// written in, which is where its elements resolve — an element naming a
// constant names it in the declaring package, not in the one that spreads it.
type constSlice struct {
	Elements []ast.Expr
	Ctx      *typeCtx
}

// constRef is a declaration whose value is another name rather than a literal.
// It is followed on lookup rather than resolved at index time, because the
// package it points at may not have been parsed yet when the reference is read.
type constRef struct {
	Value ast.Expr
	Ctx   *typeCtx
}

func newResolver(repo *repoInfo, files []*goFile, cfg *Config) *resolver {
	res := &resolver{
		maxDepth:    cfg.depth(),
		structs:     map[string]*structInfo{},
		aliases:     map[string]*typeAlias{},
		funcs:       map[string]*funcInfo{},
		validators:  map[string]*validatorInfo{},
		consts:      map[string]int{},
		strConsts:   map[string]string{},
		constRefs:   map[string]*constRef{},
		sliceConsts: map[string]*constSlice{},
		pkgNames:    map[string]string{},
		ruleCache:   map[string]map[string]*fieldRules{},
		loaded:      map[string]struct{}{},
	}
	for _, gf := range files {
		ctx := &typeCtx{ImportPath: importPathForDir(repo, gf.dirRel), Imports: gf.imports}
		res.loaded[ctx.ImportPath] = struct{}{}
		res.pkgNames[ctx.ImportPath] = gf.packageName
		res.indexFile(ctx, gf.file)
	}
	return res
}

func (r *resolver) structAt(importPath, typeName string) *structInfo {
	return r.structAtDepth(importPath, typeName, 0)
}

// structAtDepth resolves a named type to the declaration that holds its fields,
// following as many aliases as it takes to get there.
//
// A module that re-exports another package's request — `type ProjectFilter =
// requests.ProjectFilter` — binds through the alias, and a lookup that stopped
// at struct declarations would find nothing and silently document the endpoint
// as taking no parameters at all.
func (r *resolver) structAtDepth(importPath, typeName string, depth int) *structInfo {
	key := registryKey(importPath, typeName)
	if info, found := r.structs[key]; found {
		return info
	}
	if alias, found := r.aliases[key]; found {
		return r.followAlias(alias, depth)
	}

	r.load(importPath)
	if info, found := r.structs[key]; found {
		return info
	}
	if alias, found := r.aliases[key]; found {
		return r.followAlias(alias, depth)
	}
	return nil
}

// followAlias resolves what an alias names. The depth bound is what stops a pair
// of packages that name each other from looping forever.
func (r *resolver) followAlias(alias *typeAlias, depth int) *structInfo {
	if depth >= maxAliasDepth || alias.Ctx == nil {
		return nil
	}
	ref := resolveTypeRefType(alias.Ctx.Imports, alias.Ctx.ImportPath, aliasTargetBase(alias.Target))
	if ref == nil {
		return nil
	}
	return r.structAtDepth(ref.ImportPath, ref.TypeName, depth+1)
}

// aliasTargetBase strips a generic instantiation down to the type being
// instantiated: `type Page = pkg.Page[Item]` still declares pkg.Page's fields.
func aliasTargetBase(expr ast.Expr) ast.Expr {
	switch e := expr.(type) {
	case *ast.IndexExpr:
		return e.X
	case *ast.IndexListExpr:
		return e.X
	default:
		return expr
	}
}

func (r *resolver) funcAt(importPath, recvType, name string) *funcInfo {
	if info, found := r.funcs[funcKey(importPath, recvType, name)]; found {
		return info
	}
	r.load(importPath)
	return r.funcs[funcKey(importPath, recvType, name)]
}

// load parses a dependency's package directory, so a type it declares — a
// paginated page, say — is rendered with its real fields rather than as an empty
// object. Failure is not fatal: the sample degrades to {}.
func (r *resolver) load(importPath string) {
	if _, done := r.loaded[importPath]; done || importPath == "" {
		return
	}
	r.loaded[importPath] = struct{}{}

	dir, err := packageDir(importPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: cannot locate package %s: %v\n", importPath, err)
		return
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: cannot read package %s: %v\n", importPath, err)
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(dir, name), nil, parser.ParseComments)
		if err != nil {
			continue
		}
		r.pkgNames[importPath] = file.Name.Name
		r.indexFile(&typeCtx{ImportPath: importPath, Imports: buildImportMap(file)}, file)
	}
}

// packageDir asks the go tool where a package's source lives.
//
// -mod=readonly is the default, but it is stated anyway: a caller with
// GOFLAGS=-mod=mod set would otherwise have their go.mod and go.sum rewritten as
// a side effect of generating documentation, which is never what they asked for.
func packageDir(importPath string) (string, error) {
	out, err := exec.Command("go", "list", "-mod=readonly", "-f", "{{.Dir}}", importPath).Output()
	if err != nil {
		return "", err
	}
	dir := strings.TrimSpace(string(out))
	if dir == "" {
		return "", fmt.Errorf("no directory reported")
	}
	return dir, nil
}

// indexFile records every struct, interface method, function, validation body
// and integer constant a file declares, keyed by the package it belongs to.
func (r *resolver) indexFile(ctx *typeCtx, file *ast.File) {
	structs, funcs := r.structs, r.funcs
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.GenDecl:
			if d.Tok == token.CONST || d.Tok == token.VAR {
				r.indexConsts(ctx, d)
				continue
			}
			if d.Tok != token.TYPE {
				continue
			}
			for _, spec := range d.Specs {
				typeSpec, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				switch t := typeSpec.Type.(type) {
				case *ast.StructType:
					structs[registryKey(ctx.ImportPath, typeSpec.Name.Name)] = &structInfo{
						TypeName:   typeSpec.Name.Name,
						TypeParams: typeParamNames(typeSpec.TypeParams),
						Ctx:        ctx,
						Fields:     structFields(t),
					}
				case *ast.InterfaceType:
					// An interface is where a service's signature is stated, and
					// the constructor hands back the interface, not the struct —
					// so this is what a response type is traced through.
					for _, method := range t.Methods.List {
						fn, ok := method.Type.(*ast.FuncType)
						if !ok || len(method.Names) == 0 {
							continue
						}
						for _, name := range method.Names {
							funcs[funcKey(ctx.ImportPath, typeSpec.Name.Name, name.Name)] = &funcInfo{
								Results: resultExprs(fn.Results),
								Ctx:     ctx,
							}
						}
					}
				default:
					// A name standing for another type, rather than declaring
					// fields of its own. What it names is where the fields are.
					if isNamedTypeExpr(typeSpec.Type) {
						r.aliases[registryKey(ctx.ImportPath, typeSpec.Name.Name)] = &typeAlias{
							Target: typeSpec.Type,
							Ctx:    ctx,
						}
					}
				}
			}
		case *ast.FuncDecl:
			recvType := ""
			if d.Recv != nil && len(d.Recv.List) > 0 {
				if recvType = receiverTypeName(d.Recv.List[0].Type); recvType == "" {
					continue
				}
			}
			funcs[funcKey(ctx.ImportPath, recvType, d.Name.Name)] = &funcInfo{
				Results: resultExprs(d.Type.Results),
				Ctx:     ctx,
			}
			if recvType != "" && d.Body != nil && isValidatorMethod(d.Name.Name) {
				r.validators[registryKey(ctx.ImportPath, recvType)] = &validatorInfo{Body: d.Body, Ctx: ctx}
			}
		}
	}
}

// isNamedTypeExpr reports whether a type declaration names another type rather
// than describing one. A map, a slice or a func type names nothing to follow.
func isNamedTypeExpr(expr ast.Expr) bool {
	switch expr.(type) {
	case *ast.Ident, *ast.SelectorExpr, *ast.IndexExpr, *ast.IndexListExpr:
		return true
	default:
		return false
	}
}

// isValidatorMethod reports whether a method states a request's rules. Valid is
// the entry point the framework calls; Validate is what a nested item declares.
func isValidatorMethod(name string) bool {
	return name == "Valid" || name == "Validate"
}

// indexConsts records the constants a rule may be written in terms of, so
// Length(minLen, 72) is read as the number it stands for and
// In(consts.RoleAdmin, consts.RoleMember) as the values it stands for.
//
// Naming the allowed values once and referring to them is the normal way to
// write this — the handler branches on the same constants, and a literal
// repeated in the validator is a literal that drifts. Reading only literals
// meant every such field documented no enum at all, and the page looked
// complete while saying nothing about what the server accepts.
//
// Package-level vars are indexed alongside consts: a set of allowed values is
// as often a var as a const, and neither is going to be reassigned at runtime
// in a way this could see anyway.
func (r *resolver) indexConsts(ctx *typeCtx, decl *ast.GenDecl) {
	for _, spec := range decl.Specs {
		valueSpec, ok := spec.(*ast.ValueSpec)
		if !ok {
			continue
		}
		for i, name := range valueSpec.Names {
			if i >= len(valueSpec.Values) {
				continue
			}
			key := registryKey(ctx.ImportPath, name.Name)
			if elements, ok := sliceElements(valueSpec.Values[i]); ok {
				r.sliceConsts[key] = &constSlice{Elements: elements, Ctx: ctx}
				continue
			}
			switch value := constValue(valueSpec.Values[i]).(type) {
			case int:
				r.consts[key] = value
			case string:
				r.strConsts[key] = value
			default:
				// Not a literal: a constant named in terms of another one, or
				// one imported from elsewhere. Keep the expression so the
				// lookup can follow it once every package is indexed.
				r.constRefs[key] = &constRef{Value: valueSpec.Values[i], Ctx: ctx}
			}
		}
	}
}

// constValue is the literal a constant stands for, or nil when it is an
// expression this cannot evaluate.
//
// A conversion is unwrapped — `Role("ADMIN")`, which is how a typed string
// constant is written when the type is declared elsewhere — because the value
// a caller must send is the string either way.
func constValue(expr ast.Expr) any {
	switch e := expr.(type) {
	case *ast.BasicLit:
		switch e.Kind {
		case token.INT:
			if value, err := strconv.Atoi(e.Value); err == nil {
				return value
			}
		case token.STRING:
			if value, err := strconv.Unquote(e.Value); err == nil {
				return value
			}
		}
	case *ast.CallExpr:
		// a conversion has exactly one argument; anything else is a function
		// call whose result is not knowable from the source alone
		if len(e.Args) == 1 {
			switch e.Fun.(type) {
			case *ast.Ident, *ast.SelectorExpr:
				return constValue(e.Args[0])
			}
		}
	}
	return nil
}

// sliceElements is the element expressions of a slice or array literal, which
// is how a set of allowed values is written when it is also ranged over
// somewhere else in the service and then spread into In(Kinds...).
//
// The elements are kept as expressions rather than resolved here: an element
// may itself be a constant from a package this has not parsed yet, and the
// same lookup that follows a bare constant argument follows these too.
func sliceElements(expr ast.Expr) ([]ast.Expr, bool) {
	lit, ok := expr.(*ast.CompositeLit)
	if !ok {
		return nil, false
	}
	switch lit.Type.(type) {
	case *ast.ArrayType:
		return lit.Elts, true
	default:
		return nil, false
	}
}

func structFields(structType *ast.StructType) []structField {
	fields := make([]structField, 0, len(structType.Fields.List))
	for _, field := range structType.Fields.List {
		tag := ""
		if field.Tag != nil {
			tag = strings.Trim(field.Tag.Value, "`")
		}
		if len(field.Names) == 0 {
			fields = append(fields, structField{
				Name:     exprTypeName(field.Type),
				Tag:      tag,
				Expr:     field.Type,
				Embedded: true,
			})
			continue
		}
		for _, name := range field.Names {
			fields = append(fields, structField{
				Name: name.Name,
				Tag:  tag,
				Expr: field.Type,
			})
		}
	}
	return fields
}

func typeParamNames(params *ast.FieldList) []string {
	if params == nil {
		return nil
	}
	names := []string{}
	for _, param := range params.List {
		for _, name := range param.Names {
			names = append(names, name.Name)
		}
	}
	return names
}

func resultExprs(results *ast.FieldList) []ast.Expr {
	if results == nil {
		return nil
	}
	exprs := []ast.Expr{}
	for _, result := range results.List {
		count := len(result.Names)
		if count == 0 {
			count = 1
		}
		for i := 0; i < count; i++ {
			exprs = append(exprs, result.Type)
		}
	}
	return exprs
}

// buildControllerMethodRegistry indexes every handler by the type it hangs off
// and its name. Package-level handlers are indexed under an empty type, which is
// how a route that passes a plain function still finds its request metadata.
func buildControllerMethodRegistry(repo *repoInfo, files []*goFile, cfg *Config) map[string]*controllerMethodInfo {
	registry := map[string]*controllerMethodInfo{}
	for _, gf := range files {
		ctx := &typeCtx{ImportPath: importPathForDir(repo, gf.dirRel), Imports: gf.imports}
		for _, decl := range gf.file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			recvType := ""
			if fn.Recv != nil && len(fn.Recv.List) > 0 {
				if recvType = receiverTypeName(fn.Recv.List[0].Type); recvType == "" {
					continue
				}
			}

			signals := collectRequestSignals(fn)
			info := &controllerMethodInfo{
				PathParams: signals.Params,
				Signals:    signals,
				Response:   collectResponse(fn, ctx),
			}
			localVars := collectTypedVars(fn.Body, cfg, nil)
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}

				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				// Prefix, not equality: GetPageOptionsWithAllowed reads the same
				// page/limit query as GetPageOptions does.
				if strings.HasPrefix(sel.Sel.Name, paginationPrefix) {
					info.UsesPagination = true
				}
				if sel.Sel.Name != "Bind" && sel.Sel.Name != "BindWithValidate" {
					return true
				}
				if len(call.Args) == 0 || info.RequestType != nil {
					return true
				}
				if ref := resolveTypeRefExpr(gf.imports, ctx.ImportPath, call.Args[0], localVars); ref != nil {
					info.RequestType = ref
				}
				return true
			})

			key := controllerRegistryKey(gf.dirRel, recvType, fn.Name.Name)
			registry[key] = info
		}
	}
	return registry
}

// collectResponse finds the success reply a handler writes. Failures return the
// error rather than writing a body, so the first 2xx a handler sends is the one
// worth showing as an example.
func collectResponse(fn *ast.FuncDecl, ctx *typeCtx) *responseInfo {
	body := fn.Body
	locals := collectAssignedExprs(body)
	recvName, recvType := receiverBinding(fn)

	var found *responseInfo
	ast.Inspect(body, func(node ast.Node) bool {
		if found != nil {
			return false
		}
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}

		var value ast.Expr
		switch sel.Sel.Name {
		case "JSON", "JSONPretty", "XML", "String", "Blob":
			if len(call.Args) < 2 {
				return true
			}
			value = call.Args[1]
		case "NoContent":
		default:
			return true
		}

		code, ok := statusCode(call.Args[0])
		if !ok || code < 200 || code > 299 {
			return true
		}
		found = &responseInfo{
			StatusCode: code,
			Value:      value,
			Locals:     locals,
			Ctx:        ctx,
			RecvName:   recvName,
			RecvType:   recvType,
		}
		return false
	})
	return found
}

// receiverBinding is the name a method gave its receiver and the type that
// receiver has — "h" and "UserHandler" for `func (h UserHandler) Get(...)`.
// A method with no receiver, or one that discarded it, has neither.
func receiverBinding(fn *ast.FuncDecl) (name, typeName string) {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return "", ""
	}
	field := fn.Recv.List[0]
	if len(field.Names) == 0 || field.Names[0].Name == "_" {
		return "", ""
	}
	return field.Names[0].Name, receiverTypeName(field.Type)
}

// localValue is what a variable was assigned: the expression, and which of its
// results the name was bound to when the right-hand side returns several.
type localValue struct {
	Expr  ast.Expr
	Index int
}

// collectAssignedExprs maps a variable to the value it was assigned, which is
// how `return c.JSON(http.StatusOK, user)` leads back to the call that produced
// `user`.
func collectAssignedExprs(body *ast.BlockStmt) map[string]*localValue {
	assigned := map[string]*localValue{}
	ast.Inspect(body, func(node ast.Node) bool {
		assign, ok := node.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for i, lhs := range assign.Lhs {
			name, ok := lhs.(*ast.Ident)
			if !ok || name.Name == "_" {
				continue
			}
			switch {
			case len(assign.Rhs) == len(assign.Lhs):
				assigned[name.Name] = &localValue{Expr: assign.Rhs[i]}
			case len(assign.Rhs) == 1:
				// `user, err := service.Find(id)` — one call feeding several
				// names, so the position of the name is the result to read.
				assigned[name.Name] = &localValue{Expr: assign.Rhs[0], Index: i}
			}
		}
		return true
	})
	return assigned
}

// statusCode reads http.StatusCreated or a bare 201 into its numeric value.
func statusCode(expr ast.Expr) (int, bool) {
	switch e := expr.(type) {
	case *ast.SelectorExpr:
		code, found := httpStatusCodes[e.Sel.Name]
		return code, found
	case *ast.BasicLit:
		if e.Kind != token.INT {
			return 0, false
		}
		code, err := strconv.Atoi(e.Value)
		return code, err == nil
	}
	return 0, false
}

func buildRoutes(repo *repoInfo, files []*goFile, controllerMethods map[string]*controllerMethodInfo, cfg *Config) []routeInfo {
	var routes []routeInfo

	for _, gf := range files {
		if !cfg.isRouteFile(gf.relPath) {
			continue
		}

		for _, decl := range gf.file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}

			controllerVars := collectControllerVars(repo, gf, fn.Body, cfg)
			// Groups are resolved as they are met. ast.Inspect walks in source
			// order, so a group is always recorded before the routes hung off
			// it — including groups opened inside an if or a loop, which a
			// top-level statement scan would miss entirely.
			groups := map[string]*groupInfo{}

			ast.Inspect(fn.Body, func(node ast.Node) bool {
				switch n := node.(type) {
				case *ast.AssignStmt:
					collectGroupVars(groups, n.Lhs, n.Rhs, cfg)
				case *ast.DeclStmt:
					if gen, ok := n.Decl.(*ast.GenDecl); ok && gen.Tok == token.VAR {
						for _, spec := range gen.Specs {
							if valueSpec, ok := spec.(*ast.ValueSpec); ok {
								collectGroupVars(groups, identExprs(valueSpec.Names), valueSpec.Values, cfg)
							}
						}
					}
				case *ast.CallExpr:
					route, ok := parseRouteCall(gf, n, controllerVars, groups, cfg)
					if !ok {
						return true
					}
					if route.HandlerMethod != "" {
						key := controllerRegistryKey(route.HandlerPackage, route.HandlerType, route.HandlerMethod)
						if meta, found := controllerMethods[key]; found {
							route.RequestType = meta.RequestType
							route.UsesPagination = meta.UsesPagination
							route.PathParams = uniqueSorted(append(route.PathParams, meta.PathParams...))
							route.Signals = meta.Signals
							route.Response = meta.Response
						}
					}
					if len(route.PathParams) == 0 {
						route.PathParams = pathParamsFromRoute(route.Path)
					}
					routes = append(routes, route)
				}
				return true
			})
		}
	}

	routes = appendHealthProbes(routes, healthRegistrarRoutes(files, cfg))

	sort.Slice(routes, func(i, j int) bool {
		if routes[i].Folder != routes[j].Folder {
			return routes[i].Folder < routes[j].Folder
		}
		if routes[i].Path != routes[j].Path {
			return routes[i].Path < routes[j].Path
		}
		return routes[i].Method < routes[j].Method
	})
	return routes
}

// groupInfo is a route group's accumulated context: the prefix every route under
// it inherits, and the authentication the group applied once for all of them.
type groupInfo struct {
	Prefix    string
	NeedsAuth bool
}

func collectGroupVars(groups map[string]*groupInfo, lhs, rhs []ast.Expr, cfg *Config) {
	for i, left := range lhs {
		if i >= len(rhs) {
			continue
		}
		name, ok := left.(*ast.Ident)
		if !ok || name.Name == "_" {
			continue
		}
		if group := groupFromExpr(groups, rhs[i], cfg); group != nil {
			groups[name.Name] = group
		}
	}
}

// groupFromExpr reads a `parent.Group("/prefix", middleware...)` call, inheriting
// the prefix and middleware of the parent group when there is one.
func groupFromExpr(groups map[string]*groupInfo, expr ast.Expr, cfg *Config) *groupInfo {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return nil
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != groupMethod || len(call.Args) == 0 {
		return nil
	}
	prefix, ok := stringLiteral(call.Args[0])
	if !ok {
		return nil
	}

	group := &groupInfo{}
	if parent, found := groups[receiverName(sel.X)]; found {
		*group = *parent
	}
	group.Prefix = joinRoutePath(group.Prefix, prefix)
	group.NeedsAuth = group.NeedsAuth || containsAnyName(call.Args[1:], cfg.AuthMiddlewares)
	return group
}

func identExprs(names []*ast.Ident) []ast.Expr {
	exprs := make([]ast.Expr, 0, len(names))
	for _, name := range names {
		exprs = append(exprs, name)
	}
	return exprs
}

// controllerRef is a controller type and the package that declares it.
//
// The package has to be carried separately because a module keeps its routes and
// its handlers in different directories:
//
//	// modules/note/note.http.go
//	c := &handler.NoteHandler{}
//
// The registry of handler methods is keyed by the directory the method is
// declared in, so reading the package off the route's own file would look up
// modules/note and find nothing — and every request body and response example
// would silently go missing.
type controllerRef struct {
	Package string // directory, relative to the repository root
	Type    string
}

func collectControllerVars(repo *repoInfo, gf *goFile, body *ast.BlockStmt, cfg *Config) map[string]controllerRef {
	named := collectTypedVars(body, cfg, cfg.isHandlerTypeName)

	refs := make(map[string]controllerRef, len(named))
	for name, typeName := range named {
		if ref, ok := resolveControllerRef(repo, gf, typeName); ok {
			refs[name] = ref
		}
	}

	return refs
}

// resolveControllerRef splits `handler.NoteController` into the directory
// handler resolves to and the bare type name. An unqualified name belongs to the
// file's own package; a qualifier that resolves outside this repository is not a
// controller of ours and is dropped.
func resolveControllerRef(repo *repoInfo, gf *goFile, typeName string) (controllerRef, bool) {
	alias, bare, qualified := strings.Cut(typeName, ".")
	if !qualified {
		return controllerRef{Package: gf.dirRel, Type: typeName}, true
	}

	importPath, found := gf.imports[alias]
	if !found {
		return controllerRef{}, false
	}

	dirRel, inRepo := dirRelForImportPath(repo, importPath)
	if !inRepo {
		return controllerRef{}, false
	}

	return controllerRef{Package: dirRel, Type: bare}, true
}

// dirRelForImportPath is importPathForDir in reverse.
func dirRelForImportPath(repo *repoInfo, importPath string) (string, bool) {
	if repo.moduleName == "" {
		return importPath, true
	}
	if importPath == repo.moduleName {
		return "", true
	}

	rest, ok := strings.CutPrefix(importPath, repo.moduleName+"/")

	return rest, ok
}

// collectTypedVars maps variable names to the type they hold. It walks the whole
// body rather than its top-level statements, so a handler or request built
// inside a block is still resolvable.
func collectTypedVars(body *ast.BlockStmt, cfg *Config, keep func(typeName string) bool) map[string]string {
	vars := map[string]string{}
	record := func(name string, typeName string) {
		if name == "" || name == "_" || typeName == "" {
			return
		}
		if keep != nil && !keep(typeName) {
			return
		}
		vars[name] = typeName
	}

	ast.Inspect(body, func(node ast.Node) bool {
		switch n := node.(type) {
		case *ast.AssignStmt:
			for i, lhs := range n.Lhs {
				if i >= len(n.Rhs) {
					continue
				}
				if name, ok := lhs.(*ast.Ident); ok {
					record(name.Name, typeNameFromExpr(n.Rhs[i], cfg))
				}
			}
		case *ast.ValueSpec:
			for i, name := range n.Names {
				// `var input requests.Foo` names its type directly; with an
				// initialiser the type comes from the value instead.
				if n.Type != nil {
					record(name.Name, exprTypeString(n.Type))
					continue
				}
				if i < len(n.Values) {
					record(name.Name, typeNameFromExpr(n.Values[i], cfg))
				}
			}
		}
		return true
	})
	return vars
}

func typeNameFromExpr(expr ast.Expr, cfg *Config) string {
	switch e := expr.(type) {
	case *ast.UnaryExpr:
		if e.Op == token.AND {
			return typeNameFromExpr(e.X, cfg)
		}
	case *ast.CompositeLit:
		return exprTypeString(e.Type)
	case *ast.CallExpr:
		switch fun := e.Fun.(type) {
		case *ast.Ident:
			// new(requests.Foo)
			if fun.Name == "new" && len(e.Args) == 1 {
				return exprTypeString(e.Args[0])
			}
			return controllerTypeFromConstructor(fun.Name, cfg)
		case *ast.SelectorExpr:
			// handler.NewAuthController(deps) — a controller with dependencies
			// is built by a constructor rather than by a bare literal.
			typeName := controllerTypeFromConstructor(fun.Sel.Name, cfg)
			if typeName == "" {
				return ""
			}
			if pkg, ok := fun.X.(*ast.Ident); ok {
				return pkg.Name + "." + typeName
			}
			return typeName
		}
	}
	return ""
}

// controllerTypeFromConstructor reads NewAuthHandler as AuthHandler.
//
// Deliberately only handlers. Every constructor in these projects is named
// New<Type>, so a general rule would resolve `svc := service.NewUserService(c)`
// to a type as well — harmless today, but this is a guess about what a name
// means, and a guess is worth making only where it is needed.
func controllerTypeFromConstructor(funcName string, cfg *Config) string {
	typeName, ok := strings.CutPrefix(funcName, "New")
	if !ok || !cfg.isHandlerTypeName(typeName) {
		return ""
	}

	return typeName
}

func parseRouteCall(gf *goFile, call *ast.CallExpr, controllerVars map[string]controllerRef, groups map[string]*groupInfo, cfg *Config) (routeInfo, bool) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return routeInfo{}, false
	}
	method := strings.ToUpper(sel.Sel.Name)
	switch method {
	case "GET", "POST", "PUT", "DELETE", "PATCH":
	default:
		return routeInfo{}, false
	}
	if len(call.Args) < 2 {
		return routeInfo{}, false
	}

	// The receiver must be a router: a known group, a plain identifier (the
	// server itself), or a group opened inline. Anything else — an HTTP client's
	// own Get, say — is not a route registration.
	group, ok := receiverGroup(groups, sel.X, cfg)
	if !ok {
		return routeInfo{}, false
	}

	path, ok := stringLiteral(call.Args[0])
	if !ok {
		return routeInfo{}, false
	}

	fullPath := normalizeRoutePath(joinRoutePath(group.Prefix, path))
	route := routeInfo{
		Method:         method,
		Path:           fullPath,
		Folder:         folderNameFromRoute(fullPath),
		Module:         moduleNameFromDir(gf.dirRel),
		HandlerPackage: gf.dirRel,
		PathParams:     pathParamsFromRoute(fullPath),
	}

	for _, arg := range call.Args[1:] {
		if len(route.Examples) == 0 {
			route.Examples = healthExamplesForHandler(arg)
		}
		if route.HandlerMethod == "" {
			if ref := handlerRefFromArg(arg, controllerVars); ref != nil {
				route.HandlerType = ref.TypeName
				route.HandlerMethod = ref.MethodName
				// A controller declared elsewhere — modules/note/handler for a
				// route in modules/note — moves the lookup to that package. A
				// package-level handler function has no controller and stays
				// where the route is.
				if ref.Package != "" {
					route.HandlerPackage = ref.Package
				}
				continue
			}
		}
	}

	// The group's middleware counts as much as the route's own: a route added to
	// an authenticated group is authenticated without repeating it.
	route.NeedsAuth = group.NeedsAuth || containsAnyName(call.Args[1:], cfg.AuthMiddlewares)

	return route, true
}

type handlerRef struct {
	// Package is the directory declaring the controller, empty for a
	// package-level handler function.
	Package    string
	TypeName   string
	MethodName string
}

// handlerRefFromArg names the handler a route argument refers to, whether it is
// passed bare (`c.Login`, `Status`) or through the context adapter
// (`WithHTTPContext(c.Login)`). A middleware call resolves to nil, which is what
// keeps it from being mistaken for the handler.
func handlerRefFromArg(arg ast.Expr, controllerVars map[string]controllerRef) *handlerRef {
	switch h := arg.(type) {
	case *ast.CallExpr:
		if callContainsName(h, handlerWrapper) && len(h.Args) > 0 {
			return handlerRefFromArg(h.Args[0], controllerVars)
		}
	case *ast.SelectorExpr:
		if ident, ok := h.X.(*ast.Ident); ok {
			if ctrl, found := controllerVars[ident.Name]; found {
				return &handlerRef{Package: ctrl.Package, TypeName: ctrl.Type, MethodName: h.Sel.Name}
			}
		}
	case *ast.Ident:
		// A package-level handler function: no receiver type to key it by.
		return &handlerRef{MethodName: h.Name}
	}
	return nil
}

// receiverGroup resolves what a route was registered on. A known group carries
// its prefix and middleware; a bare identifier is the server itself and adds
// nothing; an inline `e.Group("/x").GET(...)` is resolved on the spot.
func receiverGroup(groups map[string]*groupInfo, expr ast.Expr, cfg *Config) (*groupInfo, bool) {
	switch e := expr.(type) {
	case *ast.Ident:
		if group, found := groups[e.Name]; found {
			return group, true
		}
		return &groupInfo{}, true
	case *ast.CallExpr:
		if group := groupFromExpr(groups, e, cfg); group != nil {
			return group, true
		}
	}
	return nil, false
}

func receiverName(expr ast.Expr) string {
	if ident, ok := expr.(*ast.Ident); ok {
		return ident.Name
	}
	return ""
}

// joinRoutePath concatenates a group prefix and a route path into one clean
// path, where either side may be empty or carry its own slashes.
func joinRoutePath(prefix, path string) string {
	prefix = strings.TrimRight(prefix, "/")
	path = strings.Trim(path, "/")
	switch {
	case prefix == "" && path == "":
		return "/"
	case path == "":
		return prefix
	case prefix == "":
		return "/" + path
	default:
		return prefix + "/" + path
	}
}

// containsAnyName reports whether any of the expressions mentions one of the
// given identifiers. Matching is exact: "Auth" never matches "AuthorizeOwner".
func containsAnyName(exprs []ast.Expr, names []string) bool {
	for _, expr := range exprs {
		for _, name := range names {
			if exprContainsName(expr, name) {
				return true
			}
		}
	}
	return false
}

func buildCollection(routes []routeInfo, res *resolver, repo *repoInfo, cfg *Config) (*postmanCollection, error) {
	var items []postmanItem
	var err error

	switch cfg.Layout {
	case layoutFlat:
		items, err = buildFlatItems(routes, res, cfg)
	case layoutGrouped:
		items, err = buildGroupedItems(routes, res, cfg)
	default:
		return nil, fmt.Errorf("unsupported layout %q", cfg.Layout)
	}
	if err != nil {
		return nil, err
	}

	description := cfg.Description
	if cfg.Layout == layoutFlat {
		description += " Layout: flat."
	} else {
		description += " Layout: grouped."
	}

	return &postmanCollection{
		Info: postmanInfo{
			PostmanID:   cfg.collectionID(repo.moduleName),
			Name:        cfg.Name,
			Description: description,
			Schema:      "https://schema.getpostman.com/json/collection/v2.1.0/collection.json",
		},
		Variable: cfg.variables(),
		Item:     items,
	}, nil
}

func buildGroupedItems(routes []routeInfo, res *resolver, cfg *Config) ([]postmanItem, error) {
	root := newFolderNode("root", "")
	for _, route := range routes {
		item, err := buildRouteItem(route, res, cfg)
		if err != nil {
			return nil, err
		}
		insertRouteItem(root, route, item)
	}

	return buildFolderItems(root), nil
}

func buildFlatItems(routes []routeInfo, res *resolver, cfg *Config) ([]postmanItem, error) {
	folders := map[string][]postmanItem{}
	for _, route := range routes {
		item, err := buildRouteItem(route, res, cfg)
		if err != nil {
			return nil, err
		}
		folders[route.Folder] = append(folders[route.Folder], item)
	}

	folderNames := make([]string, 0, len(folders))
	for folder := range folders {
		folderNames = append(folderNames, folder)
	}
	sort.Strings(folderNames)

	items := make([]postmanItem, 0, len(folderNames))
	for _, folder := range folderNames {
		children := folders[folder]
		sort.Slice(children, func(i, j int) bool {
			if children[i].SortKey != children[j].SortKey {
				return children[i].SortKey < children[j].SortKey
			}
			return children[i].Name < children[j].Name
		})
		items = append(items, postmanItem{
			Name: folder,
			Item: children,
		})
	}
	return items, nil
}

func buildRouteItem(route routeInfo, res *resolver, cfg *Config) (postmanItem, error) {
	req := &postmanReq{
		Method: route.Method,
		URL:    buildURL(route),
	}

	if route.NeedsAuth {
		req.Header = append(req.Header, postmanHeader{
			Key:   "Authorization",
			Value: fmt.Sprintf("Bearer {{%s}}", cfg.AuthVariable),
			Type:  "text",
		})
	}

	var request *structInfo
	if route.RequestType != nil {
		request = res.structAt(route.RequestType.ImportPath, route.RequestType.TypeName)
	}

	if query := buildQueryParams(request, res, route); len(query) > 0 {
		req.URL.Query = query
		req.URL.Raw = appendQuery(req.URL.Raw, query)
	}

	req.Header = append(req.Header, buildHeaders(route, cfg)...)

	if methodAllowsBody(route.Method) {
		if body, contentType := buildBody(request, res, route.Signals); body != nil {
			req.Body = body
			if contentType != "" {
				req.Header = append(req.Header, postmanHeader{Key: "Content-Type", Value: contentType, Type: "text"})
			}
		}
	}

	req.Header = sortedHeaders(req.Header)

	item := postmanItem{
		SortKey:  strings.ToLower(route.Path + " " + route.Method),
		Name:     fmt.Sprintf("%s %s", route.Method, route.Path),
		Request:  req,
		Response: buildExampleResponses(route, req, res),
	}

	if event := loginCaptureEvent(route.Path, cfg); event != nil {
		item.Event = []postmanEvent{*event}
	}

	return item, nil
}

// buildExampleResponses saves the replies a call can produce as Postman
// examples, so the collection documents the endpoint without anyone having to
// run it first: the replies the framework writes on its own (the health
// probes), the success the handler writes, and the failures the framework
// answers with before the handler is ever reached.
func buildExampleResponses(route routeInfo, req *postmanReq, res *resolver) []postmanResponse {
	examples := []postmanResponse{}

	for _, static := range route.Examples {
		example := newExampleResponse(static.Code, req)
		example.withBody(static.Body)
		examples = append(examples, example)
	}

	if route.Response != nil {
		example := newExampleResponse(route.Response.StatusCode, req)
		if body, ok := res.sampleResponseBody(route.Response); ok {
			example.withBody(body)
		}
		examples = append(examples, example)
	}

	if body, ok := res.validationErrorBody(route); ok {
		example := newExampleResponse(http.StatusBadRequest, req)
		example.Name = "400 Invalid parameters"
		example.withBody(body)
		examples = append(examples, example)
	}

	if route.NeedsAuth {
		example := newExampleResponse(http.StatusUnauthorized, req)
		example.withBody(errorBody(unauthorizedCode, unauthorizedMessage, nil))
		examples = append(examples, example)
	}

	return examples
}

func newExampleResponse(code int, req *postmanReq) postmanResponse {
	return postmanResponse{
		Name:            fmt.Sprintf("%d %s", code, http.StatusText(code)),
		OriginalRequest: req,
		Status:          http.StatusText(code),
		Code:            code,
		Header:          []postmanHeader{},
		Cookie:          []any{},
	}
}

func (p *postmanResponse) withBody(body string) {
	p.PreviewLanguage = "json"
	p.Header = append(p.Header, postmanHeader{Key: "Content-Type", Value: "application/json"})
	p.Body = body
}

// validationErrorBody renders the rejection a caller gets for omitting a
// required field. The field and its code come from the request's own rules, so
// the example matches what the server would actually answer.
func (r *resolver) validationErrorBody(route routeInfo) (string, bool) {
	if route.RequestType == nil {
		return "", false
	}
	info := r.structAt(route.RequestType.ImportPath, route.RequestType.TypeName)
	if info == nil {
		return "", false
	}

	rules := r.rulesFor(info)
	for _, field := range info.Fields {
		rule := rules[field.Name]
		if rule == nil || !rule.has(requiredRule) {
			continue
		}
		name := rule.ReportedName
		if name == "" {
			name = reflectTagValue(field.Tag, "json")
		}
		if name == "" {
			continue
		}
		return errorBody(invalidParamsCode, invalidParamsMessage, map[string]fieldErrorJSON{
			name: {
				Code:    requiredCode,
				Message: fmt.Sprintf("The %s field is required", name),
				In:      fieldSource(field),
			},
		}), true
	}
	return "", false
}

// fieldSource is where the framework says a value came from, which is the
// binding tag it was read through.
func fieldSource(field structField) string {
	switch {
	case reflectTagValue(field.Tag, "param") != "":
		return "path"
	case reflectTagValue(field.Tag, "query") != "":
		return "query"
	case reflectTagValue(field.Tag, "header") != "":
		return "header"
	default:
		return "body"
	}
}

// errorBodyJSON and fieldErrorJSON mirror the framework's own error body, field
// for field and in the same order, so the example reads exactly like the real
// reply rather than merely carrying the same data.
type errorBodyJSON struct {
	Code    string                    `json:"code"`
	Message string                    `json:"message"`
	Fields  map[string]fieldErrorJSON `json:"fields,omitempty"`
}

type fieldErrorJSON struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	In      string `json:"in,omitempty"`
}

func errorBody(code, message string, fields map[string]fieldErrorJSON) string {
	raw, ok := marshalSample(errorBodyJSON{Code: code, Message: message, Fields: fields})
	if !ok {
		return ""
	}
	return raw
}

func buildURL(route routeInfo) postmanURL {
	trimmed := strings.Trim(route.Path, "/")
	segments := []string{}
	rawPath := "{{baseUrl}}"
	if trimmed == "" {
		rawPath += "/"
	} else {
		// Path params stay as ":id". That is the form Postman substitutes from
		// the url.variable list below — "{{id}}" would look for a collection
		// variable that does not exist and be sent through literally.
		segments = append(segments, strings.Split(trimmed, "/")...)
		rawPath += "/" + strings.Join(segments, "/")
	}

	variables := make([]postmanVariable, 0, len(route.PathParams))
	for _, param := range route.PathParams {
		variables = append(variables, postmanVariable{
			Key:   param,
			Value: sampleValueForPathParam(param),
		})
	}
	sort.Slice(variables, func(i, j int) bool {
		return variables[i].Key < variables[j].Key
	})

	return postmanURL{
		Raw:      rawPath,
		Host:     []string{"{{baseUrl}}"},
		Path:     segments,
		Variable: variables,
	}
}

// buildQueryParams is every query parameter the endpoint takes, from all three
// places one can be stated: a `query:` tag on the bound request, the page and
// limit GetPageOptions reads, and a c.QueryParam the handler makes itself.
//
// info is nil for a route that binds nothing, which is exactly the route the
// third of those matters most for.
func buildQueryParams(info *structInfo, res *resolver, route routeInfo) []postmanQuery {
	queries := map[string]postmanQuery{}
	if info != nil {
		res.collectQueryFields(info, queries, visitedSet(info))
	}
	if route.UsesPagination {
		queries["page"] = postmanQuery{Key: "page", Value: "1"}
		queries["limit"] = postmanQuery{Key: "limit", Value: "30"}
	}
	// A tag and a c.QueryParam call can name the same parameter. The tag carries
	// a type and the field's rules, so it says more; the call only fills a gap.
	for _, param := range route.Signals.Query {
		if _, found := queries[param.Name]; found {
			continue
		}
		queries[param.Name] = postmanQuery{
			Key:         param.Name,
			Value:       param.sampleValue(),
			Description: param.description(),
		}
	}

	keys := make([]string, 0, len(queries))
	for key := range queries {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	result := make([]postmanQuery, 0, len(keys))
	for _, key := range keys {
		result = append(result, queries[key])
	}
	return result
}

// buildHeaders states the headers the handler reads for itself. A cookie is one
// too: Postman's cookie jar belongs to a domain rather than to a collection, so
// writing the header is the only way a file can carry the cookie an endpoint
// wants.
func buildHeaders(route routeInfo, cfg *Config) []postmanHeader {
	headers := make([]postmanHeader, 0, len(route.Signals.Headers)+1)
	for _, name := range route.Signals.Headers {
		headers = append(headers, postmanHeader{Key: name, Value: sampleHeaderValue(name, cfg), Type: "text"})
	}

	if len(route.Signals.Cookies) > 0 {
		pairs := make([]string, 0, len(route.Signals.Cookies))
		for _, name := range route.Signals.Cookies {
			pairs = append(pairs, name+"="+sampleTextForName(name))
		}
		headers = append(headers, postmanHeader{Key: "Cookie", Value: strings.Join(pairs, "; "), Type: "text"})
	}
	return headers
}

// sampleHeaderValue reads a header name the way a field name is read, once its
// dashes are spelled the way the sample tables spell a separator. Authorization
// is the one header whose value the collection already knows.
func sampleHeaderValue(name string, cfg *Config) string {
	if strings.EqualFold(name, "Authorization") && cfg.AuthVariable != "" {
		return fmt.Sprintf("Bearer {{%s}}", cfg.AuthVariable)
	}
	return sampleTextForName(strings.ReplaceAll(name, "-", "_"))
}

// sortedHeaders puts the headers in a settled order and keeps one of each. A
// route that requires a token and also reads Authorization for itself states the
// same header twice; sending it twice is not what the handler meant.
func sortedHeaders(headers []postmanHeader) []postmanHeader {
	sort.SliceStable(headers, func(i, j int) bool {
		return headers[i].Key < headers[j].Key
	})

	unique := headers[:0]
	for i, header := range headers {
		if i > 0 && strings.EqualFold(headers[i-1].Key, header.Key) {
			continue
		}
		unique = append(unique, header)
	}
	return unique
}

// buildBody is the payload to send and the Content-Type to send it under, or nil
// when the endpoint takes no body.
//
// What the handler does decides the shape. A c.FormFile makes it multipart, a
// c.FormValue makes it a form, and a bound request struct on its own stays JSON.
// A handler doing both — the usual upload-with-metadata — gets one body carrying
// the struct's fields and the file together, which is the request it accepts.
func buildBody(info *structInfo, res *resolver, signals requestSignals) (*postmanBody, string) {
	switch {
	case signals.Multipart:
		params := buildFormParams(info, res, signals, true)
		if len(params) == 0 {
			return nil, ""
		}
		// No Content-Type: a multipart body needs the boundary Postman writes,
		// and a fixed header cannot state a boundary that does not exist yet.
		return &postmanBody{Mode: bodyModeFormData, FormData: params}, ""
	case signals.HasForm || len(signals.Form) > 0:
		params := buildFormParams(info, res, signals, false)
		if len(params) == 0 {
			return nil, ""
		}
		return &postmanBody{Mode: bodyModeURLEncoded, URLEncoded: params}, contentTypeForm
	default:
		raw, ok := buildRequestBody(info, res)
		if !ok {
			return nil, ""
		}
		return &postmanBody{Mode: bodyModeRaw, Raw: raw}, contentTypeJSON
	}
}

// buildFormParams is a form body's entries: what the bound request declares,
// what the handler reads by name, and the files it asks for.
func buildFormParams(info *structInfo, res *resolver, signals requestSignals, withFiles bool) []postmanFormParam {
	params := map[string]postmanFormParam{}

	if info != nil {
		fields := map[string]any{}
		res.collectBodyFields(info, nil, fields, visitedSet(info), formTags)
		for key, value := range fields {
			params[key] = postmanFormParam{Key: key, Value: formValueText(value), Type: "text"}
		}
	}
	for _, param := range signals.Form {
		if _, found := params[param.Name]; found {
			continue
		}
		params[param.Name] = postmanFormParam{
			Key:         param.Name,
			Value:       param.sampleValue(),
			Type:        "text",
			Description: param.description(),
		}
	}
	if withFiles {
		// A file beats a text entry of the same name: the handler asking for a
		// file is the one that says what the part actually carries.
		for _, name := range signals.Files {
			params[name] = postmanFormParam{Key: name, Type: "file"}
		}
	}

	keys := make([]string, 0, len(params))
	for key := range params {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	result := make([]postmanFormParam, 0, len(keys))
	for _, key := range keys {
		result = append(result, params[key])
	}
	return result
}

// formValueText renders a body field as the text a form part carries. A nested
// object has no form spelling of its own, so it is sent as the JSON it is.
func formValueText(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	if raw, err := json.Marshal(value); err == nil {
		return string(raw)
	}
	return fmt.Sprint(value)
}

func buildRequestBody(info *structInfo, res *resolver) (string, bool) {
	if info == nil {
		return "", false
	}
	body := map[string]any{}
	res.collectBodyFields(info, nil, body, visitedSet(info), bodyTags)
	if len(body) == 0 {
		return "", false
	}
	return marshalSample(body)
}

func marshalSample(value any) (string, bool) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return "", false
	}
	return string(data), true
}

// Sample values a reader recognises at a glance. Every one is deterministic:
// regenerating the collection with the routes unchanged must not change a byte,
// which is why nothing here is random.
const (
	sampleEmail    = "user@example.com"
	sampleUUID     = "3fa85f64-5717-4562-b3fc-2c963f66afa6"
	sampleURL      = "https://example.com"
	sampleDate     = "2026-01-01"
	sampleTime     = "00:00:00"
	sampleDateTime = "2026-01-01 00:00:00"
	sampleISO8601  = "2026-01-01T00:00:00Z"
	sampleString   = "string"
)

// sampleStrings gives a field a value that looks like what belongs in it, for
// the fields no rule describes. Matched against the JSON name; the first entry
// that matches wins, so a longer name is listed before the shorter one it ends
// with ("full_name" before "name").
var sampleStrings = []struct {
	Match string
	Value string
}{
	{"email", sampleEmail},
	{"password", "secret1234"},
	{"full_name", "Jane Doe"},
	{"first_name", "Jane"},
	{"last_name", "Doe"},
	{"username", "jane"},
	{"phone", "+66812345678"},
	{"url", sampleURL},
	{"image", sampleURL + "/image.png"},
	{"token", "eyJhbGciOiJIUzI1NiJ9.sample.signature"},
	{"status", "ACTIVE"},
	{"slug", "sample-slug"},
	{"code", "SAMPLE"},
	{"title", "Sample title"},
	{"description", "Sample description"},
	{"name", "Sample name"},
	{"id", sampleUUID},
}

// sampleNumbers does the same for numeric fields, where 1 is rarely the number
// that explains what the field means.
var sampleNumbers = []struct {
	Match string
	Value int
}{
	{"page", 1},
	{"per_page", 10},
	{"limit", 10},
	{"offset", 0},
	{"total", 1},
	{"count", 1},
	{"expires_in", 3600},
	{"age", 30},
	{"quantity", 1},
	{"amount", 100},
	{"price", 100},
}

// sampleStringForName picks a value by what the field is called. Matching allows
// a prefix or suffix, so "user_email" and "email_address" both read as an email.
func sampleStringForName(name string) string {
	lower := strings.ToLower(name)
	for _, entry := range sampleStrings {
		if nameMatches(lower, entry.Match) {
			return entry.Value
		}
	}
	return sampleString
}

// sampleTextForName is the value a parameter read by name alone gets. There is
// no declared type to render from — a query string carries text and nothing
// else — so the name is all there is to go on, and a name that plainly means a
// number or a flag is written as one rather than as "string".
func sampleTextForName(name string) string {
	lower := strings.ToLower(name)
	if strings.HasPrefix(lower, "is_") || strings.HasPrefix(lower, "has_") || strings.HasSuffix(lower, "_enabled") {
		return "true"
	}
	for _, entry := range sampleNumbers {
		if nameMatches(lower, entry.Match) {
			return strconv.Itoa(entry.Value)
		}
	}
	return sampleStringForName(name)
}

func sampleNumberForName(name string) int {
	lower := strings.ToLower(name)
	for _, entry := range sampleNumbers {
		if nameMatches(lower, entry.Match) {
			return entry.Value
		}
	}
	return 1
}

func nameMatches(name, match string) bool {
	return name == match || strings.HasSuffix(name, "_"+match) || strings.HasPrefix(name, match+"_")
}

// fieldRule is one rule applied to a field, such as Length(8, 72).
type fieldRule struct {
	Name string
	Args []ast.Expr
	// Spread marks In(Kinds...), where the single argument stands for the whole
	// list of allowed values rather than for one of them.
	Spread bool
}

// fieldRules are everything a request states about one field: what the framework
// calls it, what kind of value it is, and the rules it must satisfy.
type fieldRules struct {
	ReportedName string
	Kind         string
	Rules        []fieldRule
	// Ctx is the file the rules were written in, which is where their arguments
	// resolve. It is not always the file the struct was declared in.
	Ctx *typeCtx
}

// resolveCtx is where a rule's arguments are looked up: the file the rules were
// written in when that is known, and the caller's context otherwise.
func (f *fieldRules) resolveCtx(fallback *typeCtx) *typeCtx {
	if f.Ctx != nil {
		return f.Ctx
	}
	return fallback
}

func (f *fieldRules) has(name string) bool {
	return f.rule(name) != nil
}

func (f *fieldRules) rule(name string) *fieldRule {
	for i := range f.Rules {
		if f.Rules[i].Name == name {
			return &f.Rules[i]
		}
	}
	return nil
}

// validatorEntries are the calls that open a rule chain, mapped to the kind of
// value they describe.
var validatorEntries = map[string]string{
	"Str":   "string",
	"Int":   "number",
	"Float": "number",
	"Bool":  "bool",
	"Arr":   "array",
	"Time":  "time",
}

// rulesFor reads the rules a struct states in its Valid/Validate method. The
// rules are the authority on what a valid value looks like, so a sample built
// from them is one the server will accept.
func (r *resolver) rulesFor(info *structInfo) map[string]*fieldRules {
	key := structKey(info)
	if cached, found := r.ruleCache[key]; found {
		return cached
	}

	rules := map[string]*fieldRules{}
	if validator := r.validators[key]; validator != nil {
		rules = extractRules(validator.Body, validator.Ctx)
	}
	r.ruleCache[key] = rules
	return rules
}

// extractRules reads every `v.Str("email", r.Email).Required().Email()` chain in
// a validator body back to the fields it opened with.
//
// A chain is taken apart wherever it appears rather than only as a statement of
// its own, and every entry in it is read rather than only the last. The
// framework documents the fully chained form —
//
//	return valid.New(ctx).
//	    Str("role", &r.Role).Required().In("ADMIN", "MEMBER").
//	    Int("score", &r.Score).Min(0).
//	    Valid()
//
// — which is one return statement holding one chain with two fields in it. Read
// as a statement ending at the first entry, that whole method stated nothing:
// every request written the documented way published no rules at all, and the
// page still looked complete, which is what kept it from being noticed.
func extractRules(body *ast.BlockStmt, ctx *typeCtx) map[string]*fieldRules {
	rules := map[string]*fieldRules{}

	// A chain is read from its outermost call, and the calls nested inside it
	// are the same chain seen again. Marking them keeps `Required().Min(3)`
	// from being counted once for the whole chain and once for its own tail.
	consumed := map[*ast.CallExpr]struct{}{}

	ast.Inspect(body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		if _, done := consumed[call]; done {
			return true
		}
		links, calls, ok := unrollChain(call)
		if !ok {
			return true
		}
		for _, inner := range calls {
			consumed[inner] = struct{}{}
		}

		// The chain was walked outermost first; source order is what pairs a
		// rule with the entry it was written after.
		var current *fieldRules
		for i := len(links) - 1; i >= 0; i-- {
			link := links[i]
			kind, isEntry := validatorEntries[link.Name]
			if !isEntry {
				if current != nil {
					current.Rules = append(current.Rules, link)
				}
				continue
			}

			current = nil
			if len(link.Args) < 2 {
				continue
			}
			field, reported := ruleFieldName(link.Args)
			if field == "" {
				continue
			}
			entry, found := rules[field]
			if !found {
				entry = &fieldRules{ReportedName: reported, Kind: kind, Ctx: ctx}
				rules[field] = entry
			}
			current = entry
		}

		return true
	})
	return rules
}

// unrollChain takes a method chain apart into its links, outermost first, and
// reports the calls those links came from. It reports false for a call that
// opens no rules, so an ordinary method call in the validator body is left
// alone.
func unrollChain(call *ast.CallExpr) ([]fieldRule, []*ast.CallExpr, bool) {
	links := []fieldRule{}
	calls := []*ast.CallExpr{}
	hasEntry := false

	for expr := ast.Expr(call); ; {
		next, ok := expr.(*ast.CallExpr)
		if !ok {
			break
		}
		sel, ok := next.Fun.(*ast.SelectorExpr)
		if !ok {
			break
		}
		if _, isEntry := validatorEntries[sel.Sel.Name]; isEntry {
			hasEntry = true
		}
		links = append(links, fieldRule{
			Name:   sel.Sel.Name,
			Args:   next.Args,
			Spread: next.Ellipsis.IsValid(),
		})
		calls = append(calls, next)
		expr = sel.X
	}
	return links, calls, hasEntry
}

// ruleFieldName reports which struct field a rule chain is about. The value
// argument (r.Email) names it exactly; the reported name is what the framework
// puts in an error.
func ruleFieldName(args []ast.Expr) (field string, reported string) {
	reported, _ = stringLiteral(args[0])

	value := args[1]
	if unary, ok := value.(*ast.UnaryExpr); ok && unary.Op == token.AND {
		value = unary.X
	}
	if sel, ok := value.(*ast.SelectorExpr); ok {
		return sel.Sel.Name, reported
	}
	return reported, reported
}

// sampleFromRules builds a value the rules accept: the format rule decides what
// the value looks like, and the length rules stretch or trim it to fit.
func (r *resolver) sampleFromRules(rules *fieldRules, name string, ctx *typeCtx) (any, bool) {
	ctx = rules.resolveCtx(ctx)
	switch rules.Kind {
	case "number":
		return r.numberFromRules(rules, name, ctx), true
	case "bool":
		return true, true
	case "string":
	default:
		return nil, false
	}

	value := ""
	formatted := false
	prefix, suffix, contains := "", "", ""
	minLen, maxLen := 0, 0
	lower, upper := false, false

	for _, rule := range rules.Rules {
		switch rule.Name {
		case "Email":
			value, formatted = sampleEmail, true
		case "UUID":
			value, formatted = sampleUUID, true
		case "URL":
			value, formatted = sampleURL, true
		case "IP":
			value, formatted = "192.0.2.1", true
		case "Base64":
			value, formatted = "c2FtcGxl", true
		case "JSON":
			value, formatted = `{"key":"value"}`, true
		case "Numeric":
			value, formatted = "1234567890", true
		case "Date", "AnyDate":
			value, formatted = sampleDate, true
		case "Time", "AnyTime":
			value, formatted = sampleTime, true
		case "DateTime", "AnyDateTime", "AnyTemporal":
			value, formatted = sampleDateTime, true
		case "ISO8601":
			value, formatted = sampleISO8601, true
		case "In":
			// An allowed value beats anything invented: the first option is
			// always one the server accepts.
			if option, ok := r.stringArg(rule, 0, ctx); ok {
				value, formatted = option, true
			}
		case "Length":
			minLen, maxLen = r.intArg(rule, 0, ctx), r.intArg(rule, 1, ctx)
		case "Min":
			minLen = r.intArg(rule, 0, ctx)
		case "Max":
			maxLen = r.intArg(rule, 0, ctx)
		case "Prefix":
			prefix, _ = r.stringArg(rule, 0, ctx)
		case "Suffix":
			suffix, _ = r.stringArg(rule, 0, ctx)
		case "Contains":
			contains, _ = r.stringArg(rule, 0, ctx)
		case "Lowercase", "Lower":
			lower = true
		case "Uppercase":
			upper = true
		}
	}

	if value == "" {
		value = sampleStringForName(name)
	}
	if formatted {
		// Padding an email or a UUID would only make it invalid.
		return value, true
	}

	if contains != "" && !strings.Contains(value, contains) {
		value += contains
	}
	if prefix != "" && !strings.HasPrefix(value, prefix) {
		value = prefix + value
	}
	if suffix != "" && !strings.HasSuffix(value, suffix) {
		value += suffix
	}
	if minLen > len(value) {
		value += strings.Repeat("x", minLen-len(value))
	}
	if maxLen > 0 && len(value) > maxLen {
		value = value[:maxLen]
	}
	switch {
	case lower:
		value = strings.ToLower(value)
	case upper:
		value = strings.ToUpper(value)
	}
	return value, true
}

func (r *resolver) numberFromRules(rules *fieldRules, name string, ctx *typeCtx) int {
	value := sampleNumberForName(name)
	for _, rule := range rules.Rules {
		switch rule.Name {
		case "In":
			// the first allowed value, whether it was written as a literal or
			// as the constant the rest of the service branches on
			if args, _ := r.ruleArgs(rule, ctx); len(args) > 0 {
				return r.intArg(rule, 0, ctx)
			}
		case "Min", "Between":
			if min := r.intArg(rule, 0, ctx); min > value {
				value = min
			}
		case "Max":
			if max := r.intArg(rule, 0, ctx); max > 0 && value > max {
				value = max
			}
		case "Positive":
			if value < 1 {
				value = 1
			}
		}
	}
	return value
}

// intArg reads a rule's numeric argument, following a constant to its value.
// stringArg reads a rule's string argument, following a constant or a
// package-level var to the value it stands for. It is the string counterpart of
// intArg, and exists for the same reason: a rule written in terms of the
// constants the rest of the service uses says exactly as much as one written
// with a literal, and is the more common way to write it.
func (r *resolver) stringArg(rule fieldRule, index int, ctx *typeCtx) (string, bool) {
	if index >= len(rule.Args) {
		return "", false
	}
	return r.stringExpr(rule.Args[index], ctx)
}

// ruleArgs is the expressions a rule states, with a spread argument replaced by
// the elements it stands for, and the context those expressions resolve in.
// In(Kinds...) names the same set of allowed values as In("A", "B") and has to
// read as the same enum — but its elements were written where Kinds was
// declared, so they resolve there.
func (r *resolver) ruleArgs(rule fieldRule, ctx *typeCtx) ([]ast.Expr, *typeCtx) {
	if !rule.Spread || len(rule.Args) != 1 {
		return rule.Args, ctx
	}
	if slice, ok := r.sliceExpr(rule.Args[0], ctx, 0); ok {
		return slice.Elements, slice.Ctx
	}
	return nil, ctx
}

// sliceExpr resolves an expression to the slice it stands for, whether written
// inline or declared elsewhere and named.
func (r *resolver) sliceExpr(expr ast.Expr, ctx *typeCtx, depth int) (*constSlice, bool) {
	if depth > maxConstDepth {
		return nil, false
	}
	if elements, ok := sliceElements(expr); ok {
		return &constSlice{Elements: elements, Ctx: ctx}, true
	}
	key, ok := r.declKey(expr, ctx)
	if !ok {
		return nil, false
	}
	if slice, found := r.sliceConsts[key]; found {
		return slice, true
	}
	if ref, found := r.constRefs[key]; found {
		return r.sliceExpr(ref.Value, ref.Ctx, depth+1)
	}
	return nil, false
}

// stringArgs is every string argument of a rule, in order, skipping the ones
// that cannot be resolved. A partially resolvable In() reports the values it
// could read rather than nothing: some of the allowed values beats none.
func (r *resolver) stringArgs(rule fieldRule, ctx *typeCtx) []string {
	args, argCtx := r.ruleArgs(rule, ctx)
	values := make([]string, 0, len(args))
	for _, arg := range args {
		if value, ok := r.stringExpr(arg, argCtx); ok {
			values = append(values, value)
		}
	}
	return values
}

// stringExpr resolves one expression to the string it stands for.
func (r *resolver) stringExpr(expr ast.Expr, ctx *typeCtx) (string, bool) {
	return r.stringExprDepth(expr, ctx, 0)
}

// maxConstDepth bounds how far a name is followed to the value behind it. A
// constant declared in terms of another is normal; a cycle is not reachable in
// compiling Go, but the source this reads need not compile.
const maxConstDepth = 8

func (r *resolver) stringExprDepth(expr ast.Expr, ctx *typeCtx, depth int) (string, bool) {
	if depth > maxConstDepth {
		return "", false
	}

	switch arg := expr.(type) {
	case *ast.BasicLit:
		return stringLiteral(arg)
	case *ast.Ident, *ast.SelectorExpr:
		key, ok := r.declKey(expr, ctx)
		if !ok {
			return "", false
		}
		if value, found := r.strConsts[key]; found {
			return value, true
		}
		if ref, found := r.constRefs[key]; found {
			return r.stringExprDepth(ref.Value, ref.Ctx, depth+1)
		}
		return "", false
	case *ast.CallExpr:
		// a conversion at the call site: In(Role("ADMIN"), roleMember)
		if len(arg.Args) == 1 {
			switch arg.Fun.(type) {
			case *ast.Ident, *ast.SelectorExpr:
				return r.stringExprDepth(arg.Args[0], ctx, depth+1)
			}
		}
		// the other direction: a typed constant handed to In(...string) has to
		// be widened at the call site, and `Kind.String()` is how that is
		// written when the type carries a method for it.
		if len(arg.Args) == 0 {
			if sel, ok := arg.Fun.(*ast.SelectorExpr); ok {
				return r.stringExprDepth(sel.X, ctx, depth+1)
			}
		}
	}
	return "", false
}

// declKey is the registry key for the declaration a name refers to, resolving
// which package a qualified name belongs to.
func (r *resolver) declKey(expr ast.Expr, ctx *typeCtx) (string, bool) {
	if ctx == nil {
		return "", false
	}
	switch arg := expr.(type) {
	case *ast.Ident:
		return registryKey(ctx.ImportPath, arg.Name), true
	case *ast.SelectorExpr:
		pkg, ok := arg.X.(*ast.Ident)
		if !ok {
			return "", false
		}
		importPath, ok := r.importPathFor(ctx, pkg.Name)
		if !ok {
			return "", false
		}
		return registryKey(importPath, arg.Sel.Name), true
	}
	return "", false
}

// importPathFor resolves the identifier a qualified name is written under to
// the package it imports.
//
// The identifier is usually the last path segment, which is why that is tried
// first. When it is not — a `services/` directory declaring `package service`,
// which is common enough — the package's own name settles it, and without this
// every rule written in terms of that package's constants resolved to nothing
// and published no enum at all.
func (r *resolver) importPathFor(ctx *typeCtx, pkgName string) (string, bool) {
	if importPath, found := ctx.Imports[pkgName]; found {
		return importPath, true
	}

	// Only packages already parsed are consulted. Every package in the repo is
	// one of them, which is the case this exists for; loading each import just
	// to read its package clause would cost a `go list` per import of every
	// file, to settle a mismatch that only ever comes from the repo's own
	// naming.
	candidates := make([]string, 0, len(ctx.Imports))
	for _, importPath := range ctx.Imports {
		if r.pkgNames[importPath] == pkgName {
			candidates = append(candidates, importPath)
		}
	}
	if len(candidates) == 0 {
		return "", false
	}
	// Two imports cannot bind the same identifier without an alias, so a tie
	// means the guess was wrong somewhere; the shortest path is the stable
	// choice rather than whichever the map happened to yield first.
	sort.Strings(candidates)
	return candidates[0], true
}

func (r *resolver) intArg(rule fieldRule, index int, ctx *typeCtx) int {
	args, argCtx := r.ruleArgs(rule, ctx)
	if index >= len(args) {
		return 0
	}
	value, _ := r.intExpr(args[index], argCtx, 0)
	return value
}

// intExpr resolves one expression to the number it stands for.
func (r *resolver) intExpr(expr ast.Expr, ctx *typeCtx, depth int) (int, bool) {
	if depth > maxConstDepth {
		return 0, false
	}

	switch arg := expr.(type) {
	case *ast.BasicLit:
		if arg.Kind != token.INT {
			return 0, false
		}
		if value, err := strconv.Atoi(arg.Value); err == nil {
			return value, true
		}
	case *ast.Ident, *ast.SelectorExpr:
		key, ok := r.declKey(expr, ctx)
		if !ok {
			return 0, false
		}
		if value, found := r.consts[key]; found {
			return value, true
		}
		if ref, found := r.constRefs[key]; found {
			return r.intExpr(ref.Value, ref.Ctx, depth+1)
		}
	case *ast.CallExpr:
		if len(arg.Args) == 1 {
			switch arg.Fun.(type) {
			case *ast.Ident, *ast.SelectorExpr:
				return r.intExpr(arg.Args[0], ctx, depth+1)
			}
		}
	}
	return 0, false
}

// sampleResponseBody renders the value a handler writes. It follows the value
// back through the variable it was assigned and the service call that produced
// it, then renders whatever type that call returns.
func (r *resolver) sampleResponseBody(info *responseInfo) (string, bool) {
	if info.Value == nil {
		return "", false
	}
	value := r.sampleValueExpr(info.Value, info, map[string]struct{}{}, 0)
	if value == nil {
		return "", false
	}
	return marshalSample(value)
}

const maxValueDepth = 8

// sampleValueExpr renders an expression by value where it is written literally,
// and by type where it is not.
func (r *resolver) sampleValueExpr(expr ast.Expr, info *responseInfo, visited map[string]struct{}, depth int) any {
	if depth > maxValueDepth {
		return map[string]any{}
	}

	switch e := expr.(type) {
	case *ast.BasicLit:
		return literalValue(e)
	case *ast.UnaryExpr:
		if e.Op == token.AND {
			return r.sampleValueExpr(e.X, info, visited, depth+1)
		}
	case *ast.CompositeLit:
		return r.sampleCompositeLit(e, info, visited, depth)
	case *ast.Ident:
		local, found := info.Locals[e.Name]
		if !found {
			return map[string]any{}
		}
		if _, seen := visited[e.Name]; seen {
			return map[string]any{}
		}
		visited[e.Name] = struct{}{}
		defer delete(visited, e.Name)

		if call, ok := local.Expr.(*ast.CallExpr); ok {
			return r.sampleCallResult(call, local.Index, info, depth)
		}
		return r.sampleValueExpr(local.Expr, info, visited, depth+1)
	case *ast.CallExpr:
		if name, ok := e.Fun.(*ast.Ident); ok && name.Name == "len" {
			return 1
		}
		return r.sampleCallResult(e, 0, info, depth)
	}
	return map[string]any{}
}

// sampleCompositeLit renders a literal the handler builds inline, which is how a
// hand-assembled response object keeps its real keys.
func (r *resolver) sampleCompositeLit(lit *ast.CompositeLit, info *responseInfo, visited map[string]struct{}, depth int) any {
	switch lit.Type.(type) {
	case *ast.ArrayType:
		if len(lit.Elts) == 0 {
			return []any{}
		}
		return []any{r.sampleValueExpr(lit.Elts[0], info, visited, depth+1)}
	case *ast.MapType, nil:
		out := map[string]any{}
		for _, elt := range lit.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			key, ok := stringLiteral(kv.Key)
			if !ok {
				continue
			}
			out[key] = r.sampleValueExpr(kv.Value, info, visited, depth+1)
		}
		return out
	default:
		// A named struct literal: render it from its declaration.
		return r.sampleJSONValue(lit.Type, "", info.Ctx, nil, map[string]struct{}{})
	}
}

// sampleCallResult renders what a call returns, following the chain of
// constructors and interfaces between the handler and the type it receives.
func (r *resolver) sampleCallResult(call *ast.CallExpr, index int, info *responseInfo, depth int) any {
	result, ctx := r.callResult(call, index, info, info.Ctx, depth)
	if result == nil {
		return map[string]any{}
	}
	return r.sampleJSONValue(result, "", ctx, nil, map[string]struct{}{})
}

// callResult resolves the type of a call's result, plus the context that type
// should be read in.
func (r *resolver) callResult(call *ast.CallExpr, index int, info *responseInfo, ctx *typeCtx, depth int) (ast.Expr, *typeCtx) {
	if depth > maxValueDepth || ctx == nil {
		return nil, nil
	}

	var fn *funcInfo
	switch callee := call.Fun.(type) {
	case *ast.Ident:
		fn = r.funcAt(ctx.ImportPath, "", callee.Name)
	case *ast.SelectorExpr:
		// A package-level function of an imported package, which is the one
		// reading where the qualifier is not a value at all.
		if receiver, ok := callee.X.(*ast.Ident); ok {
			if importPath, found := ctx.Imports[receiver.Name]; found {
				fn = r.funcAt(importPath, "", callee.Sel.Name)
			}
		}
		if fn == nil {
			// Otherwise it is a method, and what it hangs off has to be resolved
			// first.
			recvType, recvCtx := r.receiverType(callee.X, info, ctx, depth)
			if recvType == nil {
				return nil, nil
			}
			name, nameCtx := namedType(recvType, recvCtx)
			if name == "" {
				return nil, nil
			}
			fn = r.funcAt(nameCtx.ImportPath, name, callee.Sel.Name)
		}
	}
	if fn == nil {
		return nil, nil
	}

	result := firstValueResult(fn.Results, index)
	if result == nil {
		return nil, nil
	}
	return result, fn.Ctx
}

// receiverType resolves what a method is being called on.
//
// Only two shapes used to resolve: an inline chain
// (`services.NewUserService(c).Find(id)`) and a package-level function. Neither
// is how a handler is usually written — the service is assigned to a variable
// first, or held as a field on the handler — and a receiver that did not
// resolve made the whole reply render as `{}`, which reads as "this endpoint
// returns an empty object" rather than "the tool lost the thread".
func (r *resolver) receiverType(expr ast.Expr, info *responseInfo, ctx *typeCtx, depth int) (ast.Expr, *typeCtx) {
	switch e := expr.(type) {
	case *ast.CallExpr:
		// services.NewUserService(c).Find(id)
		return r.callResult(e, 0, info, ctx, depth+1)

	case *ast.Ident:
		// svc := services.NewUserService(c); svc.Find(id)
		if info == nil {
			return nil, nil
		}
		local, found := info.Locals[e.Name]
		if !found {
			return nil, nil
		}
		switch value := local.Expr.(type) {
		case *ast.CallExpr:
			return r.callResult(value, local.Index, info, ctx, depth+1)
		case *ast.CompositeLit:
			return value.Type, ctx
		case *ast.UnaryExpr:
			if lit, ok := value.X.(*ast.CompositeLit); ok && value.Op == token.AND {
				return lit.Type, ctx
			}
		}

	case *ast.SelectorExpr:
		// h.users.Find(id), where users is a field of the handler
		return r.receiverField(e, info, ctx)
	}
	return nil, nil
}

// receiverField resolves a call written on a field of the handler the method
// hangs off. The field's declared type is the answer, and it is as often an
// interface as a struct — which is fine, because the resolver indexes interface
// methods too.
func (r *resolver) receiverField(sel *ast.SelectorExpr, info *responseInfo, ctx *typeCtx) (ast.Expr, *typeCtx) {
	if info == nil || info.RecvName == "" || info.RecvType == "" || info.Ctx == nil {
		return nil, nil
	}
	holder, ok := sel.X.(*ast.Ident)
	if !ok || holder.Name != info.RecvName {
		return nil, nil
	}

	decl := r.structAt(info.Ctx.ImportPath, info.RecvType)
	if decl == nil {
		return nil, nil
	}
	for _, field := range decl.Fields {
		if field.Name == sel.Sel.Name {
			fieldCtx := decl.Ctx
			if fieldCtx == nil {
				fieldCtx = ctx
			}
			return field.Expr, fieldCtx
		}
	}
	return nil, nil
}

// namedType reduces a type expression to the name it declares and the package
// that declares it.
func namedType(expr ast.Expr, ctx *typeCtx) (string, *typeCtx) {
	if ctx == nil {
		return "", nil
	}
	switch e := expr.(type) {
	case *ast.StarExpr:
		return namedType(e.X, ctx)
	case *ast.Ident:
		return e.Name, ctx
	case *ast.SelectorExpr:
		pkg, ok := e.X.(*ast.Ident)
		if !ok {
			return "", nil
		}
		importPath, found := ctx.Imports[pkg.Name]
		if !found {
			return "", nil
		}
		return e.Sel.Name, &typeCtx{ImportPath: importPath, Imports: ctx.Imports}
	}
	return "", nil
}

// firstValueResult picks the result a caller actually reads. Handlers bind the
// error separately, so an error result is never the value being rendered.
func firstValueResult(results []ast.Expr, index int) ast.Expr {
	if index < len(results) && !isErrorType(results[index]) {
		return results[index]
	}
	for _, result := range results {
		if !isErrorType(result) {
			return result
		}
	}
	return nil
}

func isErrorType(expr ast.Expr) bool {
	name := exprTypeName(expr)
	return name == "error" || name == "IError"
}

func literalValue(lit *ast.BasicLit) any {
	switch lit.Kind {
	case token.STRING:
		if value, err := strconv.Unquote(lit.Value); err == nil {
			return value
		}
		return lit.Value
	case token.INT:
		if value, err := strconv.Atoi(lit.Value); err == nil {
			return value
		}
	case token.FLOAT:
		if value, err := strconv.ParseFloat(lit.Value, 64); err == nil {
			return value
		}
	}
	return lit.Value
}

// typeEnv binds a generic type parameter to the argument it was given, along
// with the context that argument was written in.
type typeEnv map[string]typeBinding

type typeBinding struct {
	Expr ast.Expr
	Ctx  *typeCtx
}

// bodyTags and formTags are where a body field's name comes from, most specific
// first. A field bound from a form may spell its name differently there than it
// does in JSON, and the name a form sends is the one a form body must carry.
var (
	bodyTags = []string{"json"}
	formTags = []string{"form", "json"}
)

// firstTagValue is the first of these tags the field states, so a field tagged
// both is read under the name the body being built actually uses.
func firstTagValue(tag string, keys []string) string {
	for _, key := range keys {
		if value := reflectTagValue(tag, key); value != "" {
			return value
		}
	}
	return ""
}

// collectBodyFields writes one sample value per body field, named by the first
// of tags the field carries. An embedded struct has no name of its own, so its
// fields are promoted into the same object — exactly as encoding/json marshals
// them.
func (r *resolver) collectBodyFields(info *structInfo, env typeEnv, out map[string]any, visited map[string]struct{}, tags []string) {
	rules := r.rulesFor(info)
	for _, field := range info.Fields {
		tag := firstTagValue(field.Tag, tags)
		if field.Embedded && tag == "" {
			if embedded, embeddedEnv := r.lookupStruct(field.Expr, info.Ctx, env); embedded != nil {
				withStruct(visited, embedded, func() {
					r.collectBodyFields(embedded, embeddedEnv, out, visited, tags)
				})
			}
			continue
		}
		if tag == "" || tag == "-" {
			continue
		}
		// A relation the endpoint did not load is not in the reply at all, so it
		// does not belong in a document describing what the endpoint sends. A
		// request body's schema still declares it — that one is read off the
		// type — while a reply's is read off this value and follows it, which is
		// the honest result either way: the model can carry the relation, this
		// endpoint does not return it.
		if absentWhenEmpty(field, tags) {
			continue
		}
		// What the field's own rules ask for beats anything inferred from its
		// name or its type.
		if rule := rules[field.Name]; rule != nil && isScalarType(field.Expr) {
			if value, ok := r.sampleFromRules(rule, tag, info.Ctx); ok {
				out[tag] = value
				continue
			}
		}
		out[tag] = r.sampleJSONValue(field.Expr, tag, info.Ctx, env, visited)
	}
}

// isScalarType reports whether a rule-built value can stand in for the field. A
// slice or a nested object is rendered from its own type instead.
func isScalarType(expr ast.Expr) bool {
	switch e := expr.(type) {
	case *ast.StarExpr:
		return isScalarType(e.X)
	case *ast.Ident:
		switch e.Name {
		case "string", "bool",
			"int", "int8", "int16", "int32", "int64",
			"uint", "uint8", "uint16", "uint32", "uint64",
			"float32", "float64":
			return true
		}
	}
	return false
}

// collectQueryFields mirrors collectBodyFields for the query string, where a
// value is always a scalar rendered as text.
func (r *resolver) collectQueryFields(info *structInfo, out map[string]postmanQuery, visited map[string]struct{}) {
	rules := r.rulesFor(info)
	for _, field := range info.Fields {
		tag := reflectTagValue(field.Tag, "query")
		if field.Embedded && tag == "" {
			if embedded, _ := r.lookupStruct(field.Expr, info.Ctx, nil); embedded != nil {
				withStruct(visited, embedded, func() {
					r.collectQueryFields(embedded, out, visited)
				})
			}
			continue
		}
		if tag == "" || tag == "-" {
			continue
		}

		query := postmanQuery{Key: tag, Value: sampleScalarString(field.Expr, tag)}
		if rule := rules[field.Name]; rule != nil {
			if value, ok := r.sampleFromRules(rule, tag, info.Ctx); ok {
				query.Value = fmt.Sprint(value)
			}
			query.Description = r.describeRules(rule, info.Ctx)
		}
		out[tag] = query
	}
}

// describeRules states a parameter's rules in the words a reader needs, so the
// constraint is visible in Postman instead of only in the Go source.
func (r *resolver) describeRules(rules *fieldRules, ctx *typeCtx) string {
	ctx = rules.resolveCtx(ctx)
	parts := []string{}
	for _, rule := range rules.Rules {
		switch rule.Name {
		case requiredRule:
			parts = append(parts, "required")
		case "NotBlank":
			parts = append(parts, "must not be blank if sent")
		case "Trim":
			parts = append(parts, "trimmed by the server")
		case "Lower":
			parts = append(parts, "lowercased by the server")
		case "Apply":
			parts = append(parts, "normalized by the server")
		case "Email":
			parts = append(parts, "email address")
		case "UUID":
			parts = append(parts, "UUID")
		case "URL":
			parts = append(parts, "URL")
		case "Numeric":
			parts = append(parts, "digits only")
		case "Date":
			parts = append(parts, "date (YYYY-MM-DD)")
		case "Time":
			parts = append(parts, "time (HH:mm:ss)")
		case "DateTime":
			parts = append(parts, "datetime (YYYY-MM-DD HH:mm:ss)")
		case "AnyDate":
			parts = append(parts, "date (any common format)")
		case "AnyTime":
			parts = append(parts, "time (any common format)")
		case "AnyDateTime":
			parts = append(parts, "datetime (any common format)")
		case "AnyTemporal":
			parts = append(parts, "date or time (any common format)")
		case "ISO8601":
			parts = append(parts, "ISO 8601 datetime")
		case "Lowercase":
			parts = append(parts, "lowercase")
		case "Uppercase":
			parts = append(parts, "uppercase")
		case "In":
			if options := r.ruleOptions(rules, rule, ctx); len(options) > 0 {
				parts = append(parts, "one of: "+strings.Join(options, ", "))
			}
		case "Length":
			parts = append(parts, fmt.Sprintf("%d-%d characters", r.intArg(rule, 0, ctx), r.intArg(rule, 1, ctx)))
		case "Min":
			parts = append(parts, fmt.Sprintf("min %d", r.intArg(rule, 0, ctx)))
		case "Max":
			parts = append(parts, fmt.Sprintf("max %d", r.intArg(rule, 0, ctx)))
		}
	}
	return strings.Join(parts, ", ")
}

// ruleOptions is the allowed values of an In rule as text, whichever kind of
// value the field holds. A numeric enum reads as its numbers rather than as
// nothing, which is what a string-only reader gave it.
func (r *resolver) ruleOptions(rules *fieldRules, rule fieldRule, ctx *typeCtx) []string {
	ctx = rules.resolveCtx(ctx)
	if rules.Kind == "number" {
		args, _ := r.ruleArgs(rule, ctx)
		values := make([]string, 0, len(args))
		for i := range args {
			values = append(values, strconv.Itoa(r.intArg(rule, i, ctx)))
		}
		return values
	}
	return r.stringArgs(rule, ctx)
}

// sampleJSONValue renders a type as sample JSON. A named struct is looked up and
// expanded rather than flattened to {}, which is what makes a nested object, a
// slice of them, or a generic wrapper useful to read.
func (r *resolver) sampleJSONValue(expr ast.Expr, name string, ctx *typeCtx, env typeEnv, visited map[string]struct{}) any {
	switch e := expr.(type) {
	case *ast.StarExpr:
		return r.sampleJSONValue(e.X, name, ctx, env, visited)
	case *ast.ArrayType:
		return []any{r.sampleJSONValue(e.Elt, name, ctx, env, visited)}
	case *ast.MapType:
		return map[string]any{}
	case *ast.Ident:
		switch e.Name {
		case "string":
			return sampleStringForName(name)
		case "bool":
			return true
		case "int", "int8", "int16", "int32", "int64":
			return sampleNumberForName(name)
		case "uint", "uint8", "uint16", "uint32", "uint64":
			return sampleNumberForName(name)
		case "float32", "float64":
			return 1.23
		case "any", "error":
			return map[string]any{}
		}
		if bound, found := env[e.Name]; found {
			// A type parameter: render what it was instantiated with.
			return r.sampleJSONValue(bound.Expr, name, bound.Ctx, nil, visited)
		}
		return r.sampleStructValue(e, ctx, env, visited)
	case *ast.SelectorExpr:
		if strings.Contains(strings.ToLower(e.Sel.Name), "time") {
			return "2026-01-01T00:00:00Z"
		}
		return r.sampleStructValue(e, ctx, env, visited)
	case *ast.IndexExpr, *ast.IndexListExpr:
		return r.sampleStructValue(expr, ctx, env, visited)
	default:
		return map[string]any{}
	}
}

func (r *resolver) sampleStructValue(expr ast.Expr, ctx *typeCtx, env typeEnv, visited map[string]struct{}) any {
	nested, nestedEnv := r.lookupStruct(expr, ctx, env)
	if nested == nil {
		return map[string]any{}
	}
	if _, seen := visited[structKey(nested)]; seen {
		// A type that contains itself would otherwise recurse forever.
		return map[string]any{}
	}
	// visited holds one entry per object between here and the top of the body —
	// withStruct adds on the way down and removes on the way back — so its size
	// is the current depth, and no signature has to carry a counter for it.
	if len(visited) > r.maxDepth {
		return map[string]any{}
	}

	// bodyTags whatever the outer body is: a nested object is rendered as JSON
	// even inside a form, where it becomes the JSON text of one part.
	out := map[string]any{}
	withStruct(visited, nested, func() {
		r.collectBodyFields(nested, nestedEnv, out, visited, bodyTags)
	})
	return out
}

// lookupStruct resolves a type expression to its declaration, reading a
// qualified name through the imports of the file it was written in. A generic
// instantiation also yields the bindings its fields should be rendered with.
func (r *resolver) lookupStruct(expr ast.Expr, ctx *typeCtx, env typeEnv) (*structInfo, typeEnv) {
	if ctx == nil {
		return nil, nil
	}
	switch e := expr.(type) {
	case *ast.StarExpr:
		return r.lookupStruct(e.X, ctx, env)
	case *ast.IndexExpr:
		return r.instantiate(e.X, []ast.Expr{e.Index}, ctx, env)
	case *ast.IndexListExpr:
		return r.instantiate(e.X, e.Indices, ctx, env)
	case *ast.Ident:
		if bound, found := env[e.Name]; found {
			return r.lookupStruct(bound.Expr, bound.Ctx, nil)
		}
		return r.structAt(ctx.ImportPath, e.Name), nil
	case *ast.SelectorExpr:
		pkg, ok := e.X.(*ast.Ident)
		if !ok {
			return nil, nil
		}
		importPath, found := ctx.Imports[pkg.Name]
		if !found {
			return nil, nil
		}
		return r.structAt(importPath, e.Sel.Name), nil
	}
	return nil, nil
}

// instantiate pairs a generic struct's type parameters with the arguments it was
// given, so Page[models.User] renders its items as users.
func (r *resolver) instantiate(base ast.Expr, args []ast.Expr, ctx *typeCtx, env typeEnv) (*structInfo, typeEnv) {
	info, _ := r.lookupStruct(base, ctx, env)
	if info == nil {
		return nil, nil
	}
	bound := typeEnv{}
	for i, param := range info.TypeParams {
		if i >= len(args) {
			break
		}
		bound[param] = typeBinding{Expr: args[i], Ctx: ctx}
	}
	return info, bound
}

func structKey(info *structInfo) string {
	return registryKey(info.Ctx.ImportPath, info.TypeName)
}

func visitedSet(info *structInfo) map[string]struct{} {
	return map[string]struct{}{structKey(info): {}}
}

// withStruct marks a struct as being expanded for the duration of fn, so a cycle
// is cut short without hiding a type that legitimately appears twice.
func withStruct(visited map[string]struct{}, info *structInfo, fn func()) {
	key := structKey(info)
	visited[key] = struct{}{}
	defer delete(visited, key)
	fn()
}

func sampleScalarString(expr ast.Expr, name string) string {
	switch e := expr.(type) {
	case *ast.StarExpr:
		return sampleScalarString(e.X, name)
	case *ast.Ident:
		switch e.Name {
		case "string":
			return sampleStringForName(name)
		case "bool":
			return "true"
		case "int", "int8", "int16", "int32", "int64", "uint", "uint8", "uint16", "uint32", "uint64":
			return strconv.Itoa(sampleNumberForName(name))
		case "float32", "float64":
			return "1.23"
		default:
			return "value"
		}
	case *ast.ArrayType:
		return sampleScalarString(e.Elt, name)
	case *ast.SelectorExpr:
		typeName := strings.ToLower(e.Sel.Name)
		switch {
		case strings.Contains(typeName, "status"):
			return "ACTIVE"
		case strings.Contains(typeName, "time"), strings.Contains(typeName, "date"):
			return sampleDate
		default:
			return "value"
		}
	default:
		return "value"
	}
}

func loginCaptureEvent(path string, cfg *Config) *postmanEvent {
	variableName, found := cfg.TokenCaptures[path]
	if !found {
		return nil
	}

	return &postmanEvent{
		Listen: "test",
		Script: postmanScript{
			Type: "text/javascript",
			Exec: []string{
				"const data = pm.response.json();",
				"if (data && data.token) {",
				fmt.Sprintf("  pm.collectionVariables.set(%q, data.token);", variableName),
				"}",
			},
		},
	}
}

func methodAllowsBody(method string) bool {
	switch method {
	case "POST", "PUT", "PATCH":
		return true
	default:
		return false
	}
}

func appendQuery(raw string, query []postmanQuery) string {
	if len(query) == 0 {
		return raw
	}
	parts := make([]string, 0, len(query))
	for _, q := range query {
		parts = append(parts, q.Key+"="+encodeQueryValue(q.Value))
	}
	return raw + "?" + strings.Join(parts, "&")
}

// encodeQueryValue escapes a sample value only when it would otherwise break the
// raw URL. Plain values are left alone so the collection stays readable, and a
// {{variable}} is never escaped — Postman has to see the braces.
func encodeQueryValue(value string) string {
	if strings.Contains(value, "{{") || !strings.ContainsAny(value, " \"#%&'+/;<=>?@[\\]^`|") {
		return value
	}
	return url.QueryEscape(value)
}

// collectRequestSignals reads what a handler takes straight off its context.
//
// Only calls rooted at the handler's own context parameter count. A service
// method with a Param of its own, or a map with a Get, is not a request
// parameter — and `c.Response().Header().Get(...)` is a header the server sends,
// not one it reads, which is why only the request's Header field is followed.
func collectRequestSignals(fn *ast.FuncDecl) requestSignals {
	signals := requestSignals{}
	ctxName := contextParamName(fn)
	if ctxName == "" || fn.Body == nil {
		return signals
	}

	ast.Inspect(fn.Body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || !rootsAtContext(sel.X, ctxName) {
			return true
		}
		signals.read(sel, call.Args)
		return true
	})

	signals.sort()
	return signals
}

// contextParamName is the name a handler binds its context to. The framework's
// handler signature takes the context first and takes nothing else, so that is
// the parameter every request value is read off.
func contextParamName(fn *ast.FuncDecl) string {
	if fn.Type == nil || fn.Type.Params == nil || len(fn.Type.Params.List) == 0 {
		return ""
	}
	names := fn.Type.Params.List[0].Names
	if len(names) == 0 || names[0].Name == "_" {
		return ""
	}
	return names[0].Name
}

// rootsAtContext reports whether an expression chain starts at the handler's
// context, so `c.Request().URL.Query()` counts and `other.Query()` does not.
func rootsAtContext(expr ast.Expr, ctxName string) bool {
	for {
		switch e := expr.(type) {
		case *ast.Ident:
			return e.Name == ctxName
		case *ast.SelectorExpr:
			expr = e.X
		case *ast.CallExpr:
			expr = e.Fun
		case *ast.ParenExpr:
			expr = e.X
		case *ast.IndexExpr:
			expr = e.X
		default:
			return false
		}
	}
}

// read records one call on the context. The ...Or forms are the same read with
// a fallback stated, and are taken as the same parameter.
func (s *requestSignals) read(sel *ast.SelectorExpr, args []ast.Expr) {
	name, fallback, named := nameAndDefault(args)

	switch sel.Sel.Name {
	case "Param", "ParamOr":
		if named {
			s.Params = appendUnique(s.Params, name)
		}
	case "QueryParam", "QueryParamOr":
		if named {
			s.Query = appendSample(s.Query, name, fallback)
		}
	case "FormValue", "FormValueOr":
		if named {
			s.Form = appendSample(s.Form, name, fallback)
		}
	case "FormValues":
		s.HasForm = true
	case "FormFile":
		// A file makes the body multipart whether or not its name could be read.
		s.Multipart = true
		if named {
			s.Files = appendUnique(s.Files, name)
		}
	case "MultipartForm":
		s.Multipart = true
	case "Cookie":
		if named {
			s.Cookies = appendUnique(s.Cookies, name)
		}
	case "Get":
		// Get is echo's per-request store as well, so what it is called on
		// decides whether this is a request value at all.
		if !named {
			return
		}
		switch x := sel.X.(type) {
		case *ast.SelectorExpr:
			// c.Request().Header.Get("X-Api-Key")
			if x.Sel.Name == "Header" {
				s.Headers = appendUnique(s.Headers, name)
			}
		case *ast.CallExpr:
			// c.QueryParams().Get("q"), c.Request().URL.Query().Get("q")
			switch selectorName(x.Fun) {
			case "Query", "QueryParams":
				s.Query = appendSample(s.Query, name, "")
			}
		}
	}
}

// sort settles the order everything is written in. Source order is already
// deterministic; by name is the order a reader can find something in.
func (s *requestSignals) sort() {
	sortSamples(s.Query)
	sortSamples(s.Form)
	sort.Strings(s.Params)
	sort.Strings(s.Files)
	sort.Strings(s.Headers)
	sort.Strings(s.Cookies)
}

func sortSamples(samples []namedSample) {
	sort.Slice(samples, func(i, j int) bool { return samples[i].Name < samples[j].Name })
}

// nameAndDefault reads the name a call asks for, and the fallback an ...Or form
// states after it. A name built at runtime cannot be read here, and a parameter
// nobody can name is not one this tool can document.
func nameAndDefault(args []ast.Expr) (name, fallback string, ok bool) {
	if len(args) == 0 {
		return "", "", false
	}
	name, ok = stringLiteral(args[0])
	if !ok {
		return "", "", false
	}
	if len(args) > 1 {
		fallback, _ = stringLiteral(args[1])
	}
	return name, fallback, true
}

// appendSample records a parameter once. The first mention wins, except that a
// later one stating a fallback fills in for a first one that stated none.
func appendSample(samples []namedSample, name, fallback string) []namedSample {
	for i := range samples {
		if samples[i].Name != name {
			continue
		}
		if samples[i].Default == "" {
			samples[i].Default = fallback
		}
		return samples
	}
	return append(samples, namedSample{Name: name, Default: fallback})
}

func appendUnique(values []string, value string) []string {
	if slices.Contains(values, value) {
		return values
	}
	return append(values, value)
}

// selectorName is the method a call names, or "" when the call is not a method
// call at all.
func selectorName(expr ast.Expr) string {
	if sel, ok := expr.(*ast.SelectorExpr); ok {
		return sel.Sel.Name
	}
	return ""
}

// sampleValue is what to send for a parameter read by name. The handler's own
// fallback is used when it stated one, because that is a value the server is
// known to accept.
func (n namedSample) sampleValue() string {
	if n.Default != "" {
		return n.Default
	}
	return sampleTextForName(n.Name)
}

// description says what a caller can leave out. A stated fallback is the whole
// of what the code says about the parameter, so it is the whole of what there is
// to write.
func (n namedSample) description() string {
	if n.Default == "" {
		return ""
	}
	return "optional, defaults to " + n.Default
}

// resolveTypeRefExpr names the type an expression builds, and which package
// declares it.
//
// selfImportPath is the package the expression was written in, and is what an
// unqualified name resolves to. Without it `input := &LoginRequest{}` yields a
// type with no package, which nothing can then be looked up by — and a request
// struct that sits beside its handler, as every module's does, is exactly that
// case.
func resolveTypeRefExpr(imports map[string]string, selfImportPath string, expr ast.Expr, localVars map[string]string) *typeRef {
	switch e := expr.(type) {
	case *ast.UnaryExpr:
		if e.Op == token.AND {
			return resolveTypeRefExpr(imports, selfImportPath, e.X, localVars)
		}
	case *ast.CompositeLit:
		return resolveTypeRefType(imports, selfImportPath, e.Type)
	case *ast.Ident:
		if localVars != nil {
			if typeName, found := localVars[e.Name]; found {
				return resolveTypeRefTypeName(imports, selfImportPath, typeName)
			}
		}
	}
	return nil
}

func resolveTypeRefType(imports map[string]string, selfImportPath string, expr ast.Expr) *typeRef {
	switch e := expr.(type) {
	case *ast.SelectorExpr:
		if pkg, ok := e.X.(*ast.Ident); ok {
			if importPath, found := imports[pkg.Name]; found {
				return &typeRef{
					ImportPath: importPath,
					TypeName:   e.Sel.Name,
				}
			}
		}
	case *ast.Ident:
		return &typeRef{
			ImportPath: selfImportPath,
			TypeName:   e.Name,
		}
	}
	return nil
}

func resolveTypeRefTypeName(imports map[string]string, selfImportPath, typeName string) *typeRef {
	if alias, bare, qualified := strings.Cut(typeName, "."); qualified {
		if importPath, found := imports[alias]; found {
			return &typeRef{
				ImportPath: importPath,
				TypeName:   bare,
			}
		}
	}

	return &typeRef{
		ImportPath: selfImportPath,
		TypeName:   typeName,
	}
}

func exprTypeName(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		return e.Sel.Name
	}
	return ""
}

func exprTypeString(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		if pkg, ok := e.X.(*ast.Ident); ok {
			return pkg.Name + "." + e.Sel.Name
		}
		return e.Sel.Name
	default:
		return exprTypeName(expr)
	}
}

func receiverTypeName(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.StarExpr:
		return receiverTypeName(e.X)
	case *ast.Ident:
		return e.Name
	case *ast.IndexExpr:
		return receiverTypeName(e.X)
	case *ast.IndexListExpr:
		return receiverTypeName(e.X)
	default:
		return ""
	}
}

func callContainsName(call *ast.CallExpr, name string) bool {
	found := false
	ast.Inspect(call, func(node ast.Node) bool {
		if found {
			return false
		}
		switch n := node.(type) {
		case *ast.Ident:
			if n.Name == name {
				found = true
				return false
			}
		case *ast.SelectorExpr:
			if n.Sel.Name == name {
				found = true
				return false
			}
		}
		return true
	})
	return found
}

func exprContainsName(expr ast.Expr, name string) bool {
	found := false
	ast.Inspect(expr, func(node ast.Node) bool {
		if found {
			return false
		}
		switch n := node.(type) {
		case *ast.Ident:
			if n.Name == name {
				found = true
				return false
			}
		case *ast.SelectorExpr:
			if n.Sel.Name == name {
				found = true
				return false
			}
		}
		return true
	})
	return found
}

func stringLiteral(expr ast.Expr) (string, bool) {
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	value, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}
	return value, true
}

func normalizeRoutePath(path string) string {
	if path == "" {
		return "/"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	if len(path) > 1 {
		path = strings.TrimRight(path, "/")
	}
	return path
}

func folderNameFromRoute(path string) string {
	trimmed := strings.Trim(path, "/")
	if trimmed == "" {
		return "root"
	}
	return strings.Split(trimmed, "/")[0]
}

func newFolderNode(name, sortKey string) *folderNode {
	return &folderNode{
		Name:    name,
		SortKey: sortKey,
		Folders: map[string]*folderNode{},
	}
}

func insertRouteItem(root *folderNode, route routeInfo, item postmanItem) {
	segments := folderSegmentsFromRoute(route)
	node := root
	currentPath := ""
	for _, segment := range segments {
		if currentPath == "" {
			currentPath = segment
		} else {
			currentPath += "/" + segment
		}
		child, found := node.Folders[segment]
		if !found {
			child = newFolderNode(segment, strings.ToLower(currentPath))
			node.Folders[segment] = child
		}
		node = child
	}
	node.Requests = append(node.Requests, item)
}

func buildFolderItems(node *folderNode) []postmanItem {
	folderNames := make([]string, 0, len(node.Folders))
	for name := range node.Folders {
		folderNames = append(folderNames, name)
	}
	sort.Slice(folderNames, func(i, j int) bool {
		left := node.Folders[folderNames[i]]
		right := node.Folders[folderNames[j]]
		if left.SortKey != right.SortKey {
			return left.SortKey < right.SortKey
		}
		return left.Name < right.Name
	})

	sort.Slice(node.Requests, func(i, j int) bool {
		if node.Requests[i].SortKey != node.Requests[j].SortKey {
			return node.Requests[i].SortKey < node.Requests[j].SortKey
		}
		return node.Requests[i].Name < node.Requests[j].Name
	})

	items := make([]postmanItem, 0, len(folderNames)+len(node.Requests))
	for _, folderName := range folderNames {
		child := node.Folders[folderName]
		items = append(items, postmanItem{
			Name: child.Name,
			Item: buildFolderItems(child),
		})
	}
	items = append(items, node.Requests...)
	return items
}

// folderSegmentsFromRoute decides where a route sits in the grouped layout. The
// module owns the top folder, so every route registered together is filed
// together — /users, /users/bulk and /users/:id all land under "user" rather
// than being scattered by the shape of their URL.
func folderSegmentsFromRoute(route routeInfo) []string {
	trimmed := strings.Trim(route.Path, "/")
	static := make([]string, 0, strings.Count(trimmed, "/")+1)
	for _, part := range strings.Split(trimmed, "/") {
		if part == "" || strings.HasPrefix(part, ":") || isVersionSegment(part) {
			continue
		}
		static = append(static, part)
	}

	segments := []string{}
	if route.Module != "" {
		segments = append(segments, route.Module)
		if len(static) > 0 {
			// The leading segment names the same resource as the module
			// ("/users" in module "user") and would only repeat it.
			static = static[1:]
		}
	}

	if len(static) == 1 {
		// One segment below the resource names the request — "login", "bulk" —
		// not a folder worth opening for a single entry. Deeper paths keep their
		// segments, since by then they describe a real sub-resource.
		static = nil
	}

	segments = append(segments, static...)
	if len(segments) == 0 {
		return []string{"root"}
	}
	return segments
}

func moduleNameFromDir(dir string) string {
	parts := strings.Split(filepath.ToSlash(dir), "/")
	for i := 0; i < len(parts)-1; i++ {
		if parts[i] == "modules" {
			return parts[i+1]
		}
	}
	return ""
}

func isVersionSegment(segment string) bool {
	if len(segment) < 2 || segment[0] != 'v' {
		return false
	}
	for _, r := range segment[1:] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func pathParamsFromRoute(path string) []string {
	params := []string{}
	for _, segment := range strings.Split(strings.Trim(path, "/"), "/") {
		if strings.HasPrefix(segment, ":") {
			params = append(params, strings.TrimPrefix(segment, ":"))
		}
	}
	return uniqueSorted(params)
}

// sampleValueForPathParam reads from the same table the body samples use, so the
// id in the URL looks like the id in the response next to it.
func sampleValueForPathParam(name string) string {
	return sampleStringForName(name)
}

func reflectTagValue(tag, key string) string {
	return strings.Split(reflect.StructTag(tag).Get(key), ",")[0]
}

// absentWhenEmpty reports whether a field is simply missing from a real reply
// rather than present and empty.
//
// It is how a relation is told apart from a value. A GORM model carries its
// relations as `json:"user_tokens,omitempty"`, and an endpoint that does not
// preload them sends nothing for them at all — so an example showing a populated
// array of them describes a reply the server never sends, and the reader who
// trusted it writes a client that reads a field that is not there.
//
// Only the types encoding/json actually drops count. omitempty on a struct does
// nothing, and on a scalar it is a value the handler may well have set — a
// pointer to a struct, a slice or a map is the shape a relation takes.
func absentWhenEmpty(field structField, tagKeys []string) bool {
	if !hasOmitEmpty(field.Tag, tagKeys) {
		return false
	}
	switch expr := field.Expr.(type) {
	case *ast.ArrayType, *ast.MapType:
		return true
	case *ast.StarExpr:
		return !isScalarType(expr.X)
	}
	return false
}

func hasOmitEmpty(tag string, keys []string) bool {
	for _, key := range keys {
		value := reflect.StructTag(tag).Get(key)
		if value == "" {
			continue
		}
		for _, opt := range strings.Split(value, ",")[1:] {
			if opt == "omitempty" {
				return true
			}
		}
		// The first tag that names the field is the one that decides; a later
		// one is not consulted for the name either.
		return false
	}
	return false
}

func importPathForDir(repo *repoInfo, dirRel string) string {
	if repo.moduleName == "" {
		return dirRel
	}
	if dirRel == "" {
		return repo.moduleName
	}
	return repo.moduleName + "/" + dirRel
}

func registryKey(importPath, typeName string) string {
	return importPath + "#" + typeName
}

// funcKey identifies a function by package, receiver type (empty for a plain
// function) and name.
func funcKey(importPath, recvType, name string) string {
	return importPath + "#" + recvType + "#" + name
}

func controllerRegistryKey(dirRel, receiverType, methodName string) string {
	return dirRel + "#" + receiverType + "#" + methodName
}

func uniqueSorted(values []string) []string {
	seen := map[string]struct{}{}
	for _, value := range values {
		if value == "" {
			continue
		}
		seen[value] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for value := range seen {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func writeCollection(root, output string, collection *postmanCollection) error {
	data, err := json.MarshalIndent(collection, "", "  ")
	if err != nil {
		return err
	}
	return writeGenerated(root, output, append(data, '\n'))
}

// writeGenerated puts a generated file where the project asked for it, creating
// the directory it named. Shared by both output formats so a project that moves
// its generated files only has to move one of them.
func writeGenerated(root, output string, data []byte) error {
	target := filepath.Join(root, filepath.FromSlash(output))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	return os.WriteFile(target, data, 0o644)
}

func countRoutes(items []postmanItem) int {
	count := 0
	for _, item := range items {
		if item.Request != nil {
			count++
		}
		count += countRoutes(item.Item)
	}
	return count
}

func exitErr(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
