package boa

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// A hostapd command must leave nothing on disk, on any platform, whether or not
// hostapd answered.
//
// On macOS the client's "@boa-hostapd-..." socket used to become a FILE in the
// working directory -- the package directory under go test -- one per command,
// invisible to git, until a release tarball refused to archive them (#361). The
// failing dial is the case that matters: the local socket is bound BEFORE the
// connect, so it was left behind even when there was no hostapd to reach, which
// is every run on a development machine.
func TestHostapdCmdLeavesNoSocketFileBehind(t *testing.T) {
	pattern := fmt.Sprintf("*boa-hostapd-%d-*", os.Getpid())
	before := socketLitter(t, pattern)

	for i := 0; i < 5; i++ {
		// No hostapd serves this, so the dial fails -- after the bind.
		if _, err := hostapdCmd("wlan-nobody", "STATUS"); err == nil {
			t.Fatal("hostapdCmd reached a hostapd that should not exist")
		}
	}

	if after := socketLitter(t, pattern); after != before {
		t.Errorf("hostapd commands left %d socket file(s) behind (%s in . or %s)",
			after-before, pattern, os.TempDir())
	}
}

// socketLitter counts entries matching pattern in the working directory and
// the temp directory -- the two places a non-abstract client socket can land.
func socketLitter(t *testing.T, pattern string) int {
	t.Helper()
	n := 0
	for _, dir := range []string{".", os.TempDir()} {
		m, err := filepath.Glob(filepath.Join(dir, pattern))
		if err != nil {
			t.Fatal(err)
		}
		n += len(m)
	}
	return n
}
