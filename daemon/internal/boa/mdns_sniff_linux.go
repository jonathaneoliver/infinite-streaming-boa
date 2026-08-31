//go:build linux

package boa

import (
	"fmt"
	"net"
	"syscall"
	"unsafe"
)

// A capture path dedicated to mDNS, so a name can be keyed by the MAC that sent
// it rather than by an address.
//
// # Why a second socket
//
// The multicast listener alongside this one reads announcements as UDP, which
// gives the payload but not the sender's hardware address -- the kernel has
// already stripped the frame by then. The sampled ETH_P_ALL socket does carry
// the source MAC, but it is sampled: it drops most of what it sees on purpose,
// and mDNS is a handful of packets every few minutes, so the one packet
// carrying a name is exactly the kind of packet sampling loses.
//
// A socket that sees EVERY mDNS frame and nothing else costs almost nothing,
// because that traffic is tiny. The filter is what makes it cheap: without it
// this would be a second unsampled copy of all traffic on the bridge.
//
// # Why ETH_P_ALL rather than ETH_P_IP
//
// Same reason as the sampler: a packet socket opened with a specific protocol
// only receives frames delivered LOCALLY, and the bridge's rx_handler claims a
// forwarded frame before protocol-specific delivery ever runs. mDNS from a
// client to the multicast group is forwarded, not delivered locally, so an
// ETH_P_IP socket would see none of it. The filter below does the protocol
// selection that the socket's protocol number cannot.
//
// # Why the socket is not bound to the bridge
//
// ETH_P_ALL delivers at ingress on the REAL interface, before the bridge
// rewrites skb->dev. Binding to the bridge would therefore match nothing.
// Unbound, this also hears announcements from upstream of the box; those MACs
// are never listed as clients, so they cost a map entry and nothing else.
//
// # Why the filter is hand-assembled
//
// The daemon has no third-party dependencies, and twenty-one instructions of
// classic BPF is a poor reason to take one on.

// Classic BPF opcode pieces, from linux/bpf_common.h. Spelled out here because
// the program below is assembled by hand and the alternative is a wall of
// unexplained hex.
const (
	bpfLD  = 0x00
	bpfLDX = 0x01
	bpfALU = 0x04
	bpfJMP = 0x05
	bpfRET = 0x06

	bpfW = 0x00
	bpfH = 0x08
	bpfB = 0x10

	bpfABS = 0x20
	bpfIND = 0x40
	bpfMSH = 0xa0

	bpfRSH = 0x70

	bpfJEQ  = 0x10
	bpfJSET = 0x40

	// SO_ATTACH_FILTER is not exported by the syscall package on every
	// architecture, and its value is the same on all of them.
	soAttachFilter = 26

	// Returned by a passing filter: the number of bytes to keep. Larger than
	// any frame, so nothing is truncated by the filter itself.
	bpfPassLen = 262144
)

// mdnsFilter passes UDP datagrams to or from port 5353, over IPv4 or IPv6.
//
// Offsets are relative to the START OF THE IP HEADER, not the Ethernet header:
// on a SOCK_DGRAM packet socket the link-layer header is already pulled when
// the filter runs, so a program written against an Ethernet frame -- as every
// tcpdump example is -- reads twelve bytes into the wrong place and passes
// nothing. Measured in a container on 6.12 by sending known frames across a
// bridge and counting what arrived; see the commit that added this.
//
// IPv4 fragments after the first are dropped rather than misread: their payload
// is not a UDP header, and an mDNS announcement does not fragment in practice.
// IPv6 extension headers are not walked, matching udpPayload.
var mdnsFilter = []syscall.SockFilter{
	// i0..i3: dispatch on IP version.
	{Code: bpfLD | bpfB | bpfABS, K: 0},          // A = first byte
	{Code: bpfALU | bpfRSH, K: 4},                // A = version
	{Code: bpfJMP | bpfJEQ, Jt: 1, Jf: 0, K: 4},  // v4 -> i4, else i3
	{Code: bpfJMP | bpfJEQ, Jt: 9, Jf: 16, K: 6}, // v6 -> i13, else drop

	// i4..i12: IPv4.
	{Code: bpfLD | bpfH | bpfABS, K: 6},                // flags + fragment offset
	{Code: bpfJMP | bpfJSET, Jt: 14, Jf: 0, K: 0x1fff}, // a later fragment -> drop
	{Code: bpfLD | bpfB | bpfABS, K: 9},                // protocol
	{Code: bpfJMP | bpfJEQ, Jt: 0, Jf: 12, K: 17},      // not UDP -> drop
	{Code: bpfLDX | bpfB | bpfMSH, K: 0},               // X = header length
	{Code: bpfLD | bpfH | bpfIND, K: 0},                // source port
	{Code: bpfJMP | bpfJEQ, Jt: 8, Jf: 0, K: mdnsPort},
	{Code: bpfLD | bpfH | bpfIND, K: 2}, // destination port
	{Code: bpfJMP | bpfJEQ, Jt: 6, Jf: 7, K: mdnsPort},

	// i13..i18: IPv6.
	{Code: bpfLD | bpfB | bpfABS, K: 6},          // next header
	{Code: bpfJMP | bpfJEQ, Jt: 0, Jf: 5, K: 17}, // not UDP -> drop
	{Code: bpfLD | bpfH | bpfABS, K: ip6MinLen},  // source port
	{Code: bpfJMP | bpfJEQ, Jt: 2, Jf: 0, K: mdnsPort},
	{Code: bpfLD | bpfH | bpfABS, K: ip6MinLen + 2}, // destination port
	{Code: bpfJMP | bpfJEQ, Jt: 0, Jf: 1, K: mdnsPort},

	// i19, i20: verdicts.
	{Code: bpfRET, K: bpfPassLen},
	{Code: bpfRET, K: 0},
}

// openMDNSSocket returns a packet socket that delivers only mDNS frames.
func openMDNSSocket() (int, error) {
	fd, err := syscall.Socket(syscall.AF_PACKET, syscall.SOCK_DGRAM, int(htons(ethPALL)))
	if err != nil {
		return -1, err
	}
	prog := syscall.SockFprog{
		Len:    uint16(len(mdnsFilter)),
		Filter: &mdnsFilter[0],
	}
	// syscall exposes no typed setter for a filter program, and the daemon
	// takes no dependency for one.
	_, _, errno := syscall.Syscall6(syscall.SYS_SETSOCKOPT,
		uintptr(fd), uintptr(syscall.SOL_SOCKET), uintptr(soAttachFilter),
		uintptr(unsafe.Pointer(&prog)), unsafe.Sizeof(prog), 0)
	if errno != 0 {
		syscall.Close(fd)
		return -1, errno
	}
	// Frames queued between socket() and the filter taking effect are not
	// filtered. They are parsed as mDNS and discarded when they are not, so no
	// drain dance is needed -- only this note, so the next reader does not go
	// looking for the bug that absence implies.
	//
	// mDNS is a trickle, so a modest buffer is generous. Unlike the sampler,
	// overflow here is loss of the thing being collected rather than the
	// design.
	_ = syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_RCVBUF, 256*1024)
	return fd, nil
}

// startMDNSCapture opens the capture socket and reads it in the background.
//
// The socket is opened here rather than inside the goroutine so the descriptor
// is stored by the same goroutine that owns the learner's other one, and Close
// can shut both down.
//
// It reports whether the socket opened, because that decides whether the
// caller needs the weaker multicast fallback.
//
// Failure is non-fatal but never silent: the reason has to be visible, or the
// box just quietly shows MAC addresses forever.
func (l *Learner) startMDNSCapture() bool {
	fd, err := openMDNSSocket()
	if err != nil {
		fmt.Printf("infinite-streaming-boa: mDNS capture unavailable: %v "+
			"(falling back to multicast listeners: names by address, and the "+
			"whole segment rather than this box's clients)\n", err)
		return false
	}
	l.mdnsFd = fd
	fmt.Println("infinite-streaming-boa: mDNS capture running (names keyed by MAC)")
	go l.snoopMDNS(fd)
	return true
}

// snoopMDNS records the name each mDNS frame's SENDER announces, against the
// MAC that sent it.
func (l *Learner) snoopMDNS(fd int) {
	defer syscall.Close(fd)

	// A large record set can exceed one MTU, and recvfrom truncates silently.
	buf := make([]byte, 9000)
	for {
		n, from, err := syscall.Recvfrom(fd, buf, 0)
		if err != nil {
			fmt.Printf("infinite-streaming-boa: mDNS capture stopped: %v\n", err)
			return
		}
		ll, ok := from.(*syscall.SockaddrLinklayer)
		if !ok || ll.Halen < 6 {
			continue
		}
		// An outgoing frame carries this box's own MAC, so binding a name to
		// it would label the appliance with whatever it last relayed.
		if ll.Pkttype == syscall.PACKET_OUTGOING {
			continue
		}
		// recordMDNS drops anything that did not arrive on a downstream port.
		// That is both filters at once: upstream devices are not clients of
		// this box, and the bridge's own second copy of every multicast frame
		// -- delivered again with skb->dev rewritten to itself, measured in a
		// container -- is not a port a client can be on either.
		l.recordMDNS(net.HardwareAddr(ll.Addr[:6]).String(), buf[:n], ll.Ifindex)
	}
}
