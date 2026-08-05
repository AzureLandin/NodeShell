//go:build windows

package fonts

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

// fakeRunResult is what the stubbed commandRunner returns for one script.
type fakeRunResult struct {
	out []byte
	err error
}

// fakeCall records one commandRunner invocation.
type fakeCall struct {
	name string
	args []string
}

// stubCommandRunner swaps the package runner seam for a recorder keyed by the
// PowerShell script (the last argument). The original runner is restored on
// cleanup. These tests mutate a package variable and therefore must not call
// t.Parallel.
func stubCommandRunner(t *testing.T, byScript map[string]fakeRunResult) *[]fakeCall {
	t.Helper()
	orig := commandRunner
	var calls []fakeCall
	commandRunner = func(ctx context.Context, limit int, name string, args ...string) ([]byte, error) {
		calls = append(calls, fakeCall{name: name, args: append([]string(nil), args...)})
		res, ok := byScript[args[len(args)-1]]
		if !ok {
			return nil, errors.New("fonts: unexpected script")
		}
		return res.out, res.err
	}
	t.Cleanup(func() { commandRunner = orig })
	return &calls
}

// psCallArgs is the PowerShell invocation arguments (exclusive of the
// executable, which the runner receives separately as name) for a script.
func psCallArgs(script string) []string {
	return []string{"-NoProfile", "-NonInteractive", "-Command", script}
}

func TestPlatformListFallsBackToRegistryOnPresentationCoreError(t *testing.T) {
	calls := stubCommandRunner(t, map[string]fakeRunResult{
		psPresentationCoreScript: {err: errors.New("presentation core unavailable")},
		psRegistryScript: {
			out: []byte("Arial (TrueType)\r\nCourier New (TrueType)\r\nC:\\Windows\\Fonts\\arial.ttf\r\n"),
		},
	})
	got, err := platformList(context.Background())
	if err != nil {
		t.Fatalf("platformList: %v", err)
	}
	// The registry output is parsed (metadata suffixes stripped, paths
	// dropped) but not yet normalized — dedupe/sort stay in List.
	want := []string{"Arial", "Courier New"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("platformList = %#v, want %#v", got, want)
	}
	wantCalls := []fakeCall{
		{name: "powershell.exe", args: psCallArgs(psPresentationCoreScript)},
		{name: "powershell.exe", args: psCallArgs(psRegistryScript)},
	}
	if !reflect.DeepEqual(*calls, wantCalls) {
		t.Fatalf("commandRunner calls = %#v, want %#v", *calls, wantCalls)
	}
}

func TestPlatformListSkipsRegistryWhenPresentationCoreSucceeds(t *testing.T) {
	calls := stubCommandRunner(t, map[string]fakeRunResult{
		psPresentationCoreScript: {out: []byte("Arial\r\nConsolas\r\n")},
	})
	got, err := platformList(context.Background())
	if err != nil {
		t.Fatalf("platformList: %v", err)
	}
	if !reflect.DeepEqual(got, []string{"Arial", "Consolas"}) {
		t.Fatalf("platformList = %#v, want %#v", got, []string{"Arial", "Consolas"})
	}
	if len(*calls) != 1 || (*calls)[0].args[len((*calls)[0].args)-1] != psPresentationCoreScript {
		t.Fatalf("commandRunner calls = %#v, want exactly one PresentationCore call", *calls)
	}
}

func TestPlatformListFallsBackOnEmptyPresentationCore(t *testing.T) {
	calls := stubCommandRunner(t, map[string]fakeRunResult{
		psPresentationCoreScript: {out: []byte("\r\n")},
		psRegistryScript: {
			out: []byte("Segoe UI Variable Display (TrueType)\r\nModern (All res)\r\n"),
		},
	})
	got, err := platformList(context.Background())
	if err != nil {
		t.Fatalf("platformList: %v", err)
	}
	want := []string{"Segoe UI Variable Display", "Modern"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("platformList = %#v, want %#v", got, want)
	}
	if len(*calls) != 2 {
		t.Fatalf("commandRunner calls = %#v, want exactly two calls", *calls)
	}
}

func TestPlatformListReturnsRegistryErrorWhenBothFail(t *testing.T) {
	registryErr := errors.New("registry unreadable")
	calls := stubCommandRunner(t, map[string]fakeRunResult{
		psPresentationCoreScript: {err: errors.New("presentation core unavailable")},
		psRegistryScript:         {err: registryErr},
	})
	if _, err := platformList(context.Background()); !errors.Is(err, registryErr) {
		t.Fatalf("platformList error = %v, want the registry error", err)
	}
	if len(*calls) != 2 {
		t.Fatalf("commandRunner calls = %#v, want both attempts made", *calls)
	}
}
