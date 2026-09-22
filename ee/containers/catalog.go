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
}

// Lookup returns the catalogue entry for a compose file's image name.
func Lookup(name string) (Image, bool) {
	i, ok := catalog[name]
	return i, ok
}

// ImageNames lists the catalogue, for error messages and docs.
func ImageNames() []string { return []string{"alpine-node-22", "mysql-8.4"} }
