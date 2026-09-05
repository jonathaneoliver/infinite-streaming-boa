package boa

import (
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"
)

/*
 * Starting and stopping the two observability services the box ships with.
 *
 * They are not part of the appliance's job -- nothing about conditioning a
 * client's link needs either of them -- but they are the two processes most
 * likely to interfere with it. ntopng does deep packet inspection on everything
 * crossing the bridge, so it works hardest exactly when a measurement is
 * running: sampled 2026-09-05 it sat at 7% of a core idle and spiked to 71%.
 * That is not a resource problem on a four-core box with 3GB free; it is a
 * MEASUREMENT problem, because the thing being measured and the thing measuring
 * the box are competing for the same CPU.
 *
 * So the operator gets a switch. Turning ntopng off for the duration of a run
 * removes the only process here that could plausibly perturb a result, and
 * turning it back on afterwards costs nothing.
 *
 * SYSTEMCTL, which this daemon otherwise avoids. There is no alternative: a
 * service is a unit, and unlike hostapd neither of these offers a control
 * socket. The comment in radiopower.go claiming systemctl is unreachable under
 * ProtectSystem=strict is WRONG and was corrected after measuring it -- the
 * daemon's own sandbox runs `systemctl restart` in 168ms. Connecting to the
 * D-Bus socket is not a filesystem write, so a read-only mount never blocked it.
 */

// controllable is the ALLOWLIST, and it is the whole security model here.
//
// A unit name is never built from what a request carries. The path parameter
// selects a key in this map or the request is refused, so no input reaches
// systemctl -- an endpoint that interpolated the caller's string would let
// anyone who can reach the interface stop any unit on the box, including sshd
// and the daemon itself.
var controllable = map[string]string{
	"ntopng":  "ntopng.service",
	"glances": "glances.service",
}

// ServiceInfo is one controllable service as the interface draws it.
type ServiceInfo struct {
	Name string `json:"name"`
	// Running is what systemd says now. False also covers a unit that failed
	// or was never installed -- the button reads "start" in every one of those
	// cases, which is the right offer.
	Running bool `json:"running"`
}

// serviceRunning asks systemd. Cheap, but a subprocess all the same, so it is
// called from the bridge view's background build and never from a request.
func serviceRunning(unit string) bool {
	out, _ := exec.Command("systemctl", "is-active", unit).Output()
	return strings.TrimSpace(string(out)) == "active"
}

/*
 * Cached, because the bridge view is rebuilt every second and these are not.
 *
 * Each reading costs a `systemctl is-active` subprocess per service, and the
 * answer changes only when somebody presses one of the two buttons that change
 * it -- which invalidates this cache directly, so a press still shows up at
 * once. Asking twice a second for a value that changes twice a day is the kind
 * of waste that matters on a box already sharing four cores with ntopng.
 */
var svcCache struct {
	mu   sync.Mutex
	at   time.Time
	vals []ServiceInfo
}

const svcTTL = 10 * time.Second

// invalidateServices forces the next read to ask systemd again. Called when the
// box itself changes a service, so a button press is never waiting on a TTL.
func invalidateServices() {
	svcCache.mu.Lock()
	svcCache.at = time.Time{}
	svcCache.mu.Unlock()
}

// serviceStates reports every controllable service, in a stable order.
//
// Ordered explicitly rather than by ranging the map, because Go randomises map
// iteration and the buttons would swap places between polls -- the same
// moving-target fault that has been fixed twice elsewhere in this interface.
func serviceStates() []ServiceInfo {
	svcCache.mu.Lock()
	defer svcCache.mu.Unlock()
	if time.Since(svcCache.at) < svcTTL && svcCache.vals != nil {
		return svcCache.vals
	}
	names := []string{"ntopng", "glances"}
	out := make([]ServiceInfo, 0, len(names))
	for _, n := range names {
		out = append(out, ServiceInfo{Name: n, Running: serviceRunning(controllable[n])})
	}
	svcCache.vals, svcCache.at = out, time.Now()
	return out
}

// SetService starts or stops one of the allowlisted services.
func (e *Engine) SetService(name string, on bool) error {
	unit, ok := controllable[name]
	if !ok {
		return fmt.Errorf("%q is not a service this box will start or stop", name)
	}
	if e.cfg.Demo {
		return nil
	}
	verb := "stop"
	if on {
		verb = "start"
	}
	// Combined output: systemctl explains a refusal on stderr, and passing that
	// through verbatim is worth far more than "exit status 1" -- the same
	// reasoning the hostapd errors here already follow.
	out, err := exec.Command("systemctl", verb, unit).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %v: %s", verb, unit, err, strings.TrimSpace(string(out)))
	}
	// Recorded, because an operator reading a measurement back needs to know
	// whether ntopng was competing for CPU while it was taken.
	if on {
		e.logEvent(EventAction, "", "", "%s started", name)
	} else {
		e.logEvent(EventAction, "", "", "%s stopped — it is no longer competing for CPU", name)
	}
	// So the row the operator just pressed agrees with what they see next --
	// the cache above would otherwise hold the old answer for up to its TTL.
	invalidateServices()
	e.freshenBridge()
	return nil
}
