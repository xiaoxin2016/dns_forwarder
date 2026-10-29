// Command dns_forwarder is a small DNS server that answers A/AAAA queries
// from a hosts file (with wildcard support) and forwards everything else to
// the upstream servers listed in dns.txt.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/miekg/dns"
)

var version = "dev"

const (
	hostsFileName    = "hosts"
	upstreamFileName = "dns.txt"
	backupFileName   = "dns_backup.json"
)

func main() {
	listen := flag.String("listen", ":53", "listen address (UDP and TCP)")
	hostsPath := flag.String("hosts", "", "hosts file (default: "+hostsFileName+" next to the executable)")
	upstreamPath := flag.String("upstream", "", "upstream list (default: "+upstreamFileName+" next to the executable)")
	ttl := flag.Uint("ttl", 60, "TTL in seconds for answers from the hosts file")
	timeout := flag.Duration("timeout", 2*time.Second, "timeout per upstream query")
	reload := flag.Duration("reload", 3*time.Second, "interval to check config files for changes (0 disables)")
	setDNS := flag.Bool("setdns", true, "point the system DNS at this server while running and restore it on exit (Windows)")
	restoreOnly := flag.Bool("restore", false, "restore system DNS from "+backupFileName+" and exit")
	verbose := flag.Bool("v", false, "log every query")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}

	*hostsPath = resolveFile(*hostsPath, hostsFileName)
	*upstreamPath = resolveFile(*upstreamPath, upstreamFileName)
	backupPath := filepath.Join(filepath.Dir(*upstreamPath), backupFileName)

	if *restoreOnly {
		if err := restoreFromBackup(backupPath); err != nil {
			log.Fatalf("restore DNS: %v", err)
		}
		log.Printf("system DNS restored")
		return
	}

	listenHost, listenPort, err := net.SplitHostPort(*listen)
	if err != nil {
		log.Fatalf("invalid -listen %q: %v", *listen, err)
	}

	h := &handler{fwd: newForwarder(*timeout), ttl: uint32(*ttl), verbose: *verbose}
	loadConfig := func() error {
		hosts, err := loadHostsFile(*hostsPath)
		if errors.Is(err, os.ErrNotExist) {
			log.Printf("hosts file %s not found, only forwarding", *hostsPath)
			hosts, err = newHostsTable(), nil
		}
		if err != nil {
			return fmt.Errorf("load hosts: %w", err)
		}
		ups, err := loadUpstreamFile(*upstreamPath)
		if err != nil {
			return fmt.Errorf("load upstreams: %w", err)
		}
		ups = dropSelfUpstreams(ups, listenPort)
		if len(ups) == 0 {
			return fmt.Errorf("no valid upstream in %s", *upstreamPath)
		}
		h.hosts.Store(hosts)
		h.fwd.setUpstreams(ups)
		log.Printf("loaded %d host entries from %s, upstreams %v", hosts.size(), *hostsPath, ups)
		return nil
	}
	if err := loadConfig(); err != nil {
		log.Fatal(err)
	}

	udpConn, err := net.ListenPacket("udp", *listen)
	if err != nil {
		log.Fatalf("listen udp %s: %v", *listen, err)
	}
	tcpLn, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatalf("listen tcp %s: %v", *listen, err)
	}
	udpSrv := &dns.Server{PacketConn: udpConn, Handler: h}
	tcpSrv := &dns.Server{Listener: tcpLn, Handler: h}
	for _, srv := range []*dns.Server{udpSrv, tcpSrv} {
		go func(srv *dns.Server) {
			if err := srv.ActivateAndServe(); err != nil {
				log.Fatalf("dns server: %v", err)
			}
		}(srv)
	}
	log.Printf("dns_forwarder %s listening on %s (udp+tcp)", version, *listen)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var restore func() error
	if *setDNS {
		restore = takeOverDNS(backupPath, listenHost, listenPort)
	}

	if *reload > 0 {
		go watchFiles(ctx, *reload, []string{*hostsPath, *upstreamPath}, func() {
			if err := loadConfig(); err != nil {
				log.Printf("reload failed, keeping previous config: %v", err)
			}
		})
	}

	<-ctx.Done()
	log.Printf("shutting down")
	// Restore first: on Windows a console close only leaves a few seconds.
	if restore != nil {
		if err := restore(); err != nil {
			log.Printf("restore DNS: %v (run with -restore to retry)", err)
		}
	}
	udpSrv.Shutdown()
	tcpSrv.Shutdown()
}

// takeOverDNS points the system DNS at this server when the listen address
// makes it reachable as 127.0.0.1:53. It returns nil when nothing was changed.
func takeOverDNS(backupPath, host, port string) func() error {
	if port != "53" {
		log.Printf("not changing system DNS: listening on port %s, system DNS can only use 53", port)
		return nil
	}
	switch host {
	case "", "::", "0.0.0.0", "127.0.0.1":
	default:
		log.Printf("not changing system DNS: listen address %s does not cover 127.0.0.1", host)
		return nil
	}
	// "" and "::" listen dual-stack, so ::1 is served as well.
	setV6 := host == "" || host == "::"
	restore, err := takeOverSystemDNS(backupPath, setV6)
	if err != nil {
		log.Printf("not changing system DNS: %v", err)
		return nil
	}
	return restore
}

// resolveFile returns explicit if set, otherwise name next to the executable,
// falling back to the working directory (useful with "go run").
func resolveFile(explicit, name string) string {
	if explicit != "" {
		return explicit
	}
	var candidates []string
	if exe, err := os.Executable(); err == nil {
		if real, err := filepath.EvalSymlinks(exe); err == nil {
			exe = real
		}
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), name))
	}
	if wd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(wd, name))
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	if len(candidates) > 0 {
		return candidates[0]
	}
	return name
}

// dropSelfUpstreams removes loopback upstreams on our own port, which would
// make every forwarded query loop back to this server.
func dropSelfUpstreams(ups []string, listenPort string) []string {
	out := ups[:0:0]
	for _, u := range ups {
		host, port, _ := net.SplitHostPort(u)
		if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() && port == listenPort {
			log.Printf("upstream %s points back at this server, skipped", u)
			continue
		}
		out = append(out, u)
	}
	return out
}

// watchFiles polls the files' size and modification time and calls onChange
// when any of them differs from the previous check.
func watchFiles(ctx context.Context, interval time.Duration, paths []string, onChange func()) {
	stamp := func() string {
		s := ""
		for _, p := range paths {
			if fi, err := os.Stat(p); err == nil {
				s += p + strconv.FormatInt(fi.Size(), 10) + fi.ModTime().String() + ";"
			}
		}
		return s
	}
	last := stamp()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if cur := stamp(); cur != last {
				last = cur
				log.Printf("config changed, reloading")
				onChange()
			}
		}
	}
}
