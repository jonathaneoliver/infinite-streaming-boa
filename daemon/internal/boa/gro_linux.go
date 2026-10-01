//go:build linux

package boa

import (
	"fmt"
	"runtime"
	"syscall"
	"unsafe"
)

// GRO through the kernel's own ethtool ioctl, not the ethtool binary, which the
// Pi image does not ship and boa's OpenWrt package does not depend on. These
// are the legacy single-feature commands; current kernels still answer them by
// flipping NETIF_F_GRO, which is the whole of what boa needs. Turning GRO off
// stops list GRO too: netif_elide_gro checks the GRO bit before any variant.
const (
	siocEthtool = 0x8946
	ethtoolGGRO = 0x2b
	ethtoolSGRO = 0x2c
)

type ethtoolValue struct {
	cmd  uint32
	data uint32
}

// ifreq with the ifr_data member of its union: 16 bytes of name, then a
// pointer, padded to the kernel's 40.
type ifreqData struct {
	name [16]byte
	data uintptr
	_    [40 - 16 - unsafe.Sizeof(uintptr(0))]byte
}

func ethtoolGRO(dev string, set bool, on bool) (bool, error) {
	if len(dev) >= 16 {
		return false, fmt.Errorf("%s: interface name too long", dev)
	}
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_DGRAM, 0)
	if err != nil {
		return false, err
	}
	defer syscall.Close(fd)

	v := ethtoolValue{cmd: ethtoolGGRO}
	if set {
		v.cmd = ethtoolSGRO
		if on {
			v.data = 1
		}
	}
	var ifr ifreqData
	copy(ifr.name[:], dev)
	ifr.data = uintptr(unsafe.Pointer(&v))
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), siocEthtool,
		uintptr(unsafe.Pointer(&ifr)))
	runtime.KeepAlive(&v)
	if errno != 0 {
		return false, fmt.Errorf("%s: ethtool GRO: %w", dev, errno)
	}
	return v.data != 0, nil
}

func groGet(dev string) (bool, error) { return ethtoolGRO(dev, false, false) }

func groSet(dev string, on bool) error {
	_, err := ethtoolGRO(dev, true, on)
	return err
}
