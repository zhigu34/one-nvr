package gatewaycontrol

import (
	"context"
	"errors"
	"github.com/zhigu34/one-nvr/internal/id"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type testRuntime struct {
	prepared         Evidence
	served           Evidence
	expected         map[id.ID]Evidence
	calls            int
	wrongReload      bool
	crashAfterReload bool
}

func (r *testRuntime) Prepare(_ context.Context, target id.ID) (Evidence, error) {
	r.calls++
	if target == "" {
		return Evidence{}, nil
	}
	e, ok := r.expected[target]
	if !ok {
		return Evidence{}, errors.New("fixture missing")
	}
	r.prepared = e
	return e, nil
}
func (r *testRuntime) Switch(_ context.Context, target id.ID) error {
	r.calls++
	if target == "" {
		r.prepared = Evidence{}
	} else {
		r.prepared = r.expected[target]
	}
	return nil
}
func (r *testRuntime) Reload(context.Context) error {
	r.calls++
	if !r.wrongReload {
		r.served = r.prepared
	}
	if r.crashAfterReload {
		panic("simulated abrupt process exit after reload")
	}
	return nil
}
func (r *testRuntime) Probe(context.Context) (Evidence, error) { r.calls++; return r.served, nil }

func testController(t *testing.T) (*Controller, *testRuntime, id.ID, id.ID) {
	t.Helper()
	a, _ := id.New()
	b, _ := id.New()
	runtime := &testRuntime{expected: map[id.ID]Evidence{a: {LeafSHA256: "leaf-a", ChainSHA256: "chain-a"}, b: {LeafSHA256: "leaf-b", ChainSHA256: "chain-b"}}}
	c := &Controller{Directory: filepath.Join(t.TempDir(), "control"), Runtime: runtime, VerifyTimeout: 5 * time.Millisecond}
	job, _ := id.New()
	result, err := c.Apply(context.Background(), ApplyRequest{JobID: job, CertificateID: a, Operation: "apply"})
	if err != nil || result.State != "applied" {
		t.Fatalf("initial fixture apply %+v %v", result, err)
	}
	return c, runtime, a, b
}

func TestInvalidRequestNeverExecutes(t *testing.T) {
	c, runtime, a, b := testController(t)
	job, _ := id.New()
	wrong, _ := id.New()
	for _, request := range []ApplyRequest{{JobID: job, CertificateID: id.ID("../../escape"), ExpectedActiveID: &a, Operation: "apply"}, {JobID: id.ID("../job"), CertificateID: b, ExpectedActiveID: &a, Operation: "apply"}, {JobID: job, CertificateID: b, ExpectedActiveID: &wrong, Operation: "apply"}, {JobID: job, CertificateID: b, ExpectedActiveID: &a, Operation: "shell"}} {
		before := runtime.calls
		if _, err := c.Apply(context.Background(), request); err == nil {
			t.Fatal("unsafe request accepted")
		}
		if runtime.calls != before {
			t.Fatal("unsafe request executed runtime")
		}
	}
}
func TestReloadExitZeroWrongFingerprintFails(t *testing.T) {
	c, runtime, a, b := testController(t)
	runtime.wrongReload = true
	job, _ := id.New()
	result, err := c.Apply(context.Background(), ApplyRequest{JobID: job, CertificateID: b, ExpectedActiveID: &a, Operation: "apply"})
	if err == nil || result.State != "failed" || result.ActiveID == nil || *result.ActiveID != a {
		t.Fatalf("false reload success %+v %v", result, err)
	}
	state, err := c.State()
	if err != nil || state.ActiveID == nil || *state.ActiveID != a {
		t.Fatal("old version not retained", err)
	}
}
func TestRestartRecoversSwitchedButUncommitted(t *testing.T) {
	c, runtime, a, b := testController(t)
	job, _ := id.New()
	request := ApplyRequest{JobID: job, CertificateID: b, ExpectedActiveID: &a, Operation: "apply"}
	runtime.crashAfterReload = true
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("fixture did not crash")
			}
		}()
		_, _ = c.Apply(context.Background(), request)
	}()
	runtime.crashAfterReload = false
	restarted := &Controller{Directory: c.Directory, Runtime: runtime, VerifyTimeout: 5 * time.Millisecond}
	if err := restarted.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	state, err := restarted.State()
	if err != nil || state.ActiveID == nil || *state.ActiveID != b || state.PreviousID == nil || *state.PreviousID != a {
		t.Fatalf("crash recovery %+v %v", state, err)
	}
	before := runtime.calls
	result, err := restarted.Apply(context.Background(), request)
	if err != nil || result.State != "applied" || runtime.calls != before {
		t.Fatal("duplicate reloaded reconciled version", err)
	}
}
func TestChainOnlyWrongReloadFails(t *testing.T) {
	c, runtime, a, b := testController(t)
	runtime.expected[b] = Evidence{LeafSHA256: runtime.expected[a].LeafSHA256, ChainSHA256: "updated-chain"}
	runtime.wrongReload = true
	job, _ := id.New()
	if result, err := c.Apply(context.Background(), ApplyRequest{JobID: job, CertificateID: b, ExpectedActiveID: &a, Operation: "apply"}); err == nil || result.State != "failed" {
		t.Fatal("same-leaf stale chain accepted", err)
	}
}

func TestUnverifiedRollbackKeepsIntentWithoutTerminalReceipt(t *testing.T) {
	c, runtime, a, b := testController(t)
	job, _ := id.New()
	request := ApplyRequest{JobID: job, CertificateID: b, ExpectedActiveID: &a, Operation: "apply"}
	runtime.served = Evidence{LeafSHA256: "unknown", ChainSHA256: "unknown"}
	runtime.wrongReload = true
	if _, err := c.Apply(context.Background(), request); err == nil {
		t.Fatal("unknown listener accepted")
	}
	if _, err := os.Stat(filepath.Join(c.Directory, "results", string(job)+".json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unverified rollback published a terminal receipt", err)
	}
	if _, err := os.Stat(filepath.Join(c.Directory, "intent.json")); err != nil {
		t.Fatal("recovery intent lost", err)
	}
	runtime.served = runtime.expected[b]
	if err := c.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	result, err := (Mailbox{Directory: c.Directory}).Wait(context.Background(), request)
	if err != nil || result.State != "applied" {
		t.Fatal("verified recovery did not finish original intent", err)
	}
}
