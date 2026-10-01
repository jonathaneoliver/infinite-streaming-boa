//go:build !linux

package boa

import "errors"

// GRO is a Linux interface feature. The daemon still has to compile on a
// developer's Mac -- see CLAUDE.md -- and these say so rather than report a
// change that did not happen: syncGRO logs the error and keeps the uplink's
// deep queue, which is what a port that still merges needs.
var errNoGRO = errors.New("GRO control needs Linux")

func groGet(string) (bool, error) { return false, errNoGRO }

func groSet(string, bool) error { return errNoGRO }
