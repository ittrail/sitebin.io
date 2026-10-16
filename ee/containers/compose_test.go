//go:build ee

package containers

import (
	"fmt"
	"strings"
	"testing"
)

// The operator's own example, verbatim.
const example = `services:
  app:
    image: alpine-node-22
    environment:
      NODE_ENV: development
      DB_HOST: db
      DB_PORT: 3306
      DB_USER: appuser
      DB_PASSWORD: apppassword
      DB_NAME: appdb
    volumes:
      - sitebin-rootfolder-1:/usr/src/app
    http:
      - 3000
      - "3001"
    egres: allowed

  db:
    image: mysql-8.4
    environment:
      MYSQL_ROOT_PASSWORD: rootpassword
      MYSQL_DATABASE: appdb
      MYSQL_USER: appuser
      MYSQL_PASSWORD: apppassword
    volumes:
      - sitebin-rootfolder-2:/var/lib/mysql
`

func TestParseTheExample(t *testing.T) {
	spec, err := Parse([]byte(example))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(spec.Services) != 2 {
		t.Fatalf("services = %d", len(spec.Services))
	}
	app, db := spec.Services[0], spec.Services[1]
	if app.Name != "app" || app.Image.Name != "alpine-node-22" || !app.Egress {
		t.Errorf("app = %+v", app)
	}
	if db.Egress {
		t.Error("egress defaults to denied")
	}
	if got := strings.Join(app.Env[:3], ","); got != "NODE_ENV=development,DB_HOST=db,DB_PORT=3306" {
		t.Errorf("env keeps order and raw values: %v", app.Env)
	}
	if len(app.Volumes) != 1 || app.Volumes[0] != (Volume{Folder: "sitebin-rootfolder-1", Target: "/usr/src/app"}) {
		t.Errorf("volumes = %+v", app.Volumes)
	}
	if len(app.Ports) != 2 || app.Ports[0] != 3000 || app.Ports[1] != 3001 || len(db.Ports) != 0 {
		t.Errorf("ports = %v, %v", app.Ports, db.Ports)
	}
	if got := strings.Join(spec.Endpoints(), ","); got != "app:3000,app:3001" {
		t.Errorf("endpoints = %s", got)
	}
}

// Endpoints keep the file's order, not the start order: the first one gets
// the site's own address, and that must not depend on depends_on.
func TestEndpointsInFileOrder(t *testing.T) {
	in := "services:\n  web:\n    image: alpine-node-22\n    http: 80\n    depends_on: [api]\n" +
		"  api:\n    image: alpine-node-22\n    http: [8080, 9090]\n  db:\n    image: mysql-8.4\n"
	spec, err := Parse([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if spec.Services[0].Name != "api" {
		t.Fatalf("start order: %s first", spec.Services[0].Name)
	}
	if got := strings.Join(spec.Endpoints(), ","); got != "web:80,api:8080,api:9090" {
		t.Errorf("endpoints = %s", got)
	}
}

func TestParseNoInterpolation(t *testing.T) {
	in := "services:\n  a:\n    image: alpine-node-22\n    environment:\n      - HOME_COPY=$HOME\n      - EMPTY=\n      - BARE\n" +
		"    command: node -e 'console.log(\"${X}\")'\n"
	spec, err := Parse([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	env := spec.Services[0].Env
	if env[0] != "HOME_COPY=$HOME" || env[1] != "EMPTY=" || env[2] != "BARE=" {
		t.Errorf("env = %v", env)
	}
	cmd := spec.Services[0].Command
	if len(cmd) != 3 || cmd[2] != `console.log("${X}")` {
		t.Errorf("command = %q", cmd)
	}
}

func TestParseDependsOnOrdersStart(t *testing.T) {
	spec, err := Parse([]byte(`services:
  web:
    image: alpine-node-22
    depends_on: [api]
  api:
    image: alpine-node-22
    depends_on:
      db: {condition: service_healthy}
  db:
    image: mysql-8.4
`))
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, s := range spec.Services {
		order = append(order, s.Name)
	}
	if strings.Join(order, ",") != "db,api,web" {
		t.Errorf("start order = %v", order)
	}
}

func TestParseErrors(t *testing.T) {
	svc := func(body string) string { return "services:\n  app:\n    image: alpine-node-22\n" + body }
	two := func(a, b string) string {
		return "services:\n  a:\n    image: mysql-8.4\n" + a + "  b:\n    image: mysql-8.4\n" + b
	}
	cases := map[string]struct{ in, want string }{
		"not yaml":         {"services: [", "not valid YAML"},
		"empty":            {"", "empty"},
		"no services":      {"version: '3'\n", "at least one service"},
		"unknown top":      {"services:\n  a:\n    image: mysql-8.4\nnetworks: {}\n", "networks: not supported"},
		"unknown key":      {svc("    ports: ['80:80']\n"), "services.app.ports: not supported"},
		"foreign image":    {"services:\n  a:\n    image: nginx:latest\n", `"nginx:latest" is not one of Sitebin's images`},
		"no image":         {"services:\n  a:\n    environment: {A: b}\n", "image: is required"},
		"bad name":         {"services:\n  App_1:\n    image: mysql-8.4\n", "a service name is"},
		"bad env key":      {svc("    environment: {'1X': y}\n"), "not a variable name"},
		"dup env":          {svc("    environment: [A=1, A=2]\n"), "A is set twice"},
		"nested env":       {svc("    environment: {A: {b: c}}\n"), "plain value"},
		"volume no target": {svc("    volumes: [data]\n"), "folder:/path"},
		"volume nested":    {svc("    volumes: ['a/b:/x']\n"), "is not a folder name"},
		"volume hidden":    {svc("    volumes: ['.git:/x']\n"), "is not a folder name"},
		"volume dotdot":    {svc("    volumes: ['..:/x']\n"), "is not a folder name"},
		"volume raw":       {svc("    volumes: ['_raw:/x']\n"), "is not a folder name"},
		"volume relative":  {svc("    volumes: ['a:x']\n"), "absolute path"},
		"volume root":      {svc("    volumes: ['a:/']\n"), "absolute path"},
		"volume proc":      {svc("    volumes: ['a:/proc/x']\n"), "absolute path"},
		"volume unclean":   {svc("    volumes: ['a:/x/../y']\n"), "absolute path"},
		"volume option":    {svc("    volumes: ['a:/x:z']\n"), "ro and rw"},
		"volume twice":     {svc("    volumes: ['a:/x', 'b:/x']\n"), "mounted twice"},
		"domains gone":     {svc("    domains: ['*:3000']\n"), "list the service's HTTP ports under http:"},
		"http mapping":     {svc("    http: {a: 1}\n"), "a list of ports"},
		"http bad port":    {svc("    http: [99999]\n"), "not a port"},
		"http zero":        {svc("    http: [0]\n"), "not a port"},
		"http word":        {svc("    http: [web]\n"), "not a port"},
		"http host:port":   {svc("    http: ['80:80']\n"), "not a port"},
		"http nested":      {svc("    http: [[80]]\n"), "not a port"},
		"http twice":       {svc("    http: [80, 80]\n"), "80 is listed twice"},
		"http too many":    {svc("    http: [1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11]\n"), "at most 10 ports"},
		"egress bad":       {svc("    egress: sometimes\n"), "allowed or denied"},
		"egress twice":     {svc("    egress: allowed\n    egres: allowed\n"), "declared twice"},
		"workdir relative": {svc("    working_dir: app\n"), "absolute path"},
		"command quote":    {svc("    command: \"node 'x\"\n"), "unterminated quote"},
		"depends unknown":  {svc("    depends_on: [db]\n"), `no service named "db"`},
		"depends cycle":    {two("    depends_on: [b]\n", "    depends_on: [a]\n"), "cycle"},
	}
	for name, c := range cases {
		_, err := Parse([]byte(c.in))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want it to mention %q", name, err, c.want)
		}
	}
}

func TestParseCapsServices(t *testing.T) {
	var b strings.Builder
	b.WriteString("services:\n")
	for i := 0; i <= maxServices; i++ {
		fmt.Fprintf(&b, "  s%d:\n    image: mysql-8.4\n", i)
	}
	if _, err := Parse([]byte(b.String())); err == nil || !strings.Contains(err.Error(), "at most") {
		t.Errorf("too many services: %v", err)
	}
}

func TestSplitWords(t *testing.T) {
	cases := map[string][]string{
		`npm run start`:            {"npm", "run", "start"},
		`sh -c "echo a b"`:         {"sh", "-c", "echo a b"},
		`echo 'it''s'`:             {"echo", "its"},
		`a\ b c`:                   {"a b", "c"},
		`  lots   of   space  `:    {"lots", "of", "space"},
		`node -e "console.log(1)"`: {"node", "-e", "console.log(1)"},
		`echo "$HOME" '$HOME' \$X`: {"echo", "$HOME", "$HOME", "$X"},
		`printf ""`:                {"printf", ""},
	}
	for in, want := range cases {
		got, err := splitWords(in)
		if err != nil || len(got) != len(want) || strings.Join(got, "|") != strings.Join(want, "|") {
			t.Errorf("splitWords(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}
