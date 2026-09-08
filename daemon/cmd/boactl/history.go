package main

// history retrieves the throughput-and-cap trace a run leaves behind.
//
// This is the half of a captured run that `events -follow` does not give you.
// Events say what HAPPENED -- a roam, a deauth, a pattern step; this says what
// the link was DOING while it happened, second by second, with the cap that was
// in force at each point. Lining a player's behaviour up against the cap that
// caused it is, in the daemon's own words, the entire purpose of this box, and
// it cannot be done from either half alone.

import (
	"encoding/csv"
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jonathaneoliver/infinite-streaming-boa/daemon/internal/boa"
)

// isoMillis is RFC 3339 kept to milliseconds, which is the resolution the
// samples actually carry. The standard library has RFC3339 and RFC3339Nano and
// nothing between them; Nano would print six digits of precision this data does
// not have.
const isoMillis = "2006-01-02T15:04:05.000Z07:00"

// historyDoc is GET /api/history. The envelope is a map literal server-side, so
// there is no shared type to borrow -- see wireRoots in the drift test, which
// covers Sample but has to name these four keys by hand.
type historyDoc struct {
	// IntervalMs is the live tick the stream appends at; BucketMs is what one
	// row actually covers. They differ on long ranges and conflating them
	// would claim a resolution the data does not have.
	IntervalMs int64                   `json:"interval_ms"`
	BucketMs   int64                   `json:"bucket_ms"`
	WindowSec  int                     `json:"window_sec"`
	Now        int64                   `json:"now"`
	Clients    map[string][]boa.Sample `json:"clients"`
}

func cmdHistory(c *client, args []string) error {
	fs := flag.NewFlagSet("history", flag.ExitOnError)
	window := fs.Duration("window", 5*time.Minute, "how far back to fetch (the box clamps to 1m-1h)")
	points := fs.Int("points", 600, "maximum rows per client (the box clamps to 60-3600)")
	mac := fs.String("mac", "", "only this device, by MAC or part of a label")
	out := fs.String("o", "", "write the CSV to this file instead of stdout")
	if helpWanted(args) {
		fmt.Fprint(os.Stderr, "boactl history [flags] -- the throughput and cap trace, as CSV\n\n")
		fs.SetOutput(os.Stderr)
		fs.PrintDefaults()
		fmt.Fprint(os.Stderr, "\nOne row per client per bucket. bucket_ms is a column rather than a\n"+
			"header so it survives being redirected into a file: a row that does not\n"+
			"carry the width it covers can be read at a resolution it does not have.\n")
		return nil
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	var doc historyDoc
	path := fmt.Sprintf("/api/history?window=%d&points=%d", int(window.Seconds()), *points)
	if err := c.get(path, &doc); err != nil {
		return err
	}
	if *jsonFlag {
		return emitJSON(doc)
	}

	// The box CLAMPS rather than rejects, so asking for 10s quietly returns a
	// minute of data. Saying nothing would let a caller believe they had the
	// window they asked for.
	if want := int(window.Seconds()); doc.WindowSec != want {
		fmt.Fprintf(os.Stderr, "note: asked for a %ds window, the box returned %ds (it clamps to 60-3600)\n",
			want, doc.WindowSec)
	}

	macs := make([]string, 0, len(doc.Clients))
	for m := range doc.Clients {
		macs = append(macs, m)
	}
	sort.Strings(macs) // Go randomises map order; a trace that reorders per run cannot be diffed.

	if *mac != "" {
		only, err := findClient(c, *mac)
		if err != nil {
			return err
		}
		if _, ok := doc.Clients[only.MAC]; !ok {
			return fmt.Errorf("%s matched %s, which has no history in this window",
				*mac, only.MAC)
		}
		macs = []string{only.MAC}
	}

	dst := os.Stdout
	if *out != "" {
		f, err := os.Create(*out)
		if err != nil {
			return err
		}
		defer f.Close()
		dst = f
	}

	w := csv.NewWriter(dst)
	if err := w.Write([]string{
		"mac", "t_ms", "t_iso", "bucket_ms",
		"down_mbps", "up_mbps", "cap_mbps", "phy_down_mbps", "phy_up_mbps",
	}); err != nil {
		return err
	}
	rows := 0
	for _, m := range macs {
		for _, s := range doc.Clients[m] {
			if err := w.Write([]string{
				m,
				strconv.FormatInt(s.T, 10),
				// Both forms on purpose. The epoch column is what a script
				// joins on; the ISO one is what a human lines up against a
				// client-side log, and the box is NTP-synced so they align to
				// tens of milliseconds. See README on correlating a run.
				time.UnixMilli(s.T).UTC().Format(isoMillis),
				strconv.FormatInt(doc.BucketMs, 10),
				num(s.Down), num(s.Up), num(s.Cap), num(s.PhyDown), num(s.PhyUp),
			}); err != nil {
				return err
			}
			rows++
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return err
	}

	// An empty trace is not an error -- a box with no traffic has none -- but it
	// must not be mistaken for a command that failed to run.
	if rows == 0 {
		fmt.Fprintf(os.Stderr, "no samples in the last %ds; nothing has passed through the box\n",
			doc.WindowSec)
		return nil
	}
	fmt.Fprintf(os.Stderr, "%d row(s) for %d client(s); each covers %dms",
		rows, len(macs), doc.BucketMs)
	if doc.BucketMs > doc.IntervalMs {
		// Say it plainly: on a long range the box means several ticks together,
		// and a reader who assumes one row is one second is wrong by the ratio.
		fmt.Fprintf(os.Stderr, " (%d ticks meaned, the live rate is %dms)",
			doc.BucketMs/doc.IntervalMs, doc.IntervalMs)
	}
	fmt.Fprintln(os.Stderr)
	if *out != "" {
		fmt.Fprintf(os.Stderr, "wrote %s\n", *out)
	}
	return nil
}

// num formats a rate for a data file: fixed notation, six decimals, trailing
// zeros trimmed.
//
// Shortest-round-trip formatting is the obvious choice and is wrong here. It
// prints the float exactly, so a mean lands as 286.69999999999993 and
// 0.00012271445714268257 -- seventeen digits of arithmetic noise that differs
// in the last places between runs. These files exist to be diffed against each
// other and against a QoE report, and noise in a column nobody reads still
// shows up as a changed line. Six decimals of a megabit is a tenth of a bit per
// second, well past anything the counters resolve.
func num(f float64) string {
	s := strconv.FormatFloat(f, 'f', 6, 64)
	s = strings.TrimRight(s, "0")
	return strings.TrimSuffix(s, ".")
}
