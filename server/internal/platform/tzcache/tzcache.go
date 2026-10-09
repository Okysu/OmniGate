// Package tzcache loads IANA time zones with a process-wide cache
// (time.LoadLocation reads the zone file on every call).
package tzcache

import (
	"errors"
	"sync"
	"time"
)

// Default is the time zone used when none is configured.
const Default = "Asia/Shanghai"

var cache sync.Map // name → *time.Location

var errInvalid = errors.New("invalid time zone")

// Load returns the named zone ("" = Default). "Local" is rejected: it depends
// on the host and is never a valid configured zone.
func Load(name string) (*time.Location, error) {
	if name == "" {
		name = Default
	}
	if l, ok := cache.Load(name); ok {
		return l.(*time.Location), nil
	}
	if name == "Local" || len(name) > 64 {
		return nil, errInvalid
	}
	l, err := time.LoadLocation(name)
	if err != nil {
		return nil, err
	}
	cache.Store(name, l)
	return l, nil
}

// Valid reports whether name is a loadable zone (not "" or "Local").
func Valid(name string) bool {
	if name == "" {
		return false
	}
	_, err := Load(name)
	return err == nil
}

// MustLoad is Load falling back to Default, then UTC.
func MustLoad(name string) *time.Location {
	if l, err := Load(name); err == nil {
		return l
	}
	if l, err := Load(Default); err == nil {
		return l
	}
	return time.UTC
}
