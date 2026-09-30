//go:build !linux

package boa

import (
	"os"
	"path/filepath"
)

// hostapdLocalAddr is the non-Linux side of hostapd_local_linux.go.
//
// macOS has no abstract namespace, so "@name" there is a RELATIVE PATH: Go
// creates a socket file of that name in the working directory -- the package
// directory under `go test` -- and Close does not unlink it. The suite was
// leaving one per hostapd command in the source tree, invisible to git because
// git cannot represent a socket, until a release tarball refused to archive
// them (#361).
//
// No hostapd runs here, so this only has to bind somewhere harmless and clean
// up: the temp directory, removed on release. A stale file of the same name is
// removed first, or the bind would fail.
func hostapdLocalAddr(name string) (addr string, release func()) {
	p := filepath.Join(os.TempDir(), name)
	_ = os.Remove(p)
	return p, func() { _ = os.Remove(p) }
}
