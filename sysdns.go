package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"time"
)

// adapterDNS is the original DNS configuration of one network adapter.
// An empty server list means the servers were obtained automatically (DHCP).
type adapterDNS struct {
	GUID   string   `json:"guid"`
	Name   string   `json:"name"`
	Index  uint32   `json:"index"`
	Index6 uint32   `json:"index6"`
	V4     []string `json:"v4"`
	V6     []string `json:"v6"`
}

type dnsBackup struct {
	SavedAt  time.Time    `json:"saved_at"`
	Adapters []adapterDNS `json:"adapters"`
}

func loadBackup(path string) ([]adapterDNS, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var b dnsBackup
	if err := json.Unmarshal(data, &b); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return b.Adapters, nil
}

func saveBackup(path string, adapters []adapterDNS) error {
	data, err := json.MarshalIndent(dnsBackup{SavedAt: time.Now(), Adapters: adapters}, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// takeOverSystemDNS records the current DNS servers of every active adapter
// into backupPath, then points them at the local server. The returned
// function restores the recorded settings and removes the backup.
//
// If backupPath already exists, the previous run did not exit cleanly and
// the system may still point at 127.0.0.1, so the recorded entries take
// precedence over what is configured now.
func takeOverSystemDNS(backupPath string, setV6 bool) (restore func() error, err error) {
	if err := checkSysDNSSupported(); err != nil {
		return nil, err
	}
	current, err := snapshotDNS()
	if err != nil {
		return nil, fmt.Errorf("read current DNS settings: %w", err)
	}
	prev, err := loadBackup(backupPath)
	switch {
	case err == nil:
		log.Printf("found %s from a previous run, keeping its recorded settings as the original", backupPath)
		current = mergeBackup(current, prev)
	case !errors.Is(err, os.ErrNotExist):
		return nil, err
	}
	if len(current) == 0 {
		return nil, errors.New("no active network adapter found")
	}
	if err := saveBackup(backupPath, current); err != nil {
		return nil, fmt.Errorf("save DNS backup: %w", err)
	}
	for _, a := range current {
		log.Printf("adapter %q: original DNS v4=%s v6=%s", a.Name, describe(a.V4), describe(a.V6))
	}

	applied := 0
	for _, a := range current {
		if err := applyLocalDNS(a, setV6); err != nil {
			log.Printf("adapter %q: set DNS to local failed: %v", a.Name, err)
			continue
		}
		applied++
		log.Printf("adapter %q: DNS set to local server", a.Name)
	}
	flushDNSCache()
	if applied == 0 {
		if err := restoreFromBackup(backupPath); err != nil {
			log.Printf("restore DNS: %v", err)
		}
		return nil, errors.New("could not change DNS on any adapter")
	}
	return func() error { return restoreFromBackup(backupPath) }, nil
}

// restoreFromBackup restores every adapter recorded in backupPath and deletes
// the file when all of them succeeded. On failure the file is kept so that
// a later run (or -restore) can try again.
func restoreFromBackup(backupPath string) error {
	if err := checkSysDNSSupported(); err != nil {
		return err
	}
	adapters, err := loadBackup(backupPath)
	if err != nil {
		return err
	}
	failed := 0
	for _, a := range adapters {
		if err := restoreDNS(a); err != nil {
			failed++
			log.Printf("adapter %q: restore DNS failed: %v", a.Name, err)
			continue
		}
		log.Printf("adapter %q: DNS restored (v4=%s v6=%s)", a.Name, describe(a.V4), describe(a.V6))
	}
	flushDNSCache()
	if failed > 0 {
		return fmt.Errorf("%d adapter(s) not restored, backup kept at %s", failed, backupPath)
	}
	return os.Remove(backupPath)
}

// mergeBackup replaces entries in current with the recorded ones from prev
// (matched by adapter GUID), and keeps adapters that only exist in prev.
func mergeBackup(current, prev []adapterDNS) []adapterDNS {
	byGUID := make(map[string]adapterDNS, len(prev))
	for _, a := range prev {
		byGUID[a.GUID] = a
	}
	out := make([]adapterDNS, 0, len(current)+len(prev))
	for _, a := range current {
		if old, ok := byGUID[a.GUID]; ok {
			a.V4, a.V6 = old.V4, old.V6
			delete(byGUID, a.GUID)
		}
		out = append(out, a)
	}
	for _, a := range prev {
		if _, ok := byGUID[a.GUID]; ok {
			out = append(out, a)
		}
	}
	return out
}

func describe(servers []string) string {
	if len(servers) == 0 {
		return "auto(DHCP)"
	}
	return fmt.Sprint(servers)
}
