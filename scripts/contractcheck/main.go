// Command contractcheck verifies that the OpenAPI specs and  docs/api/authorization.yaml agree
// (implementation plan, review checklist E).
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

type route struct {
	Service     string   `yaml:"service"`
	OperationID string   `yaml:"operationId"`
	Method      string   `yaml:"method"`
	Path        string   `yaml:"path"`
	Access      string   `yaml:"access"`
	Roles       []string `yaml:"roles"`
	Scope       string   `yaml:"scope"`
	Callers     []string `yaml:"callers"`
	Ownership   string   `yaml:"ownership"`
	Note        string   `yaml:"note"`
}

type authz struct {
	Services []string `yaml:"services"`
	Roles    []string `yaml:"roles"`
	Routes   []route  `yaml:"routes"`
}

type op struct {
	service, id, method, path string
	secured                   string // "", "bearerAuth", "serviceToken"
	callers                   []string
	raw                       map[string]any
}

type checker struct {
	root string
	errs []string
}

// report prints the collected problems to stderr; a failing stderr cannot be reported anywhere else.
func (c *checker) report() {
	for _, e := range c.errs {
		_, _ = fmt.Fprintln(os.Stderr, "FAIL:", e)
	}
}

func (c *checker) errf(format string, a ...any) { c.errs = append(c.errs, fmt.Sprintf(format, a...)) }

func main() {
	root := flag.String("root", ".", "repository root")
	policyFor := flag.String("policy", "", "print the Go authorisation table (package policy) of this service and exit")
	clientCfg := flag.String("client-config", "", "print an oapi-codegen client config for database_middleware limited to the operations this caller may use, and exit")
	flag.Parse()
	c := &checker{root: *root}

	if *policyFor != "" || *clientCfg != "" {
		c.generate(*policyFor, *clientCfg)
		return
	}

	var az authz
	c.readYAML("docs/api/authorization.yaml", &az)

	specs := map[string]map[string]any{}
	var ops []op
	for _, s := range az.Services {
		var doc map[string]any
		c.readYAML("docs/api/"+s+".yaml", &doc)
		specs[s] = doc
		ops = append(ops, c.collectOps(s, doc)...)
		c.checkSchemas(s, doc)
	}

	c.checkRoutes(az, ops)
	c.checkMiddleware(specs["database_middleware"], ops)

	if len(c.errs) > 0 {
		c.report()
		_, _ = fmt.Fprintf(os.Stderr, "%d problem(s)\n", len(c.errs)) // nothing to do if stderr is gone
		os.Exit(1)
	}
	fmt.Printf("OK: %d routes in authz.yaml, %d operations in the specs\n", len(az.Routes), len(ops))
}

func (c *checker) readYAML(rel string, out any) {
	b, err := os.ReadFile(filepath.Join(c.root, rel)) //nolint:gosec // repo files
	if err != nil {
		c.errf("%s: %v", rel, err)
		return
	}
	if err := yaml.Unmarshal(b, out); err != nil {
		c.errf("%s: %v", rel, err)
	}
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func asList(v any) []any {
	l, _ := v.([]any)
	return l
}

func (c *checker) collectOps(service string, doc map[string]any) []op {
	var ops []op
	globalSec := secName(doc["security"])
	paths := asMap(doc["paths"])
	for p, item := range paths {
		for m, o := range asMap(item) {
			om := asMap(o)
			if om == nil || !isMethod(m) {
				continue
			}
			id, _ := om["operationId"].(string)
			if id == "" {
				c.errf("%s %s %s: operationId missing", service, m, p)
				continue
			}
			sec := globalSec
			if s, ok := om["security"]; ok {
				sec = secName(s)
			}
			var callers []string
			for _, x := range asList(om["x-allowed-callers"]) {
				callers = append(callers, fmt.Sprint(x))
			}
			ops = append(ops, op{service, id, strings.ToUpper(m), p, sec, callers, om})
		}
	}
	sort.Slice(ops, func(i, j int) bool { return ops[i].path+ops[i].method < ops[j].path+ops[j].method })
	return ops
}

func isMethod(m string) bool {
	switch m {
	case "get", "post", "put", "patch", "delete":
		return true
	}
	return false
}

func secName(v any) string {
	for _, e := range asList(v) {
		for k := range asMap(e) {
			return k
		}
	}
	return ""
}

func (c *checker) checkRoutes(az authz, ops []op) {
	roles := map[string]bool{"any": true}
	for _, r := range az.Roles {
		roles[r] = true
	}
	seen := map[string]bool{}
	byKey := map[string]route{}
	for _, r := range az.Routes {
		key := r.Service + " " + r.OperationID
		if seen[key] && r.OperationID != "*" {
			c.errf("authz.yaml: duplicate %s", key)
		}
		seen[key] = true
		byKey[key] = r
		switch r.Access {
		case "public", "internal":
			if len(r.Roles) > 0 {
				c.errf("authz.yaml %s: %s route must not list roles", key, r.Access)
			}
		case "user":
			if len(r.Roles) == 0 {
				c.errf("authz.yaml %s: user route needs roles (deny by default)", key)
			}
			for _, role := range r.Roles {
				if !roles[role] {
					c.errf("authz.yaml %s: unknown role %q", key, role)
				}
				if role == "any" && len(r.Roles) > 1 {
					c.errf("authz.yaml %s: 'any' cannot be combined with other roles", key)
				}
			}
			if r.Scope != "" && r.Scope != "full" && r.Scope != "setup" {
				c.errf("authz.yaml %s: scope must be full or setup", key)
			}
			if r.Scope == "setup" && (len(r.Roles) != 1 || r.Roles[0] != "any") {
				c.errf("authz.yaml %s: setup scope only for roles [any]", key)
			}
		case "service":
			if len(r.Callers) == 0 {
				c.errf("authz.yaml %s: service route needs callers", key)
			}
		default:
			c.errf("authz.yaml %s: access must be public, user, service or internal", key)
		}
		if r.Access != "user" && r.Scope != "" {
			c.errf("authz.yaml %s: scope only applies to user routes", key)
		}
		// rate-limited public routes and the mail/user rules
		if r.Service == "mail_service" && r.Access == "user" {
			c.errf("authz.yaml %s: mail_service must not accept user tokens (decision 16)", key)
		}
	}

	specKeys := map[string]bool{}
	for _, o := range ops {
		key := o.service + " " + o.id
		if o.service == "database_middleware" && strings.HasPrefix(o.path, "/v1/ops/") {
			if o.method != "POST" || o.path != "/v1/ops/"+o.id {
				c.errf("database_middleware %s: path must be POST /v1/ops/<operationId>", o.id)
			}
			if o.secured != "serviceToken" {
				c.errf("database_middleware %s: must require serviceToken", o.id)
			}
			continue
		}
		specKeys[key] = true
		r, ok := byKey[key]
		if !ok {
			c.errf("%s %s %s (%s) is in the spec but not in authz.yaml", o.service, o.method, o.path, o.id)
			continue
		}
		if r.Method != o.method || r.Path != o.path {
			c.errf("%s: authz.yaml says %s %s, spec says %s %s", key, r.Method, r.Path, o.method, o.path)
		}
		want := map[string]string{"public": "", "internal": "", "user": "bearerAuth", "service": "serviceToken"}[r.Access]
		if o.secured != want {
			c.errf("%s: access %q needs security %q in the spec, found %q", key, r.Access, want, o.secured)
		}
		if r.Access == "user" && o.service == "mail_service" {
			c.errf("%s: user access on mail_service", key)
		}
	}
	for key, r := range byKey {
		if r.OperationID == "*" {
			continue
		}
		if !specKeys[key] {
			c.errf("authz.yaml %s is not in the spec", key)
		}
	}
}

// refs collects the schema names reachable from a response or request of an operation.
func reachable(schemas map[string]any, start any, seen map[string]bool) {
	switch v := start.(type) {
	case map[string]any:
		if ref, ok := v["$ref"].(string); ok && strings.HasPrefix(ref, "#/components/schemas/") {
			name := strings.TrimPrefix(ref, "#/components/schemas/")
			if !seen[name] {
				seen[name] = true
				reachable(schemas, schemas[name], seen)
			}
			return
		}
		for _, x := range v {
			reachable(schemas, x, seen)
		}
	case []any:
		for _, x := range v {
			reachable(schemas, x, seen)
		}
	}
}

func (c *checker) checkMiddleware(doc map[string]any, ops []op) {
	schemas := asMap(asMap(doc["components"])["schemas"])
	known := map[string]bool{"user_service": true, "calendar_service": true, "purge_job": true}
	forbiddenForCalendar := []string{"UserWithCredentials", "MfaSecretRecord", "EmailTokenResult", "PasswordHash"}
	bookingOps := func(id string) bool {
		return (strings.HasPrefix(id, "booking") && id != "bookingsCompletePast") || id == "calendarGetWindows"
	}
	for _, o := range ops {
		if o.service != "database_middleware" || !strings.HasPrefix(o.path, "/v1/ops/") {
			continue
		}
		if len(o.callers) == 0 {
			c.errf("middleware %s: x-allowed-callers missing", o.id)
		}
		for _, cl := range o.callers {
			if !known[cl] {
				c.errf("middleware %s: unknown caller %q", o.id, cl)
			}
			switch cl {
			case "mail_service":
				c.errf("middleware %s: mail_service must have no operation", o.id)
			case "calendar_service":
				seen := map[string]bool{}
				reachable(schemas, o.raw["responses"], seen)
				reachable(schemas, o.raw["requestBody"], seen)
				for _, f := range forbiddenForCalendar {
					if seen[f] {
						c.errf("middleware %s: calendar_service must not touch %s", o.id, f)
					}
				}
			case "user_service":
				if bookingOps(o.id) {
					c.errf("middleware %s: user_service must not read bookings", o.id)
				}
			}
		}
		if (o.id == "purgeExpired" || o.id == "bookingsCompletePast") && !(len(o.callers) == 1 && o.callers[0] == "purge_job") {
			c.errf("middleware %s: only purge_job may call it", o.id)
		}
		if o.id != "purgeExpired" && o.id != "bookingsCompletePast" && o.id != "retentionGet" && o.id != "auditWrite" {
			for _, cl := range o.callers {
				if cl == "purge_job" {
					c.errf("middleware %s: purge_job may only call purge, retentionGet and auditWrite", o.id)
				}
			}
		}
		if bookingOps(o.id) && o.id != "calendarGetWindows" {
			for _, cl := range o.callers {
				if cl != "calendar_service" {
					c.errf("middleware %s: booking operations belong to calendar_service", o.id)
				}
			}
		}
	}
}

// checkSchemas enforces the conventions: bounded strings, arrays and numbers, closed request bodies.
func (c *checker) checkSchemas(service string, doc map[string]any) {
	schemas := asMap(asMap(doc["components"])["schemas"])
	bounded := map[string]bool{"uuid": true, "date-time": true, "date": true, "uri": false}
	var walk func(path string, s any)
	walk = func(path string, s any) {
		m := asMap(s)
		if m == nil {
			return
		}
		if _, isRef := m["$ref"]; isRef {
			return
		}
		switch m["type"] {
		case "string":
			f, _ := m["format"].(string)
			_, hasMax := m["maxLength"]
			_, hasEnum := m["enum"]
			_, hasPat := m["pattern"]
			if !(hasMax || hasEnum || hasPat || bounded[f]) {
				c.errf("%s %s: string without maxLength/enum/pattern", service, path)
			}
		case "array":
			if _, ok := m["maxItems"]; !ok {
				c.errf("%s %s: array without maxItems", service, path)
			}
		case "integer", "number":
			_, hasMin := m["minimum"]
			_, hasMax := m["maximum"]
			if !(hasMin && hasMax) {
				c.errf("%s %s: number without minimum and maximum", service, path)
			}
		}
		for k, v := range asMap(m["properties"]) {
			walk(path+"."+k, v)
		}
		if it, ok := m["items"]; ok {
			walk(path+"[]", it)
		}
		for _, k := range []string{"allOf", "oneOf", "anyOf"} {
			for i, v := range asList(m[k]) {
				walk(fmt.Sprintf("%s.%s[%d]", path, k, i), v)
			}
		}
	}
	for name, s := range schemas {
		walk("schema "+name, s)
	}
	// request bodies must be closed objects
	for p, item := range asMap(doc["paths"]) {
		for m, o := range asMap(item) {
			rb := asMap(asMap(o)["requestBody"])
			if rb == nil || !isMethod(m) {
				continue
			}
			sch := asMap(asMap(asMap(rb["content"])["application/json"])["schema"])
			c.requireClosed(service, m+" "+p, schemas, sch, 0)
		}
	}
}

func (c *checker) requireClosed(service, where string, schemas map[string]any, s map[string]any, depth int) {
	if s == nil || depth > 4 {
		return
	}
	if ref, ok := s["$ref"].(string); ok {
		c.requireClosed(service, where, schemas, asMap(schemas[strings.TrimPrefix(ref, "#/components/schemas/")]), depth+1)
		return
	}
	if all := asList(s["allOf"]); len(all) > 0 {
		for _, x := range all {
			c.requireClosed(service, where, schemas, asMap(x), depth+1)
		}
		return
	}
	if s["type"] == "object" {
		if v, ok := s["additionalProperties"]; !ok || v != false {
			c.errf("%s %s: request body object without additionalProperties: false", service, where)
		}
	}
}

// generate prints generated sources to stdout (make gen).
func (c *checker) generate(policyFor, clientCfg string) {
	var az authz
	c.readYAML("docs/api/authorization.yaml", &az)
	if policyFor != "" {
		fmt.Print(renderPolicy(az, policyFor))
	}
	if clientCfg != "" {
		var doc map[string]any
		c.readYAML("docs/api/database_middleware.yaml", &doc)
		var ids []string
		for _, o := range c.collectOps("database_middleware", doc) {
			for _, cl := range o.callers {
				if cl == clientCfg {
					ids = append(ids, o.id)
				}
			}
		}
		sort.Strings(ids)
		fmt.Print("# Generated by scripts/contractcheck: only the operations " + clientCfg + " may call (x-allowed-callers).\n")
		fmt.Print("package: mwclient\ngenerate:\n  models: true\n  client: true\noutput-options:\n  include-operation-ids:\n")
		for _, id := range ids {
			fmt.Printf("    - %s\n", id)
		}
	}
	if len(c.errs) > 0 {
		c.report()
		os.Exit(1)
	}
}

func renderPolicy(az authz, service string) string {
	var b strings.Builder
	b.WriteString("// Code generated by scripts/contractcheck from docs/api/authorization.yaml. DO NOT EDIT.\n\n")
	b.WriteString("// Package policy is the authorisation table of the service: who may call which operation.\npackage policy\n\n")
	b.WriteString("// Access is how a route is protected.\ntype Access string\n\n")
	b.WriteString("// Access values, see docs/api/authorization.yaml.\nconst (\n\tPublic   Access = \"public\"\n\tUser     Access = \"user\"\n\tService  Access = \"service\"\n\tInternal Access = \"internal\"\n)\n\n")
	b.WriteString("// Route is the rule of one operation. Roles contains \"any\" for every authenticated user. Setup routes also accept scope=setup tokens.\n")
	b.WriteString("type Route struct {\n\tAccess  Access\n\tRoles   []string\n\tSetup   bool\n\tCallers []string\n}\n\n")
	b.WriteString("// Routes maps the operationId to its rule.\nvar Routes = map[string]Route{\n")
	for _, r := range az.Routes {
		if r.Service != service || r.OperationID == "*" {
			continue
		}
		b.WriteString(fmt.Sprintf("\t%q: {Access: %s", r.OperationID, strings.ToUpper(r.Access[:1])+r.Access[1:]))
		if len(r.Roles) > 0 {
			q := make([]string, len(r.Roles))
			for i, x := range r.Roles {
				q[i] = fmt.Sprintf("%q", x)
			}
			b.WriteString(fmt.Sprintf(", Roles: []string{%s}", strings.Join(q, ", ")))
		}
		if r.Scope == "setup" {
			b.WriteString(", Setup: true")
		}
		if len(r.Callers) > 0 {
			q := make([]string, len(r.Callers))
			for i, x := range r.Callers {
				q[i] = fmt.Sprintf("%q", x)
			}
			b.WriteString(fmt.Sprintf(", Callers: []string{%s}", strings.Join(q, ", ")))
		}
		b.WriteString("},\n")
	}
	b.WriteString("}\n")
	return b.String()
}
