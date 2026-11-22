package coord

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// Regression test for bounded startup throughput: preflight issues up to
// 128 sequential Lease GETs and must fit inside startupPreflightTimeout.
// client-go defaults (QPS 5, burst 10) can exceed the deadline. The fake
// clientset applies no rate limiting, so the pacing is asserted on the
// constructed rest.Config plus a worst-case arithmetic guard.
func TestNewCoordinatorFromEnvironmentSetsBoundedClientThroughput(t *testing.T) {
	t.Setenv("NGX_COORDINATION_LEASE_PREFIX", "test-csi-ngxstorage")
	t.Setenv("NGX_COORDINATION_SHARD_COUNT", "128")
	t.Setenv("POD_NAMESPACE", "storage")
	t.Setenv("POD_NAME", "controller-0")
	t.Setenv("POD_UID", "uid-1")

	var got *rest.Config
	stubInClusterConstruction(t, &got)

	if _, err := NewCoordinatorFromEnvironment("controller"); err != nil {
		t.Fatalf("NewCoordinatorFromEnvironment() error = %v", err)
	}
	if got == nil {
		t.Fatal("clientset constructor did not receive the rest config")
	}
	if got.QPS != coordinationClientQPS || got.Burst != coordinationClientBurst {
		t.Fatalf("client throughput = QPS %v burst %v, want QPS %v burst %v",
			got.QPS, got.Burst, coordinationClientQPS, coordinationClientBurst)
	}
	// Burst requests go immediately, the remainder is paced at QPS. The full
	// pool (128 Lease GETs + 1 Pod GET for node roles) must fit in half the
	// startup deadline to leave margin for API latency.
	worst := time.Duration(float64(129-got.Burst) / float64(got.QPS) * float64(time.Second))
	if worst >= startupPreflightTimeout/2 {
		t.Fatalf("worst-case preflight %v needs half of startup deadline %v", worst, startupPreflightTimeout)
	}
}

func TestNewCoordinatorFromEnvironmentNodeRoleRequiresNodeName(t *testing.T) {
	t.Setenv("NGX_COORDINATION_LEASE_PREFIX", "test-csi-ngxstorage")
	t.Setenv("POD_NAMESPACE", "storage")
	t.Setenv("POD_NAME", "node-0")
	t.Setenv("POD_UID", "uid-1")
	t.Setenv("KUBE_NODE_NAME", "")

	if _, err := NewCoordinatorFromEnvironment("node"); err == nil || !strings.Contains(err.Error(), "KUBE_NODE_NAME is required") {
		t.Fatalf("node role without node name error = %v", err)
	}
}

func TestNewCoordinatorFromEnvironmentRejectsUnknownRole(t *testing.T) {
	t.Setenv("NGX_COORDINATION_LEASE_PREFIX", "test-csi-ngxstorage")
	t.Setenv("POD_NAMESPACE", "storage")
	t.Setenv("POD_NAME", "controller-0")
	t.Setenv("POD_UID", "uid-1")

	if _, err := NewCoordinatorFromEnvironment("sidecar"); err == nil || !strings.Contains(err.Error(), "unsupported driver role") {
		t.Fatalf("unknown role error = %v", err)
	}
}

func TestNewControllerCoordinatorFromEnvironmentIsControllerOnly(t *testing.T) {
	if _, err := NewControllerCoordinatorFromEnvironment("node"); err == nil || !strings.Contains(err.Error(), "unsupported controller-only coordination role") {
		t.Fatalf("node coordinator error = %v", err)
	}
}

func TestNewControllerCoordinatorFromEnvironmentValidatesBeforeKubernetesClient(t *testing.T) {
	t.Setenv("NGX_COORDINATION_LEASE_PREFIX", "test-csi-ngxstorage")
	t.Setenv("NGX_COORDINATION_SHARD_COUNT", "17")
	t.Setenv("POD_NAMESPACE", "storage")
	t.Setenv("POD_NAME", "controller-0")
	t.Setenv("POD_UID", "uid-1")

	if _, err := NewControllerCoordinatorFromEnvironment("controller"); err == nil || !strings.Contains(err.Error(), "unsupported shard count 17") {
		t.Fatalf("invalid shard count error = %v", err)
	}
}

func TestNewControllerCoordinatorFromEnvironmentRequiresLeasePrefix(t *testing.T) {
	t.Setenv("NGX_COORDINATION_LEASE_PREFIX", "")
	if _, err := NewControllerCoordinatorFromEnvironment("controller"); err == nil || !strings.Contains(err.Error(), "NGX_COORDINATION_LEASE_PREFIX is required") {
		t.Fatalf("missing prefix error = %v", err)
	}
}

func TestNewControllerCoordinatorFromEnvironmentSetsBoundedClientThroughput(t *testing.T) {
	t.Setenv("NGX_COORDINATION_LEASE_PREFIX", "test-csi-ngxstorage")
	t.Setenv("NGX_COORDINATION_SHARD_COUNT", "128")
	t.Setenv("POD_NAMESPACE", "storage")
	t.Setenv("POD_NAME", "controller-0")
	t.Setenv("POD_UID", "uid-1")

	var got *rest.Config
	stubInClusterConstruction(t, &got)

	if _, err := NewControllerCoordinatorFromEnvironment("controller"); err != nil {
		t.Fatalf("NewControllerCoordinatorFromEnvironment() error = %v", err)
	}
	if got == nil {
		t.Fatal("clientset constructor did not receive the rest config")
	}
	if got.QPS != coordinationClientQPS || got.Burst != coordinationClientBurst {
		t.Fatalf("client throughput = QPS %v burst %v, want QPS %v burst %v",
			got.QPS, got.Burst, coordinationClientQPS, coordinationClientBurst)
	}
	worst := time.Duration(float64(128-got.Burst) / float64(got.QPS) * float64(time.Second))
	if worst >= startupPreflightTimeout/2 {
		t.Fatalf("worst-case preflight %v needs half of startup deadline %v", worst, startupPreflightTimeout)
	}
}

// stubInClusterConstruction replaces the in-cluster seams and stores the
// rest.Config the factory handed to the clientset constructor in got.
func stubInClusterConstruction(t *testing.T, got **rest.Config) {
	t.Helper()
	oldInCluster := inClusterConfig
	oldNewClient := newClientset
	t.Cleanup(func() {
		inClusterConfig = oldInCluster
		newClientset = oldNewClient
	})

	inClusterConfig = func() (*rest.Config, error) { return &rest.Config{}, nil }
	newClientset = func(config *rest.Config) (kubernetes.Interface, error) {
		*got = config
		// Zero Clientset implements kubernetes.Interface without calling any
		// API: construction never touches the client, and the fake clientset
		// would drag a test-only dependency closure into go.mod.
		return &kubernetes.Clientset{}, nil
	}
}

type recordingCoordinator struct {
	deadline    time.Time
	hasDeadline bool
	err         error
}

func (r *recordingCoordinator) Acquire(context.Context, string, AcquireOptions) (Lock, error) {
	return nil, errors.New("recordingCoordinator: Acquire not expected")
}

func (r *recordingCoordinator) AcquireMany(context.Context, []string, AcquireOptions) (LockSet, error) {
	return nil, errors.New("recordingCoordinator: AcquireMany not expected")
}

func (r *recordingCoordinator) Preflight(ctx context.Context) error {
	r.deadline, r.hasDeadline = ctx.Deadline()
	return r.err
}

func TestPreflightCoordinatorBoundsDeadlineAndPropagatesError(t *testing.T) {
	wantErr := errors.New("preflight failed")
	recorder := &recordingCoordinator{err: wantErr}

	if err := PreflightCoordinator(context.Background(), recorder); !errors.Is(err, wantErr) {
		t.Fatalf("PreflightCoordinator() error = %v, want %v", err, wantErr)
	}
	if !recorder.hasDeadline {
		t.Fatal("preflight context has no deadline")
	}
	remaining := time.Until(recorder.deadline)
	if remaining <= 0 || remaining > startupPreflightTimeout {
		t.Fatalf("preflight deadline remaining = %v, want within (0, %v]", remaining, startupPreflightTimeout)
	}
}

func TestPreflightCoordinatorRequiresCoordinator(t *testing.T) {
	if err := PreflightCoordinator(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "coordinator is required") {
		t.Fatalf("nil coordinator error = %v", err)
	}
}
