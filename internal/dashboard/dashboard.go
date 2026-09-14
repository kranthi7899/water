// Package dashboard is a local, read-only HTTP view over existing traces,
// checkpoints and role manifests (Part 3E). It is just another Surface
// consumer: it never writes, never configures, and needs no orchestrator
// changes. Every non-GET request is refused.
package dashboard

import (
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"water/internal/diagnose"
	"water/internal/identity"
	"water/internal/orchestrator"
	"water/internal/roles"
)

//go:embed templates/*.html
var templates embed.FS

// RoleView is what the roles page renders.
type RoleView struct {
	Slug         string
	Name         string
	RoleID       string
	Singleton    bool
	Orchestrator bool
	Backend      string
	Skills       []string
	Files        []identity.FileStatus
	Capability   string
	Tools        string
}

// Server serves the dashboard.
type Server struct {
	TraceDir      string
	CheckpointDir string
	Roles         []RoleView
	Edges         *orchestrator.PermissionGraph
	tmpl          *template.Template
}

// New builds a server. roles may be nil when role loading failed (the run
// pages still work).
func New(traceDir, checkpointDir string, reg *roles.Registry, fileStatus func(r *roles.Role) []identity.FileStatus, backendFor func(slug string) string) (*Server, error) {
	t, err := template.New("").Funcs(template.FuncMap{
		"ms":   func(n int64) string { return (time.Duration(n) * time.Millisecond).Round(time.Millisecond).String() },
		"pct":  func(f float64) string { return fmt.Sprintf("%.0f%%", f*100) },
		"json": func(v any) string { b, _ := json.MarshalIndent(v, "", "  "); return string(b) },
		"trunc": func(s string, n int) string {
			s = strings.Join(strings.Fields(s), " ")
			if len(s) > n {
				return s[:n] + "…"
			}
			return s
		},
	}).ParseFS(templates, "templates/*.html")
	if err != nil {
		return nil, err
	}
	s := &Server{TraceDir: traceDir, CheckpointDir: checkpointDir, tmpl: t}
	if reg != nil {
		var ceo, coo string
		var specialists []string
		for _, r := range reg.All() {
			rv := RoleView{Slug: r.Slug, Name: r.Name, RoleID: r.RoleID, Singleton: r.Singleton, Orchestrator: r.Orchestrator, Capability: r.Capability.Render(r.Slug, r.Name)}
			if backendFor != nil {
				rv.Backend = backendFor(r.Slug)
			}
			if r.Persona != nil {
				for _, sk := range r.Persona.Skills {
					rv.Skills = append(rv.Skills, sk.Slug)
				}
			}
			if fileStatus != nil {
				rv.Files = fileStatus(r)
			}
			if r.Tools != nil {
				rv.Tools = fmt.Sprintf("filesystem %s (%d roots) · shell %s · network %s", orNone(r.Tools.Filesystem.Mode), len(r.Tools.Filesystem.Roots), orNone(r.Tools.Shell.Mode), orNone(r.Tools.Network))
			} else {
				rv.Tools = "none (no tools block)"
			}
			s.Roles = append(s.Roles, rv)
			switch {
			case r.Orchestrator:
				ceo = r.Slug
			case r.Slug == "coo":
				coo = r.Slug
			default:
				specialists = append(specialists, r.Slug)
			}
		}
		if ceo != "" && coo != "" {
			s.Edges = orchestrator.Hierarchy(ceo, coo, specialists)
		}
	}
	return s, nil
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// RunInfo summarises one trace file.
type RunInfo struct {
	RunID    string
	Brief    string
	Modified time.Time
	Size     int64
	Complete bool
}

// Runs lists traces newest first.
func (s *Server) Runs() ([]RunInfo, error) {
	entries, err := os.ReadDir(s.TraceDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []RunInfo
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".jsonl")
		ri := RunInfo{RunID: id, Modified: fi.ModTime(), Size: fi.Size()}
		if evs, err := diagnose.ReadTrace(filepath.Join(s.TraceDir, e.Name())); err == nil {
			for _, ev := range evs {
				if ev.Type == "run_started" {
					ri.Brief = ev.Text
				}
				if ev.Type == "run_finished" {
					ri.Complete = true
				}
			}
		}
		out = append(out, ri)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Modified.After(out[j].Modified) })
	return out, nil
}

// Handler returns the read-only HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.index)
	mux.HandleFunc("/roles", s.rolesPage)
	mux.HandleFunc("/run/", s.runPage)
	mux.HandleFunc("/api/runs", s.apiRuns)
	mux.HandleFunc("/api/run/", s.apiRun)
	return readOnly(mux)
}

// readOnly refuses every method except GET and HEAD — there is no write path.
func readOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "read-only dashboard", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}

// Serve listens on addr (loopback only unless the user overrides) and blocks.
func (s *Server) Serve(addr string) (string, error) {
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return "", err
	}
	srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	return "http://" + ln.Addr().String(), nil
}

func (s *Server) render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	runs, err := s.Runs()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.render(w, "index.html", map[string]any{"Runs": runs, "Roles": s.Roles, "TraceDir": s.TraceDir})
}

func (s *Server) rolesPage(w http.ResponseWriter, r *http.Request) {
	var edges []orchestrator.Edge
	if s.Edges != nil {
		edges = s.Edges.Edges()
	}
	s.render(w, "roles.html", map[string]any{"Roles": s.Roles, "Edges": edges})
}

// Run assembles everything the run page shows.
type Run struct {
	RunID    string
	Report   *diagnose.Report
	Messages []orchestrator.AgentMessage
	Final    string
	Brief    string
	Calls    []callRow
	Tools    []toolRow
	Steps    []stepRow
	Prompts  []promptRow
	Snapshot *orchestrator.Snapshot
}

type callRow struct {
	Role, Backend string
	Metered       bool
	DurationMS    int64
	In, Out       int
	Error         string
}
type toolRow struct {
	Role, Tool, Args, Basis, Error string
	Allowed                        bool
	DurationMS                     int64
	Size                           int
}
type stepRow struct {
	Role       string
	DurationMS int64
	Error      string
}
type promptRow struct {
	Role                        string
	Skills, MemoryIDs, InboxIDs []string
}

// LoadRun builds the run view from disk.
func (s *Server) LoadRun(id string) (*Run, error) {
	id = filepath.Base(id)
	events, err := diagnose.ReadTrace(filepath.Join(s.TraceDir, id+".jsonl"))
	if err != nil {
		return nil, err
	}
	var snap *orchestrator.Snapshot
	if s.CheckpointDir != "" {
		snap, _ = diagnose.ReadSnapshot(filepath.Join(s.CheckpointDir, id+".json"))
	}
	run := &Run{RunID: id, Snapshot: snap}
	for _, e := range events {
		switch e.Type {
		case "run_started":
			run.Brief = e.Text
		case "message":
			if e.Message != nil {
				run.Messages = append(run.Messages, *e.Message)
			}
		case "backend_call":
			m := e.Metered != nil && *e.Metered
			run.Calls = append(run.Calls, callRow{e.Role, e.Backend, m, e.DurationMS, e.InTokens, e.OutTokens, e.Error})
		case "tool_call":
			if e.Tool != nil {
				a, _ := json.Marshal(e.Tool.Args)
				run.Tools = append(run.Tools, toolRow{e.Role, e.Tool.Tool, string(a), e.Tool.Basis, e.Tool.Error, e.Tool.Allowed, e.Tool.DurationMS, e.Tool.ResultSize})
			}
		case "node_finished":
			run.Steps = append(run.Steps, stepRow{e.Role, e.DurationMS, e.Error})
		case "prompt_assembled":
			run.Prompts = append(run.Prompts, promptRow{e.Role, e.Skills, e.MemoryIDs, e.InboxIDs})
		}
	}
	if snap != nil && snap.FinalOutput != nil {
		run.Final = *snap.FinalOutput
	}
	run.Report = diagnose.Analyze(id, events, snap, s.Edges)
	return run, nil
}

func (s *Server) runPage(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/run/")
	run, err := s.LoadRun(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	s.render(w, "run.html", run)
}

func (s *Server) apiRuns(w http.ResponseWriter, r *http.Request) {
	runs, err := s.Runs()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(runs)
}

func (s *Server) apiRun(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/run/")
	run, err := s.LoadRun(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"run_id": run.RunID, "brief": run.Brief, "report": run.Report, "messages": run.Messages, "calls": run.Calls, "tools": run.Tools})
}
