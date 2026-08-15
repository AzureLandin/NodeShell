package tunnel

import (
	"net"
	"sort"
	"strconv"
	"strings"
)

// DiscoverCommand lists TCP listeners on a remote Linux host. ss is preferred;
// netstat is the fallback. The remote SSH exec typically runs this via the
// user's shell -c, so the || chain is honoured.
const DiscoverCommand = `ss -lntH 2>/dev/null || netstat -lnt 2>/dev/null`

// Listener is one remote TCP bind the user can forward.
type Listener struct {
	Bind string `json:"bind"`
	Port int    `json:"port"`
}

// ParseListeners extracts unique TCP listen addresses from ss -lntH or
// netstat -lnt output. Non-listen lines and unparseable addresses are skipped.
func ParseListeners(out string) []Listener {
	seen := make(map[string]struct{})
	var list []Listener
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if !isListenLine(fields) {
			continue
		}
		local := localAddressField(fields)
		bind, port, ok := splitListenAddr(local)
		if !ok || port <= 0 || port > 65535 {
			continue
		}
		bind = normalizeBind(bind)
		if IsSystemPort(port) {
			continue
		}
		key := bind + "\x00" + strconv.Itoa(port)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		list = append(list, Listener{Bind: bind, Port: port})
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].Port != list[j].Port {
			return list[i].Port < list[j].Port
		}
		return list[i].Bind < list[j].Bind
	})
	return list
}

func isListenLine(fields []string) bool {
	for _, f := range fields {
		if f == "LISTEN" {
			return true
		}
	}
	return false
}

// localAddressField picks the local addr:port token. ss -lntH puts it at
// index 3 (State Recv-Q Send-Q Local Peer). netstat -lnt puts it at index 3
// as well (Proto Recv-Q Send-Q Local Foreign State). Fall back to the first
// token that looks like host:port.
func localAddressField(fields []string) string {
	if len(fields) >= 4 {
		if _, _, ok := splitListenAddr(fields[3]); ok {
			return fields[3]
		}
	}
	for _, f := range fields {
		if f == "LISTEN" || f == "tcp" || f == "tcp6" || f == "tcp4" {
			continue
		}
		if _, _, ok := splitListenAddr(f); ok {
			return f
		}
	}
	return ""
}

func splitListenAddr(s string) (bind string, port int, ok bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", 0, false
	}
	host, portStr, err := net.SplitHostPort(s)
	if err != nil {
		// netstat tcp6 writes :::80 (unbracketed). Split on the last colon.
		i := strings.LastIndex(s, ":")
		if i <= 0 || i == len(s)-1 {
			return "", 0, false
		}
		host, portStr = s[:i], s[i+1:]
	}
	n, err := strconv.Atoi(portStr)
	if err != nil {
		return "", 0, false
	}
	return host, n, true
}

func normalizeBind(bind string) string {
	bind = strings.Trim(bind, "[]")
	if bind == "" {
		return "*"
	}
	return bind
}

// DialAddr is the remote endpoint opened over direct-tcpip. Wildcard binds
// (0.0.0.0, *, ::) go to 127.0.0.1 so a typical localhost service is reached;
// a specific address is dialled as-is.
func DialAddr(bind string, port int) string {
	host := normalizeBind(bind)
	switch host {
	case "0.0.0.0", "*", "::", "::0":
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, strconv.Itoa(port))
}

// systemPorts are well-known OS and infrastructure daemons that are almost
// never the service a user wants to reach through a local forward (sshd,
// CUPS, systemd-resolved, rpcbind, …). Application ports (HTTP, databases,
// user apps) are left visible.
var systemPorts = map[int]struct{}{
	22:   {}, // ssh
	23:   {}, // telnet
	25:   {}, // smtp
	37:   {}, // time
	53:   {}, // dns
	67:   {}, // bootps
	68:   {}, // bootpc
	69:   {}, // tftp
	88:   {}, // kerberos
	110:  {}, // pop3
	111:  {}, // rpcbind
	113:  {}, // ident
	123:  {}, // ntp
	135:  {}, // msrpc
	137:  {}, // netbios-ns
	138:  {}, // netbios-dgm
	139:  {}, // netbios-ssn
	143:  {}, // imap
	161:  {}, // snmp
	162:  {}, // snmptrap
	177:  {}, // xdmcp
	389:  {}, // ldap
	427:  {}, // slp
	445:  {}, // smb
	464:  {}, // kpasswd
	465:  {}, // smtps
	512:  {}, // exec
	513:  {}, // login
	514:  {}, // shell/syslog
	515:  {}, // printer
	530:  {}, // courier
	543:  {}, // klogin
	544:  {}, // kshell
	548:  {}, // afp
	587:  {}, // submission
	631:  {}, // cups/ipp
	636:  {}, // ldaps
	993:  {}, // imaps
	995:  {}, // pop3s
	2049: {}, // nfs
	3268: {}, // global catalog ldap
	3269: {}, // global catalog ldaps
	5353: {}, // mdns/avahi
	5355: {}, // llmnr
	9100: {}, // jetdirect/cups
}

// IsSystemPort reports whether port belongs to a common OS service that
// Discover hides from the forwarding list.
func IsSystemPort(port int) bool {
	if _, ok := systemPorts[port]; ok {
		return true
	}
	// X11 display sockets.
	return port >= 6000 && port <= 6063
}
