package main

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strings"

	"github.com/miekg/dns"
)

// record holds the addresses configured for one name in the hosts file.
type record struct {
	v4 []net.IP
	v6 []net.IP
}

// hostsTable is an immutable snapshot of the hosts file. Names are stored
// lower-cased and fully qualified (with trailing dot).
type hostsTable struct {
	exact map[string]*record
	// wild is keyed by the suffix of a wildcard entry: "*.example.com"
	// is stored as "example.com.".
	wild map[string]*record
}

func newHostsTable() *hostsTable {
	return &hostsTable{exact: map[string]*record{}, wild: map[string]*record{}}
}

func loadHostsFile(path string) (*hostsTable, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return parseHosts(f)
}

// parseHosts parses Linux hosts(5) format: "IP name [alias...] [# comment]".
// A name starting with "*." is a wildcard that matches every subdomain at any
// depth, but not the bare domain itself.
func parseHosts(r io.Reader) (*hostsTable, error) {
	t := newHostsTable()
	sc := bufio.NewScanner(r)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := sc.Text()
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) < 2 {
			log.Printf("hosts: line %d: missing host name, skipped", lineNo)
			continue
		}
		ip := net.ParseIP(fields[0])
		if ip == nil {
			log.Printf("hosts: line %d: invalid IP %q, skipped", lineNo, fields[0])
			continue
		}
		for _, name := range fields[1:] {
			name = strings.ToLower(dns.Fqdn(name))
			table := t.exact
			if strings.HasPrefix(name, "*.") {
				table = t.wild
				name = name[2:]
			}
			if _, ok := dns.IsDomainName(name); !ok || name == "." {
				log.Printf("hosts: line %d: invalid name %q, skipped", lineNo, name)
				continue
			}
			rec := table[name]
			if rec == nil {
				rec = &record{}
				table[name] = rec
			}
			rec.add(ip)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read hosts: %w", err)
	}
	return t, nil
}

func (r *record) add(ip net.IP) {
	list := &r.v6
	if v4 := ip.To4(); v4 != nil {
		ip, list = v4, &r.v4
	}
	for _, have := range *list {
		if have.Equal(ip) {
			return
		}
	}
	*list = append(*list, ip)
}

// lookup returns the record for name. Exact entries win over wildcards, and
// among wildcards the longest (most specific) suffix wins.
func (t *hostsTable) lookup(name string) (*record, bool) {
	name = strings.ToLower(dns.Fqdn(name))
	if rec, ok := t.exact[name]; ok {
		return rec, true
	}
	for off, end := dns.NextLabel(name, 0); !end; off, end = dns.NextLabel(name, off) {
		if rec, ok := t.wild[name[off:]]; ok {
			return rec, true
		}
	}
	return nil, false
}

func (t *hostsTable) size() int { return len(t.exact) + len(t.wild) }
