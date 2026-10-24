// Command specprep turns the pinned OpenSearch OpenAPI specification into the
// input oapi-codegen reads. It never changes what an operation sends or
// receives; it only:
//
//   - keeps the operation groups selected by spec/specprep.yaml (include and
//     exclude globs over x-operation-group) and, optionally, drops operations
//     marked deprecated;
//   - keeps one HTTP method per (group, path) pair where the specification
//     lists equivalent methods (GET and POST /_search, PUT and POST
//     /{index}/_doc/{id}, ...), chosen by method-priority;
//   - replaces the specification's positional operation ids (search.0,
//     search.1, ...) with stable Go names derived from the group and the path
//     parameters (Search, SearchWithIndex, ...), so that generated method
//     names don't shift when a release adds or reorders variants;
//   - rewrites parameters declared as "one X or a list of X" (oneOf: [X,
//     {type: array, items: X}]), which oapi-codegen turns into a union over
//     an undefined type, as a list of X sent comma-separated (explode:
//     false), the form OpenSearch documents for those parameters;
//   - drops the leading underscore of component names (_common.mapping___X
//     becomes common.mapping___X, rewriting every $ref), so generated types
//     read CommonMappingX rather than UnderscoreCommonMappingX;
//   - names properties that start with an underscore (_id, _source, ...)
//     through x-go-name (ID, Source, ...) unless a sibling property would
//     take the same name;
//   - rewrites OpenAPI 3.1 multi-type schemas (type: [string, "null"], ...)
//     that oapi-codegen cannot express: a single non-null type is kept as is,
//     a query or path parameter with several types becomes a string, and any
//     other schema with several types becomes untyped (any JSON value).
//
// Every other fix to the specification lives in spec/overlay.yaml, which
// oapi-codegen applies itself.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"log"
	"os"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config is the content of spec/specprep.yaml.
type Config struct {
	// Spec is the pinned specification, relative to the config file.
	Spec string `yaml:"spec"`
	// Output is where the prepared specification is written, relative to
	// the config file.
	Output string `yaml:"output"`
	// Include lists x-operation-group globs (path.Match syntax) to keep.
	Include []string `yaml:"include"`
	// Exclude lists x-operation-group globs to drop, applied after Include.
	Exclude []string `yaml:"exclude"`
	// ExcludeOperations lists original operation ids (search.0, ...) to drop.
	ExcludeOperations []string `yaml:"exclude-operations"`
	// SkipDeprecated drops operations marked `deprecated: true`.
	SkipDeprecated bool `yaml:"skip-deprecated"`
	// MethodPriority orders HTTP methods; when one group lists several
	// methods for the same path, only the first in this order is kept.
	MethodPriority []string `yaml:"method-priority"`
	// Names overrides the derived Go name for an original operation id.
	Names map[string]string `yaml:"names"`
	// Initialisms are words written in upper case in Go names.
	Initialisms []string `yaml:"initialisms"`
}

var httpMethods = []string{"get", "put", "post", "delete", "options", "head", "patch", "trace"}

var pathParam = regexp.MustCompile(`\{([^}]+)\}`)

type operation struct {
	path    string
	method  string
	node    *yaml.Node // the operation object
	id      string     // original operationId
	group   string     // x-operation-group
	params  []string   // path parameters, in path order
	newName string
}

func main() {
	configPath := flag.String("config", "spec/specprep.yaml", "specprep configuration")
	flag.Parse()
	if err := run(*configPath); err != nil {
		log.Fatalf("specprep: %v", err)
	}
}

func run(configPath string) error {
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return err
	}
	var cfg Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return fmt.Errorf("parse %s: %w", configPath, err)
	}
	base := path.Dir(configPath)
	specRaw, err := os.ReadFile(path.Join(base, cfg.Spec))
	if err != nil {
		return err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(specRaw, &doc); err != nil {
		return fmt.Errorf("parse spec: %w", err)
	}
	root := doc.Content[0]

	kept, dropped, err := selectOperations(&cfg, root)
	if err != nil {
		return err
	}
	if err := nameOperations(&cfg, kept); err != nil {
		return err
	}
	for _, op := range kept {
		setScalar(op.node, "operationId", op.newName)
	}
	fixMultiTypes(root, false)
	fixListParameters(root)
	renameComponents(root)
	nameUnderscoreProperties(&cfg, root)

	var out bytes.Buffer
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return err
	}
	if err := enc.Close(); err != nil {
		return err
	}
	outPath := path.Join(base, cfg.Output)
	if err := os.MkdirAll(path.Dir(outPath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(outPath, out.Bytes(), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "specprep: kept %d operations, dropped %d, wrote %s\n", len(kept), dropped, outPath)
	return nil
}

// selectOperations removes every operation the configuration doesn't keep
// from the document and returns the ones it keeps.
func selectOperations(cfg *Config, root *yaml.Node) ([]*operation, int, error) {
	paths := mapValue(root, "paths")
	if paths == nil {
		return nil, 0, fmt.Errorf("spec has no paths")
	}
	priority := func(method string) int {
		for i, m := range cfg.MethodPriority {
			if strings.EqualFold(m, method) {
				return i
			}
		}
		return len(cfg.MethodPriority)
	}

	var candidates []*operation
	for i := 0; i < len(paths.Content); i += 2 {
		p := paths.Content[i].Value
		item := paths.Content[i+1]
		for j := 0; j < len(item.Content); j += 2 {
			method := item.Content[j].Value
			if !slices.Contains(httpMethods, method) {
				continue
			}
			node := item.Content[j+1]
			op := &operation{
				path:   p,
				method: method,
				node:   node,
				id:     scalar(node, "operationId"),
				group:  scalar(node, "x-operation-group"),
			}
			for _, m := range pathParam.FindAllStringSubmatch(p, -1) {
				op.params = append(op.params, m[1])
			}
			candidates = append(candidates, op)
		}
	}

	keep := map[*yaml.Node]bool{}
	var kept []*operation
	best := map[string]*operation{} // group + path -> preferred method
	for _, op := range candidates {
		ok, err := wanted(cfg, op)
		if err != nil {
			return nil, 0, err
		}
		if !ok {
			continue
		}
		key := op.group + " " + op.path
		if cur, found := best[key]; !found || priority(op.method) < priority(cur.method) {
			best[key] = op
		}
	}
	for _, op := range candidates {
		if best[op.group+" "+op.path] == op {
			keep[op.node] = true
			kept = append(kept, op)
		}
	}

	dropped := 0
	var newPaths []*yaml.Node
	for i := 0; i < len(paths.Content); i += 2 {
		item := paths.Content[i+1]
		var content []*yaml.Node
		ops := 0
		for j := 0; j < len(item.Content); j += 2 {
			k, v := item.Content[j], item.Content[j+1]
			if slices.Contains(httpMethods, k.Value) {
				if !keep[v] {
					dropped++
					continue
				}
				ops++
			}
			content = append(content, k, v)
		}
		if ops == 0 {
			continue
		}
		item.Content = content
		newPaths = append(newPaths, paths.Content[i], item)
	}
	paths.Content = newPaths
	return kept, dropped, nil
}

func wanted(cfg *Config, op *operation) (bool, error) {
	if op.group == "" || op.id == "" {
		return false, fmt.Errorf("%s %s: missing operationId or x-operation-group", op.method, op.path)
	}
	if cfg.SkipDeprecated && scalar(op.node, "deprecated") == "true" {
		return false, nil
	}
	if slices.Contains(cfg.ExcludeOperations, op.id) {
		return false, nil
	}
	in, err := matchAny(cfg.Include, op.group)
	if err != nil || !in {
		return false, err
	}
	out, err := matchAny(cfg.Exclude, op.group)
	return !out, err
}

func matchAny(patterns []string, group string) (bool, error) {
	for _, p := range patterns {
		ok, err := path.Match(p, group)
		if err != nil {
			return false, fmt.Errorf("pattern %q: %w", p, err)
		}
		if ok {
			return true, nil
		}
	}
	return false, nil
}

// pascal converts snake_case, dotted or path-like words to a Go name.
func (cfg *Config) pascal(s string) string {
	var b strings.Builder
	for _, w := range strings.FieldsFunc(s, func(r rune) bool {
		return r == '_' || r == '.' || r == '-' || r == '/' || r == '{' || r == '}'
	}) {
		if slices.ContainsFunc(cfg.Initialisms, func(i string) bool { return strings.EqualFold(i, w) }) {
			b.WriteString(strings.ToUpper(w))
			continue
		}
		b.WriteString(strings.ToUpper(w[:1]) + w[1:])
	}
	return b.String()
}

// nameOperations gives each kept operation a Go name. Within a group the
// variant with the fewest path parameters takes the group's name and every
// other variant appends "With" and the path parameters that variant adds. Variants that still
// collide append the literal path segments that tell them apart.
func nameOperations(cfg *Config, ops []*operation) error {
	pascal := cfg.pascal

	byGroup := map[string][]*operation{}
	for _, op := range ops {
		byGroup[op.group] = append(byGroup[op.group], op)
	}
	groups := make([]string, 0, len(byGroup))
	for g := range byGroup {
		groups = append(groups, g)
	}
	sort.Strings(groups)

	seen := map[string]*operation{}
	for _, g := range groups {
		variants := byGroup[g]
		common := variants[0].params
		for _, v := range variants {
			if len(v.params) < len(common) {
				common = v.params
			}
		}
		baseName := pascal(g)
		names := map[string][]*operation{}
		for _, v := range variants {
			name := baseName
			var ps []string
			for _, p := range v.params {
				if !slices.Contains(common, p) {
					ps = append(ps, pascal(p))
				}
			}
			if len(ps) > 0 {
				name += "With" + strings.Join(ps, "And")
			}
			names[name] = append(names[name], v)
		}
		for name, vs := range names {
			if len(vs) == 1 {
				vs[0].newName = name
				continue
			}
			for _, v := range vs {
				var lits []string
				for _, seg := range strings.Split(v.path, "/") {
					if seg != "" && !strings.HasPrefix(seg, "{") {
						lits = append(lits, pascal(seg))
					}
				}
				v.newName = name + "Via" + strings.Join(lits, "")
			}
		}
		for _, v := range variants {
			if override, ok := cfg.Names[v.id]; ok {
				v.newName = override
			}
			if prev, dup := seen[v.newName]; dup {
				return fmt.Errorf("name %s given to both %s and %s; add a names: override", v.newName, prev.id, v.id)
			}
			seen[v.newName] = v
		}
	}
	return nil
}

// fixMultiTypes rewrites `type: [a, b, ...]` schemas, see the package
// comment. inParameter is true below a parameter object (one with `in`).
func fixMultiTypes(n *yaml.Node, inParameter bool) {
	switch n.Kind {
	case yaml.DocumentNode, yaml.SequenceNode:
		for _, c := range n.Content {
			fixMultiTypes(c, inParameter)
		}
	case yaml.MappingNode:
		if mapValue(n, "in") != nil && mapValue(n, "name") != nil {
			inParameter = true
		}
		for i := 0; i < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			if k.Value == "type" && v.Kind == yaml.SequenceNode {
				var types []string
				for _, t := range v.Content {
					if t.Value != "null" {
						types = append(types, t.Value)
					}
				}
				switch {
				case len(types) == 1:
					n.Content[i+1] = &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: types[0]}
				case inParameter:
					n.Content[i+1] = &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "string"}
				default:
					n.Content = append(n.Content[:i], n.Content[i+2:]...)
					i -= 2
				}
				continue
			}
			fixMultiTypes(v, inParameter)
		}
	}
}

var componentSections = []string{"schemas", "parameters", "responses", "requestBodies", "headers"}

// renameComponents drops leading underscores from each dot-separated part of
// component names and rewrites the references to them. A name that would
// collide with an existing one is left alone.
func renameComponents(root *yaml.Node) {
	components := mapValue(root, "components")
	renames := map[string]string{}
	for _, section := range componentSections {
		entries := mapValue(components, section)
		if entries == nil {
			continue
		}
		taken := map[string]bool{}
		for i := 0; i < len(entries.Content); i += 2 {
			taken[entries.Content[i].Value] = true
		}
		for i := 0; i < len(entries.Content); i += 2 {
			key := entries.Content[i]
			parts := strings.Split(key.Value, ".")
			for j, part := range parts {
				parts[j] = strings.TrimLeft(part, "_")
			}
			renamed := strings.Join(parts, ".")
			if renamed == key.Value || taken[renamed] {
				continue
			}
			taken[renamed] = true
			prefix := "#/components/" + section + "/"
			renames[prefix+key.Value] = prefix + renamed
			key.Value = renamed
		}
	}
	var walk func(n *yaml.Node)
	walk = func(n *yaml.Node) {
		if n.Kind == yaml.MappingNode {
			for i := 0; i < len(n.Content); i += 2 {
				if n.Content[i].Value == "$ref" {
					if to, ok := renames[n.Content[i+1].Value]; ok {
						n.Content[i+1].Value = to
					}
				}
			}
		}
		for _, c := range n.Content {
			walk(c)
		}
	}
	walk(root)
}

// nameUnderscoreProperties gives properties that start with an underscore a
// Go name without it, see the package comment. Struct fields come from a
// schema's own properties and from every allOf part (followed through
// $refs), so two properties collide when they meet anywhere in that closure.
func nameUnderscoreProperties(cfg *Config, root *yaml.Node) {
	schemas := mapValue(mapValue(root, "components"), "schemas")
	resolve := func(n *yaml.Node) *yaml.Node {
		ref := scalar(n, "$ref")
		if name, ok := strings.CutPrefix(ref, "#/components/schemas/"); ok {
			return mapValue(schemas, name)
		}
		return n
	}
	type field struct {
		name   string
		schema *yaml.Node
	}
	var closure func(n *yaml.Node, seen map[*yaml.Node]bool) []field
	closure = func(n *yaml.Node, seen map[*yaml.Node]bool) []field {
		n = resolve(n)
		if n == nil || n.Kind != yaml.MappingNode || seen[n] {
			return nil
		}
		seen[n] = true
		var fields []field
		if props := mapValue(n, "properties"); props != nil && props.Kind == yaml.MappingNode {
			for i := 0; i < len(props.Content); i += 2 {
				fields = append(fields, field{props.Content[i].Value, props.Content[i+1]})
			}
		}
		if parts := mapValue(n, "allOf"); parts != nil {
			for _, part := range parts.Content {
				fields = append(fields, closure(part, seen)...)
			}
		}
		return fields
	}

	blocked := map[*yaml.Node]bool{}
	candidates := map[*yaml.Node]string{}
	var walk func(n *yaml.Node)
	walk = func(n *yaml.Node) {
		if n.Kind == yaml.MappingNode && (mapValue(n, "properties") != nil || mapValue(n, "allOf") != nil) {
			fields := closure(n, map[*yaml.Node]bool{})
			// The same property can arrive twice (a part redefining a
			// base's _source); only distinct JSON names collide.
			jsonNames := map[string]map[string]bool{}
			for _, f := range fields {
				goName := cfg.pascal(f.name)
				if jsonNames[goName] == nil {
					jsonNames[goName] = map[string]bool{}
				}
				jsonNames[goName][f.name] = true
			}
			for _, f := range fields {
				goName := cfg.pascal(f.name)
				if !strings.HasPrefix(f.name, "_") || goName == "" || f.schema.Kind != yaml.MappingNode {
					continue
				}
				if len(jsonNames[goName]) > 1 {
					blocked[f.schema] = true
				}
				candidates[f.schema] = goName
			}
		}
		for _, c := range n.Content {
			walk(c)
		}
	}
	walk(root)
	for schema, goName := range candidates {
		if blocked[schema] || mapValue(schema, "x-go-name") != nil {
			continue
		}
		schema.Content = append(schema.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "x-go-name"},
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: goName})
	}
}

// fixListParameters rewrites "one X or a list of X" parameters, see the
// package comment.
func fixListParameters(n *yaml.Node) {
	switch n.Kind {
	case yaml.DocumentNode, yaml.SequenceNode:
		for _, c := range n.Content {
			fixListParameters(c)
		}
	case yaml.MappingNode:
		if mapValue(n, "in") != nil && mapValue(n, "name") != nil {
			if schema := mapValue(n, "schema"); schema != nil {
				if list := singleOrList(schema); list != nil {
					replaceValue(n, "schema", list)
					if scalar(n, "in") == "query" {
						setOrAdd(n, "explode", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "false"})
						setOrAdd(n, "style", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "form"})
					}
				}
			}
			return
		}
		for i := 1; i < len(n.Content); i += 2 {
			fixListParameters(n.Content[i])
		}
	}
}

// singleOrList returns the array schema of a oneOf/anyOf [X, array of X].
func singleOrList(schema *yaml.Node) *yaml.Node {
	for _, key := range []string{"oneOf", "anyOf"} {
		alts := mapValue(schema, key)
		if alts == nil || alts.Kind != yaml.SequenceNode || len(alts.Content) != 2 {
			continue
		}
		for i, alt := range alts.Content {
			other := alts.Content[1-i]
			if scalar(alt, "type") == "array" && sameNode(mapValue(alt, "items"), other) {
				return alt
			}
		}
	}
	return nil
}

func sameNode(a, b *yaml.Node) bool {
	if a == nil || b == nil {
		return false
	}
	ea, errA := yaml.Marshal(a)
	eb, errB := yaml.Marshal(b)
	return errA == nil && errB == nil && bytes.Equal(ea, eb)
}

func replaceValue(n *yaml.Node, key string, value *yaml.Node) {
	for i := 0; i < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			n.Content[i+1] = value
			return
		}
	}
}

func setOrAdd(n *yaml.Node, key string, value *yaml.Node) {
	if mapValue(n, key) != nil {
		replaceValue(n, key, value)
		return
	}
	n.Content = append(n.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, value)
}

func mapValue(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

func scalar(n *yaml.Node, key string) string {
	if v := mapValue(n, key); v != nil && v.Kind == yaml.ScalarNode {
		return v.Value
	}
	return ""
}

func setScalar(n *yaml.Node, key, value string) {
	if v := mapValue(n, key); v != nil {
		v.Value = value
		v.Tag = "!!str"
		v.Style = 0
	}
}
