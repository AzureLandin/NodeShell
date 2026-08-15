package sshclient

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"nodeshell/internal/apperror"
	"nodeshell/internal/knownhosts"
	"nodeshell/internal/sshtest"
)

// memoryHostKeys is an in-memory HostKeys store for tests.
type memoryHostKeys struct {
	mu sync.Mutex
	m  map[string]string
}

func newMemoryHostKeys() *memoryHostKeys { return &memoryHostKeys{m: map[string]string{}} }

func (k *memoryHostKeys) key(host string, port int) string { return fmt.Sprintf("%s:%d", host, port) }

func (k *memoryHostKeys) Check(host string, port int, fingerprint string) (knownhosts.CheckResult, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	prev, ok := k.m[k.key(host, port)]
	if !ok {
		return knownhosts.CheckResult{Status: "unknown"}, nil
	}
	if prev == fingerprint {
		return knownhosts.CheckResult{Status: "ok"}, nil
	}
	return knownhosts.CheckResult{Status: "changed", Previous: prev}, nil
}

func (k *memoryHostKeys) Remember(host string, port int, fingerprint string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.m[k.key(host, port)] = fingerprint
	return nil
}

func (k *memoryHostKeys) seed(host string, port int, fingerprint string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.m[k.key(host, port)] = fingerprint
}

// hostPort splits "127.0.0.1:port" into its parts.
func hostPort(t *testing.T, addr string) (string, int) {
	t.Helper()
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("SplitHostPort(%q): %v", addr, err)
	}
	var port int
	if _, err := fmt.Sscanf(portStr, "%d", &port); err != nil {
		t.Fatalf("port %q: %v", portStr, err)
	}
	return host, port
}

func baseOptions(t *testing.T, srv *sshtest.Server, keys HostKeys) Options {
	t.Helper()
	host, port := hostPort(t, srv.Addr)
	return Options{
		Host:       host,
		Port:       port,
		Username:   "user",
		AuthMethod: "password",
		Password:   "secret",
		HostKeys:   keys,
		Deadline:   5 * time.Second,
	}
}

// assertErrorCode fails unless err is a typed Error with code.
func assertErrorCode(t *testing.T, err error, code string) {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("error = %v, want sshclient.Error with code %s", err, code)
	}
	if e.Code != code {
		t.Fatalf("code = %s, want %s (error: %v)", e.Code, code, err)
	}
}

// RED/GREEN: a wrong host key must reject the connection BEFORE any
// credential is exchanged, so the server's auth callbacks never run.
func TestConnectHostKeyUnknownRejectsBeforeAuth(t *testing.T) {
	srv := sshtest.New(t)
	srv.PasswordOK = func(user, pass string) bool { return user == "user" && pass == "secret" }
	keys := newMemoryHostKeys() // empty trust store

	_, err := Connect(context.Background(), baseOptions(t, srv, keys))
	assertErrorCode(t, err, apperror.HostKeyUnknown)
	if !strings.Contains(err.Error(), "SHA256:") {
		t.Fatalf("message %q must contain the fingerprint", err.Error())
	}
	if got := srv.AuthAttempts(); got != 0 {
		t.Fatalf("auth callbacks fired %d times on host-key rejection, want 0", got)
	}
}

func TestConnectHostKeyChangedRejectsBeforeAuth(t *testing.T) {
	srv := sshtest.New(t)
	srv.PasswordOK = func(user, pass string) bool { return true }
	keys := newMemoryHostKeys()
	keys.seed(mustHost(t, srv), mustPort(t, srv), srv.HostKeyFingerprint()[:16]) // deliberately wrong fingerprint

	_, err := Connect(context.Background(), baseOptions(t, srv, keys))
	assertErrorCode(t, err, apperror.HostKeyChanged)
	msg := err.Error()
	if !strings.Contains(msg, "was SHA256:") || !strings.Contains(msg, "now SHA256:") {
		t.Fatalf("changed message %q must carry previous and new fingerprints", msg)
	}
	var e *Error
	errors.As(err, &e)
	if e.Previous != keys.m[keys.key("127.0.0.1", mustPort(t, srv))] {
		t.Fatalf("Previous = %q, want the stored fingerprint", e.Previous)
	}
	if got := srv.AuthAttempts(); got != 0 {
		t.Fatalf("auth callbacks fired %d times on host-key rejection, want 0", got)
	}
}

func mustPort(t *testing.T, srv *sshtest.Server) int {
	_, port := hostPort(t, srv.Addr)
	return port
}

// failingHostKeys is a HostKeys seam whose Check always returns the injected
// error, emulating a broken host-key store (e.g. a corrupt known_hosts.json).
type failingHostKeys struct{ err error }

func (f *failingHostKeys) Check(string, int, string) (knownhosts.CheckResult, error) {
	return knownhosts.CheckResult{}, f.err
}

func (f *failingHostKeys) Remember(string, int, string) error { return nil }

// injectedCodeError carries an arbitrary ErrorCode — the host-key callback
// guard must collapse it to UNKNOWN so no foreign code can leak into the IPC
// whitelist.
type injectedCodeError struct{ code string }

func (e *injectedCodeError) Error() string     { return "boom" }
func (e *injectedCodeError) ErrorCode() string { return e.code }

// RED/GREEN: a host-key store read failure must survive the hostKeyCallback
// boundary with its stable code — the frontend contract maps a corrupt
// known_hosts.json to CONFIG_READ_FAILED. The real handshake proves the typed
// code travels through x/crypto's "ssh: handshake failed" wrapper via
// errors.As, not just a direct in-package callback call.
func TestConnectHostKeyStoreReadFailureKeepsCode(t *testing.T) {
	srv := sshtest.New(t)
	keys := &failingHostKeys{err: &knownhosts.Error{Code: apperror.ConfigReadFailed, Message: "Failed to read known hosts"}}

	_, err := Connect(context.Background(), baseOptions(t, srv, keys))
	assertErrorCode(t, err, apperror.ConfigReadFailed)
	// The message stays generic: neither the inner message nor a path leaks.
	if msg := err.Error(); msg != "Failed to verify host key" {
		t.Fatalf("message = %q, want the generic host-key verification message", msg)
	}
}

// RED/GREEN: an uncoded HostKeys.Check error collapses to UNKNOWN and its raw
// text (which may embed a filesystem path) is never surfaced.
func TestConnectHostKeyStorePlainErrorIsUnknownAndPathFree(t *testing.T) {
	srv := sshtest.New(t)
	keys := &failingHostKeys{err: errors.New(`open C:\Users\alice\.nodeshell\known_hosts.json: permission denied`)}

	_, err := Connect(context.Background(), baseOptions(t, srv, keys))
	assertErrorCode(t, err, apperror.Unknown)
	if strings.Contains(err.Error(), "known_hosts.json") {
		t.Fatalf("message %q leaks the store path", err.Error())
	}
}

// RED/GREEN: an ErrorCode outside the config whitelist must not inject its
// code — CONFIG_READ_FAILED / CONFIG_WRITE_FAILED are the only codes a
// host-key store error may carry.
func TestConnectHostKeyStoreForeignCodeIsUnknown(t *testing.T) {
	srv := sshtest.New(t)
	keys := &failingHostKeys{err: &injectedCodeError{code: "INJECTED_SECRET"}}

	_, err := Connect(context.Background(), baseOptions(t, srv, keys))
	assertErrorCode(t, err, apperror.Unknown)
}

func TestConnectPasswordAuth(t *testing.T) {
	srv := sshtest.New(t)
	srv.PasswordOK = func(user, pass string) bool { return user == "user" && pass == "secret" }
	keys := newMemoryHostKeys()
	keys.seed(mustHost(t, srv), mustPort(t, srv), srv.HostKeyFingerprint())

	sess, err := Connect(context.Background(), baseOptions(t, srv, keys))
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer sess.Close()
	if sess.Fingerprint() != srv.HostKeyFingerprint() {
		t.Fatalf("fingerprint = %q, want %q", sess.Fingerprint(), srv.HostKeyFingerprint())
	}
	// stdin echo round-trip
	if _, err := sess.Write([]byte("hi\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, err := readN(t, sess.Stdout(), 3, 1*time.Second)
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	if string(got) != "hi\n" {
		t.Fatalf("echo = %q, want %q", got, "hi\n")
	}
}

func mustHost(t *testing.T, srv *sshtest.Server) string {
	host, _ := hostPort(t, srv.Addr)
	return host
}

func TestConnectPasswordAuthFailed(t *testing.T) {
	srv := sshtest.New(t)
	srv.PasswordOK = func(user, pass string) bool { return false }
	keys := newMemoryHostKeys()
	keys.seed(mustHost(t, srv), mustPort(t, srv), srv.HostKeyFingerprint())

	_, err := Connect(context.Background(), baseOptions(t, srv, keys))
	assertErrorCode(t, err, apperror.AuthFailed)
}

// TestConnectKeyboardInteractive proves the client answers every non-echoed
// prompt with the password (Electron rule), leaving only echoed
// non-password prompts empty.
func TestConnectKeyboardInteractive(t *testing.T) {
	srv := sshtest.New(t)
	var gotAnswers []string
	srv.KeyboardOK = func(user string, questions []string, echos []bool, answers []string) bool {
		gotAnswers = answers
		return answers[0] == "secret" && answers[1] == "secret"
	}
	keys := newMemoryHostKeys()
	keys.seed(mustHost(t, srv), mustPort(t, srv), srv.HostKeyFingerprint())

	sess, err := Connect(context.Background(), baseOptions(t, srv, keys))
	if err != nil {
		t.Fatalf("Connect (keyboard-interactive): %v", err)
	}
	defer sess.Close()
	if len(gotAnswers) != 2 || gotAnswers[0] != "secret" || gotAnswers[1] != "secret" {
		t.Fatalf("answers = %q, want [secret secret]", gotAnswers)
	}
}

// TestConnectKeyboardInteractiveElectronMatrix: password is offered when the
// prompt mentions a password OR the prompt is not echoed; an echoed
// non-password prompt stays empty.
func TestConnectKeyboardInteractiveElectronMatrix(t *testing.T) {
	srv := sshtest.New(t)
	srv.KeyboardQuestions = []string{"Password: ", "Verification code: ", "OTP: "}
	srv.KeyboardEchos = []bool{true, false, true}
	var gotAnswers []string
	srv.KeyboardOK = func(user string, questions []string, echos []bool, answers []string) bool {
		gotAnswers = answers
		return answers[0] == "secret" && answers[1] == "secret" && answers[2] == ""
	}
	keys := newMemoryHostKeys()
	keys.seed(mustHost(t, srv), mustPort(t, srv), srv.HostKeyFingerprint())

	sess, err := Connect(context.Background(), baseOptions(t, srv, keys))
	if err != nil {
		t.Fatalf("Connect (keyboard-interactive matrix): %v", err)
	}
	defer sess.Close()
	want := []string{"secret", "secret", ""}
	if len(gotAnswers) != len(want) {
		t.Fatalf("answers = %q, want %q", gotAnswers, want)
	}
	for i := range want {
		if gotAnswers[i] != want[i] {
			t.Fatalf("answers = %q, want %q", gotAnswers, want)
		}
	}
}

// TestKeyboardInteractiveShortEchos: the challenge function must never panic
// when the echos array is shorter than the questions array (defensive: the
// wire protocol always matches lengths, but a malformed peer must not crash
// the client). Missing entries count as not echoed.
func TestKeyboardInteractiveShortEchos(t *testing.T) {
	challenge := keyboardInteractive("secret")
	answers, err := challenge("user", "", []string{"Password: ", "Code: ", "Pin: "}, []bool{true})
	if err != nil {
		t.Fatalf("challenge: %v", err)
	}
	want := []string{"secret", "secret", "secret"}
	if len(answers) != len(want) {
		t.Fatalf("answers = %q, want %q", answers, want)
	}
	for i := range want {
		if answers[i] != want[i] {
			t.Fatalf("answers = %q, want %q", answers, want)
		}
	}
}

// TestConnectNilHostKeys: a missing host-key store is an observable error,
// never a nil-pointer panic and never a dial attempt.
func TestConnectNilHostKeys(t *testing.T) {
	opts := Options{
		Host: "127.0.0.1", Port: 22, Username: "u", AuthMethod: "password",
		Deadline: time.Second,
	}
	opts.Dialer = func(ctx context.Context, network, addr string) (net.Conn, error) {
		t.Fatal("must not dial without a host-key store")
		return nil, nil
	}
	_, err := Connect(context.Background(), opts)
	assertErrorCode(t, err, apperror.Unknown)
}

func TestConnectPrivateKeyFromContent(t *testing.T) {
	srv := sshtest.New(t)
	pub, _, priv := testKeyPair(t)
	srv.PublicKeyOK = func(user string, key ssh.PublicKey) bool {
		return user == "user" && string(key.Marshal()) == string(pub.Marshal())
	}
	keys := newMemoryHostKeys()
	keys.seed(mustHost(t, srv), mustPort(t, srv), srv.HostKeyFingerprint())

	opts := baseOptions(t, srv, keys)
	opts.AuthMethod = "privateKey"
	opts.PrivateKey = marshalTestKey(t, priv)
	sess, err := Connect(context.Background(), opts)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer sess.Close()
}

// TestConnectPrivateKeyFromPath proves the key content is read through the
// injected KeyReader (home-boundary checked by the credentials package).
func TestConnectPrivateKeyFromPath(t *testing.T) {
	srv := sshtest.New(t)
	pub, _, priv := testKeyPair(t)
	srv.PublicKeyOK = func(user string, key ssh.PublicKey) bool {
		return string(key.Marshal()) == string(pub.Marshal())
	}
	keys := newMemoryHostKeys()
	keys.seed(mustHost(t, srv), mustPort(t, srv), srv.HostKeyFingerprint())

	opts := baseOptions(t, srv, keys)
	opts.AuthMethod = "privateKey"
	opts.PrivateKeyPath = "/home/u/.ssh/id"
	opts.KeyReader = func(path string) (string, error) {
		if path != "/home/u/.ssh/id" {
			return "", errors.New("wrong path")
		}
		return marshalTestKey(t, priv), nil
	}
	sess, err := Connect(context.Background(), opts)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer sess.Close()
}

func TestConnectEncryptedKeyIsAuthFailed(t *testing.T) {
	srv := sshtest.New(t)
	keys := newMemoryHostKeys()
	keys.seed(mustHost(t, srv), mustPort(t, srv), srv.HostKeyFingerprint())

	opts := baseOptions(t, srv, keys)
	opts.AuthMethod = "privateKey"
	opts.PrivateKey = encryptedTestKey(t)
	_, err := Connect(context.Background(), opts)
	assertErrorCode(t, err, apperror.AuthFailed)
	if !strings.Contains(strings.ToLower(err.Error()), "passphrase") {
		t.Fatalf("message %q must mention the missing passphrase", err.Error())
	}
}

func TestConnectPrivateKeyPathMissing(t *testing.T) {
	srv := sshtest.New(t)
	keys := newMemoryHostKeys()
	keys.seed(mustHost(t, srv), mustPort(t, srv), srv.HostKeyFingerprint())

	opts := baseOptions(t, srv, keys)
	opts.AuthMethod = "privateKey"
	opts.PrivateKey = ""
	opts.PrivateKeyPath = ""
	_, err := Connect(context.Background(), opts)
	assertErrorCode(t, err, apperror.AuthFailed)
}

// TestConnectAcceptHostKeyUnknown proves acceptHostKey=true accepts and
// records a new key only after the full connect (PTY+shell) succeeds.
func TestConnectAcceptHostKeyUnknown(t *testing.T) {
	srv := sshtest.New(t)
	srv.PasswordOK = func(user, pass string) bool { return true }
	keys := newMemoryHostKeys()

	opts := baseOptions(t, srv, keys)
	opts.AcceptHostKey = true
	sess, err := Connect(context.Background(), opts)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer sess.Close()
	if sess.Fingerprint() != srv.HostKeyFingerprint() {
		t.Fatalf("accepted fingerprint = %q, want %q", sess.Fingerprint(), srv.HostKeyFingerprint())
	}
}

// TestConnectAcceptHostKeyOnlyAfterAuthSuccess: acceptHostKey must NOT
// survive an auth failure — the key is accepted during handshake but the
// caller must not remember it (the sessions layer remembers only after the
// whole connect succeeds). The sshclient contract is exercised here by
// checking that the callback accepted the key while auth still failed.
func TestConnectAcceptHostKeyAuthStillFails(t *testing.T) {
	srv := sshtest.New(t)
	srv.PasswordOK = func(user, pass string) bool { return false }
	keys := newMemoryHostKeys()

	opts := baseOptions(t, srv, keys)
	opts.AcceptHostKey = true
	_, err := Connect(context.Background(), opts)
	assertErrorCode(t, err, apperror.AuthFailed)
}

func TestConnectCancelStalledHandshake(t *testing.T) {
	srv := sshtest.New(t)
	srv.StallHandshake = true
	keys := newMemoryHostKeys()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	start := time.Now()
	done := make(chan error, 1)
	go func() {
		_, err := Connect(ctx, baseOptions(t, srv, keys))
		done <- err
	}()
	time.Sleep(50 * time.Millisecond) // let the dial land
	cancel()
	select {
	case err := <-done:
		if elapsed := time.Since(start); elapsed > 3*time.Second {
			t.Fatalf("cancel took %v, want immediate teardown", elapsed)
		}
		assertErrorCode(t, err, apperror.Cancelled)
	case <-time.After(5 * time.Second):
		t.Fatal("Connect did not return after cancel")
	}
}

func TestConnectTimeoutStalledHandshake(t *testing.T) {
	srv := sshtest.New(t)
	srv.StallHandshake = true
	keys := newMemoryHostKeys()

	opts := baseOptions(t, srv, keys)
	opts.Deadline = 300 * time.Millisecond
	start := time.Now()
	_, err := Connect(context.Background(), opts)
	assertErrorCode(t, err, apperror.Timeout)
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("timeout took %v", elapsed)
	}
}

// RED/GREEN: the hard deadline must bound the WHOLE connect, not just the
// dial+handshake. Here auth succeeds but the server never opens the session
// channel, so openShell's NewSession would hang forever without the deadline.
func TestConnectTimeoutStalledSessionOpen(t *testing.T) {
	srv := sshtest.New(t)
	srv.PasswordOK = func(user, pass string) bool { return true }
	srv.StallSessionOpen = true
	keys := newMemoryHostKeys()
	keys.seed(mustHost(t, srv), mustPort(t, srv), srv.HostKeyFingerprint())

	opts := baseOptions(t, srv, keys)
	opts.Deadline = 300 * time.Millisecond
	before := runtime.NumGoroutine()
	start := time.Now()
	done := make(chan error, 1)
	go func() {
		_, err := Connect(context.Background(), opts)
		done <- err
	}()
	select {
	case err := <-done:
		assertErrorCode(t, err, apperror.Timeout)
		if elapsed := time.Since(start); elapsed > 3*time.Second {
			t.Fatalf("timeout took %v", elapsed)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Connect did not time out on a stalled session open")
	}
	// The openShell goroutine must be joined, not leaked.
	waitForGoroutines(t, before, 3*time.Second)
}

// RED/GREEN: cancelling ctx after auth must abort a stalled pty-req and return
// CANCELLED without leaking the openShell goroutine.
func TestConnectCancelStalledPTY(t *testing.T) {
	srv := sshtest.New(t)
	srv.PasswordOK = func(user, pass string) bool { return true }
	srv.StallPTY = true
	keys := newMemoryHostKeys()
	keys.seed(mustHost(t, srv), mustPort(t, srv), srv.HostKeyFingerprint())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	before := runtime.NumGoroutine()
	done := make(chan error, 1)
	go func() {
		_, err := Connect(ctx, baseOptions(t, srv, keys))
		done <- err
	}()
	// Let the handshake+auth+session-open land so the pty-req is the stalled
	// step, then abort.
	time.Sleep(100 * time.Millisecond)
	start := time.Now()
	cancel()
	select {
	case err := <-done:
		assertErrorCode(t, err, apperror.Cancelled)
		if elapsed := time.Since(start); elapsed > 3*time.Second {
			t.Fatalf("cancel took %v, want immediate teardown", elapsed)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Connect did not return after cancel with a stalled pty-req")
	}
	waitForGoroutines(t, before, 3*time.Second)
}

// waitForGoroutines waits until the goroutine count settles at or below want
// (the server's acceptLoop counts against the baseline).
func waitForGoroutines(t *testing.T, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= want+1 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("goroutines did not settle (want <= %d, got %d)", want+1, runtime.NumGoroutine())
}

func TestConnectConnectionRefused(t *testing.T) {
	keys := newMemoryHostKeys()
	// Reserve a port then close the listener so nothing is bound to it.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	host, port := hostPort(t, addr)
	opts := Options{Host: host, Port: port, Username: "u", AuthMethod: "password", HostKeys: keys, Deadline: 5 * time.Second}
	_, err = Connect(context.Background(), opts)
	assertErrorCode(t, err, apperror.ConnectionRefused)
}

func TestConnectHostNotFound(t *testing.T) {
	keys := newMemoryHostKeys()
	opts := Options{Host: "does-not-exist.invalid", Port: 22, Username: "u", AuthMethod: "password", HostKeys: keys, Deadline: 5 * time.Second}
	_, err := Connect(context.Background(), opts)
	assertErrorCode(t, err, apperror.HostNotFound)
}

// TestConnectPTYAndResize verifies the initial PTY size and window-change.
func TestConnectPTYAndResize(t *testing.T) {
	srv := sshtest.New(t)
	srv.PasswordOK = func(user, pass string) bool { return true }
	sizes := make(chan [2]int, 8)
	exited := make(chan struct{})
	srv.OnShell = sshtest.SizeShell(sizes, exited)
	keys := newMemoryHostKeys()
	keys.seed(mustHost(t, srv), mustPort(t, srv), srv.HostKeyFingerprint())

	opts := baseOptions(t, srv, keys)
	opts.Cols, opts.Rows = 132, 43
	sess, err := Connect(context.Background(), opts)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer sess.Close()

	first := <-sizes
	if first != [2]int{43, 132} {
		t.Fatalf("initial pty size = %v, want [43 132]", first)
	}
	if err := sess.Resize(200, 60); err != nil {
		t.Fatalf("Resize: %v", err)
	}
	second := <-sizes
	if second != [2]int{60, 200} {
		t.Fatalf("resized size = %v, want [60 200]", second)
	}
	if err := sess.Resize(0, 0); err == nil {
		t.Fatal("Resize(0,0) must error")
	}
}

// TestConnectRemoteClose: when the server ends the shell, Wait returns and
// reads hit EOF.
// TestConnectPTYTerminalType: the requested PTY must be xterm-256color (the
// Electron build's term).
func TestConnectPTYTerminalType(t *testing.T) {
	srv := sshtest.New(t)
	srv.PasswordOK = func(user, pass string) bool { return true }
	terms := make(chan string, 1)
	exited := make(chan struct{})
	srv.OnShell = sshtest.TermShell(terms, exited)
	keys := newMemoryHostKeys()
	keys.seed(mustHost(t, srv), mustPort(t, srv), srv.HostKeyFingerprint())

	sess, err := Connect(context.Background(), baseOptions(t, srv, keys))
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer sess.Close()
	select {
	case term := <-terms:
		if term != "xterm-256color" {
			t.Fatalf("pty term = %q, want xterm-256color", term)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no pty-req received")
	}
}

// TestConnectReconnectSafety: a fresh connect right after a session was closed
// must work — no state from the previous connection may leak in.
func TestConnectReconnectSafety(t *testing.T) {
	srv := sshtest.New(t)
	srv.PasswordOK = func(user, pass string) bool { return true }
	keys := newMemoryHostKeys()
	keys.seed(mustHost(t, srv), mustPort(t, srv), srv.HostKeyFingerprint())

	for i := 0; i < 2; i++ {
		sess, err := Connect(context.Background(), baseOptions(t, srv, keys))
		if err != nil {
			t.Fatalf("connect %d: %v", i, err)
		}
		if _, err := sess.Write([]byte("hi\n")); err != nil {
			t.Fatalf("connect %d write: %v", i, err)
		}
		if got, err := readN(t, sess.Stdout(), 3, time.Second); err != nil || string(got) != "hi\n" {
			t.Fatalf("connect %d echo = %q, err %v", i, got, err)
		}
		if err := sess.Close(); err != nil {
			t.Fatalf("connect %d close: %v", i, err)
		}
	}
}

func TestConnectRemoteClose(t *testing.T) {
	srv := sshtest.New(t)
	srv.PasswordOK = func(user, pass string) bool { return true }
	srv.OnShell = func(ch ssh.Channel, reqs <-chan *ssh.Request) {
		shellSeen := make(chan struct{})
		go func() {
			defer close(shellSeen)
			for req := range reqs {
				_ = req.Reply(req.Type == "shell" || req.Type == "pty-req" || req.Type == "exec", nil)
				if req.Type == "shell" {
					return // both PTY and shell are in place; the client is ready
				}
			}
		}()
		<-shellSeen
		_, _ = ch.Write([]byte("bye"))
		_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
		_ = ch.Close()
	}
	keys := newMemoryHostKeys()
	keys.seed(mustHost(t, srv), mustPort(t, srv), srv.HostKeyFingerprint())

	sess, err := Connect(context.Background(), baseOptions(t, srv, keys))
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer sess.Close()
	got, err := readN(t, sess.Stdout(), 3, 2*time.Second)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "bye" {
		t.Fatalf("output = %q, want %q", got, "bye")
	}
	if err := sess.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	srv := sshtest.New(t)
	srv.PasswordOK = func(user, pass string) bool { return true }
	keys := newMemoryHostKeys()
	keys.seed(mustHost(t, srv), mustPort(t, srv), srv.HostKeyFingerprint())

	sess, err := Connect(context.Background(), baseOptions(t, srv, keys))
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if err := sess.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := sess.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestFingerprintMatchesOldTS(t *testing.T) {
	_, signer, _ := testKeyPair(t)
	fp := Fingerprint(signer.PublicKey())
	// Old Electron: createHash('sha256').update(rawKeyBlob).digest('base64')
	sum := sha256.Sum256(signer.PublicKey().Marshal())
	want := base64.StdEncoding.EncodeToString(sum[:])
	if fp != want {
		t.Fatalf("fingerprint = %q, want %q", fp, want)
	}
}

func TestConstants(t *testing.T) {
	if DialTimeout != 10*time.Second {
		t.Fatalf("DialTimeout = %v, want 10s", DialTimeout)
	}
	if HardDeadline != 10*time.Second+500*time.Millisecond {
		t.Fatalf("HardDeadline = %v, want 10.5s", HardDeadline)
	}
	if DefaultCols != 80 || DefaultRows != 24 {
		t.Fatalf("default PTY size = %dx%d, want 80x24", DefaultCols, DefaultRows)
	}
}

// --- exec (T1.6.1) ---

// TestExecStdoutReturns: a non-interactive exec over a fresh session channel
// returns the command's stdout.
func TestExecStdoutReturns(t *testing.T) {
	srv := sshtest.New(t)
	srv.PasswordOK = func(user, pass string) bool { return true }
	srv.OnExec = sshtest.EchoExec
	keys := newMemoryHostKeys()
	keys.seed(mustHost(t, srv), mustPort(t, srv), srv.HostKeyFingerprint())

	sess, err := Connect(context.Background(), baseOptions(t, srv, keys))
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer sess.Close()
	out, err := sess.Exec(context.Background(), "echo hi", 5*time.Second)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if out != "echo hi" {
		t.Fatalf("stdout = %q, want %q", out, "echo hi")
	}
}

// RED/GREEN: exit non-zero with empty stdout is an error with a generic
// message — the raw stderr (which may embed secrets) must never cross back
// into the caller or IPC.
func TestExecNonZeroExitEmptyStdout(t *testing.T) {
	srv := sshtest.New(t)
	srv.PasswordOK = func(user, pass string) bool { return true }
	srv.OnExec = sshtest.ExitExec("", "access token sk-secret-123", 1)
	keys := newMemoryHostKeys()
	keys.seed(mustHost(t, srv), mustPort(t, srv), srv.HostKeyFingerprint())

	sess, err := Connect(context.Background(), baseOptions(t, srv, keys))
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer sess.Close()
	_, err = sess.Exec(context.Background(), "dangerous", 5*time.Second)
	assertErrorCode(t, err, apperror.Unknown)
	if strings.Contains(err.Error(), "sk-secret-123") {
		t.Fatalf("error message %q leaks the raw stderr", err.Error())
	}
}

// RED/GREEN: exit non-zero WITH stdout resolves with the stdout (Electron
// parity) — the exit status only errors when nothing was produced.
func TestExecNonZeroExitWithStdout(t *testing.T) {
	srv := sshtest.New(t)
	srv.PasswordOK = func(user, pass string) bool { return true }
	srv.OnExec = sshtest.ExitExec("partial", "boom", 1)
	keys := newMemoryHostKeys()
	keys.seed(mustHost(t, srv), mustPort(t, srv), srv.HostKeyFingerprint())

	sess, err := Connect(context.Background(), baseOptions(t, srv, keys))
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer sess.Close()
	out, err := sess.Exec(context.Background(), "cmd", 5*time.Second)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if out != "partial" {
		t.Fatalf("stdout = %q, want %q", out, "partial")
	}
}

// RED/GREEN: a stalled command returns TIMEOUT within the timeout and leaves
// no goroutines behind.
func TestExecTimeout(t *testing.T) {
	srv := sshtest.New(t)
	srv.PasswordOK = func(user, pass string) bool { return true }
	srv.OnExec = sshtest.StallExec
	keys := newMemoryHostKeys()
	keys.seed(mustHost(t, srv), mustPort(t, srv), srv.HostKeyFingerprint())

	sess, err := Connect(context.Background(), baseOptions(t, srv, keys))
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer sess.Close()
	before := runtime.NumGoroutine()
	start := time.Now()
	_, err = sess.Exec(context.Background(), "hang", 300*time.Millisecond)
	assertErrorCode(t, err, apperror.Timeout)
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("timeout took %v, want prompt teardown", elapsed)
	}
	waitForGoroutines(t, before, 3*time.Second)
}

// RED/GREEN: cancelling ctx returns CANCELLED promptly and joins every exec
// goroutine.
func TestExecCancel(t *testing.T) {
	srv := sshtest.New(t)
	srv.PasswordOK = func(user, pass string) bool { return true }
	srv.OnExec = sshtest.StallExec
	keys := newMemoryHostKeys()
	keys.seed(mustHost(t, srv), mustPort(t, srv), srv.HostKeyFingerprint())

	sess, err := Connect(context.Background(), baseOptions(t, srv, keys))
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer sess.Close()
	before := runtime.NumGoroutine()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := sess.Exec(ctx, "hang", 30*time.Second)
		done <- err
	}()
	time.Sleep(100 * time.Millisecond) // let the exec land
	start := time.Now()
	cancel()
	select {
	case err := <-done:
		assertErrorCode(t, err, apperror.Cancelled)
		if elapsed := time.Since(start); elapsed > 3*time.Second {
			t.Fatalf("cancel took %v, want prompt teardown", elapsed)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Exec did not return after cancel")
	}
	waitForGoroutines(t, before, 3*time.Second)
}

// RED/GREEN: when the server answers a session channel-open only AFTER the
// exec timeout, Exec returns TIMEOUT and the late session that resolves on
// the wire is closed by the client — the server observes the channel close
// and the active-session count falls back to the terminal baseline — while
// the PTY stays fully usable. A leaked late session would pin a session
// channel until the whole connection closes, eventually exhausting the
// server's session limit under monitor polling.
func TestExecTimeoutLateOpenClosed(t *testing.T) {
	srv := sshtest.New(t)
	srv.PasswordOK = func(user, pass string) bool { return true }
	sizes := make(chan [2]int, 8)
	exited := make(chan struct{})
	srv.OnShell = sshtest.SizeShell(sizes, exited)
	srv.OnExec = sshtest.EchoExec
	keys := newMemoryHostKeys()
	keys.seed(mustHost(t, srv), mustPort(t, srv), srv.HostKeyFingerprint())

	sess, err := Connect(context.Background(), baseOptions(t, srv, keys))
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer sess.Close()

	// Baseline: only the terminal session channel is open.
	waitForCond(t, 10*time.Second, "terminal session served", func() bool {
		return srv.ActiveSessions() >= 1
	})
	if opened := srv.TotalSessionsOpened(); opened != 1 {
		t.Fatalf("sessions opened before exec = %d, want 1 (terminal only)", opened)
	}

	// Delay the next channel-open past the exec timeout: Exec must return
	// TIMEOUT while the open is still pending, then the server answers it.
	srv.SetSessionOpenDelay(500 * time.Millisecond)
	before := runtime.NumGoroutine()
	start := time.Now()
	_, err = sess.Exec(context.Background(), "late-open", 200*time.Millisecond)
	assertErrorCode(t, err, apperror.Timeout)
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("timeout took %v, want prompt teardown", elapsed)
	}

	// The delayed open is eventually confirmed on the wire: the client's
	// NewSession resolves after its caller already gave up. Generous deadline:
	// the server goroutine that answers the open can be starved for seconds
	// under -race -count=10 load.
	waitForCond(t, 10*time.Second, "delayed channel-open confirmed", func() bool {
		return srv.TotalSessionsOpened() >= 2
	})

	// The client must close the late session: the active-session count
	// settles back at the terminal baseline (1) instead of pinning the
	// leaked exec channel until the connection closes.
	waitForCond(t, 10*time.Second, "late session closed by client", func() bool {
		return srv.ActiveSessions() == 1
	})
	waitForGoroutines(t, before, 3*time.Second)

	// The PTY is untouched: it still echoes stdin and accepts resize.
	if _, err := sess.Write([]byte("pty-after\n")); err != nil {
		t.Fatalf("Write after late open: %v", err)
	}
	got, err := readN(t, sess.Stdout(), len("pty-after\n"), 2*time.Second)
	if err != nil {
		t.Fatalf("read PTY echo after late open: %v", err)
	}
	if string(got) != "pty-after\n" {
		t.Fatalf("PTY echo after late open = %q, want %q", got, "pty-after\n")
	}
	if err := sess.Resize(120, 40); err != nil {
		t.Fatalf("Resize after late open: %v", err)
	}
}

// RED/GREEN: the both-ready select race — the delayed channel-open resolves at
// almost the same instant the exec is cancelled, so the NewSession result and
// ctx.Done are BOTH ready when the select runs and Go picks one at random.
// Whichever branch wins, Exec returns CANCELLED and the session channel is
// closed by the client (runExec's ctx teardown when res won, the cleanup
// goroutine when ctx won), so the active-session count settles back at the
// terminal baseline. The cancellation is triggered the moment the server
// accepts the channel-open, so the reply and ctx.Done land in the same
// scheduling window and both select branches are exercised across -count runs.
// Run with -race -count to cover the race semantics.
func TestExecTimeoutLateOpenBothReady(t *testing.T) {
	srv := sshtest.New(t)
	srv.PasswordOK = func(user, pass string) bool { return true }
	sizes := make(chan [2]int, 8)
	exited := make(chan struct{})
	srv.OnShell = sshtest.SizeShell(sizes, exited)
	srv.OnExec = sshtest.StallExec
	keys := newMemoryHostKeys()
	keys.seed(mustHost(t, srv), mustPort(t, srv), srv.HostKeyFingerprint())

	sess, err := Connect(context.Background(), baseOptions(t, srv, keys))
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer sess.Close()

	// Baseline: only the terminal session channel is open.
	waitForCond(t, 3*time.Second, "terminal session served", func() bool {
		return srv.ActiveSessions() >= 1
	})

	// Delay the channel-open so the NewSession result is still in flight when
	// the exec context is cancelled: the reply and ctx.Done become ready
	// together and the select races between them.
	srv.SetSessionOpenDelay(300 * time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Cancel the instant the server confirms the channel-open: the reply is
	// then in flight, so the result and ctx.Done land in the same window.
	// Poll tightly so the cancellation lands within the reply round-trip,
	// making the both-ready window wide enough to hit reliably.
	go func() {
		for srv.TotalSessionsOpened() < 2 {
			time.Sleep(50 * time.Microsecond)
		}
		cancel()
	}()

	before := runtime.NumGoroutine()
	_, err = sess.Exec(ctx, "both-ready", 30*time.Second)
	assertErrorCode(t, err, apperror.Cancelled)

	// Whichever select branch won, the client closes the session channel and
	// the active count settles back at the terminal baseline (1).
	waitForCond(t, 3*time.Second, "no leaked session channel", func() bool {
		return srv.ActiveSessions() == 1
	})
	waitForGoroutines(t, before, 3*time.Second)

	// The PTY is untouched: it still echoes stdin and accepts resize.
	if _, err := sess.Write([]byte("pty-after\n")); err != nil {
		t.Fatalf("Write after both-ready: %v", err)
	}
	got, err := readN(t, sess.Stdout(), len("pty-after\n"), 2*time.Second)
	if err != nil {
		t.Fatalf("read PTY echo after both-ready: %v", err)
	}
	if string(got) != "pty-after\n" {
		t.Fatalf("PTY echo after both-ready = %q, want %q", got, "pty-after\n")
	}
	if err := sess.Resize(120, 40); err != nil {
		t.Fatalf("Resize after both-ready: %v", err)
	}
}

// waitForCond polls until cond is true or the timeout expires.
func waitForCond(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// RED/GREEN: stdout beyond the 2MiB hard cap closes the exec channel and
// returns coded UNKNOWN with a generic message (no command/stdout/path).
func TestExecStdoutOverflow(t *testing.T) {
	srv := sshtest.New(t)
	srv.PasswordOK = func(user, pass string) bool { return true }
	srv.OnExec = sshtest.ExitExec(strings.Repeat("x", 2*1024*1024+64*1024), "", 0)
	keys := newMemoryHostKeys()
	keys.seed(mustHost(t, srv), mustPort(t, srv), srv.HostKeyFingerprint())

	sess, err := Connect(context.Background(), baseOptions(t, srv, keys))
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer sess.Close()
	before := runtime.NumGoroutine()
	_, err = sess.Exec(context.Background(), "cat /dev/zero", 5*time.Second)
	assertErrorCode(t, err, apperror.Unknown)
	if strings.Contains(err.Error(), "/dev/zero") || strings.Contains(err.Error(), "xxx") {
		t.Fatalf("overflow message %q leaks command/output details", err.Error())
	}
	waitForGoroutines(t, before, 3*time.Second)
}

// RED/GREEN: stderr beyond 2MiB neither exhausts memory nor blocks the exec
// channel; stdout still comes back and no stderr content leaks.
func TestExecStderrBounded(t *testing.T) {
	srv := sshtest.New(t)
	srv.PasswordOK = func(user, pass string) bool { return true }
	srv.OnExec = sshtest.ExitExec("ok", strings.Repeat("e", 2*1024*1024+128*1024), 0)
	keys := newMemoryHostKeys()
	keys.seed(mustHost(t, srv), mustPort(t, srv), srv.HostKeyFingerprint())

	sess, err := Connect(context.Background(), baseOptions(t, srv, keys))
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer sess.Close()
	out, err := sess.Exec(context.Background(), "noisy", 5*time.Second)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if out != "ok" {
		t.Fatalf("stdout = %q, want %q", out, "ok")
	}
}

// RED/GREEN: an exec over a fresh channel never touches the interactive PTY
// session — the shell still echoes stdin and accepts resize afterwards.
func TestExecAfterPTYStillReadsWrites(t *testing.T) {
	srv := sshtest.New(t)
	srv.PasswordOK = func(user, pass string) bool { return true }
	sizes := make(chan [2]int, 8)
	exited := make(chan struct{})
	srv.OnShell = sshtest.SizeShell(sizes, exited)
	srv.OnExec = sshtest.EchoExec
	keys := newMemoryHostKeys()
	keys.seed(mustHost(t, srv), mustPort(t, srv), srv.HostKeyFingerprint())

	sess, err := Connect(context.Background(), baseOptions(t, srv, keys))
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer sess.Close()

	out, err := sess.Exec(context.Background(), "cmd-1", 5*time.Second)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if out != "cmd-1" {
		t.Fatalf("stdout = %q, want %q", out, "cmd-1")
	}
	// The PTY shell still echoes stdin and resizes.
	if _, err := sess.Write([]byte("pty-data\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, err := readN(t, sess.Stdout(), len("pty-data\n"), 2*time.Second)
	if err != nil {
		t.Fatalf("read PTY echo: %v", err)
	}
	if string(got) != "pty-data\n" {
		t.Fatalf("PTY echo = %q, want %q", got, "pty-data\n")
	}
	if err := sess.Resize(120, 40); err != nil {
		t.Fatalf("Resize after exec: %v", err)
	}
}

// RED/GREEN: two execs on the same session run independently — each opens its
// own channel and neither disturbs the other.
func TestExecConcurrent(t *testing.T) {
	srv := sshtest.New(t)
	srv.PasswordOK = func(user, pass string) bool { return true }
	srv.OnExec = sshtest.EchoExec
	keys := newMemoryHostKeys()
	keys.seed(mustHost(t, srv), mustPort(t, srv), srv.HostKeyFingerprint())

	sess, err := Connect(context.Background(), baseOptions(t, srv, keys))
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer sess.Close()
	type res struct {
		out string
		err error
	}
	results := make(chan res, 2)
	for _, cmd := range []string{"first", "second"} {
		go func(cmd string) {
			out, err := sess.Exec(context.Background(), cmd, 5*time.Second)
			results <- res{out, err}
		}(cmd)
	}
	got := map[string]bool{}
	for i := 0; i < 2; i++ {
		select {
		case r := <-results:
			if r.err != nil {
				t.Fatalf("concurrent Exec: %v", r.err)
			}
			if got[r.out] {
				t.Fatalf("duplicate stdout %q", r.out)
			}
			got[r.out] = true
		case <-time.After(5 * time.Second):
			t.Fatal("concurrent exec did not complete")
		}
	}
	if !got["first"] || !got["second"] {
		t.Fatalf("stdouts = %v, want both first and second", got)
	}
}

// RED/GREEN: an exec on a closed session fails immediately with coded UNKNOWN.
func TestExecAfterSessionClose(t *testing.T) {
	srv := sshtest.New(t)
	srv.PasswordOK = func(user, pass string) bool { return true }
	srv.OnExec = sshtest.EchoExec
	keys := newMemoryHostKeys()
	keys.seed(mustHost(t, srv), mustPort(t, srv), srv.HostKeyFingerprint())

	sess, err := Connect(context.Background(), baseOptions(t, srv, keys))
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if err := sess.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	_, err = sess.Exec(context.Background(), "cmd", time.Second)
	assertErrorCode(t, err, apperror.Unknown)
}

// RED/GREEN: closing the SSH session mid-exec releases the blocked exec
// promptly and leaves no goroutines behind.
func TestExecSessionCloseReleasesInFlight(t *testing.T) {
	srv := sshtest.New(t)
	srv.PasswordOK = func(user, pass string) bool { return true }
	srv.OnExec = sshtest.StallExec
	keys := newMemoryHostKeys()
	keys.seed(mustHost(t, srv), mustPort(t, srv), srv.HostKeyFingerprint())

	sess, err := Connect(context.Background(), baseOptions(t, srv, keys))
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	before := runtime.NumGoroutine()
	done := make(chan error, 1)
	go func() {
		_, err := sess.Exec(context.Background(), "hang", 30*time.Second)
		done <- err
	}()
	time.Sleep(100 * time.Millisecond) // let the exec land
	start := time.Now()
	if err := sess.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Exec must error when the session closes mid-flight")
		}
		assertErrorCode(t, err, apperror.Unknown)
		if elapsed := time.Since(start); elapsed > 3*time.Second {
			t.Fatalf("release took %v, want prompt unblock", elapsed)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Exec did not unblock after session close")
	}
	waitForGoroutines(t, before, 3*time.Second)
}

// --- helpers ---

// testKeyPair returns an ed25519 signer (for the server's PublicKeyOK
// comparison) plus the raw private key (for re-serializing PEM test input).
func testKeyPair(t *testing.T) (ssh.PublicKey, ssh.Signer, ed25519.PrivateKey) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate ed25519: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	return signer.PublicKey(), signer, priv
}

// marshalTestKey serializes the raw ed25519 key to OpenSSH PEM text.
func marshalTestKey(t *testing.T, priv ed25519.PrivateKey) string {
	t.Helper()
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatalf("MarshalPrivateKey: %v", err)
	}
	return string(pem.EncodeToMemory(block))
}

// encryptedTestKey is a real OpenSSH private key encrypted with the
// passphrase "pw" (generated via MarshalPrivateKeyWithPassphrase).
func encryptedTestKey(t *testing.T) string {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519: %v", err)
	}
	block, err := ssh.MarshalPrivateKeyWithPassphrase(priv, "", []byte("pw"))
	if err != nil {
		t.Fatalf("MarshalPrivateKeyWithPassphrase: %v", err)
	}
	return string(pem.EncodeToMemory(block))
}

// --- helpers ---

// readN reads exactly n bytes from r, failing if they do not arrive before
// the timeout. Bounded — never waits for EOF, so echo-style streams work.
func readN(t *testing.T, r io.Reader, n int, timeout time.Duration) ([]byte, error) {
	t.Helper()
	out := make([]byte, 0, n)
	remaining := n
	for remaining > 0 {
		type res struct {
			n   int
			err error
		}
		ch := make(chan res, 1)
		buf := make([]byte, remaining)
		go func() {
			n, err := r.Read(buf)
			ch <- res{n, err}
		}()
		select {
		case r2 := <-ch:
			if r2.err != nil {
				return out, r2.err
			}
			if r2.n == 0 {
				continue
			}
			out = append(out, buf[:r2.n]...)
			remaining -= r2.n
		case <-time.After(timeout):
			return out, errors.New("timed out waiting for output")
		}
	}
	return out, nil
}

func TestSessionDialWhenClosed(t *testing.T) {
	s := &Session{closed: true}
	_, err := s.Dial("tcp", "127.0.0.1:1")
	var coded *Error
	if !errors.As(err, &coded) || coded.Code != apperror.Unknown {
		t.Fatalf("Dial closed = %v", err)
	}
}

func TestMapForwardError(t *testing.T) {
	err := mapForwardError(errors.New("ssh: rejected: administratively prohibited"))
	var coded *Error
	if !errors.As(err, &coded) || coded.Code != apperror.PermissionDenied {
		t.Fatalf("mapForwardError prohibited = %v", err)
	}
	err = mapForwardError(errors.New("connection reset"))
	if !errors.As(err, &coded) || coded.Code != apperror.Unknown {
		t.Fatalf("mapForwardError other = %v", err)
	}
}
