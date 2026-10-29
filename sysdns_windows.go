//go:build windows

package main

import (
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

func checkSysDNSSupported() error {
	if !windows.GetCurrentProcessToken().IsElevated() {
		return errors.New("administrator privileges are required to change system DNS (run as administrator)")
	}
	return nil
}

// snapshotDNS lists the active, non-loopback adapters together with their
// statically configured DNS servers. Static servers are read from the
// registry because GetAdaptersAddresses cannot tell static from DHCP ones.
func snapshotDNS() ([]adapterDNS, error) {
	size := uint32(16 * 1024)
	var buf []byte
	for {
		buf = make([]byte, size)
		err := windows.GetAdaptersAddresses(windows.AF_UNSPEC, windows.GAA_FLAG_SKIP_ANYCAST|windows.GAA_FLAG_SKIP_MULTICAST,
			0, (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0])), &size)
		if err == nil {
			break
		}
		if !errors.Is(err, windows.ERROR_BUFFER_OVERFLOW) {
			return nil, err
		}
	}

	var out []adapterDNS
	for aa := (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0])); aa != nil; aa = aa.Next {
		if aa.OperStatus != windows.IfOperStatusUp ||
			aa.IfType == windows.IF_TYPE_SOFTWARE_LOOPBACK || aa.IfType == windows.IF_TYPE_TUNNEL {
			continue
		}
		guid := windows.BytePtrToString(aa.AdapterName)
		out = append(out, adapterDNS{
			GUID:   guid,
			Name:   windows.UTF16PtrToString(aa.FriendlyName),
			Index:  aa.IfIndex,
			Index6: aa.Ipv6IfIndex,
			V4:     staticNameServers(`Tcpip`, guid),
			V6:     staticNameServers(`Tcpip6`, guid),
		})
	}
	return out, nil
}

func staticNameServers(service, guid string) []string {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE,
		`SYSTEM\CurrentControlSet\Services\`+service+`\Parameters\Interfaces\`+guid, registry.QUERY_VALUE)
	if err != nil {
		return nil
	}
	defer k.Close()
	v, _, err := k.GetStringValue("NameServer")
	if err != nil {
		return nil
	}
	return strings.FieldsFunc(v, func(r rune) bool { return r == ',' || r == ' ' })
}

func applyLocalDNS(a adapterDNS, setV6 bool) error {
	if a.Index != 0 {
		if err := setDNS("ipv4", a.Index, []string{"127.0.0.1"}); err != nil {
			return err
		}
	}
	if setV6 && a.Index6 != 0 {
		if err := setDNS("ipv6", a.Index6, []string{"::1"}); err != nil {
			return err
		}
	}
	return nil
}

func restoreDNS(a adapterDNS) error {
	var errs []error
	if a.Index != 0 {
		errs = append(errs, setDNS("ipv4", a.Index, a.V4))
	}
	if a.Index6 != 0 {
		errs = append(errs, setDNS("ipv6", a.Index6, a.V6))
	}
	return errors.Join(errs...)
}

// setDNS sets the DNS servers of one interface via netsh. An empty list
// switches the interface back to automatically obtained (DHCP) servers.
func setDNS(family string, ifIndex uint32, servers []string) error {
	name := "name=" + strconv.FormatUint(uint64(ifIndex), 10)
	if len(servers) == 0 {
		return netsh("interface", family, "set", "dnsservers", name, "source=dhcp")
	}
	if err := netsh("interface", family, "set", "dnsservers", name, "source=static",
		"address="+servers[0], "validate=no"); err != nil {
		return err
	}
	for i, s := range servers[1:] {
		if err := netsh("interface", family, "add", "dnsservers", name,
			"address="+s, "index="+strconv.Itoa(i+2), "validate=no"); err != nil {
			return err
		}
	}
	return nil
}

func netsh(args ...string) error {
	out, err := exec.Command("netsh", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("netsh %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func flushDNSCache() {
	exec.Command("ipconfig", "/flushdns").Run()
}
