package main

import (
	"net"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/miekg/dns"
)

const testHosts = `
# comment line
127.0.0.1   localhost
::1         localhost ip6-localhost
10.0.0.1    exact.example.com Alias.Example.com   # trailing comment
10.0.0.2    *.example.com
10.0.0.3    *.deep.example.com
10.0.0.4    *.example.com
2001:db8::5 v6only.test
not-an-ip   broken.test
10.0.0.9
`

func mustHosts(t *testing.T) *hostsTable {
	t.Helper()
	h, err := parseHosts(strings.NewReader(testHosts))
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func ips(list []net.IP) []string {
	var out []string
	for _, ip := range list {
		out = append(out, ip.String())
	}
	return out
}

func TestHostsLookup(t *testing.T) {
	h := mustHosts(t)
	cases := []struct {
		name   string
		found  bool
		v4, v6 []string
	}{
		{"localhost", true, []string{"127.0.0.1"}, []string{"::1"}},
		{"LOCALHOST.", true, []string{"127.0.0.1"}, []string{"::1"}},
		{"exact.example.com", true, []string{"10.0.0.1"}, nil},
		{"alias.example.com.", true, []string{"10.0.0.1"}, nil},
		{"foo.example.com", true, []string{"10.0.0.2", "10.0.0.4"}, nil},
		{"a.b.c.example.com", true, []string{"10.0.0.2", "10.0.0.4"}, nil},
		{"x.deep.example.com", true, []string{"10.0.0.3"}, nil},
		{"deep.example.com", true, []string{"10.0.0.2", "10.0.0.4"}, nil},
		{"example.com", false, nil, nil},
		{"notexample.com", false, nil, nil},
		{"v6only.test", true, nil, []string{"2001:db8::5"}},
		{"broken.test", false, nil, nil},
	}
	for _, c := range cases {
		rec, ok := h.lookup(c.name)
		if ok != c.found {
			t.Errorf("%s: found=%v, want %v", c.name, ok, c.found)
			continue
		}
		if !ok {
			continue
		}
		if got := ips(rec.v4); !reflect.DeepEqual(got, c.v4) {
			t.Errorf("%s: v4=%v, want %v", c.name, got, c.v4)
		}
		if got := ips(rec.v6); !reflect.DeepEqual(got, c.v6) {
			t.Errorf("%s: v6=%v, want %v", c.name, got, c.v6)
		}
	}
}

func TestParseUpstreams(t *testing.T) {
	in := "8.8.8.8\n# comment\n\n 1.1.1.1:5353 \n2001:4860:4860::8888\n[2001:db8::1]:53\nbad\n1.2.3.4:99999\n"
	got, err := parseUpstreams(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"8.8.8.8:53", "1.1.1.1:5353", "[2001:4860:4860::8888]:53", "[2001:db8::1]:53"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestDropSelfUpstreams(t *testing.T) {
	got := dropSelfUpstreams([]string{"127.0.0.1:53", "[::1]:53", "127.0.0.1:5353", "8.8.8.8:53"}, "53")
	want := []string{"127.0.0.1:5353", "8.8.8.8:53"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestMergeBackup(t *testing.T) {
	current := []adapterDNS{
		{GUID: "a", Name: "eth", V4: []string{"127.0.0.1"}},
		{GUID: "b", Name: "new"},
	}
	prev := []adapterDNS{
		{GUID: "a", Name: "eth", V4: []string{"192.168.1.1"}},
		{GUID: "c", Name: "gone", V4: []string{"10.0.0.1"}},
	}
	got := mergeBackup(current, prev)
	want := []adapterDNS{
		{GUID: "a", Name: "eth", V4: []string{"192.168.1.1"}},
		{GUID: "b", Name: "new"},
		{GUID: "c", Name: "gone", V4: []string{"10.0.0.1"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

// startServer serves handler on a random UDP and TCP port of 127.0.0.1.
func startServer(t *testing.T, handler dns.Handler) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", pc.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	for _, srv := range []*dns.Server{{PacketConn: pc, Handler: handler}, {Listener: ln, Handler: handler}} {
		started := make(chan struct{})
		srv.NotifyStartedFunc = func() { close(started) }
		go srv.ActivateAndServe()
		<-started
		t.Cleanup(func() { srv.Shutdown() })
	}
	return pc.LocalAddr().String()
}

func TestServerEndToEnd(t *testing.T) {
	// Fake upstream: answers every A query with 9.9.9.9 and every MX query
	// with one record.
	upstream := startServer(t, dns.HandlerFunc(func(w dns.ResponseWriter, r *dns.Msg) {
		m := new(dns.Msg)
		m.SetReply(r)
		q := r.Question[0]
		hdr := dns.RR_Header{Name: q.Name, Rrtype: q.Qtype, Class: dns.ClassINET, Ttl: 30}
		switch q.Qtype {
		case dns.TypeA:
			m.Answer = append(m.Answer, &dns.A{Hdr: hdr, A: net.ParseIP("9.9.9.9")})
		case dns.TypeAAAA:
			m.Answer = append(m.Answer, &dns.AAAA{Hdr: hdr, AAAA: net.ParseIP("2001:db8::99")})
		case dns.TypeMX:
			m.Answer = append(m.Answer, &dns.MX{Hdr: hdr, Preference: 10, Mx: "mail." + q.Name})
		}
		w.WriteMsg(m)
	}))

	h := &handler{fwd: newForwarder(time.Second), ttl: 60}
	h.hosts.Store(mustHosts(t))
	// The first upstream is unreachable, so this also covers failover.
	h.fwd.setUpstreams([]string{"127.0.0.1:1", upstream})
	addr := startServer(t, h)

	cases := []struct {
		name  string
		qtype uint16
		want  []string // answer data, empty means NOERROR without answers
		auth  bool
	}{
		{"exact.example.com.", dns.TypeA, []string{"10.0.0.1"}, true},
		{"www.example.com.", dns.TypeA, []string{"10.0.0.2", "10.0.0.4"}, true},
		{"www.example.com.", dns.TypeAAAA, nil, true},
		{"v6only.test.", dns.TypeAAAA, []string{"2001:db8::5"}, true},
		{"other.org.", dns.TypeA, []string{"9.9.9.9"}, false},
		{"example.com.", dns.TypeA, []string{"9.9.9.9"}, false},
		{"exact.example.com.", dns.TypeMX, []string{"10 mail.exact.example.com."}, false},
	}
	for _, network := range []string{"udp", "tcp"} {
		c := &dns.Client{Net: network, Timeout: 3 * time.Second}
		for _, tc := range cases {
			m := new(dns.Msg)
			m.SetQuestion(tc.name, tc.qtype)
			resp, _, err := c.Exchange(m, addr)
			if err != nil {
				t.Fatalf("%s %s %s: %v", network, dns.TypeToString[tc.qtype], tc.name, err)
			}
			if resp.Rcode != dns.RcodeSuccess || resp.Authoritative != tc.auth {
				t.Errorf("%s %s %s: rcode=%s aa=%v", network, dns.TypeToString[tc.qtype], tc.name,
					dns.RcodeToString[resp.Rcode], resp.Authoritative)
			}
			var got []string
			for _, rr := range resp.Answer {
				got = append(got, strings.TrimPrefix(rr.String(), rr.Header().String()))
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("%s %s %s: answers %v, want %v", network, dns.TypeToString[tc.qtype], tc.name, got, tc.want)
			}
		}
	}
}

func TestServerAllUpstreamsDown(t *testing.T) {
	h := &handler{fwd: newForwarder(200 * time.Millisecond), ttl: 60}
	h.hosts.Store(newHostsTable())
	h.fwd.setUpstreams([]string{"127.0.0.1:1"})
	addr := startServer(t, h)

	m := new(dns.Msg)
	m.SetQuestion("nothing.test.", dns.TypeA)
	resp, err := dns.Exchange(m, addr)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Rcode != dns.RcodeServerFailure {
		t.Fatalf("rcode=%s, want SERVFAIL", dns.RcodeToString[resp.Rcode])
	}
}
