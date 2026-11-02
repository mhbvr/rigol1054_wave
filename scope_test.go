package main

import (
	"bufio"
	"fmt"
	"math"
	"net"
	"strconv"
	"strings"
	"testing"
)

// fakeScope answers the subset of DS1000Z SCPI used by Capture.
// CH1 and CH2 are displayed; CH1 is a ramp, CH2 a constant.
func fakeScope(t *testing.T, points int) string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	answers := map[string]string{
		"*IDN?": "RIGOL TECHNOLOGIES,DS1054Z,DS1ZA000000000,00.04.04", "*OPC?": "1",
		":TRIGger:STATus?": "TD", ":TIMebase:MODE?": "MAIN",
		":TIMebase:MAIN:SCALe?": "1.000000e-03", ":TIMebase:MAIN:OFFSet?": "0.000000e+00",
		":ACQuire:TYPE?": "NORM", ":ACQuire:SRATe?": "1.000000e+08", ":ACQuire:MDEPth?": "AUTO",
		":TRIGger:MODE?": "EDGE", ":TRIGger:SWEep?": "SING", ":TRIGger:COUPling?": "DC",
		":TRIGger:HOLDoff?": "1.600000e-08", ":TRIGger:EDGe:SOURce?": "CHAN1",
		":TRIGger:EDGe:SLOPe?": "POS", ":TRIGger:EDGe:LEVel?": "1.000000e+00",
	}
	for n := 1; n <= 4; n++ {
		p := fmt.Sprintf(":CHANnel%d:", n)
		answers[p+"DISPlay?"] = map[bool]string{true: "1", false: "0"}[n <= 2]
		answers[p+"UNITs?"] = "VOLT"
		answers[p+"SCALe?"] = "1.000000e+00"
		answers[p+"OFFSet?"] = "0.000000e+00"
		answers[p+"COUPling?"] = "DC"
		answers[p+"PROBe?"] = "1.000000e+01"
		answers[p+"BWLimit?"] = "OFF"
		answers[p+"INVert?"] = "0"
	}
	go func() {
		c, err := l.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		r := bufio.NewReader(c)
		src, start, stop := "", 1, 1
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			cmd := strings.TrimSpace(line)
			f := strings.Fields(cmd)
			switch {
			case len(f) == 2 && f[0] == ":WAVeform:SOURce":
				src = f[1]
			case len(f) == 2 && f[0] == ":WAVeform:STARt":
				start, _ = strconv.Atoi(f[1])
			case len(f) == 2 && f[0] == ":WAVeform:STOP":
				stop, _ = strconv.Atoi(f[1])
			}
			if start > stop {
				// The real scope moves the other point and shows a message.
				t.Errorf("%s: start %d > stop %d", cmd, start, stop)
			}
			switch {
			case cmd == ":WAVeform:PREamble?":
				fmt.Fprintf(c, "0,0,%d,1,1.000000e-08,-6.000000e-03,0,4.000000e-02,0,127\n", points)
			case cmd == ":WAVeform:DATA?":
				b := make([]byte, stop-start+1)
				for i := range b {
					if src == "CHAN1" {
						b[i] = byte(start - 1 + i)
					} else {
						b[i] = 152
					}
				}
				fmt.Fprintf(c, "#9%09d%s\n", len(b), b)
			case strings.HasSuffix(cmd, "?"):
				a, ok := answers[cmd]
				if !ok {
					t.Errorf("unexpected query %q", cmd)
					return
				}
				fmt.Fprintln(c, a)
			}
		}
	}()
	return l.Addr().String()
}

func TestCapture(t *testing.T) {
	points := 2*chunkSize + 123
	s, err := Dial(fakeScope(t, points))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	c, err := s.Capture()
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Channels) != 2 || c.Points != points || len(c.Channels[1].Data) != points {
		t.Fatalf("got %d channels, %d points", len(c.Channels), c.Points)
	}
	for i, b := range c.Channels[0].Data {
		if b != byte(i) {
			t.Fatalf("CH1 sample %d = %d", i, b)
		}
	}
	if v := c.Channels[1].Value(152); math.Abs(v-1.0) > 1e-9 {
		t.Errorf("CH2 value = %g, want 1", v)
	}
	if c.SampleRate != 1e8 || c.XOrig != -6e-3 {
		t.Errorf("sample rate %g, xorigin %g", c.SampleRate, c.XOrig)
	}

	// Decimated: 256 samples into 2 buckets of a 0..255 ramp.
	v := c.View(0, 256, 2)
	if len(v.Channels[0].Min) != 2 || v.Step != 128 {
		t.Fatalf("view %+v", v)
	}
	if v.Channels[0].Min[1] != float32(c.Channels[0].Value(128)) || v.Channels[0].Max[1] != float32(c.Channels[0].Value(255)) {
		t.Errorf("bucket 1 = %g..%g", v.Channels[0].Min[1], v.Channels[0].Max[1])
	}
	// Zoomed in: one bucket per sample.
	v = c.View(10, 15, 100)
	if len(v.Channels[0].Min) != 5 || v.Step != 1 {
		t.Errorf("view %+v", v)
	}
}

func TestWriteCSV(t *testing.T) {
	s, err := Dial(fakeScope(t, 1000))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	c, err := s.Capture()
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	if err := c.WriteCSV(&b, 127, 129); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(b.String()), "\n")
	got := strings.Join(lines[len(lines)-3:], "\n")
	want := "time_s,CH1_V,CH2_V\n-0.00599873,0,1\n-0.00599872,0.04,1"
	if got != want {
		t.Errorf("CSV tail:\n%s\nwant:\n%s", got, want)
	}
	if !strings.Contains(b.String(), "# CH1 Scale: 1 V/div\n") {
		t.Errorf("CSV header has no channel settings:\n%s", b.String())
	}
}

func TestSI(t *testing.T) {
	for v, want := range map[float64]string{1e8: "100 MSa/s", 2e-6: "2 µSa/s", 0: "0 Sa/s", -0.05: "-50 mSa/s"} {
		if got := si(v, "Sa/s"); got != want {
			t.Errorf("si(%g) = %q, want %q", v, got, want)
		}
	}
}
