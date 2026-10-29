package main

import (
	"log"
	"net"
	"sync/atomic"

	"github.com/miekg/dns"
)

type handler struct {
	hosts   atomic.Pointer[hostsTable]
	fwd     *forwarder
	ttl     uint32
	verbose bool
}

func (h *handler) ServeDNS(w dns.ResponseWriter, req *dns.Msg) {
	if len(req.Question) != 1 {
		m := new(dns.Msg)
		m.SetRcode(req, dns.RcodeFormatError)
		w.WriteMsg(m)
		return
	}
	q := req.Question[0]
	_, overTCP := w.RemoteAddr().(*net.TCPAddr)

	// Names in the hosts file are answered locally for A/AAAA. A matched name
	// without an address of the requested family gets an empty NOERROR reply,
	// so the upstream cannot override the local entry with its own records.
	if q.Qclass == dns.ClassINET && (q.Qtype == dns.TypeA || q.Qtype == dns.TypeAAAA) {
		if rec, ok := h.hosts.Load().lookup(q.Name); ok {
			m := h.localReply(req, q, rec)
			if h.verbose {
				log.Printf("%s %s %s -> hosts (%d answers)", w.RemoteAddr(), dns.TypeToString[q.Qtype], q.Name, len(m.Answer))
			}
			w.WriteMsg(m)
			return
		}
	}

	resp, err := h.fwd.exchange(req, overTCP)
	if err != nil {
		log.Printf("%s %s %s -> forward failed: %v", w.RemoteAddr(), dns.TypeToString[q.Qtype], q.Name, err)
		m := new(dns.Msg)
		m.SetRcode(req, dns.RcodeServerFailure)
		m.RecursionAvailable = true
		w.WriteMsg(m)
		return
	}
	if h.verbose {
		log.Printf("%s %s %s -> upstream %s", w.RemoteAddr(), dns.TypeToString[q.Qtype], q.Name, dns.RcodeToString[resp.Rcode])
	}
	if !overTCP {
		resp.Truncate(udpSize(req))
	}
	w.WriteMsg(resp)
}

func (h *handler) localReply(req *dns.Msg, q dns.Question, rec *record) *dns.Msg {
	m := new(dns.Msg)
	m.SetReply(req)
	m.Authoritative = true
	m.RecursionAvailable = true
	hdr := dns.RR_Header{Name: q.Name, Rrtype: q.Qtype, Class: dns.ClassINET, Ttl: h.ttl}
	if q.Qtype == dns.TypeA {
		for _, ip := range rec.v4 {
			m.Answer = append(m.Answer, &dns.A{Hdr: hdr, A: ip})
		}
	} else {
		for _, ip := range rec.v6 {
			m.Answer = append(m.Answer, &dns.AAAA{Hdr: hdr, AAAA: ip})
		}
	}
	return m
}

func udpSize(req *dns.Msg) int {
	if opt := req.IsEdns0(); opt != nil && int(opt.UDPSize()) > dns.MinMsgSize {
		return int(opt.UDPSize())
	}
	return dns.MinMsgSize
}
