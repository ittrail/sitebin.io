//go:build ee

// Package containers runs container sites: the enterprise half of the third
// site mode. The core records what a site should be doing and routes requests
// to it; this package parses the site's sitebin-container-compose.yaml, runs
// the project on the Docker Engine, and reports what it observed.
//
// This package is NOT covered by the repository's MIT license; see ee/LICENSE.
package containers

// Image is one entry of the catalogue: the only images a container site can
// run. Customers name them by Name; the reference is pinned by digest, so
// what runs is exactly what was reviewed, whatever happens to the tag.
type Image struct {
	// Name is what a compose file says.
	Name string
	// Display is the upstream tag, for people.
	Display string
	// Repo and Digest are what is pulled and run.
	Repo   string
	Digest string
	// Cmd replaces the image's default command when the service declares
	// none.
	Cmd []string
	// Env is set before the service's own environment, which wins.
	Env map[string]string
	// Tmpfs are writable scratch mounts. The root filesystem is read-only,
	// and a tmpfs is charged to the container's memory limit, so what a
	// service can write outside its volumes is bounded.
	Tmpfs map[string]string
	// DataPaths are where the image keeps state. One the service does not
	// mount a folder over gets a tmpfs, so it cannot become an unbounded
	// anonymous volume on the host's disk.
	DataPaths []string
	// DataTmpfs is the tmpfs option string used for an unmounted data path.
	DataTmpfs string
	// OldDataPaths are mount targets where earlier versions kept their data
	// and this one refuses to start — target → where the data lives now. A
	// compose file mounting one is refused with that path, rather than the
	// project restarting forever, unless the service sets DataPathEnv and so
	// chose its own data path.
	OldDataPaths map[string]string
	DataPathEnv  string
}

// Ref is the reference containers are created from.
func (i Image) Ref() string { return i.Repo + "@" + i.Digest }

// nodeStart is the node image's default command. It installs dependencies
// only when there are none — without egress an install cannot work, so a
// project meant to run offline uploads its node_modules — and never exits on
// an empty folder, so a fresh project is not a restart loop.
const nodeStart = `if [ -f package.json ]; then
  if [ ! -d node_modules ]; then
    npm install --no-audit --no-fund || echo "sitebin: npm install failed (does this service have egress: allowed?)"
  fi
  exec npm start
elif [ -f index.js ]; then
  exec node index.js
else
  echo "sitebin: no package.json or index.js in $(pwd) -- upload your app into the folder mounted here"
  exec tail -f /dev/null
fi`

// dotnetStart is the .NET image's default command. `dotnet publish` writes
// one <App>.runtimeconfig.json beside <App>.dll; with exactly one in the
// folder, that app runs. Anything else says what to do and idles, so an
// empty or ambiguous project is not a restart loop.
const dotnetStart = `set -- *.runtimeconfig.json
if [ "$#" -eq 1 ] && [ -f "$1" ]; then
  exec dotnet "${1%.runtimeconfig.json}.dll"
elif [ "$#" -gt 1 ]; then
  echo "sitebin: several *.runtimeconfig.json in $(pwd) -- name the app: command: [\"dotnet\", \"YourApp.dll\"]"
else
  echo "sitebin: no *.runtimeconfig.json in $(pwd) -- upload the output of dotnet publish into the folder mounted here"
fi
exec tail -f /dev/null`

// catalog is the whole catalogue. Bump a Digest together with its Display:
// `docker buildx imagetools inspect <repo>:<tag>`.
var catalog = map[string]Image{
	"alpine-node-22": {
		Name:    "alpine-node-22",
		Display: "node:22.23.2-alpine3.24",
		Repo:    "node",
		Digest:  "sha256:b6f26b36c8ff49624cfdac716b8ea1138d606df02586a77d364bb5536a634f85",
		Cmd:     []string{"sh", "-c", nodeStart},
		Env: map[string]string{
			// The container runs as Sitebin's uid, which need not have a home
			// directory, and the root filesystem is read-only.
			"HOME":             "/tmp",
			"npm_config_cache": "/tmp/.npm",
		},
		Tmpfs: map[string]string{"/tmp": "rw,nosuid,nodev,size=256m,mode=1777"},
	},
	// The ASP.NET Core runtime (which carries the .NET runtime) on Alpine,
	// the "extra" variant: with ICU and tzdata, so cultures (de-AT dates and
	// numbers) and time zones work — the plain Alpine image runs .NET in
	// globalization-invariant mode. ASP.NET listens on 8080
	// (ASPNETCORE_HTTP_PORTS in the image), so a service declares http: [8080].
	"alpine-dotnet-10": {
		Name:    "alpine-dotnet-10",
		Display: "mcr.microsoft.com/dotnet/aspnet:10.0.9-alpine3.24-extra",
		Repo:    "mcr.microsoft.com/dotnet/aspnet",
		Digest:  "sha256:e3abedf7073227bec8a047f53692584f6646318d3cfbb8ce64e3199ee257321b",
		Cmd:     []string{"sh", "-c", dotnetStart},
		Env: map[string]string{
			// Sitebin's uid need not have a home directory, and the root
			// filesystem is read-only: ASP.NET's data-protection keys and
			// anything else under HOME land in the tmpfs (an app that must
			// keep them across restarts points them into its volume).
			"HOME": "/tmp",
			// No diagnostics IPC socket or debugger/profiler attach in a
			// customer's container.
			"DOTNET_EnableDiagnostics": "0",
		},
		Tmpfs: map[string]string{"/tmp": "rw,nosuid,nodev,size=128m,mode=1777"},
	},
	"mysql-8.4": {
		Name:    "mysql-8.4",
		Display: "mysql:8.4.11",
		Repo:    "mysql",
		Digest:  "sha256:0744ee5ef89ce6ccfa13de3e579fe6b9e27f93dd70da9c06d2c908b1b193fb8d",
		// Sized for the fixed per-container memory limit: the performance
		// schema alone wants more than a small instance has, and a 128 MB
		// buffer pool is plenty for what a site's database holds. No file
		// import or export: secure-file-priv would otherwise name a directory
		// in the read-only image.
		Cmd: []string{"mysqld", "--performance-schema=OFF", "--innodb-buffer-pool-size=128M", "--secure-file-priv=NULL"},
		Tmpfs: map[string]string{
			"/tmp":            "rw,nosuid,nodev,size=64m,mode=1777",
			"/var/run/mysqld": "rw,nosuid,nodev,size=1m,mode=1777",
		},
		DataPaths: []string{"/var/lib/mysql"},
		DataTmpfs: "rw,nosuid,nodev,size=256m,mode=1777",
	},
	// The Docker Official Image on Alpine. Its entrypoint runs as any uid
	// (nss_wrapper fakes the passwd entry initdb wants) and initialises the
	// cluster on the first start. Its defaults (128 MB shared buffers, 100
	// connections) fit the per-container memory limit as they are.
	"alpine-postgres-18": {
		Name:    "alpine-postgres-18",
		Display: "postgres:18.6-alpine3.24",
		Repo:    "postgres",
		Digest:  "sha256:123b17998ccaf47e53b502e8c6a1e79afd8f579c6f3eec83c563da026f957395",
		Cmd:     []string{"postgres"},
		Env: map[string]string{
			// musl sorts by code point ("Bär" < "Zebra" < "apfel" <
			// "Äpfel"); the ICU root collation sorts text as people expect
			// in every language. It applies when the cluster is created,
			// and a service's own value replaces it.
			"POSTGRES_INITDB_ARGS": "--locale-provider=icu --icu-locale=und",
		},
		Tmpfs: map[string]string{
			"/tmp":                "rw,nosuid,nodev,size=64m,mode=1777",
			"/var/run/postgresql": "rw,nosuid,nodev,size=1m,mode=1777",
		},
		// Since 18 the image keeps the cluster in /var/lib/postgresql/18/docker
		// (PGDATA), so the folder goes at /var/lib/postgresql.
		DataPaths:    []string{"/var/lib/postgresql"},
		DataTmpfs:    "rw,nosuid,nodev,size=256m,mode=1777",
		OldDataPaths: map[string]string{"/var/lib/postgresql/data": "/var/lib/postgresql"},
		DataPathEnv:  "PGDATA",
	},
}

// Lookup returns the catalogue entry for a compose file's image name.
func Lookup(name string) (Image, bool) {
	i, ok := catalog[name]
	return i, ok
}

// ImageNames lists the catalogue, for error messages and docs.
func ImageNames() []string {
	return []string{"alpine-node-22", "alpine-dotnet-10", "mysql-8.4", "alpine-postgres-18"}
}
