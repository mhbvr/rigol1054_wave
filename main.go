// Command rigol1054_wave downloads the full sample memory of a Rigol DS1054Z
// (DS1000Z series) over the network and shows it in a web browser.
package main

import (
	_ "embed"
	"encoding/json"
	"flag"
	"html/template"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

//go:embed index.html
var indexHTML string

var tmpl = template.Must(template.New("index").Funcs(template.FuncMap{"si": si}).Parse(indexHTML))

type server struct {
	mu    sync.Mutex // serializes scope access and guards the fields
	scope *Scope
	addr  string
	idn   string
	cap   *Capture
	msg   string // last error, shown once
}

// Plot metadata passed to the page script.
type meta struct {
	Points            int
	XInc, XOrig, XRef float64
	Channels          []chMeta
}

type chMeta struct {
	Name, Unit    string
	Scale, Offset float64
}

func (s *server) index(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	data := map[string]any{
		"Addr":      s.addr,
		"Connected": s.scope != nil,
		"IDN":       s.idn,
		"Msg":       s.msg,
		"Cap":       s.cap,
	}
	if c := s.cap; c != nil {
		m := meta{Points: c.Points, XInc: c.XInc, XOrig: c.XOrig, XRef: c.XRef}
		for _, ch := range c.Channels {
			m.Channels = append(m.Channels, chMeta{ch.Name, ch.Unit, ch.Scale, ch.Offset})
		}
		data["Meta"] = m
	}
	s.msg = ""
	s.mu.Unlock()
	if err := tmpl.Execute(w, data); err != nil {
		log.Print(err)
	}
}

func (s *server) connect(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	defer http.Redirect(w, r, "/", http.StatusSeeOther)
	s.closeScope()
	s.addr = strings.TrimSpace(r.FormValue("addr"))
	sc, err := Dial(s.addr)
	if err != nil {
		s.msg = err.Error()
		return
	}
	idn, err := sc.Query("*IDN?")
	if err != nil {
		sc.Close()
		s.msg = err.Error()
		return
	}
	s.scope, s.idn = sc, idn
}

func (s *server) capture(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	defer http.Redirect(w, r, "/", http.StatusSeeOther)
	if s.scope == nil {
		s.msg = "not connected"
		return
	}
	c, err := s.scope.Capture(r.FormValue("mode") == "screen")
	if err != nil {
		// The connection may be out of sync after an error; start over.
		s.closeScope()
		s.msg = "capture failed: " + err.Error() + " (reconnect to retry)"
		return
	}
	s.cap = c
}

func (s *server) closeScope() {
	if s.scope != nil {
		s.scope.Close()
		s.scope, s.idn = nil, ""
	}
}

// data returns a decimated part of the capture: /data?start=&end=&n=
func (s *server) data(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	c := s.cap
	s.mu.Unlock()
	if c == nil {
		http.Error(w, "no capture", http.StatusNotFound)
		return
	}
	q := r.URL.Query()
	start, _ := strconv.Atoi(q.Get("start"))
	end, _ := strconv.Atoi(q.Get("end"))
	n, _ := strconv.Atoi(q.Get("n"))
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(c.View(start, end, n))
}

// csv sends samples as a CSV file: /csv for the full record,
// /csv?start=&end= for a part of it.
func (s *server) csv(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	c := s.cap
	s.mu.Unlock()
	if c == nil {
		http.Error(w, "no capture", http.StatusNotFound)
		return
	}
	start, end := 0, c.Points
	if v, err := strconv.Atoi(r.URL.Query().Get("start")); err == nil {
		start = v
	}
	if v, err := strconv.Atoi(r.URL.Query().Get("end")); err == nil {
		end = v
	}
	name := "rigol_" + c.Time.Format("20060102_150405") + ".csv"
	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	if err := c.WriteCSV(w, start, end); err != nil {
		log.Print(err)
	}
}

func main() {
	listen := flag.String("listen", "localhost:8080", "HTTP listen address")
	flag.Parse()

	s := &server{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.index)
	mux.HandleFunc("POST /connect", s.connect)
	mux.HandleFunc("POST /capture", s.capture)
	mux.HandleFunc("GET /data", s.data)
	mux.HandleFunc("GET /csv", s.csv)
	log.Printf("listening on http://%s", *listen)
	log.Fatal(http.ListenAndServe(*listen, mux))
}
