package tunnel

import "testing"

func TestParseListenersSS(t *testing.T) {
	const out = `LISTEN 0      4096         0.0.0.0:8080       0.0.0.0:*
LISTEN 0      128          127.0.0.1:5432     0.0.0.0:*
LISTEN 0      128                *:22               *:*
LISTEN 0      128             [::]:80            [::]:*
LISTEN 0      128                [::1]:631          [::]:*
LISTEN 0      128          0.0.0.0:8080       0.0.0.0:*
`
	got := ParseListeners(out)
	want := []Listener{
		{Bind: "::", Port: 80},
		{Bind: "127.0.0.1", Port: 5432},
		{Bind: "0.0.0.0", Port: 8080},
	}
	if len(got) != len(want) {
		t.Fatalf("ParseListeners = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ParseListeners[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestParseListenersNetstat(t *testing.T) {
	const out = `Active Internet connections (only servers)
Proto Recv-Q Send-Q Local Address           Foreign Address         State
tcp        0      0 0.0.0.0:22              0.0.0.0:*               LISTEN
tcp        0      0 127.0.0.1:631           0.0.0.0:*               LISTEN
tcp6       0      0 :::80                   :::*                    LISTEN
udp        0      0 0.0.0.0:68              0.0.0.0:*
`
	got := ParseListeners(out)
	want := []Listener{
		{Bind: "::", Port: 80},
	}
	if len(got) != len(want) {
		t.Fatalf("ParseListeners = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ParseListeners[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestParseListenersSkipsNoise(t *testing.T) {
	got := ParseListeners("hello\nState Recv-Q\n")
	if len(got) != 0 {
		t.Fatalf("ParseListeners noise = %#v, want empty", got)
	}
}

func TestParseListenersSkipsSystemPorts(t *testing.T) {
	const out = `LISTEN 0 128 0.0.0.0:22 0.0.0.0:*
LISTEN 0 128 127.0.0.53:53 0.0.0.0:*
LISTEN 0 128 127.0.0.1:631 0.0.0.0:*
LISTEN 0 128 0.0.0.0:5355 0.0.0.0:*
LISTEN 0 128 127.0.0.1:6000 0.0.0.0:*
LISTEN 0 128 0.0.0.0:80 0.0.0.0:*
LISTEN 0 128 127.0.0.1:5432 0.0.0.0:*
LISTEN 0 128 0.0.0.0:3000 0.0.0.0:*
`
	got := ParseListeners(out)
	want := []Listener{
		{Bind: "0.0.0.0", Port: 80},
		{Bind: "0.0.0.0", Port: 3000},
		{Bind: "127.0.0.1", Port: 5432},
	}
	if len(got) != len(want) {
		t.Fatalf("ParseListeners system filter = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ParseListeners[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestDialAddrWildcard(t *testing.T) {
	if got := DialAddr("0.0.0.0", 8080); got != "127.0.0.1:8080" {
		t.Fatalf("DialAddr 0.0.0.0 = %q", got)
	}
	if got := DialAddr("*", 22); got != "127.0.0.1:22" {
		t.Fatalf("DialAddr * = %q", got)
	}
	if got := DialAddr("::", 80); got != "127.0.0.1:80" {
		t.Fatalf("DialAddr :: = %q", got)
	}
	if got := DialAddr("10.0.0.5", 443); got != "10.0.0.5:443" {
		t.Fatalf("DialAddr specific = %q", got)
	}
	if got := DialAddr("127.0.0.1", 5432); got != "127.0.0.1:5432" {
		t.Fatalf("DialAddr loopback = %q", got)
	}
}
