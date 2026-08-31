//go:build !linux

package boa

import (
	"errors"
	"time"
)

// Stub so the daemon compiles and its tests run on a developer's macOS
// machine. AF_PACKET has no portable equivalent; boa only ever runs on the Pi,
// but being unable to build locally would make development needlessly painful.
type Learner struct{ bridge string }

type Seen struct {
	IP   string
	IPv6 []string
	Port string
}

func NewLearner(bridge, wlanPort, lanPort string) *Learner { return &Learner{bridge: bridge} }

func (l *Learner) Run() error {
	return errors.New("passive learning requires Linux (AF_PACKET)")
}
func (l *Learner) Names() map[string]string            { return map[string]string{} }
func (l *Learner) MACNames() map[string]string         { return map[string]string{} }
func (l *Learner) LoadNames(string)                    {}
func (l *Learner) SaveNames(string)                    {}
func (l *Learner) Close()                              {}
func (l *Learner) Table(time.Duration) map[string]Seen { return map[string]Seen{} }
