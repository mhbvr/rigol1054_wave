# rigol1054_wave

Downloads the full sample memory of a Rigol DS1054Z (DS1000Z series)
oscilloscope over LAN and shows it in a web browser. Go standard library only.

## Build and run

```bash
go build
```

```bash
./rigol1054_wave -listen localhost:8080
```

Open http://localhost:8080, enter the scope IP address (SCPI over raw TCP,
port 5555; `host:port` also works) and press **Connect**.

## Usage

**Download waveforms** stops the scope and reads, for every displayed channel
(CH1–CH4), the whole acquisition memory in RAW mode (8-bit samples, read in
250k-point chunks), together with the channel, timebase, acquisition and
trigger settings. The scope is left stopped, so its screen matches the
downloaded data. Deep memory takes a while: 24 Mpts over LAN can take tens of
seconds.

The graph shows the record the way the scope screen does (8 vertical
divisions, each channel with its own V/div and offset, trigger at t = 0 as an
orange dashed line):

- mouse wheel zooms around the cursor, dragging moves in time,
  **Zoom in / Zoom out / Full record** buttons;
- the line above the graph shows the visible time span, number of samples and
  samples per pixel; the cursor readout shows time and channel values;
- when zoomed out, each pixel column shows the min/max of its samples, so no
  peaks are lost; when zoomed in to more than 4 px per sample, every sample is
  drawn as a dot, so the real time resolution (sample interval) is visible.

The sample rate, sample interval, number of points and record length are shown
above the graph and in the Acquisition table.

## Files

- `scope.go` — SCPI over TCP: queries, commands, binary block reads.
- `capture.go` — reading settings and memory, min/max decimation for plotting.
- `main.go` — HTTP server, `index.html` — page template with the plot script.
