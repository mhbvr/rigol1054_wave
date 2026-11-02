package main

import (
	"bufio"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"
)

// Max number of points per :WAV:DATA? read in BYTE format (DS1000Z limit).
const chunkSize = 250000

// Param is a named, human readable setting of the capture.
type Param struct{ Name, Value string }

// Preamble is the parsed answer of :WAVeform:PREamble?.
type Preamble struct {
	Points            int
	XInc, XOrig, XRef float64
	YInc, YOrig, YRef float64
}

type Channel struct {
	Name   string  // "CH1".."CH4"
	Unit   string  // "V", "A", "W" or "U"
	Scale  float64 // units/div
	Offset float64 // units
	Params []Param
	Pre    Preamble
	Data   []byte // raw ADC samples
}

// Value converts a raw sample to the channel unit.
func (ch *Channel) Value(b byte) float64 {
	return (float64(b) - ch.Pre.YOrig - ch.Pre.YRef) * ch.Pre.YInc
}

// Capture is a full memory download of all displayed channels.
type Capture struct {
	Time       time.Time
	IDN        string
	SampleRate float64 // Sa/s
	Points     int     // samples per channel
	XInc       float64 // s between samples
	XOrig      float64 // time of first sample relative to trigger
	XRef       float64
	Timebase   []Param
	Acquire    []Param
	Trigger    []Param
	Channels   []*Channel
}

// Duration is the length of the record in seconds.
func (c *Capture) Duration() float64 { return float64(c.Points) * c.XInc }

// reader runs a sequence of scope queries and keeps the first error, so the
// many settings queries below stay readable.
type reader struct {
	s   *Scope
	err error
}

func (r *reader) str(q string) string {
	if r.err != nil {
		return ""
	}
	v, err := r.s.Query(q)
	r.err = err
	return v
}

func (r *reader) num(q string) float64 {
	v := r.str(q)
	if r.err != nil {
		return 0
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		r.err = fmt.Errorf("%s: bad number %q", q, v)
	}
	return f
}

func (r *reader) cmd(c string) {
	if r.err == nil {
		r.err = r.s.Command(c)
	}
}

func onOff(v string) string {
	if v == "1" {
		return "ON"
	}
	if v == "0" {
		return "OFF"
	}
	return v
}

var unitNames = map[string]string{"VOLT": "V", "AMP": "A", "WATT": "W", "UNKN": "U"}

// Capture stops the scope, reads the settings and downloads the whole sample
// memory of every displayed analog channel. The scope stays stopped so the
// screen shows the same data.
func (s *Scope) Capture() (*Capture, error) {
	r := &reader{s: s}
	c := &Capture{Time: time.Now()}
	c.IDN = r.str("*IDN?")
	status := r.str(":TRIGger:STATus?") // before :STOP changes it
	r.cmd(":STOP")

	for n := 1; n <= 4; n++ {
		p := fmt.Sprintf(":CHANnel%d:", n)
		if r.str(p+"DISPlay?") != "1" {
			continue
		}
		ch := &Channel{Name: fmt.Sprintf("CH%d", n)}
		ch.Unit = unitNames[r.str(p+"UNITs?")]
		if ch.Unit == "" {
			ch.Unit = "V"
		}
		ch.Scale = r.num(p + "SCALe?")
		ch.Offset = r.num(p + "OFFSet?")
		ch.Params = []Param{
			{"Scale", si(ch.Scale, ch.Unit+"/div")},
			{"Offset", si(ch.Offset, ch.Unit)},
			{"Coupling", r.str(p + "COUPling?")},
			{"Probe", strconv.FormatFloat(r.num(p+"PROBe?"), 'g', -1, 64) + "x"},
			{"BW limit", r.str(p + "BWLimit?")},
			{"Invert", onOff(r.str(p + "INVert?"))},
		}
		c.Channels = append(c.Channels, ch)
	}

	c.Timebase = []Param{
		{"Mode", r.str(":TIMebase:MODE?")},
		{"Scale", si(r.num(":TIMebase:MAIN:SCALe?"), "s/div")},
		{"Offset", si(r.num(":TIMebase:MAIN:OFFSet?"), "s")},
	}

	acqType := r.str(":ACQuire:TYPE?")
	c.SampleRate = r.num(":ACQuire:SRATe?")
	c.Acquire = []Param{
		{"Type", acqType},
		{"Sample rate", si(c.SampleRate, "Sa/s")},
		{"Memory depth", r.str(":ACQuire:MDEPth?")},
	}
	if acqType == "AVER" {
		c.Acquire = append(c.Acquire, Param{"Averages", r.str(":ACQuire:AVERages?")})
	}

	trigMode := r.str(":TRIGger:MODE?")
	c.Trigger = []Param{
		{"Mode", trigMode},
		{"Status", status},
		{"Sweep", r.str(":TRIGger:SWEep?")},
		{"Coupling", r.str(":TRIGger:COUPling?")},
		{"Holdoff", si(r.num(":TRIGger:HOLDoff?"), "s")},
	}
	if trigMode == "EDGE" {
		c.Trigger = append(c.Trigger,
			Param{"Source", r.str(":TRIGger:EDGe:SOURce?")},
			Param{"Slope", r.str(":TRIGger:EDGe:SLOPe?")},
			Param{"Level", si(r.num(":TRIGger:EDGe:LEVel?"), "V")},
		)
	}
	if r.err != nil {
		return nil, r.err
	}
	if len(c.Channels) == 0 {
		return nil, fmt.Errorf("no channel is displayed on the scope")
	}

	r.cmd(":WAVeform:MODE RAW")
	r.cmd(":WAVeform:FORMat BYTE")
	for _, ch := range c.Channels {
		r.cmd(":WAVeform:SOURce CHAN" + ch.Name[2:])
		pre := r.str(":WAVeform:PREamble?")
		if r.err != nil {
			return nil, r.err
		}
		var err error
		if ch.Pre, err = parsePreamble(pre); err != nil {
			return nil, err
		}
		ch.Data = make([]byte, 0, ch.Pre.Points)
		for start := 1; start <= ch.Pre.Points; start += chunkSize {
			stop := min(start+chunkSize-1, ch.Pre.Points)
			r.cmd(fmt.Sprintf(":WAVeform:STARt %d", start))
			r.cmd(fmt.Sprintf(":WAVeform:STOP %d", stop))
			if r.err != nil {
				return nil, r.err
			}
			b, err := s.QueryBlock(":WAVeform:DATA?")
			if err != nil {
				return nil, err
			}
			if len(b) != stop-start+1 {
				return nil, fmt.Errorf("%s: got %d points, want %d", ch.Name, len(b), stop-start+1)
			}
			ch.Data = append(ch.Data, b...)
		}
	}

	first := c.Channels[0].Pre
	for _, ch := range c.Channels {
		if ch.Pre.Points != first.Points || ch.Pre.XInc != first.XInc {
			return nil, fmt.Errorf("channels have different sampling (%s)", ch.Name)
		}
	}
	c.Points, c.XInc, c.XOrig, c.XRef = first.Points, first.XInc, first.XOrig, first.XRef
	c.Acquire = append(c.Acquire,
		Param{"Points/channel", strconv.Itoa(c.Points)},
		Param{"Sample interval", si(c.XInc, "s")},
		Param{"Record length", si(c.Duration(), "s")},
	)
	return c, nil
}

// parsePreamble parses
// "format,type,points,count,xincrement,xorigin,xreference,yincrement,yorigin,yreference".
func parsePreamble(s string) (Preamble, error) {
	f := strings.Split(s, ",")
	if len(f) != 10 {
		return Preamble{}, fmt.Errorf("bad preamble %q", s)
	}
	v := make([]float64, 10)
	for i := range f {
		var err error
		if v[i], err = strconv.ParseFloat(strings.TrimSpace(f[i]), 64); err != nil {
			return Preamble{}, fmt.Errorf("bad preamble %q", s)
		}
	}
	p := Preamble{
		Points: int(v[2]),
		XInc:   v[4], XOrig: v[5], XRef: v[6],
		YInc: v[7], YOrig: v[8], YRef: v[9],
	}
	if p.Points <= 0 || p.XInc <= 0 {
		return Preamble{}, fmt.Errorf("bad preamble %q", s)
	}
	return p, nil
}

// View is a min/max decimated part of the capture, ready for plotting.
// Bucket k covers samples [Start+k*Step, Start+(k+1)*Step).
type View struct {
	Start, End int
	Step       float64
	Channels   []ViewChannel
}

type ViewChannel struct {
	Name     string
	Min, Max []float32
}

// View reduces samples [start, end) to at most n min/max buckets.
// When there are fewer samples than n every sample is its own bucket.
func (c *Capture) View(start, end, n int) View {
	start = max(0, min(start, c.Points-1))
	end = max(start+1, min(end, c.Points))
	n = max(1, min(n, 10000))
	count, step := end-start, 1.0
	if count > n {
		count, step = n, float64(end-start)/float64(n)
	}
	v := View{Start: start, End: end, Step: step}
	for _, ch := range c.Channels {
		vc := ViewChannel{Name: ch.Name, Min: make([]float32, count), Max: make([]float32, count)}
		for k := 0; k < count; k++ {
			a := start + int(float64(k)*step)
			b := max(a+1, start+int(float64(k+1)*step))
			lo, hi := byte(255), byte(0)
			for _, x := range ch.Data[a:b] {
				lo, hi = min(lo, x), max(hi, x)
			}
			lv, hv := ch.Value(lo), ch.Value(hi)
			vc.Min[k], vc.Max[k] = float32(min(lv, hv)), float32(max(lv, hv))
		}
		v.Channels = append(v.Channels, vc)
	}
	return v
}

// WriteCSV writes samples [start, end) as CSV: a time column (seconds from
// the trigger) and one column per channel. The capture settings come first
// as "#" comment lines.
func (c *Capture) WriteCSV(w io.Writer, start, end int) error {
	start = max(0, min(start, c.Points))
	end = max(start, min(end, c.Points))
	bw := bufio.NewWriter(w)
	fmt.Fprintf(bw, "# %s\n# captured %s\n", c.IDN, c.Time.Format(time.RFC3339))
	section := func(name string, ps []Param) {
		for _, p := range ps {
			fmt.Fprintf(bw, "# %s %s: %s\n", name, p.Name, p.Value)
		}
	}
	section("Timebase", c.Timebase)
	section("Acquisition", c.Acquire)
	section("Trigger", c.Trigger)
	for _, ch := range c.Channels {
		section(ch.Name, ch.Params)
	}
	fmt.Fprintf(bw, "# samples %d..%d of %d\ntime_s", start, end, c.Points)
	for _, ch := range c.Channels {
		fmt.Fprintf(bw, ",%s_%s", ch.Name, ch.Unit)
	}
	bw.WriteByte('\n')
	var line []byte
	for i := start; i < end; i++ {
		line = strconv.AppendFloat(line[:0], c.XOrig+(float64(i)-c.XRef)*c.XInc, 'g', 10, 64)
		for _, ch := range c.Channels {
			line = append(line, ',')
			line = strconv.AppendFloat(line, ch.Value(ch.Data[i]), 'g', 6, 64)
		}
		line = append(line, '\n')
		if _, err := bw.Write(line); err != nil {
			return err
		}
	}
	return bw.Flush()
}

// si formats a value with an SI prefix, e.g. si(2e-6, "s") = "2 µs".
func si(v float64, unit string) string {
	if v == 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		return strconv.FormatFloat(v, 'g', 4, 64) + " " + unit
	}
	prefixes := []string{"p", "n", "µ", "m", "", "k", "M", "G"}
	e := int(math.Floor(math.Log10(math.Abs(v)) / 3))
	e = max(-4, min(3, e))
	m := v / math.Pow(1000, float64(e))
	return strconv.FormatFloat(m, 'g', 4, 64) + " " + prefixes[e+4] + unit
}
