//go:build ee

package containers

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/ittrail/sitebin.io/internal/store"
)

// Limits on what one compose file may declare. The plan's container cap is
// the real bound on services; these keep a hostile file cheap to reject.
const (
	maxServices     = 20
	maxEnvPerSvc    = 200
	maxEnvBytes     = 32 << 10
	maxVolumesSvc   = 10
	maxDomainsSvc   = 10
	maxCommandParts = 100
)

// Spec is a validated compose file. Services are in start order.
type Spec struct {
	Services []Service
}

// Service is one validated service.
type Service struct {
	Name       string
	Image      Image
	Env        []string // KEY=value, in file order
	Volumes    []Volume
	Domains    []Domain
	Egress     bool
	Command    []string
	WorkingDir string
	DependsOn  []string
}

// Volume mounts a root folder of the project.
type Volume struct {
	Folder   string
	Target   string
	ReadOnly bool
}

// String is the volume as a compose file writes it.
func (v Volume) String() string {
	s := v.Folder + ":" + v.Target
	if v.ReadOnly {
		s += ":ro"
	}
	return s
}

// Domain maps a host ("*" = the site's own address) onto a port.
type Domain struct {
	Host string
	Port int
}

// CustomDomains are the spec's domains other than "*".
func (s *Spec) CustomDomains() []string {
	var out []string
	for _, sv := range s.Services {
		for _, d := range sv.Domains {
			if d.Host != store.DefaultDomain {
				out = append(out, d.Host)
			}
		}
	}
	return out
}

var (
	serviceName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,30}[a-z0-9]$|^[a-z]$`)
	envKey      = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]*$`)
	hostLabel   = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
)

// fieldErr names where in the file a problem is.
func fieldErr(at string, format string, args ...any) error {
	return fmt.Errorf("%s: %s", at, fmt.Sprintf(format, args...))
}

// Parse reads and validates a compose file. The error names the offending
// key, because the customer's only way to learn what went wrong is this
// message on the edit page.
func Parse(b []byte) (*Spec, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("%s is not valid YAML: %v", store.ComposeFile, err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return nil, fmt.Errorf("%s is empty", store.ComposeFile)
	}
	top := doc.Content[0]
	if top.Kind != yaml.MappingNode {
		return nil, errors.New("the file must be a mapping with a services: key")
	}
	var services *yaml.Node
	for i := 0; i < len(top.Content); i += 2 {
		k, v := top.Content[i], top.Content[i+1]
		switch k.Value {
		case "services":
			services = v
		case "version", "name":
			// Compose's own bookkeeping; harmless, and nearly every file has it.
		default:
			return nil, fieldErr(k.Value, "not supported (only services: is)")
		}
	}
	if services == nil || services.Kind != yaml.MappingNode || len(services.Content) == 0 {
		return nil, errors.New("services: must list at least one service")
	}
	if len(services.Content)/2 > maxServices {
		return nil, fmt.Errorf("services: at most %d services", maxServices)
	}

	spec := &Spec{}
	byName := map[string]bool{}
	star := ""
	hosts := map[string]string{}
	for i := 0; i < len(services.Content); i += 2 {
		name := services.Content[i].Value
		at := "services." + name
		if !serviceName.MatchString(name) {
			return nil, fieldErr(at, "a service name is lowercase letters, digits and dashes, starting with a letter, at most 32 long")
		}
		if byName[name] {
			return nil, fieldErr(at, "declared twice")
		}
		sv, err := parseService(at, name, services.Content[i+1])
		if err != nil {
			return nil, err
		}
		for _, d := range sv.Domains {
			if d.Host == store.DefaultDomain {
				if star != "" {
					return nil, fieldErr(at+".domains", `"*" is the site's own address and can be mapped once; %s already maps it`, star)
				}
				star = name
				continue
			}
			if other, ok := hosts[d.Host]; ok {
				return nil, fieldErr(at+".domains", "%s is already mapped by %s", d.Host, other)
			}
			hosts[d.Host] = name
		}
		spec.Services = append(spec.Services, sv)
		byName[name] = true
	}
	for _, sv := range spec.Services {
		for _, dep := range sv.DependsOn {
			if !byName[dep] {
				return nil, fieldErr("services."+sv.Name+".depends_on", "no service named %q", dep)
			}
		}
	}
	ordered, err := startOrder(spec.Services)
	if err != nil {
		return nil, err
	}
	spec.Services = ordered
	return spec, nil
}

func parseService(at, name string, n *yaml.Node) (Service, error) {
	sv := Service{Name: name}
	if n.Kind != yaml.MappingNode {
		return sv, fieldErr(at, "must be a mapping")
	}
	seen := map[string]bool{}
	egressSet := false
	for i := 0; i < len(n.Content); i += 2 {
		k, v := n.Content[i].Value, n.Content[i+1]
		key := at + "." + k
		if seen[k] {
			return sv, fieldErr(key, "declared twice")
		}
		seen[k] = true
		var err error
		switch k {
		case "image":
			img, ok := Lookup(scalar(v))
			if !ok || v.Kind != yaml.ScalarNode {
				return sv, fieldErr(key, "%q is not one of Sitebin's images (%s)", scalar(v), strings.Join(ImageNames(), ", "))
			}
			sv.Image = img
		case "environment":
			sv.Env, err = parseEnv(key, v)
		case "volumes":
			sv.Volumes, err = parseVolumes(key, v)
		case "domains":
			sv.Domains, err = parseDomains(key, v)
		case "egress", "egres":
			if egressSet {
				return sv, fieldErr(key, "egress is declared twice")
			}
			egressSet = true
			sv.Egress, err = parseEgress(key, v)
		case "command":
			sv.Command, err = parseCommand(key, v)
		case "working_dir":
			sv.WorkingDir = scalar(v)
			if v.Kind != yaml.ScalarNode || !validTarget(sv.WorkingDir) {
				err = fieldErr(key, "must be an absolute path")
			}
		case "depends_on":
			sv.DependsOn, err = parseDependsOn(key, v)
		default:
			return sv, fieldErr(key, "not supported (a service takes image, environment, volumes, domains, egress, command, working_dir, depends_on)")
		}
		if err != nil {
			return sv, err
		}
	}
	if sv.Image.Name == "" {
		return sv, fieldErr(at, "image: is required (%s)", strings.Join(ImageNames(), ", "))
	}
	return sv, nil
}

// scalar is a node's text. YAML scalars keep what was written, so 3306 is
// "3306" and true is "true" — which is exactly what an environment wants.
func scalar(n *yaml.Node) string {
	if n.Kind == yaml.ScalarNode && n.Tag != "!!null" {
		return n.Value
	}
	return ""
}

func parseEnv(at string, n *yaml.Node) ([]string, error) {
	var out []string
	add := func(k, v string) error {
		if !envKey.MatchString(k) {
			return fieldErr(at, "%q is not a variable name", k)
		}
		for _, e := range out {
			if strings.HasPrefix(e, k+"=") {
				return fieldErr(at, "%s is set twice", k)
			}
		}
		out = append(out, k+"="+v)
		return nil
	}
	switch n.Kind {
	case yaml.MappingNode:
		for i := 0; i < len(n.Content); i += 2 {
			v := n.Content[i+1]
			if v.Kind != yaml.ScalarNode {
				return nil, fieldErr(at+"."+n.Content[i].Value, "must be a plain value")
			}
			if err := add(n.Content[i].Value, scalar(v)); err != nil {
				return nil, err
			}
		}
	case yaml.SequenceNode:
		for _, e := range n.Content {
			if e.Kind != yaml.ScalarNode {
				return nil, fieldErr(at, "each entry is KEY=value")
			}
			k, v, _ := strings.Cut(e.Value, "=")
			if err := add(k, v); err != nil {
				return nil, err
			}
		}
	default:
		return nil, fieldErr(at, "must be a mapping or a list of KEY=value")
	}
	if len(out) > maxEnvPerSvc {
		return nil, fieldErr(at, "at most %d variables", maxEnvPerSvc)
	}
	size := 0
	for _, e := range out {
		size += len(e)
	}
	if size > maxEnvBytes {
		return nil, fieldErr(at, "at most %d KB in total", maxEnvBytes>>10)
	}
	return out, nil
}

// validTarget is an absolute, clean container path that is not a place the
// kernel or the runtime owns.
func validTarget(p string) bool {
	if !strings.HasPrefix(p, "/") || path.Clean(p) != p || p == "/" || strings.ContainsAny(p, ":,\x00") {
		return false
	}
	for _, r := range []string{"/proc", "/sys", "/dev"} {
		if p == r || strings.HasPrefix(p, r+"/") {
			return false
		}
	}
	return true
}

func parseVolumes(at string, n *yaml.Node) ([]Volume, error) {
	if n.Kind != yaml.SequenceNode {
		return nil, fieldErr(at, "must be a list of folder:/path")
	}
	if len(n.Content) > maxVolumesSvc {
		return nil, fieldErr(at, "at most %d volumes", maxVolumesSvc)
	}
	var out []Volume
	for _, e := range n.Content {
		parts := strings.Split(scalar(e), ":")
		if e.Kind != yaml.ScalarNode || len(parts) < 2 || len(parts) > 3 {
			return nil, fieldErr(at, "%q: a volume is folder:/path or folder:/path:ro", e.Value)
		}
		v := Volume{Folder: parts[0], Target: parts[1]}
		if len(parts) == 3 {
			if parts[2] != "ro" && parts[2] != "rw" {
				return nil, fieldErr(at, "%q: the only options are ro and rw", e.Value)
			}
			v.ReadOnly = parts[2] == "ro"
		}
		if !store.ValidVolumeFolder(v.Folder) {
			return nil, fieldErr(at, "%q: %q is not a folder name — a volume is one of the project's root folders", e.Value, v.Folder)
		}
		if !validTarget(v.Target) {
			return nil, fieldErr(at, "%q: %q must be an absolute path inside the container", e.Value, v.Target)
		}
		for _, o := range out {
			if o.Target == v.Target {
				return nil, fieldErr(at, "%s is mounted twice", v.Target)
			}
		}
		out = append(out, v)
	}
	return out, nil
}

func parseDomains(at string, n *yaml.Node) ([]Domain, error) {
	if n.Kind != yaml.SequenceNode {
		return nil, fieldErr(at, `must be a list of "host:port"`)
	}
	if len(n.Content) > maxDomainsSvc {
		return nil, fieldErr(at, "at most %d domains", maxDomainsSvc)
	}
	var out []Domain
	for _, e := range n.Content {
		raw := scalar(e)
		i := strings.LastIndex(raw, ":")
		if e.Kind != yaml.ScalarNode || i < 0 {
			return nil, fieldErr(at, `%q: a domain is "host:port", e.g. "*:3000"`, e.Value)
		}
		host, portS := strings.ToLower(strings.TrimSpace(raw[:i])), raw[i+1:]
		port, err := strconv.Atoi(portS)
		if err != nil || port < 1 || port > 65535 {
			return nil, fieldErr(at, "%q: %q is not a port", raw, portS)
		}
		if host != store.DefaultDomain && !validHost(host) {
			return nil, fieldErr(at, `%q: %q is not a domain name (use "*" for the site's own address)`, raw, host)
		}
		for _, o := range out {
			if o.Host == host {
				return nil, fieldErr(at, "%s is mapped twice", host)
			}
		}
		out = append(out, Domain{Host: host, Port: port})
	}
	return out, nil
}

func validHost(h string) bool {
	if len(h) > 253 || !strings.Contains(h, ".") {
		return false
	}
	for _, l := range strings.Split(h, ".") {
		if !hostLabel.MatchString(l) {
			return false
		}
	}
	return true
}

func parseEgress(at string, n *yaml.Node) (bool, error) {
	switch strings.ToLower(scalar(n)) {
	case "allowed", "allow", "true", "yes", "on":
		return true, nil
	case "denied", "deny", "false", "no", "off", "":
		if n.Kind == yaml.ScalarNode {
			return false, nil
		}
	}
	return false, fieldErr(at, "is allowed or denied")
}

func parseCommand(at string, n *yaml.Node) ([]string, error) {
	var out []string
	switch n.Kind {
	case yaml.ScalarNode:
		words, err := splitWords(n.Value)
		if err != nil {
			return nil, fieldErr(at, "%v", err)
		}
		out = words
	case yaml.SequenceNode:
		for _, e := range n.Content {
			if e.Kind != yaml.ScalarNode {
				return nil, fieldErr(at, "each entry is a plain value")
			}
			out = append(out, e.Value)
		}
	default:
		return nil, fieldErr(at, "must be a string or a list")
	}
	if len(out) == 0 || len(out) > maxCommandParts {
		return nil, fieldErr(at, "must have between 1 and %d words", maxCommandParts)
	}
	return out, nil
}

// splitWords splits a command string the way Compose does: on whitespace,
// honouring single and double quotes and backslash escapes. There is no
// variable expansion — Sitebin never interpolates.
func splitWords(s string) ([]string, error) {
	var out []string
	var cur strings.Builder
	inWord := false
	var quote rune
	escaped := false
	for _, r := range s {
		switch {
		case escaped:
			cur.WriteRune(r)
			escaped = false
		case r == '\\' && quote != '\'':
			escaped, inWord = true, true
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote, inWord = r, true
		case r == ' ' || r == '\t' || r == '\n':
			if inWord {
				out = append(out, cur.String())
				cur.Reset()
				inWord = false
			}
		default:
			cur.WriteRune(r)
			inWord = true
		}
	}
	if quote != 0 || escaped {
		return nil, errors.New("unterminated quote")
	}
	if inWord {
		out = append(out, cur.String())
	}
	return out, nil
}

func parseDependsOn(at string, n *yaml.Node) ([]string, error) {
	var out []string
	switch n.Kind {
	case yaml.SequenceNode:
		for _, e := range n.Content {
			out = append(out, scalar(e))
		}
	case yaml.MappingNode:
		// Compose's long form ({db: {condition: …}}): the order is honoured,
		// the condition is not — there is no health waiting.
		for i := 0; i < len(n.Content); i += 2 {
			out = append(out, n.Content[i].Value)
		}
	default:
		return nil, fieldErr(at, "must be a list of service names")
	}
	return out, nil
}

// startOrder sorts services so each starts after what it depends on, keeping
// file order otherwise.
func startOrder(in []Service) ([]Service, error) {
	var out []Service
	state := map[string]int{} // 0 new, 1 visiting, 2 done
	idx := map[string]int{}
	for i, s := range in {
		idx[s.Name] = i
	}
	var visit func(name string, chain []string) error
	visit = func(name string, chain []string) error {
		switch state[name] {
		case 1:
			return fmt.Errorf("depends_on forms a cycle: %s", strings.Join(append(chain, name), " -> "))
		case 2:
			return nil
		}
		state[name] = 1
		for _, d := range in[idx[name]].DependsOn {
			if err := visit(d, append(slices.Clone(chain), name)); err != nil {
				return err
			}
		}
		state[name] = 2
		out = append(out, in[idx[name]])
		return nil
	}
	for _, s := range in {
		if err := visit(s.Name, nil); err != nil {
			return nil, err
		}
	}
	return out, nil
}
