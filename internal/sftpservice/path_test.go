package sftpservice

import "testing"

func TestJoinRemoteRelative(t *testing.T) {
	cases := []struct{ cwd, name, want string }{
		{"/home/u", "file", "/home/u/file"},
		{"/home/u/", "file", "/home/u/file"},
		{"/", "file", "/file"},
		{"", "file", "/file"},
		{"/home/u", "a/b", "/home/u/a/b"},
		{"/home/u", ".", "/home/u"},
		{"/home/u", "..", "/home"},
		{"/home/u", "../..", "/"},
		{"/home/u", "a/../b", "/home/u/b"},
		{"/home/u", "a/./b", "/home/u/a/b"},
		{"/home/u", "a//b", "/home/u/a/b"},
	}
	for _, c := range cases {
		if got := JoinRemote(c.cwd, c.name); got != c.want {
			t.Errorf("JoinRemote(%q, %q) = %q, want %q", c.cwd, c.name, got, c.want)
		}
	}
}

func TestJoinRemoteAbsolute(t *testing.T) {
	// A leading "/" is absolute: cwd is ignored (Electron parity).
	if got := JoinRemote("/home/u", "/etc/hosts"); got != "/etc/hosts" {
		t.Errorf("JoinRemote with absolute name = %q, want /etc/hosts", got)
	}
	if got := JoinRemote("/", "/"); got != "/" {
		t.Errorf("JoinRemote(/ , /) = %q, want /", got)
	}
}

func TestJoinRemoteBackslashTreatedAsSeparator(t *testing.T) {
	// Windows-style separators in a remote name normalise to POSIX.
	if got := JoinRemote("/home/u", "a\\b"); got != "/home/u/a/b" {
		t.Errorf("JoinRemote backslash = %q, want /home/u/a/b", got)
	}
}

func TestJoinRemoteEmptyName(t *testing.T) {
	if got := JoinRemote("/home/u", ""); got != "/home/u" {
		t.Errorf("JoinRemote empty = %q, want /home/u", got)
	}
	if got := JoinRemote("", ""); got != "/" {
		t.Errorf("JoinRemote empty+empty = %q, want /", got)
	}
}

func TestJoinRemoteNeverEndsWithSlash(t *testing.T) {
	if got := JoinRemote("/home/u", "a/"); got != "/home/u/a" {
		t.Errorf("JoinRemote trailing slash = %q, want /home/u/a", got)
	}
}

func TestWithinRoot(t *testing.T) {
	cases := []struct {
		p, root string
		want    bool
	}{
		{"/home/u", "/home/u", true},
		{"/home/u/file", "/home/u", true},
		{"/home/u/sub/file", "/home/u", true},
		{"/home", "/home/u", false},
		{"/home/user2", "/home/u", false},
		{"/home/u2", "/home/u", false}, // prefix but not a path segment
		{"/etc", "/", true},
		{"/", "/", true},
	}
	for _, c := range cases {
		if got := withinRoot(c.p, c.root); got != c.want {
			t.Errorf("withinRoot(%q, %q) = %v, want %v", c.p, c.root, got, c.want)
		}
	}
}
