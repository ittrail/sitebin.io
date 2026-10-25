//go:build ee

package ee

import (
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"maps"
	"math"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ittrail/sitebin.io/ee/containers"
	"github.com/ittrail/sitebin.io/internal/ext"
	"github.com/ittrail/sitebin.io/internal/store"
)

// The Containers tab of the instance register: the memory, CPU and process
// limits of every customer container, the instance's defaults and each
// service's own. The runtime converges running containers to them in place.
// Server-rendered, like the rest of the console (script-src 'none'). See
// docs/superpowers/specs/2026-10-25-container-limits-admin-design.md.

// limitCell is one effective limit as a row shows it.
type limitCell struct {
	Value string
	Own   bool // the service's own value, not the default
}

type containerServiceRow struct {
	Name, Image, State string
	Memory, CPUs, Pids limitCell
	OwnMemory, OwnCPUs string // the form's values: the service's own, "" where none
	OwnPids            string
	HasOwn             bool
	// Updating is what the container runs with while it differs from what
	// it should (the runtime changes it within seconds); empty when equal.
	Updating string
	// Missing marks an own entry for a service the project no longer has.
	Missing bool
	// Usage is what the running container uses, as one line; empty when
	// there is no recent measurement.
	Usage string
}

type containerProject struct {
	ViewID, Name, Owner string
	Status, Message     string
	UpdateErr           string
	Services            []containerServiceRow
	// Web is the project's web traffic when several of its services serve
	// HTTP and it cannot be given to one of them.
	Web string
}

type containersView struct {
	Email, CSRF, Flash string
	Reports            int

	// The defaults: in effect, the admin's saved ones (form values), the
	// environment's (placeholders).
	Defaults                          string
	SavedMemory, SavedCPUs, SavedPids string
	EnvMemory, EnvCPUs, EnvPids       string
	HasSaved                          bool
	DefaultsErr                       string

	Host                    string
	Projects, Running       int
	MemReserved, CPUReserve string
	Rows                    []containerProject

	// What the running containers use, summed: memory and CPU against the
	// host, public traffic in and out (the projects' web traffic once).
	MemUsed, CPUUsed      string
	TrafficIn, TrafficOut string
	// AsOf is when the newest measurement shown was taken; Live reloads
	// the page every 10 s (the console runs without JavaScript).
	AsOf string
	Live bool
}

// rateText is bytes per second for people; "—" when not measured yet.
func rateText(v *float64) string {
	if v == nil {
		return "—"
	}
	return humanBytes(int64(math.Round(*v))) + "/s"
}

func coresText(v *float64) string {
	if v == nil {
		return "—"
	}
	return cpuText(math.Round(*v*100) / 100)
}

// usageText is one container's usage as a row shows it, in the edit page's
// words.
func usageText(u ext.ServiceUsage) string {
	out := "RAM " + humanBytes(u.MemoryBytes)
	if u.MemoryLimitBytes > 0 {
		out += " / " + humanBytes(u.MemoryLimitBytes)
	}
	out += " · CPU " + coresText(u.CPU)
	if u.CPULimit > 0 {
		out += " / " + cpuText(u.CPULimit)
	}
	out += " · " + strconv.Itoa(u.Pids)
	if u.PidsLimit > 0 {
		out += " / " + strconv.Itoa(u.PidsLimit)
	}
	return out + " processes · ↓ " + rateText(u.In) + " ↑ " + rateText(u.Out)
}

func memText(mb int) string    { return strconv.Itoa(mb) + " MB" }
func cpuText(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }
func limitsText(l ext.ContainerLimits) string {
	return memText(l.MemoryMB) + " · " + cpuText(l.CPUs) + " CPU · " + strconv.Itoa(l.Pids) + " processes"
}

// formValue is a limit field as an input's value: empty when not set.
func formValue[T int | float64](v T) string {
	if v <= 0 {
		return ""
	}
	return strings.TrimSuffix(fmt.Sprint(v), ".0")
}

func (p *provider) handleAdminContainers(w http.ResponseWriter, r *http.Request) {
	acc, ok := p.adminAccount(r)
	if !ok || p.containerLimits == nil {
		http.NotFound(w, r)
		return
	}
	sites, err := p.host.Sites().ContainerSites()
	if err != nil {
		slog.Error("admin console could not list the container sites", "admin", acc.ID, "err", err)
		http.Error(w, "could not read the instance's container sites", http.StatusInternalServerError)
		return
	}
	book := p.containerLimits
	env, saved := book.Env(), book.Saved()
	v := containersView{
		Email: acc.Email, CSRF: p.csrf(acc), Flash: r.URL.Query().Get("flash"),
		Defaults:    limitsText(book.Defaults()),
		SavedMemory: formValue(saved.MemoryMB), SavedCPUs: formValue(saved.CPUs), SavedPids: formValue(saved.Pids),
		EnvMemory: strconv.Itoa(env.MemoryMB), EnvCPUs: cpuText(env.CPUs), EnvPids: strconv.Itoa(env.Pids),
		HasSaved: saved != (ext.ContainerLimits{}),
		Projects: len(sites),
	}
	if err := book.Err(); err != nil {
		v.DefaultsErr = err.Error()
	}
	if reps, err := p.host.Sites().Reports(); err == nil {
		v.Reports = len(reps)
	}
	cpus, mem := book.Capacity()
	if cpus > 0 {
		v.Host = fmt.Sprintf("%d CPUs · %s", cpus, humanBytes(mem))
	}

	emails := map[string]string{}
	var memMB int
	var cpuSum float64
	var usedMem int64
	var usedCPU, trafficIn, trafficOut float64
	var asOf time.Time
	for _, cs := range sites {
		var usage map[string]ext.ServiceUsage
		var webText string
		if p.containerUsage != nil {
			if u, ok := p.containerUsage(cs.ViewID); ok {
				usage = map[string]ext.ServiceUsage{}
				for _, s := range u.Services {
					usage[s.Name] = s
					usedMem += s.MemoryBytes
					if s.CPU != nil {
						usedCPU += *s.CPU
					}
					if s.In != nil && s.Out != nil {
						trafficIn += *s.In
						trafficOut += *s.Out
					}
				}
				if u.Web != nil {
					trafficIn += u.Web.In
					trafficOut += u.Web.Out
					webText = "↓ " + rateText(&u.Web.In) + " ↑ " + rateText(&u.Web.Out)
				}
				if u.At.After(asOf) {
					asOf = u.At
				}
			}
		}
		owner, done := emails[cs.Owner]
		if !done {
			owner = cs.Owner
			if acc, err := p.accounts.ByID(cs.Owner); err == nil {
				owner = acc.Email
			}
			emails[cs.Owner] = owner
		}
		row := containerProject{ViewID: cs.ViewID, Name: cs.Name, Owner: owner, Status: cs.Observed.Status, Message: cs.Observed.Message, Web: webText}
		if !cs.Enabled && row.Status == "" {
			row.Status = store.ContainerStopped
		}
		if p.containers != nil {
			row.UpdateErr = p.containers.UpdateError(cs.ViewID)
		}
		running := cs.Observed.Status == store.ContainerRunning
		services := projectServices(cs)
		for _, sv := range services {
			eff := book.For(cs.Limits, sv.Name)
			own := cs.Limits[sv.Name]
			sr := containerServiceRow{
				Name: sv.Name, Image: sv.Image, State: sv.State,
				Memory:    limitCell{memText(eff.MemoryMB), own.MemoryMB > 0},
				CPUs:      limitCell{cpuText(eff.CPUs), own.CPUs > 0},
				Pids:      limitCell{strconv.Itoa(eff.Pids), own.Pids > 0},
				OwnMemory: formValue(own.MemoryMB), OwnCPUs: formValue(own.CPUs), OwnPids: formValue(own.Pids),
				HasOwn: own != (ext.ContainerLimits{}),
			}
			if u, ok := usage[sv.Name]; ok {
				sr.Usage = usageText(u)
			}
			if running {
				v.Running++
				memMB += eff.MemoryMB
				cpuSum += eff.CPUs
				if sv.Limits != eff && book.Known() {
					sr.Updating = "not recorded yet"
					if sv.Limits != (ext.ContainerLimits{}) {
						sr.Updating = "runs with " + limitsText(sv.Limits)
					}
				}
			}
			row.Services = append(row.Services, sr)
		}
		for _, name := range slices.Sorted(maps.Keys(cs.Limits)) {
			if !hasService(services, name) {
				row.Services = append(row.Services, containerServiceRow{Name: name, Missing: true, HasOwn: true})
			}
		}
		v.Rows = append(v.Rows, row)
	}
	sort.Slice(v.Rows, func(a, b int) bool {
		if v.Rows[a].Owner != v.Rows[b].Owner {
			return v.Rows[a].Owner < v.Rows[b].Owner
		}
		return v.Rows[a].ViewID < v.Rows[b].ViewID
	})
	v.MemReserved = humanBytes(int64(memMB) << 20)
	if mem > 0 {
		v.MemReserved += " of " + humanBytes(mem)
	}
	v.CPUReserve = cpuText(math.Round(cpuSum*100) / 100)
	if cpus > 0 {
		v.CPUReserve += " of " + strconv.Itoa(cpus)
	}
	v.MemUsed = humanBytes(usedMem)
	if mem > 0 {
		v.MemUsed += " of " + humanBytes(mem)
	}
	v.CPUUsed = coresText(&usedCPU)
	if cpus > 0 {
		v.CPUUsed += " of " + strconv.Itoa(cpus)
	}
	v.TrafficIn, v.TrafficOut = rateText(&trafficIn), rateText(&trafficOut)
	if !asOf.IsZero() {
		v.AsOf = asOf.Local().Format("15:04:05")
	}
	v.Live = r.URL.Query().Get("live") == "1"
	if v.Live {
		w.Header().Set("Refresh", "10")
	}
	p.securityHeaders(w)
	containersTmpl.Execute(w, v)
}

// projectServices are the services a project has: its compose file's when
// the file parses — a project whose last start failed has recorded none —
// with what was last recorded of each, else the ones last recorded.
func projectServices(cs ext.ContainerSite) []ext.ContainerService {
	spec, err := containers.Parse(cs.Compose)
	if cs.Compose == nil || err != nil {
		return cs.Observed.Services
	}
	out := make([]ext.ContainerService, 0, len(spec.Services))
	for _, sv := range spec.Services {
		d := ext.ContainerService{Name: sv.Name, Image: sv.Image.Name}
		for _, o := range cs.Observed.Services {
			if o.Name == sv.Name {
				d.State, d.Limits = o.State, o.Limits
			}
		}
		out = append(out, d)
	}
	return out
}

func hasService(services []ext.ContainerService, name string) bool {
	return slices.ContainsFunc(services, func(sv ext.ContainerService) bool { return sv.Name == name })
}

// parseLimitsForm reads memory_mb, cpus and pids. An empty field is not set
// (the default applies); anything else must be a positive number. CPUs take
// a decimal comma and are rounded to hundredths. The bounds are the book's
// Check.
func parseLimitsForm(r *http.Request) (ext.ContainerLimits, error) {
	var l ext.ContainerLimits
	if v := strings.TrimSpace(r.PostFormValue("memory_mb")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return l, errors.New("memory: a whole number of megabytes, e.g. 1024")
		}
		l.MemoryMB = n
	}
	if v := strings.TrimSpace(r.PostFormValue("cpus")); v != "" {
		f, err := strconv.ParseFloat(strings.Replace(v, ",", ".", 1), 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || f <= 0 {
			return l, errors.New("CPUs: a number of CPUs, e.g. 0.5")
		}
		if l.CPUs = math.Round(f*100) / 100; l.CPUs == 0 {
			return l, errors.New("CPUs: at least 0.01")
		}
	}
	if v := strings.TrimSpace(r.PostFormValue("pids")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return l, errors.New("processes: a whole number, e.g. 256")
		}
		l.Pids = n
	}
	return l, nil
}

// kickContainers asks the runtime to converge the sites now rather than at
// their next tick.
func (p *provider) kickContainers(ids ...string) {
	if p.containers == nil {
		return
	}
	for _, id := range ids {
		p.containers.Kick(id)
	}
}

// handleAdminContainerDefaults saves the instance's default limits, or
// returns them to the environment's (reset). Every running container without
// its own value for a field follows.
func (p *provider) handleAdminContainerDefaults(w http.ResponseWriter, r *http.Request) {
	acc, ok := p.adminAccount(r)
	if !ok || p.containerLimits == nil {
		http.NotFound(w, r)
		return
	}
	if !p.checkCSRF(r, acc) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var l ext.ContainerLimits
	if r.PostFormValue("reset") == "" {
		var err error
		if l, err = parseLimitsForm(r); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	if err := p.containerLimits.Check(l); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := p.containerLimits.SetDefaults(l); err != nil {
		slog.Error("admin could not save the container defaults", "admin", acc.ID, "err", err)
		http.Error(w, "could not save the defaults", http.StatusInternalServerError)
		return
	}
	slog.Info("admin set container defaults", "admin", acc.ID, "memory_mb", l.MemoryMB, "cpus", l.CPUs, "pids", l.Pids)
	if sites, err := p.host.Sites().ContainerSites(); err == nil {
		for _, cs := range sites {
			p.kickContainers(cs.ViewID)
		}
	}
	p.redirect(w, r, "/account/admin/containers?flash=defaults")
}

// handleAdminContainerLimits sets one service's own limits, or clears them
// (clear, or every field empty): the defaults apply to it again.
func (p *provider) handleAdminContainerLimits(w http.ResponseWriter, r *http.Request) {
	acc, ok := p.adminAccount(r)
	if !ok || p.containerLimits == nil {
		http.NotFound(w, r)
		return
	}
	if !p.checkCSRF(r, acc) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	viewID := r.PathValue("id")
	cs, err := p.host.Sites().ContainerSite(viewID)
	if errors.Is(err, ext.ErrSiteGone) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		slog.Error("admin console could not read a container site", "admin", acc.ID, "site", viewID, "err", err)
		http.Error(w, "could not read the site", http.StatusInternalServerError)
		return
	}
	service := r.PostFormValue("service")
	_, hasOwn := cs.Limits[service]
	if !hasOwn && !hasService(projectServices(cs), service) {
		http.Error(w, fmt.Sprintf("the project has no service %q", service), http.StatusBadRequest)
		return
	}
	var l ext.ContainerLimits
	if r.PostFormValue("clear") == "" {
		if l, err = parseLimitsForm(r); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	if err := p.containerLimits.Check(l); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	next := maps.Clone(cs.Limits)
	if next == nil {
		next = map[string]ext.ContainerLimits{}
	}
	if l == (ext.ContainerLimits{}) {
		delete(next, service)
	} else {
		next[service] = l
	}
	if err := p.host.Sites().SetContainerLimits(viewID, next); err != nil {
		if errors.Is(err, ext.ErrSiteGone) {
			http.NotFound(w, r)
			return
		}
		slog.Error("admin could not set a container's limits", "admin", acc.ID, "site", viewID, "service", service, "err", err)
		http.Error(w, "could not save the limits", http.StatusInternalServerError)
		return
	}
	slog.Info("admin set container limits", "admin", acc.ID, "site", viewID, "service", service,
		"memory_mb", l.MemoryMB, "cpus", l.CPUs, "pids", l.Pids)
	p.kickContainers(viewID)
	p.redirect(w, r, "/account/admin/containers?flash=limits")
}

// containersCSS is the tab's own layout, on the console's.
const containersCSS = `
<style>
  .adm h2 { font: 650 17px var(--display); margin: 4px 0 8px; letter-spacing: -.01em; }
  .adm .defs { border: 1px dashed var(--line); border-radius: var(--radius); padding: 16px 18px; margin-bottom: 22px; background: linear-gradient(160deg, #151e33, #101727); }
  .adm .defs .lede { margin-bottom: 12px; }
  .adm .defs .now { font: 12px var(--mono); color: var(--ink-dim); margin-top: 10px; }
  .adm .defs .now b { color: var(--ink); font-weight: 600; }
  .adm .warnbox { border: 1px dashed rgba(242,109,109,.6); color: var(--danger); border-radius: 9px; padding: 9px 13px; font: 12px var(--mono); margin-bottom: 12px; overflow-wrap: anywhere; }
  .adm form.lim { display: flex; flex-wrap: wrap; gap: 10px; align-items: end; }
  .adm form.lim label { display: flex; flex-direction: column; gap: 4px; font: 600 10px var(--mono); letter-spacing: .12em; text-transform: uppercase; color: var(--ink-faint); }
  .adm input.num { background: var(--bg-raise); color: var(--ink); border: 1px solid var(--line); border-radius: 7px; padding: 6px 8px; font: 12px var(--mono); width: 104px; }
  .adm .proj { border-top: 1px solid var(--line-soft); padding: 12px 16px; }
  .adm .proj:first-of-type { border-top: 0; }
  .adm .phead { display: flex; flex-wrap: wrap; gap: 6px 14px; align-items: baseline; font-size: 13px; margin-bottom: 8px; }
  .adm .phead .nm { font-weight: 650; color: var(--ink); }
  .adm .phead .vid { font: 12px var(--mono); }
  .adm .phead .own { font-size: 12px; color: var(--ink-dim); }
  .adm .phead .st { font: 600 10px var(--mono); letter-spacing: .1em; text-transform: uppercase; padding: 1px 6px; border-radius: 4px; border: 1px solid var(--line); color: var(--ink-dim); }
  .adm .phead .st.running { border-color: rgba(94,207,138,.6); color: var(--ok); }
  .adm .phead .st.error { border-color: var(--danger); color: var(--danger); }
  .adm .phead .msg { font: 11px var(--mono); color: var(--ink-faint); overflow-wrap: anywhere; }
  .adm .phead .err { font: 11px var(--mono); color: var(--danger); overflow-wrap: anywhere; }
  .adm form.svc { display: grid; grid-template-columns: minmax(150px,1fr) minmax(230px,1.4fr) auto; gap: 8px 14px; align-items: center; padding: 6px 0; border-top: 1px dashed var(--line-soft); }
  .adm form.svc:first-of-type { border-top: 0; }
  .adm .sn { font: 600 13px var(--mono); color: var(--ink); }
  .adm .sn .img { display: block; font: 11px var(--mono); color: var(--ink-faint); font-weight: 400; }
  .adm .eff { font: 12px var(--mono); color: var(--ink-dim); }
  .adm .eff .o { color: var(--amber); }
  .adm .eff .o::after { content: " own"; font-size: 10px; letter-spacing: .06em; }
  .adm .eff .upd { display: block; font-size: 11px; color: var(--amber); }
  .adm .eff .use { display: block; font-size: 11px; color: var(--ink-faint); margin-top: 2px; }
  .adm .phead .web { font: 11px var(--mono); color: var(--ink-dim); }
  .adm .asof { font: 12px var(--mono); color: var(--ink-faint); margin: -12px 0 22px; }
  .adm .asof a { color: var(--ink-dim); }
  .adm .eff .gone { color: var(--ink-faint); font-style: italic; }
  .adm .svc .acts { display: flex; gap: 6px; align-items: center; flex-wrap: wrap; justify-content: flex-end; }
  @media (max-width: 900px) { .adm form.svc { grid-template-columns: 1fr; } .adm .svc .acts { justify-content: flex-start; } }
</style>`

var containersTmpl = template.Must(template.New("containers").Parse(pageHead + adminConsoleCSS + containersCSS + `
<main class="adm">
  <h1>Containers</h1>
  <p class="lede">The memory, CPU and process limits of every customer container. Signed in as {{.Email}} · <a href="/account">back to your account</a></p>
  <nav class="tabs"><a href="/account/admin">Sites</a><a href="/account/admin/reports">Reports{{if .Reports}} ({{.Reports}}){{end}}</a><a class="on" href="/account/admin/containers">Containers</a></nav>

  {{if eq .Flash "defaults"}}<p class="flash">Defaults saved: running containers without their own value follow within seconds, without a restart.</p>{{end}}
  {{if eq .Flash "limits"}}<p class="flash">Limits saved: the container follows within seconds, without a restart.</p>{{end}}

  <section class="figures">
    <div class="fig"><span class="k">Projects</span><span class="v">{{.Projects}}</span></div>
    <div class="fig"><span class="k">Running containers</span><span class="v">{{.Running}}</span></div>
    <div class="fig"><span class="k">Memory limits</span><span class="v">{{.MemReserved}}</span></div>
    <div class="fig"><span class="k">CPU limits</span><span class="v">{{.CPUReserve}}</span></div>
    <div class="fig"><span class="k">Memory in use</span><span class="v">{{.MemUsed}}</span></div>
    <div class="fig"><span class="k">CPU in use</span><span class="v">{{.CPUUsed}}</span></div>
    <div class="fig"><span class="k">Public in</span><span class="v">{{.TrafficIn}}</span></div>
    <div class="fig"><span class="k">Public out</span><span class="v">{{.TrafficOut}}</span></div>
  </section>
  <p class="asof">{{if .AsOf}}Usage as of {{.AsOf}}{{else}}No usage measured yet{{end}} · public traffic is the visitors' and the internet's, not what a project's services exchange ·
    {{if .Live}}<a href="/account/admin/containers">Stop live</a> (reloads every 10 s; stop it before you edit){{else}}<a href="/account/admin/containers?live=1">Live</a> (reload every 10 s){{end}}</p>

  <section class="defs">
    <h2>Instance defaults</h2>
    <p class="lede">Every container gets these unless it has its own value below. Saving changes running containers within seconds, in place: the apps are not restarted. Memory set below what a process already uses gets it stopped by the kernel, and Docker starts it again. An empty field is the environment's value (SITEBIN_CONTAINER_*).</p>
    {{if .DefaultsErr}}<p class="warnbox">The saved defaults could not be read ({{.DefaultsErr}}). New containers get the environment's values; running ones keep theirs until you save defaults here.</p>{{end}}
    <form method="post" action="/account/admin/containers/defaults" class="lim">
      <input type="hidden" name="csrf" value="{{.CSRF}}">
      <label>Memory (MB)<input class="num" type="text" inputmode="numeric" name="memory_mb" value="{{.SavedMemory}}" placeholder="{{.EnvMemory}}"></label>
      <label>CPUs<input class="num" type="text" inputmode="decimal" name="cpus" value="{{.SavedCPUs}}" placeholder="{{.EnvCPUs}}"></label>
      <label>Processes<input class="num" type="text" inputmode="numeric" name="pids" value="{{.SavedPids}}" placeholder="{{.EnvPids}}"></label>
      <button class="btn small" type="submit">Save defaults</button>
      {{if .HasSaved}}<button class="btn small" type="submit" name="reset" value="1">Reset to environment</button>{{end}}
    </form>
    <p class="now">In effect: <b>{{.Defaults}}</b>{{if .Host}} · host: {{.Host}}{{end}}</p>
  </section>

  <section class="reg">
    {{range $p := .Rows}}
    <div class="proj">
      <div class="phead">
        {{if $p.Name}}<span class="nm">{{$p.Name}}</span>{{end}}<a class="vid" href="/account/admin?q={{$p.ViewID}}">{{$p.ViewID}}</a>
        <span class="own">{{$p.Owner}}</span>
        <span class="st {{$p.Status}}">{{$p.Status}}</span>
        {{if $p.Message}}<span class="msg">{{$p.Message}}</span>{{end}}
        {{if $p.UpdateErr}}<span class="err">&#9888; {{$p.UpdateErr}}</span>{{end}}
        {{if $p.Web}}<span class="web">web traffic of the project (several services serve HTTP): {{$p.Web}}</span>{{end}}
      </div>
      {{range $p.Services}}
      <form method="post" action="/account/admin/containers/sites/{{$p.ViewID}}/limits" class="svc">
        <input type="hidden" name="csrf" value="{{$.CSRF}}">
        <input type="hidden" name="service" value="{{.Name}}">
        <span class="sn">{{.Name}}{{if .Image}}<span class="img">{{.Image}}{{if .State}} · {{.State}}{{end}}</span>{{end}}</span>
        {{if .Missing}}
        <span class="eff"><span class="gone">has its own limits but is not in the project</span></span>
        <span class="acts"><button class="btn small" type="submit" name="clear" value="1">Remove</button></span>
        {{else}}
        <span class="eff"><span{{if .Memory.Own}} class="o"{{end}}>{{.Memory.Value}}</span> · <span{{if .CPUs.Own}} class="o"{{end}}>{{.CPUs.Value}} CPU</span> · <span{{if .Pids.Own}} class="o"{{end}}>{{.Pids.Value}} processes</span>{{if .Updating}}<span class="upd">updating — {{.Updating}}</span>{{end}}{{if .Usage}}<span class="use">{{.Usage}}</span>{{end}}</span>
        <span class="acts">
          <input class="num" type="text" inputmode="numeric" name="memory_mb" value="{{.OwnMemory}}" placeholder="MB" aria-label="Memory of {{.Name}} in MB">
          <input class="num" type="text" inputmode="decimal" name="cpus" value="{{.OwnCPUs}}" placeholder="CPUs" aria-label="CPUs of {{.Name}}">
          <input class="num" type="text" inputmode="numeric" name="pids" value="{{.OwnPids}}" placeholder="processes" aria-label="Processes of {{.Name}}">
          <button class="btn small" type="submit">Save</button>
          {{if .HasOwn}}<button class="btn small" type="submit" name="clear" value="1" title="Back to the defaults">Clear</button>{{end}}
        </span>
        {{end}}
      </form>
      {{else}}
      <p class="lede">No services recorded: the project has not started yet.</p>
      {{end}}
    </div>
    {{else}}
    <p class="empty">No container sites on this instance.</p>
    {{end}}
  </section>
</main>
` + pageFoot))
