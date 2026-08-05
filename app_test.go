package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"nodeshell/internal/credentials"
	"nodeshell/internal/hosts"
	"nodeshell/internal/knownhosts"
	"nodeshell/internal/settings"
)

func testApp(t *testing.T) (*App, string) {
	t.Helper()
	dir := t.TempDir()
	a := NewAppWithServices(dir, hosts.New(dir), settings.New(dir), knownhosts.New(dir), credentials.New(&fakeKeyring{}), credentials.NewHomeReader(dir), dir)
	return a, dir
}

func TestHostsListDelegates(t *testing.T) {
	a, _ := testApp(t)
	created, err := a.HostsCreate(hosts.HostInput{Name: "lab", Host: "h", Port: 22, Username: "u", AuthMethod: "password"})
	if err != nil {
		t.Fatalf("HostsCreate: %v", err)
	}
	got, err := a.HostsList()
	if err != nil {
		t.Fatalf("HostsList: %v", err)
	}
	if len(got) != 1 || got[0].Id != created.Id || got[0].Name != "lab" {
		t.Fatalf("HostsList = %+v, want the created host", got)
	}
}

func TestHostsCreateDelegatesAndPersists(t *testing.T) {
	a, dir := testApp(t)
	h, err := a.HostsCreate(hosts.HostInput{Name: "lab", Host: "192.168.1.10", Port: 22, Username: "root", AuthMethod: "password"})
	if err != nil {
		t.Fatalf("HostsCreate: %v", err)
	}
	if h.Id == "" {
		t.Fatal("HostsCreate must return a host with an id")
	}
	// A fresh store over the same dir must see the persisted host.
	again, err := hosts.New(dir).List()
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 1 || again[0].Id != h.Id {
		t.Fatalf("persisted hosts = %+v", again)
	}
}

func TestHostsUpdateDelegates(t *testing.T) {
	a, _ := testApp(t)
	h, err := a.HostsCreate(hosts.HostInput{Name: "lab", Host: "h", Port: 22, Username: "u", AuthMethod: "password"})
	if err != nil {
		t.Fatal(err)
	}
	name := "prod"
	updated, err := a.HostsUpdate(h.Id, hosts.Patch{Name: &name})
	if err != nil {
		t.Fatalf("HostsUpdate: %v", err)
	}
	if updated.Name != "prod" || updated.Id != h.Id {
		t.Fatalf("HostsUpdate = %+v", updated)
	}
}

func TestHostsRemoveDelegates(t *testing.T) {
	a, _ := testApp(t)
	h, err := a.HostsCreate(hosts.HostInput{Name: "lab", Host: "h", Port: 22, Username: "u", AuthMethod: "password"})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.HostsRemove(h.Id); err != nil {
		t.Fatalf("HostsRemove: %v", err)
	}
	got, err := a.HostsList()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("hosts after remove = %+v", got)
	}
}

func TestHostsUpdateUnknownIDErrorIsObservable(t *testing.T) {
	a, _ := testApp(t)
	name := "x"
	_, err := a.HostsUpdate("nope", hosts.Patch{Name: &name})
	if err == nil {
		t.Fatal("HostsUpdate of unknown id must error, not fake success")
	}
	var e *hosts.Error
	if !errors.As(err, &e) || e.Code != "UNKNOWN" || !strings.Contains(e.Message, "nope") {
		t.Fatalf("error = %v, want hosts.Error UNKNOWN mentioning the id", err)
	}
}

func TestSettingsGetDelegates(t *testing.T) {
	a, _ := testApp(t)
	got, err := a.SettingsGet()
	if err != nil {
		t.Fatalf("SettingsGet: %v", err)
	}
	if got != settings.Defaults {
		t.Fatalf("SettingsGet = %+v, want defaults", got)
	}
}

func TestSettingsSetDelegatesAndPersists(t *testing.T) {
	a, dir := testApp(t)
	lang := "en"
	got, err := a.SettingsSet(settings.Patch{Language: &lang})
	if err != nil {
		t.Fatalf("SettingsSet: %v", err)
	}
	if got.Language != "en" || got.ThemePreference != "system" {
		t.Fatalf("SettingsSet = %+v", got)
	}
	again, err := settings.New(dir).Get()
	if err != nil {
		t.Fatal(err)
	}
	if again.Language != "en" {
		t.Fatalf("persisted settings = %+v", again)
	}
}

func TestUninitialisedAppReturnsObservableErrors(t *testing.T) {
	a := NewApp()
	if _, err := a.HostsList(); err == nil {
		t.Fatal("HostsList on uninitialised App must error")
	}
	if _, err := a.SettingsGet(); err == nil {
		t.Fatal("SettingsGet on uninitialised App must error")
	}
	if _, err := a.HostsCreate(hosts.HostInput{}); err == nil {
		t.Fatal("HostsCreate on uninitialised App must error")
	}
}

func TestStartupInitialisesDataDir(t *testing.T) {
	orig := resolveDataDir
	defer func() { resolveDataDir = orig }()
	dir := t.TempDir()
	resolveDataDir = func() (string, error) { return dir, nil }

	a := NewApp()
	a.startup(context.Background())

	got, err := a.HostsList()
	if err != nil {
		t.Fatalf("HostsList after startup: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("hosts after startup = %+v", got)
	}
	s, err := a.SettingsGet()
	if err != nil {
		t.Fatalf("SettingsGet after startup: %v", err)
	}
	if s != settings.Defaults {
		t.Fatalf("settings after startup = %+v", s)
	}
}

func TestStartupFailureLeavesObservableFailure(t *testing.T) {
	orig := resolveDataDir
	defer func() { resolveDataDir = orig }()
	resolveDataDir = func() (string, error) { return "", errors.New("no home") }

	a := NewApp()
	a.startup(context.Background())
	if _, err := a.HostsList(); err == nil {
		t.Fatal("HostsList must error when data dir resolution failed at startup")
	}
}

func TestInjectedAppStartupIsNoOp(t *testing.T) {
	a, dir := testApp(t)
	a.startup(context.Background())
	if a.dataDir != dir {
		t.Fatalf("startup must not clobber an injected app, dataDir = %q", a.dataDir)
	}
}

// TestStartupRaceWithConcurrentCalls drives the Wails startup path
// concurrently with bound-method calls (which can happen if the WebView calls
// back before OnStartup finishes). The service pointers must be published
// safely; calls before startup error observably, and calls after startup
// succeed rather than faking success.
func TestStartupRaceWithConcurrentCalls(t *testing.T) {
	orig := resolveDataDir
	defer func() { resolveDataDir = orig }()
	dir := t.TempDir()
	resolveDataDir = func() (string, error) { return dir, nil }

	a := NewApp()
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				_, _ = a.HostsList()
				_, _ = a.SettingsGet()
			}
		}()
	}
	a.startup(context.Background())
	wg.Wait()

	got, err := a.HostsList()
	if err != nil {
		t.Fatalf("HostsList after startup: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("hosts after startup = %+v, want empty", got)
	}
	s, err := a.SettingsGet()
	if err != nil {
		t.Fatalf("SettingsGet after startup: %v", err)
	}
	if s != settings.Defaults {
		t.Fatalf("settings after startup = %+v", s)
	}
}
