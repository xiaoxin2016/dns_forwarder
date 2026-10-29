//go:build !windows

package main

import (
	"errors"
	"runtime"
)

var errSysDNSUnsupported = errors.New("changing system DNS is only supported on Windows, not " + runtime.GOOS)

func checkSysDNSSupported() error { return errSysDNSUnsupported }

func snapshotDNS() ([]adapterDNS, error)   { return nil, errSysDNSUnsupported }
func applyLocalDNS(adapterDNS, bool) error { return errSysDNSUnsupported }
func restoreDNS(adapterDNS) error          { return errSysDNSUnsupported }
func flushDNSCache()                       {}
