package main

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// openAPIVersion is the spec version emitted. 3.1 rather than 3.0 because its
// Schema Object *is* JSON Schema 2020-12, so the shapes built here from Go
// types need no dialect translation, and because it is what Scalar renders
// natively.
const openAPIVersion = "3.1.0"

// bearerSchemeName is the security scheme a route requiring a token points at.
// One scheme for the whole document: the framework's auth middleware reads one
// header, so a second scheme would describe a server that does not exist.
const bearerSchemeName = "bearerAuth"

// sameOriginServer is the first server entry, and therefore the one a reader
// starts on. It is relative on purpose: the panel is served by the process it
// documents, so "send this request to whatever host served this page" is right
// on a laptop, in staging and in a port-forward alike — none of which a baked-in
// absolute URL survives.
const sameOriginServer = "/"

type oasDocument struct {
	OpenAPI string      `json:"openapi"`
	Info    oasInfo     `json:"info"`
	Servers []oasServer `json:"servers,omitempty"`
	Tags    []oasTag    `json:"tags,omitempty"`
	// TagGroups is the folder above the tags, which is as deep as a renderer
	// goes today: Scalar reads x-tagGroups but has no nesting below it, and the
	// tags.parent that OpenAPI 3.2 adds for real nesting is not implemented
	// anywhere yet. Two levels is therefore the whole of what can be said.
	TagGroups  []oasTagGroup           `json:"x-tagGroups,omitempty"`
	Paths      map[string]*oasPathItem `json:"paths"`
	Components oasComponents           `json:"components"`
}

type oasInfo struct {
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Version     string `json:"version"`
}

type oasServer struct {
	URL         string `json:"url"`
	Description string `json:"description,omitempty"`
}

type oasTag struct {
	Name string `json:"name"`
}

type oasTagGroup struct {
	Name string   `json:"name"`
	Tags []string `json:"tags"`
}

type oasComponents struct {
	SecuritySchemes map[string]oasSecurityScheme `json:"securitySchemes,omitempty"`
}

type oasSecurityScheme struct {
	Type         string `json:"type"`
	Scheme       string `json:"scheme,omitempty"`
	BearerFormat string `json:"bearerFormat,omitempty"`
}

// oasPathItem is the set of operations one path answers. Every field is a
// pointer so an absent method is absent from the JSON rather than present and
// empty, which readers render as an endpoint that takes nothing.
type oasPathItem struct {
	Get    *oasOperation `json:"get,omitempty"`
	Post   *oasOperation `json:"post,omitempty"`
	Put    *oasOperation `json:"put,omitempty"`
	Patch  *oasOperation `json:"patch,omitempty"`
	Delete *oasOperation `json:"delete,omitempty"`
	Head   *oasOperation `json:"head,omitempty"`
	Option *oasOperation `json:"options,omitempty"`
}

// set files the operation under its method, and reports whether the method is
// one a path item can carry.
func (p *oasPathItem) set(method string, op *oasOperation) bool {
	switch strings.ToUpper(method) {
	case http.MethodGet:
		p.Get = op
	case http.MethodPost:
		p.Post = op
	case http.MethodPut:
		p.Put = op
	case http.MethodPatch:
		p.Patch = op
	case http.MethodDelete:
		p.Delete = op
	case http.MethodHead:
		p.Head = op
	case http.MethodOptions:
		p.Option = op
	default:
		return false
	}
	return true
}

type oasOperation struct {
	Tags        []string                `json:"tags,omitempty"`
	Summary     string                  `json:"summary,omitempty"`
	Description string                  `json:"description,omitempty"`
	OperationID string                  `json:"operationId,omitempty"`
	Parameters  []oasParameter          `json:"parameters,omitempty"`
	RequestBody *oasRequestBody         `json:"requestBody,omitempty"`
	Responses   map[string]*oasResponse `json:"responses"`
	Security    []map[string][]string   `json:"security,omitempty"`
}

type oasParameter struct {
	Name        string     `json:"name"`
	In          string     `json:"in"`
	Description string     `json:"description,omitempty"`
	Required    bool       `json:"required,omitempty"`
	Schema      *oasSchema `json:"schema,omitempty"`
	Example     any        `json:"example,omitempty"`
}

type oasRequestBody struct {
	Required bool                    `json:"required,omitempty"`
	Content  map[string]oasMediaType `json:"content"`
}

type oasResponse struct {
	Description string                  `json:"description"`
	Content     map[string]oasMediaType `json:"content,omitempty"`
}

type oasMediaType struct {
	Schema  *oasSchema `json:"schema,omitempty"`
	Example any        `json:"example,omitempty"`
}

// oasSchema is the subset of JSON Schema this generator can honestly fill in
// from Go source: a shape, the constraints the validator states, and nothing
// invented.
type oasSchema struct {
	Type        string                `json:"type,omitempty"`
	Format      string                `json:"format,omitempty"`
	Description string                `json:"description,omitempty"`
	Enum        []any                 `json:"enum,omitempty"`
	Items       *oasSchema            `json:"items,omitempty"`
	Properties  map[string]*oasSchema `json:"properties,omitempty"`
	Required    []string              `json:"required,omitempty"`
	MinLength   *int                  `json:"minLength,omitempty"`
	MaxLength   *int                  `json:"maxLength,omitempty"`
	Minimum     *float64              `json:"minimum,omitempty"`
	Maximum     *float64              `json:"maximum,omitempty"`
	MinItems    *int                  `json:"minItems,omitempty"`
	MaxItems    *int                  `json:"maxItems,omitempty"`
	// ContentMediaType marks an upload. In 3.1 a binary part is a string with a
	// media type rather than the "format: binary" 3.0 spelled it.
	ContentMediaType string `json:"contentMediaType,omitempty"`
}

func (s *oasSchema) setProperty(name string, schema *oasSchema) {
	if s.Properties == nil {
		s.Properties = map[string]*oasSchema{}
	}
	s.Properties[name] = schema
}

// The media types a generated request body is sent under. Multipart is named
// here rather than reused from the Postman side because Postman deliberately
// states no Content-Type for a multipart body — it writes its own boundary —
// while a spec must name the type and lets the client write the boundary.
const contentTypeMultipart = "multipart/form-data"

// buildOpenAPI renders the same parsed routes the Postman collection is built
// from as an OpenAPI document.
//
// It is a second view of one parse, not a second tool: everything it says about
// a route — the fields, the samples, the rules, the example replies — comes from
// the helpers the collection uses, so the two can never disagree about what the
// service accepts.
func buildOpenAPI(routes []routeInfo, res *resolver, cfg *Config) (*oasDocument, error) {
	doc := &oasDocument{
		OpenAPI: openAPIVersion,
		Info: oasInfo{
			Title:       cfg.Name,
			Description: cfg.Description,
			Version:     cfg.apiVersion(),
		},
		Servers: openAPIServers(cfg),
		Paths:   map[string]*oasPathItem{},
		Components: oasComponents{
			SecuritySchemes: map[string]oasSecurityScheme{
				bearerSchemeName: {Type: "http", Scheme: "bearer", BearerFormat: "JWT"},
			},
		},
	}

	// operationIDs are unique per document, and two handlers of the same name in
	// two modules are ordinary. A collision is resolved rather than emitted,
	// because a duplicate id makes generated clients drop an endpoint silently.
	operationIDs := map[string]struct{}{}
	naming := newTagNaming(routes)

	for _, route := range routes {
		path := openAPIPath(route.Path)
		item, found := doc.Paths[path]
		if !found {
			item = &oasPathItem{}
			doc.Paths[path] = item
		}

		op := buildOperation(route, res, naming, operationIDs)
		if !item.set(route.Method, op) {
			return nil, fmt.Errorf("route %s %s: unsupported method", route.Method, route.Path)
		}
	}

	doc.Tags, doc.TagGroups = naming.declare()
	return doc, nil
}

// tagNaming decides what each route's sidebar entry is called and which folder
// it sits in.
//
// It is computed over every route at once rather than per route because both
// answers depend on the whole set: a tag name has to be unique across the
// document, and a tag that belongs to no group is not rendered at all — Scalar
// drops it silently, so an endpoint filed under a forgotten tag simply is not
// in the panel.
type tagNaming struct {
	// byRoute is every route's tag, decided once. Deciding it again per call
	// would mean the same collision rules applied twice, and a tag that came out
	// differently the second time would be one no group contains.
	byRoute map[string]string
	// groups maps a folder to the tags under it. Empty when no route is deep
	// enough to be worth one: a folder holding a single tag of the same name is
	// a level of nesting that says nothing.
	groups map[string]map[string]struct{}
}

func newTagNaming(routes []routeInfo) *tagNaming {
	// A leaf name is used where it is unambiguous, since "sessions" under the
	// "user" folder reads better than "user / sessions" does. Two modules with
	// a "search" below them make that name mean two things, and only the ones
	// that collide fall back to the full path.
	owners := map[string]map[string]struct{}{}
	nested := false
	for _, route := range routes {
		segments := folderSegmentsFromRoute(route)
		if len(segments) < 2 {
			continue
		}
		nested = true
		leaf := strings.Join(segments[1:], " / ")
		if owners[leaf] == nil {
			owners[leaf] = map[string]struct{}{}
		}
		owners[leaf][segments[0]] = struct{}{}
	}

	naming := &tagNaming{
		byRoute: map[string]string{},
		groups:  map[string]map[string]struct{}{},
	}
	for _, route := range routes {
		segments := folderSegmentsFromRoute(route)
		if !nested {
			naming.byRoute[routeTagKey(route)] = strings.Join(segments, " / ")
			continue
		}

		group, tag := splitTag(segments, owners)
		naming.byRoute[routeTagKey(route)] = tag
		if naming.groups[group] == nil {
			naming.groups[group] = map[string]struct{}{}
		}
		naming.groups[group][tag] = struct{}{}
	}
	return naming
}

// splitTag is the folder and the tag one route belongs to.
func splitTag(segments []string, owners map[string]map[string]struct{}) (group, tag string) {
	group = segments[0]
	if len(segments) < 2 {
		// Routes sitting directly under the resource. They still need a tag,
		// because a group holds tags and never operations, and naming it after
		// the resource is where a reader looks for them.
		return group, group
	}

	leaf := strings.Join(segments[1:], " / ")
	if len(owners[leaf]) > 1 {
		return group, strings.Join(segments, " / ")
	}
	return group, leaf
}

func routeTagKey(route routeInfo) string {
	return route.Method + " " + route.Path
}

// tagFor is the single tag an operation carries.
func (t *tagNaming) tagFor(route routeInfo) string {
	return t.byRoute[routeTagKey(route)]
}

// declare is the document's tag list and its folders, in a settled order.
func (t *tagNaming) declare() ([]oasTag, []oasTagGroup) {
	if len(t.groups) == 0 {
		names := map[string]struct{}{}
		for _, tag := range t.byRoute {
			names[tag] = struct{}{}
		}
		return sortedTags(names), nil
	}

	groupNames := make([]string, 0, len(t.groups))
	for name := range t.groups {
		groupNames = append(groupNames, name)
	}
	sort.Strings(groupNames)

	tags := []oasTag{}
	groups := make([]oasTagGroup, 0, len(groupNames))
	for _, name := range groupNames {
		members := make([]string, 0, len(t.groups[name]))
		for tag := range t.groups[name] {
			members = append(members, tag)
		}
		sort.Strings(members)

		for _, tag := range members {
			tags = append(tags, oasTag{Name: tag})
		}
		groups = append(groups, oasTagGroup{Name: name, Tags: members})
	}
	return tags, groups
}

func sortedTags(names map[string]struct{}) []oasTag {
	sorted := make([]string, 0, len(names))
	for name := range names {
		sorted = append(sorted, name)
	}
	sort.Strings(sorted)

	tags := make([]oasTag, 0, len(sorted))
	for _, name := range sorted {
		tags = append(tags, oasTag{Name: name})
	}
	return tags
}

// openAPIServers is where a request can be sent. The same-origin entry comes
// first so the "Test Request" button in a docs panel talks to the process
// serving the panel; the configured base URL follows for anyone who exported
// the file and needs an absolute address.
func openAPIServers(cfg *Config) []oasServer {
	servers := []oasServer{{URL: sameOriginServer, Description: "Same origin as this page"}}
	if cfg.BaseURL != "" {
		servers = append(servers, oasServer{URL: cfg.BaseURL, Description: "Configured base URL"})
	}
	return servers
}

// openAPIPath rewrites echo's ":id" into OpenAPI's "{id}".
func openAPIPath(path string) string {
	segments := strings.Split(path, "/")
	for i, segment := range segments {
		if strings.HasPrefix(segment, ":") {
			segments[i] = "{" + strings.TrimPrefix(segment, ":") + "}"
		}
	}
	joined := strings.Join(segments, "/")
	if joined == "" {
		return "/"
	}
	return joined
}

func buildOperation(route routeInfo, res *resolver, naming *tagNaming, ids map[string]struct{}) *oasOperation {
	op := &oasOperation{
		Tags:        []string{naming.tagFor(route)},
		Summary:     fmt.Sprintf("%s %s", route.Method, openAPIPath(route.Path)),
		Description: handlerDescription(route),
		OperationID: uniqueOperationID(route, ids),
		Responses:   map[string]*oasResponse{},
	}
	if route.NeedsAuth {
		op.Security = []map[string][]string{{bearerSchemeName: {}}}
	}

	var request *structInfo
	if route.RequestType != nil {
		request = res.structAt(route.RequestType.ImportPath, route.RequestType.TypeName)
	}

	op.Parameters = buildOASParameters(route, request, res)
	if methodAllowsBody(route.Method) {
		op.RequestBody = buildOASRequestBody(request, res, route.Signals)
	}
	buildOASResponses(op, route, res)
	return op
}

// handlerDescription names the code that answers the call. It is the one thing
// a reader of the panel cannot find out for themselves and the first thing they
// ask for — "who handles this?" — and it costs nothing to state.
func handlerDescription(route routeInfo) string {
	if route.HandlerType == "" || route.HandlerMethod == "" {
		return ""
	}
	return fmt.Sprintf("Handled by `%s.%s`.", route.HandlerType, route.HandlerMethod)
}

func uniqueOperationID(route routeInfo, ids map[string]struct{}) string {
	base := operationIDBase(route)
	id := base
	for i := 2; ; i++ {
		if _, taken := ids[id]; !taken {
			ids[id] = struct{}{}
			return id
		}
		id = fmt.Sprintf("%s%d", base, i)
	}
}

// operationIDBase names the operation after the handler that serves it, falling
// back to the route itself for the framework's own endpoints, which have no
// handler type in the project's source.
func operationIDBase(route routeInfo) string {
	if route.HandlerType != "" && route.HandlerMethod != "" {
		return route.HandlerType + route.HandlerMethod
	}

	parts := []string{strings.ToLower(route.Method)}
	for _, segment := range strings.Split(strings.Trim(route.Path, "/"), "/") {
		if segment == "" {
			continue
		}
		parts = append(parts, capitalize(strings.Trim(segment, ":{}")))
	}
	return sanitizeIdentifier(strings.Join(parts, ""))
}

func capitalize(value string) string {
	if value == "" {
		return ""
	}
	runes := []rune(value)
	runes[0] = unicode.ToUpper(runes[0])
	return string(runes)
}

func sanitizeIdentifier(value string) string {
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	if b.Len() == 0 {
		return "operation"
	}
	return b.String()
}

// --- parameters ---

// oasField is one input a request declares outside the body: what it is called
// on the wire, what shape it has, and what the validator says about it.
type oasField struct {
	Description string
	Required    bool
	Schema      *oasSchema
	Example     any
}

// buildOASParameters is every path, query, header and cookie input the endpoint
// takes, from the same three places the Postman side reads them: the binding
// tags on the request struct, the page options read off the query, and the
// values the handler takes off its context by name.
func buildOASParameters(route routeInfo, request *structInfo, res *resolver) []oasParameter {
	params := []oasParameter{}

	// Path first, and always required: a path parameter that is optional
	// describes a route that does not exist.
	pathFields := map[string]oasField{}
	if request != nil {
		res.collectTaggedFields(request, "param", pathFields, visitedSet(request))
	}
	for _, name := range route.PathParams {
		field, found := pathFields[name]
		if !found {
			field = oasField{Schema: &oasSchema{Type: "string"}, Example: sampleValueForPathParam(name)}
		}
		params = append(params, oasParameter{
			Name:        name,
			In:          "path",
			Description: field.Description,
			Required:    true,
			Schema:      field.Schema,
			Example:     field.Example,
		})
	}

	queryFields := map[string]oasField{}
	if request != nil {
		res.collectTaggedFields(request, "query", queryFields, visitedSet(request))
	}
	if route.UsesPagination {
		queryFields["page"] = oasField{
			Description: "page number, 1-based",
			Schema:      &oasSchema{Type: "integer"},
			Example:     1,
		}
		queryFields["limit"] = oasField{
			Description: "rows per page",
			Schema:      &oasSchema{Type: "integer"},
			Example:     30,
		}
	}
	// A tag and a c.QueryParam call can name the same parameter. The tag carries
	// a type and the field's rules, so it says more; the call only fills a gap.
	for _, sample := range route.Signals.Query {
		if _, found := queryFields[sample.Name]; found {
			continue
		}
		queryFields[sample.Name] = oasField{
			Description: sample.description(),
			Schema:      &oasSchema{Type: "string"},
			Example:     sample.sampleValue(),
		}
	}
	params = append(params, sortedParameters(queryFields, "query")...)

	headerFields := map[string]oasField{}
	if request != nil {
		res.collectTaggedFields(request, "header", headerFields, visitedSet(request))
	}
	for _, name := range route.Signals.Headers {
		// Authorization is described by the security scheme. Stating it again as
		// a plain header gives a docs panel two places to type the same token,
		// and the request carries whichever was filled in last.
		if strings.EqualFold(name, "Authorization") {
			continue
		}
		if _, found := headerFields[name]; found {
			continue
		}
		headerFields[name] = oasField{
			Schema:  &oasSchema{Type: "string"},
			Example: sampleTextForName(strings.ReplaceAll(name, "-", "_")),
		}
	}
	params = append(params, sortedParameters(headerFields, "header")...)

	cookieFields := map[string]oasField{}
	for _, name := range route.Signals.Cookies {
		cookieFields[name] = oasField{
			Schema:  &oasSchema{Type: "string"},
			Example: sampleTextForName(name),
		}
	}
	params = append(params, sortedParameters(cookieFields, "cookie")...)

	return params
}

func sortedParameters(fields map[string]oasField, in string) []oasParameter {
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	sort.Strings(names)

	params := make([]oasParameter, 0, len(names))
	for _, name := range names {
		field := fields[name]
		params = append(params, oasParameter{
			Name:        name,
			In:          in,
			Description: field.Description,
			Required:    field.Required,
			Schema:      field.Schema,
			Example:     field.Example,
		})
	}
	return params
}

// collectTaggedFields reads the fields bound from one non-body source — a
// `param:`, `query:` or `header:` tag — the way collectQueryFields reads the
// query, promoting an embedded struct's fields as the binder does.
func (r *resolver) collectTaggedFields(info *structInfo, tagKey string, out map[string]oasField, visited map[string]struct{}) {
	rules := r.rulesFor(info)
	for _, field := range info.Fields {
		tag := reflectTagValue(field.Tag, tagKey)
		if field.Embedded && tag == "" {
			if embedded, _ := r.lookupStruct(field.Expr, info.Ctx, nil); embedded != nil {
				withStruct(visited, embedded, func() {
					r.collectTaggedFields(embedded, tagKey, out, visited)
				})
			}
			continue
		}
		if tag == "" || tag == "-" {
			continue
		}

		schema := scalarSchema(field.Expr)
		entry := oasField{Schema: schema, Example: sampleForSchema(schema, tag)}
		if rule := rules[field.Name]; rule != nil {
			entry.Description = r.describeRules(rule, info.Ctx)
			entry.Required = rule.has(requiredRule)
			if value, ok := r.sampleFromRules(rule, tag, info.Ctx); ok {
				entry.Example = value
			}
			r.applyRuleConstraints(schema, rule, info.Ctx)
		}
		out[tag] = entry
	}
}

// --- request body ---

// buildOASRequestBody states the payload in the shape the handler reads it,
// following the same rule the Postman body follows: a c.FormFile makes it
// multipart, a c.FormValue makes it a form, and a bound request struct on its
// own stays JSON.
func buildOASRequestBody(request *structInfo, res *resolver, signals requestSignals) *oasRequestBody {
	switch {
	case signals.Multipart:
		schema := res.formSchema(request, signals)
		for _, name := range signals.Files {
			// A file beats a text entry of the same name: the handler asking for
			// a file is the one that says what the part actually carries.
			schema.setProperty(name, &oasSchema{Type: "string", ContentMediaType: "application/octet-stream"})
		}
		if len(schema.Properties) == 0 {
			return nil
		}
		return &oasRequestBody{Required: true, Content: map[string]oasMediaType{
			contentTypeMultipart: {Schema: schema},
		}}

	case signals.HasForm || len(signals.Form) > 0:
		schema := res.formSchema(request, signals)
		if len(schema.Properties) == 0 {
			return nil
		}
		return &oasRequestBody{Required: true, Content: map[string]oasMediaType{
			contentTypeForm: {Schema: schema},
		}}

	default:
		if request == nil {
			return nil
		}
		schema := &oasSchema{Type: "object"}
		res.collectBodySchema(request, nil, schema, visitedSet(request), bodyTags)
		if len(schema.Properties) == 0 {
			return nil
		}
		example := map[string]any{}
		res.collectBodyFields(request, nil, example, visitedSet(request), bodyTags)
		return &oasRequestBody{Required: true, Content: map[string]oasMediaType{
			contentTypeJSON: {Schema: schema, Example: example},
		}}
	}
}

// formSchema is a form body's fields: what the bound request declares plus what
// the handler reads by name. Every part is a string — a form carries text, and
// saying otherwise would have a client send a JSON number into a urlencoded
// body — but the rules the validator states survive the flattening, since
// "required" and "at most 50 characters" are true of the text too.
func (r *resolver) formSchema(request *structInfo, signals requestSignals) *oasSchema {
	schema := &oasSchema{Type: "object"}
	if request != nil {
		r.collectBodySchema(request, nil, schema, visitedSet(request), formTags)
		for name, property := range schema.Properties {
			schema.setProperty(name, asTextPart(property))
		}
	}
	for _, sample := range signals.Form {
		if _, found := schema.Properties[sample.Name]; found {
			continue
		}
		schema.setProperty(sample.Name, &oasSchema{Type: "string", Description: sample.description()})
	}
	return schema
}

// asTextPart is a field as a form carries it. A nested object goes into a part
// as the JSON it is, so its properties describe a shape the part does not have
// and are dropped rather than left to mislead.
func asTextPart(schema *oasSchema) *oasSchema {
	if schema.Type == "string" {
		return schema
	}
	return &oasSchema{Type: "string", Description: schema.Description}
}

// collectBodySchema mirrors collectBodyFields, recording what each field is
// rather than one value it could hold. The two are walked separately so the
// example stays a plain document a caller can edit and send, while the schema
// carries the rules a reader needs — trying to serve both from one structure
// gives a panel that renders neither well.
func (r *resolver) collectBodySchema(info *structInfo, env typeEnv, out *oasSchema, visited map[string]struct{}, tags []string) {
	rules := r.rulesFor(info)
	for _, field := range info.Fields {
		tag := firstTagValue(field.Tag, tags)
		if field.Embedded && tag == "" {
			if embedded, embeddedEnv := r.lookupStruct(field.Expr, info.Ctx, env); embedded != nil {
				withStruct(visited, embedded, func() {
					r.collectBodySchema(embedded, embeddedEnv, out, visited, tags)
				})
			}
			continue
		}
		if tag == "" || tag == "-" {
			continue
		}

		schema := r.schemaForField(field.Expr, tag, info.Ctx, env, visited)
		if rule := rules[field.Name]; rule != nil {
			schema.Description = r.describeRules(rule, info.Ctx)
			r.applyRuleConstraints(schema, rule, info.Ctx)
			if rule.has(requiredRule) {
				out.Required = append(out.Required, tag)
			}
		}
		out.setProperty(tag, schema)
	}
}

// schemaForField is the shape of one field. A scalar is read straight off its
// Go type, which is the authority; anything else — a named struct, an alias, a
// generic wrapper — is read off the sample value, because that is the one place
// those are already resolved.
func (r *resolver) schemaForField(expr ast.Expr, name string, ctx *typeCtx, env typeEnv, visited map[string]struct{}) *oasSchema {
	switch e := expr.(type) {
	case *ast.StarExpr:
		return r.schemaForField(e.X, name, ctx, env, visited)
	case *ast.ArrayType:
		return &oasSchema{Type: "array", Items: r.schemaForField(e.Elt, name, ctx, env, visited)}
	case *ast.MapType:
		return &oasSchema{Type: "object"}
	case *ast.Ident:
		if schema := identSchema(e.Name); schema != nil {
			return schema
		}
	}
	return schemaFromSample(r.sampleJSONValue(expr, name, ctx, env, visited))
}

// scalarSchema is the shape of a value that arrives as text — a path segment, a
// query parameter, a header. A non-scalar in one of those places has no wire
// spelling of its own, so it is described as the string it is sent as.
func scalarSchema(expr ast.Expr) *oasSchema {
	switch e := expr.(type) {
	case *ast.StarExpr:
		return scalarSchema(e.X)
	case *ast.ArrayType:
		return &oasSchema{Type: "array", Items: scalarSchema(e.Elt)}
	case *ast.Ident:
		if schema := identSchema(e.Name); schema != nil {
			return schema
		}
	}
	return &oasSchema{Type: "string"}
}

func identSchema(name string) *oasSchema {
	switch name {
	case "string":
		return &oasSchema{Type: "string"}
	case "bool":
		return &oasSchema{Type: "boolean"}
	case "int", "int8", "int16", "int32", "int64",
		"uint", "uint8", "uint16", "uint32", "uint64":
		return &oasSchema{Type: "integer"}
	case "float32", "float64":
		return &oasSchema{Type: "number"}
	}
	return nil
}

// schemaFromSample reads a shape off a rendered sample. Everything the sample
// machinery produces is one of these, and a value it could not render at all
// becomes a schema with no type — which says "anything", and is the truth.
func schemaFromSample(value any) *oasSchema {
	switch v := value.(type) {
	case bool:
		return &oasSchema{Type: "boolean"}
	case string:
		return &oasSchema{Type: "string"}
	case int:
		return &oasSchema{Type: "integer"}
	case int64:
		return &oasSchema{Type: "integer"}
	case float64:
		// Samples parsed back out of JSON have lost the distinction, so a whole
		// number is described as one: "integer" reads as a mistake on a price
		// far less often than "number" reads as one on an id.
		if v == math.Trunc(v) {
			return &oasSchema{Type: "integer"}
		}
		return &oasSchema{Type: "number"}
	case []any:
		schema := &oasSchema{Type: "array"}
		if len(v) > 0 {
			schema.Items = schemaFromSample(v[0])
		}
		return schema
	case map[string]any:
		schema := &oasSchema{Type: "object"}
		for name, item := range v {
			schema.setProperty(name, schemaFromSample(item))
		}
		return schema
	default:
		return &oasSchema{}
	}
}

func sampleForSchema(schema *oasSchema, name string) any {
	switch schema.Type {
	case "integer", "number":
		return sampleNumberForName(name)
	case "boolean":
		return true
	case "array":
		return []any{sampleStringForName(name)}
	default:
		return sampleStringForName(name)
	}
}

// applyRuleConstraints writes the validator's rules into the schema, so a client
// generated from the file rejects what the server would reject.
//
// The kind the rule chain opened with decides what a bound means: Max(50) is a
// length on a string, a value on a number and a count on an array, and reading
// it off the schema type instead would put a character limit on an integer.
func (r *resolver) applyRuleConstraints(schema *oasSchema, rules *fieldRules, ctx *typeCtx) {
	ctx = rules.resolveCtx(ctx)
	for _, rule := range rules.Rules {
		switch rule.Name {
		case "Email":
			schema.Format = "email"
		case "UUID":
			schema.Format = "uuid"
		case "URL":
			schema.Format = "uri"
		case "IP":
			schema.Format = "ipv4"
		case "Date", "AnyDate":
			schema.Format = "date"
		case "DateTime", "AnyDateTime", "ISO8601":
			schema.Format = "date-time"
		case "Time", "AnyTime":
			schema.Format = "time"
		case "In":
			schema.Enum = enumValues(rule, rules.Kind, r, ctx)
		case "Length":
			if rules.Kind == "string" {
				schema.MinLength = intPtr(r.intArg(rule, 0, ctx))
				schema.MaxLength = intPtr(r.intArg(rule, 1, ctx))
			}
		case "Min":
			switch rules.Kind {
			case "string":
				schema.MinLength = intPtr(r.intArg(rule, 0, ctx))
			case "number":
				schema.Minimum = floatPtr(r.intArg(rule, 0, ctx))
			case "array":
				schema.MinItems = intPtr(r.intArg(rule, 0, ctx))
			}
		case "Max":
			switch rules.Kind {
			case "string":
				schema.MaxLength = intPtr(r.intArg(rule, 0, ctx))
			case "number":
				schema.Maximum = floatPtr(r.intArg(rule, 0, ctx))
			case "array":
				schema.MaxItems = intPtr(r.intArg(rule, 0, ctx))
			}
		}
	}
}

// enumValues renders the options an In rule allows, in the type the field
// carries: an enum of "1" where the field is an integer is a schema no client
// can satisfy.
func enumValues(rule fieldRule, kind string, r *resolver, ctx *typeCtx) []any {
	if kind == "number" {
		args, _ := r.ruleArgs(rule, ctx)
		values := make([]any, 0, len(args))
		for i := range args {
			values = append(values, r.intArg(rule, i, ctx))
		}
		return values
	}

	options := r.stringArgs(rule, ctx)
	if len(options) == 0 {
		return nil
	}
	values := make([]any, 0, len(options))
	for _, option := range options {
		values = append(values, option)
	}
	return values
}

func intPtr(v int) *int { return &v }

func floatPtr(v int) *float64 {
	f := float64(v)
	return &f
}

// --- responses ---

// buildOASResponses documents the replies a call can produce, from the same
// sources the Postman examples are saved from: what the framework writes on its
// own, what the handler writes, and the failures answered before the handler is
// ever reached.
func buildOASResponses(op *oasOperation, route routeInfo, res *resolver) {
	for _, static := range route.Examples {
		op.Responses[strconv.Itoa(static.Code)] = jsonResponse(static.Code, decodeJSON(static.Body))
	}

	if route.Response != nil {
		var example any
		if body, ok := res.sampleResponseBody(route.Response); ok {
			example = decodeJSON(body)
		}
		op.Responses[strconv.Itoa(route.Response.StatusCode)] = jsonResponse(route.Response.StatusCode, example)
	}

	if body, ok := res.validationErrorBody(route); ok {
		op.Responses[strconv.Itoa(http.StatusBadRequest)] = jsonResponse(http.StatusBadRequest, decodeJSON(body))
	}

	if route.NeedsAuth {
		example := decodeJSON(errorBody(unauthorizedCode, unauthorizedMessage, nil))
		op.Responses[strconv.Itoa(http.StatusUnauthorized)] = jsonResponse(http.StatusUnauthorized, example)
	}

	// A path item with no responses at all is invalid, and a route whose handler
	// this tool could not read is common enough that refusing to emit it would
	// be worse than saying only that it answers.
	if len(op.Responses) == 0 {
		op.Responses[strconv.Itoa(http.StatusOK)] = &oasResponse{Description: http.StatusText(http.StatusOK)}
	}
}

func jsonResponse(code int, example any) *oasResponse {
	response := &oasResponse{Description: http.StatusText(code)}
	if example == nil {
		return response
	}
	response.Content = map[string]oasMediaType{
		contentTypeJSON: {Schema: schemaFromSample(example), Example: example},
	}
	return response
}

// decodeJSON reads a rendered example back into values, so the schema beside it
// is read off the same document the reader sees rather than off a second render
// that could drift from it.
func decodeJSON(body string) any {
	if strings.TrimSpace(body) == "" {
		return nil
	}
	var value any
	if err := json.Unmarshal([]byte(body), &value); err != nil {
		return nil
	}
	return value
}

// writeOpenAPI writes the document where the project asked for it.
func writeOpenAPI(root, output string, doc *oasDocument) error {
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal openapi: %w", err)
	}
	return writeGenerated(root, output, append(data, '\n'))
}

// countOperations is how many endpoints the document describes, which is what
// the run reports.
func countOperations(doc *oasDocument) int {
	count := 0
	for _, item := range doc.Paths {
		for _, op := range []*oasOperation{item.Get, item.Post, item.Put, item.Patch, item.Delete, item.Head, item.Option} {
			if op != nil {
				count++
			}
		}
	}
	return count
}
