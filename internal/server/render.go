package server

import (
	"embed"
	"html/template"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"example.invalid/hearthlog/internal/model"
)

//go:embed web/*
var embedFS embed.FS

var tmpl = template.Must(template.New("root").Funcs(template.FuncMap{
	"first": func(ss []string) string {
		if len(ss) == 0 {
			return ""
		}
		return ss[0]
	},
	"add": func(a, b int) int { return a + b },
	"formatUTC": func(s, layout string) string {
		t, err := time.Parse(time.RFC3339Nano, s)
		if err != nil {
			return s
		}
		return t.UTC().Format(layout)
	},
	"srcLabel": func(sources []sourceInfo, id string) string {
		for _, s := range sources {
			if s.ID == id {
				return s.Service + " (" + s.Environment + ")"
			}
		}
		return id
	},
	"hasSource": func(selected []string, id string) bool {
		if len(selected) == 0 {
			return false
		}
		for _, s := range selected {
			if s == id {
				return true
			}
		}
		return false
	},
}).ParseFS(embedFS, "web/*.html"))

type sourceInfo struct {
	ID          string
	Service     string
	Environment string
}

type savedInfo struct {
	Name   string
	Preset string
}

type view struct {
	CSRF            string
	Query           model.Query
	Rows            []model.Record
	NextCursor      string
	PageCursor      string
	Detail          *model.Record
	Attrs           []attrNode
	Status          string
	Code            string
	Generation      string
	Sequence        string
	Selection       string
	OriginID        string
	Sources         []sourceInfo
	SourcesOK       bool
	Saved           []savedInfo
	Preset          string
	TermFields      []string
	PredFields      []predInput
	Window          string
	Focus           string
	NoEvents        bool
	SuppressResults bool
	Partial         bool
	LiveStatus      string
	LiveState       string
	LiveReceived    string
	LiveIngest      string
	LiveIdle        bool
}

// beforeRender runs at the start of render when a test installs it.
var beforeRender atomic.Value

func (s *Server) render(w http.ResponseWriter, r *http.Request, kind string, v view, status int) {
	if fn, ok := beforeRender.Load().(func()); ok && fn != nil {
		fn()
	}
	name := "page"
	if r.Header.Get("HX-Request") == "true" {
		name = kind
	}

	var bw boundedWriter
	if err := tmpl.ExecuteTemplate(&bw, name, v); err != nil {
		if bw.overflow {
			safeError(w, http.StatusUnprocessableEntity, "query_too_broad", "Result too large", false)
		} else {
			safeError(w, http.StatusInternalServerError, "internal", "Render failed", false)
		}
		return
	}
	if bw.overflow {
		safeError(w, http.StatusUnprocessableEntity, "query_too_broad", "Result too large", false)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Fragment-Kind", name)
	w.Header().Set("X-Generation", v.Generation)
	w.Header().Set("X-Sequence", v.Sequence)
	w.Header().Set("X-Selection", v.Selection)
	w.Header().Set("X-Origin", v.OriginID)
	w.WriteHeader(status)
	w.Write(bw.Bytes())
}

func (s *Server) assets(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/assets/")
	if path == "" || strings.Contains(path, "..") {
		http.NotFound(w, r)
		return
	}

	mime := ""
	switch path {
	case "htmx.min.js", "app.js":
		mime = "application/javascript"
	case "style.css":
		mime = "text/css"
	case "HTMX-LICENSE.txt":
		mime = "text/plain"
	default:
		http.NotFound(w, r)
		return
	}

	data, err := embedFS.ReadFile("web/" + path)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", mime)
	w.Write(data)
}
