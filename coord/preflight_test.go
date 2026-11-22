package coord

import (
	"context"
	"errors"
	"testing"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestPreflightNodePassesWithCompletePoolAndExactPodUID(t *testing.T) {
	cfg := Config{Prefix: "iscsi-test", ShardCount: 32}
	objects := leasePoolObjects(t, cfg)
	objects = append(objects, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name: "node-a", Namespace: testNamespace, UID: types.UID("uid-a"),
	}})
	client := fake.NewSimpleClientset(objects...)
	m := newTestManager(t, client, cfg, testIdentity("node-a", "uid-a"))
	if err := m.Preflight(context.Background()); err != nil {
		t.Fatalf("Preflight: %v", err)
	}
}

func TestPreflightFailsWhenShardMissing(t *testing.T) {
	cfg := Config{Prefix: "fc-test", ShardCount: 32}
	objects := leasePoolObjects(t, cfg)
	objects = objects[1:]
	objects = append(objects, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name: "node-a", Namespace: testNamespace, UID: types.UID("uid-a"),
	}})
	client := fake.NewSimpleClientset(objects...)
	m := newTestManager(t, client, cfg, testIdentity("node-a", "uid-a"))
	if err := m.Preflight(context.Background()); err == nil {
		t.Fatal("Preflight should fail when a Lease shard is missing")
	}
}

func TestPreflightFailsOnPodUIDMismatch(t *testing.T) {
	cfg := Config{Prefix: "fc-test", ShardCount: 32}
	objects := leasePoolObjects(t, cfg)
	objects = append(objects, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name: "node-a", Namespace: testNamespace, UID: types.UID("different-uid"),
	}})
	client := fake.NewSimpleClientset(objects...)
	m := newTestManager(t, client, cfg, testIdentity("node-a", "uid-a"))
	if err := m.Preflight(context.Background()); err == nil {
		t.Fatal("Preflight should fail on Pod UID mismatch")
	}
}

func TestPreflightControllerDoesNotRequirePodGet(t *testing.T) {
	cfg := Config{Prefix: "shared-test", ShardCount: 32}
	client := fake.NewSimpleClientset(leasePoolObjects(t, cfg)...)
	identity := ProcessIdentity{Role: RoleController, PodName: "controller-a", PodUID: "uid-c"}
	m := newTestManager(t, client, cfg, identity)
	client.PrependReactor("get", "pods", func(action ktesting.Action) (bool, runtime.Object, error) {
		t.Fatal("controller preflight must not read Pods")
		return true, nil, nil
	})
	if err := m.Preflight(context.Background()); err != nil {
		t.Fatalf("controller Preflight: %v", err)
	}
}

func TestDestructiveTakeoverPodForbiddenFailsClosed(t *testing.T) {
	cfg := Config{Prefix: "fc-test", ShardCount: 32}
	key := "volume:format-forbidden"
	old := Holder{Role: RoleNode, PodName: "old-node", PodUID: "old-uid", NodeName: "worker-old", Safety: SafetyDestructive, Operation: OperationFilesystem}
	client := fake.NewSimpleClientset(heldLease(t, cfg, key, old, time.Now().Add(-10*time.Second), 1))
	client.PrependReactor("get", "pods", func(action ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "old-node", errors.New("forbidden"))
	})
	m := newTestManager(t, client, cfg, testIdentity("node-b", "uid-b"))
	_, err := m.Acquire(context.Background(), key, AcquireOptions{Safety: SafetyDestructive, Operation: OperationFilesystem})
	if !errors.Is(err, ErrUnsafeTakeover) {
		t.Fatalf("Acquire = %v, want ErrUnsafeTakeover", err)
	}
}

// Compile-time guard: preflight operates on coordination.k8s.io/v1 Lease.
var _ = coordinationv1.Lease{}
