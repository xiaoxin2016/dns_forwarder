package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/miekg/dns"
)

func loadUpstreamFile(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return parseUpstreams(f)
}

// parseUpstreams reads one upstream per line. Each line is an IP address,
// optionally with a port ("8.8.8.8", "1.1.1.1:5353", "[2001:db8::1]:53").
// Empty lines and "#" comments are ignored.
func parseUpstreams(r io.Reader) ([]string, error) {
	var out []string
	sc := bufio.NewScanner(r)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := sc.Text()
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		addr, err := normalizeUpstream(line)
		if err != nil {
			log.Printf("dns.txt: line %d: %v, skipped", lineNo, err)
			continue
		}
		out = append(out, addr)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read upstream list: %w", err)
	}
	return out, nil
}

func normalizeUpstream(s string) (string, error) {
	if ip := net.ParseIP(s); ip != nil {
		return net.JoinHostPort(ip.String(), "53"), nil
	}
	host, port, err := net.SplitHostPort(s)
	if err != nil {
		return "", fmt.Errorf("invalid upstream %q", s)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return "", fmt.Errorf("invalid upstream IP %q", host)
	}
	if p, err := strconv.Atoi(port); err != nil || p < 1 || p > 65535 {
		return "", fmt.Errorf("invalid upstream port %q", port)
	}
	return net.JoinHostPort(ip.String(), port), nil
}

// forwarder sends queries to the upstream servers in order, falling back to
// the next one on error.
type forwarder struct {
	upstreams atomic.Pointer[[]string]
	udp       *dns.Client
	tcp       *dns.Client
}

func newForwarder(timeout time.Duration) *forwarder {
	return &forwarder{
		udp: &dns.Client{Net: "udp", Timeout: timeout},
		tcp: &dns.Client{Net: "tcp", Timeout: timeout},
	}
}

func (f *forwarder) setUpstreams(list []string) { f.upstreams.Store(&list) }

func (f *forwarder) exchange(req *dns.Msg, overTCP bool) (*dns.Msg, error) {
	list := f.upstreams.Load()
	if list == nil || len(*list) == 0 {
		return nil, errors.New("no upstream configured")
	}
	var lastErr error
	for _, up := range *list {
		var resp *dns.Msg
		var err error
		if overTCP {
			resp, _, err = f.tcp.Exchange(req, up)
		} else {
			resp, _, err = f.udp.Exchange(req, up)
			if err == nil && resp.Truncated {
				resp, _, err = f.tcp.Exchange(req, up)
			}
		}
		if err == nil {
			return resp, nil
		}
		log.Printf("upstream %s: %v", up, err)
		lastErr = err
	}
	return nil, lastErr
}
