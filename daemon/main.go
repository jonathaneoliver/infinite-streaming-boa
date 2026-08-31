// Command boad is the infinite-streaming-boa link-conditioner daemon.
//
// It runs on a Raspberry Pi configured as a transparent bridge, conditions each
// client's traffic independently, and serves a web interface for control and
// monitoring. Everything ships in this one binary: the Vue interface is
// embedded, so the appliance has no runtime dependencies beyond the standard
// iproute2 tools already present in Raspberry Pi OS.
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jonathaneoliver/infinite-streaming-boa/daemon/internal/boa"
	"github.com/jonathaneoliver/infinite-streaming-boa/daemon/web"
)

func main() {
	cfg := boa.Config{}
	var tickMs int

	flag.StringVar(&cfg.Addr, "addr", ":80", "address to serve the web interface on")
	flag.StringVar(&cfg.Bridge, "bridge", "br-lan", "bridge interface")
	flag.StringVar(&cfg.WANPort, "wan", "eth0",
		"bridge port cabled to the existing network; uplink is shaped here")
	flag.StringVar(&cfg.WlanPort, "wlan", "wlan0", "wireless AP interface")
	flag.StringVar(&cfg.LanPort, "lan", "lan0", "downstream wired port (USB adapter)")
	flag.StringVar(&cfg.StatePath, "state", "/var/lib/infinite-streaming-boa/policies.json",
		"where operator policy is persisted")
	flag.IntVar(&tickMs, "tick", 1000, "telemetry poll interval in milliseconds")
	flag.BoolVar(&cfg.Demo, "demo", false,
		"serve synthetic clients and touch no kernel state; for UI development")
	flag.Parse()

	cfg.Tick = time.Duration(tickMs) * time.Millisecond

	// Shaping and packet capture both require privilege. Failing loudly here is
	// far kinder than starting up and conditioning nothing.
	if os.Geteuid() != 0 && !cfg.Demo {
		fmt.Fprintln(os.Stderr,
			"boad must run as root: it configures queueing disciplines and "+
				"opens a packet socket")
		os.Exit(1)
	}

	eng := boa.NewEngine(cfg)
	eng.Start()

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           boa.NewAPI(eng, web.FS()).Routes(),
		ReadHeaderTimeout: 10 * time.Second,
		// No WriteTimeout: server-sent event streams are long-lived by design
		// and a write deadline would sever them on a fixed interval.
	}

	go func() {
		if cfg.Demo {
			fmt.Printf("infinite-streaming-boa: DEMO MODE on %s -- synthetic clients, nothing is shaped\n", cfg.Addr)
		} else {
			fmt.Printf("infinite-streaming-boa: serving on %s (wan=%s bridge=%s)\n",
				cfg.Addr, cfg.WANPort, cfg.Bridge)
		}
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fmt.Fprintln(os.Stderr, "boa:", err)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	// Tear the queueing disciplines down on exit. Leaving them installed would
	// mean a stopped daemon silently continues to condition traffic, which is
	// the single most confusing failure this box could present.
	if !cfg.Demo {
		fmt.Println("infinite-streaming-boa: shutting down, removing traffic conditioning")
		eng.FlushNames()
		eng.Shaper().Teardown()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}
