package coord

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync/atomic"
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

const testNamespace = "ngxstorage-test"

func testIdentity(name, uid string) ProcessIdentity {
	return ProcessIdentity{
		Role:     RoleNode,
		PodName:  name,
		PodUID:   uid,
		NodeName: name + "-node",
	}
}

func testAcquireOptions() AcquireOptions {
	return AcquireOptions{Safety: SafetyOrdinary, Operation: OperationOther}
}

func emptyLease(name string) *coordinationv1.Lease {
	return &coordinationv1.Lease{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace}}
}

func leasePoolObjects(t *testing.T, cfg Config) []runtime.Object {
	t.Helper()
	names, err := cfg.LeaseNames()
	if err != nil {
		t.Fatal(err)
	}
	objects := make([]runtime.Object, 0, len(names))
	for _, name := range names {
		objects = append(objects, emptyLease(name))
	}
	return objects
}

func newTestManager(t *testing.T, client *fake.Clientset, cfg Config, identity ProcessIdentity) *Manager {
	t.Helper()
	m, err := NewManager(ManagerOptions{
		Client:        client,
		Namespace:     testNamespace,
		Config:        cfg,
		Identity:      identity,
		LeaseDuration: 2 * time.Second,
		RenewInterval: 20 * time.Millisecond,
		RetryInterval: 5 * time.Millisecond,
		TakeoverGrace: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return m
}

func TestAcquireRelease(t *testing.T) {
	cfg := Config{Prefix: "iscsi-test", ShardCount: 32}
	key := "volume:one"
	name, _ := cfg.LeaseName(key)
	client := fake.NewSimpleClientset(emptyLease(name))
	m := newTestManager(t, client, cfg, testIdentity("node-a", "uid-a"))

	lock, err := m.Acquire(context.Background(), key, testAcquireOptions())
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if err := lock.AssertOwned(context.Background()); err != nil {
		t.Fatalf("AssertOwned: %v", err)
	}
	lease, _ := client.CoordinationV1().Leases(testNamespace).Get(context.Background(), name, metav1.GetOptions{})
	if lease.Spec.HolderIdentity == nil || *lease.Spec.HolderIdentity == "" {
		t.Fatal("Lease has no holder after acquire")
	}
	if err := lock.Release(context.Background()); err != nil {
		t.Fatalf("Release: %v", err)
	}
	lease, _ = client.CoordinationV1().Leases(testNamespace).Get(context.Background(), name, metav1.GetOptions{})
	if lease.Spec.HolderIdentity == nil || *lease.Spec.HolderIdentity != "" {
		t.Fatalf("holder after release = %v, want empty", lease.Spec.HolderIdentity)
	}
}

func TestTwoContendersSerialize(t *testing.T) {
	cfg := Config{Prefix: "iscsi-test", ShardCount: 32}
	key := "volume:shared"
	name, _ := cfg.LeaseName(key)
	client := fake.NewSimpleClientset(emptyLease(name))
	a := newTestManager(t, client, cfg, testIdentity("node-a", "uid-a"))
	b := newTestManager(t, client, cfg, testIdentity("node-b", "uid-b"))

	first, err := a.Acquire(context.Background(), key, testAcquireOptions())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := b.Acquire(ctx, key, testAcquireOptions()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second contender error = %v, want deadline", err)
	}
	if err := first.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
	second, err := b.Acquire(context.Background(), key, testAcquireOptions())
	if err != nil {
		t.Fatalf("second acquire after release: %v", err)
	}
	if err := second.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestAcquireRetriesResourceVersionConflict(t *testing.T) {
	cfg := Config{Prefix: "fc-test", ShardCount: 32}
	key := "volume:conflict"
	name, _ := cfg.LeaseName(key)
	client := fake.NewSimpleClientset(emptyLease(name))
	var updates atomic.Int32
	client.PrependReactor("update", "leases", func(action ktesting.Action) (bool, runtime.Object, error) {
		if updates.Add(1) == 1 {
			return true, nil, apierrors.NewConflict(
				schema.GroupResource{Group: "coordination.k8s.io", Resource: "leases"},
				name,
				errors.New("forced conflict"),
			)
		}
		return false, nil, nil
	})
	m := newTestManager(t, client, cfg, testIdentity("node-a", "uid-a"))
	lock, err := m.Acquire(context.Background(), key, testAcquireOptions())
	if err != nil {
		t.Fatalf("Acquire after conflict: %v", err)
	}
	if updates.Load() < 2 {
		t.Fatalf("update attempts = %d, want >=2", updates.Load())
	}
	_ = lock.Release(context.Background())
}

func TestRenewalAdvancesRenewTime(t *testing.T) {
	cfg := Config{Prefix: "fc-test", ShardCount: 32}
	key := "volume:renew"
	name, _ := cfg.LeaseName(key)
	client := fake.NewSimpleClientset(emptyLease(name))
	m := newTestManager(t, client, cfg, testIdentity("node-a", "uid-a"))
	lock, err := m.Acquire(context.Background(), key, testAcquireOptions())
	if err != nil {
		t.Fatal(err)
	}
	before, _ := client.CoordinationV1().Leases(testNamespace).Get(context.Background(), name, metav1.GetOptions{})
	if before.Spec.RenewTime == nil {
		t.Fatal("missing initial renew time")
	}
	initial := before.Spec.RenewTime.Time
	deadline := time.Now().Add(500 * time.Millisecond)
	for {
		after, _ := client.CoordinationV1().Leases(testNamespace).Get(context.Background(), name, metav1.GetOptions{})
		if after.Spec.RenewTime != nil && after.Spec.RenewTime.Time.After(initial) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("renew time did not advance")
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = lock.Release(context.Background())
}

func TestOwnershipLossSignal(t *testing.T) {
	cfg := Config{Prefix: "fc-test", ShardCount: 32}
	key := "volume:lost"
	name, _ := cfg.LeaseName(key)
	client := fake.NewSimpleClientset(emptyLease(name))
	m := newTestManager(t, client, cfg, testIdentity("node-a", "uid-a"))
	lock, err := m.Acquire(context.Background(), key, testAcquireOptions())
	if err != nil {
		t.Fatal(err)
	}
	lease, _ := client.CoordinationV1().Leases(testNamespace).Get(context.Background(), name, metav1.GetOptions{})
	foreign := "foreign-holder"
	lease.Spec.HolderIdentity = &foreign
	if _, err := client.CoordinationV1().Leases(testNamespace).Update(context.Background(), lease, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-lock.OwnershipLost():
	case <-time.After(500 * time.Millisecond):
		t.Fatal("ownership loss was not signaled")
	}
	if err := lock.AssertOwned(context.Background()); !errors.Is(err, ErrOwnershipLost) {
		t.Fatalf("AssertOwned = %v, want ErrOwnershipLost", err)
	}
	if err := lock.Release(context.Background()); !errors.Is(err, ErrOwnershipLost) {
		t.Fatalf("Release = %v, want ErrOwnershipLost", err)
	}
}

func heldLease(t *testing.T, cfg Config, key string, holder Holder, renew time.Time, durationSeconds int32) *coordinationv1.Lease {
	t.Helper()
	name, err := cfg.LeaseName(key)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := EncodeHolder(holder, key)
	if err != nil {
		t.Fatal(err)
	}
	micro := metav1.NewMicroTime(renew)
	return &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace},
		Spec: coordinationv1.LeaseSpec{
			HolderIdentity:       &identity,
			LeaseDurationSeconds: &durationSeconds,
			AcquireTime:          &micro,
			RenewTime:            &micro,
		},
	}
}

func TestOrdinaryExpiredLeaseTakeover(t *testing.T) {
	cfg := Config{Prefix: "fc-test", ShardCount: 32}
	key := "volume:expired"
	old := Holder{Role: RoleNode, PodName: "old", PodUID: "old-uid", NodeName: "worker-old", Safety: SafetyOrdinary, Operation: OperationExpand}
	client := fake.NewSimpleClientset(heldLease(t, cfg, key, old, time.Now().Add(-10*time.Second), 1))
	m := newTestManager(t, client, cfg, testIdentity("node-b", "uid-b"))
	lock, err := m.Acquire(context.Background(), key, testAcquireOptions())
	if err != nil {
		t.Fatalf("ordinary expired takeover: %v", err)
	}
	_ = lock.Release(context.Background())
}

func TestDestructiveExpiredLeaseLiveHolderNotStolen(t *testing.T) {
	cfg := Config{Prefix: "fc-test", ShardCount: 32}
	key := "volume:format"
	old := Holder{Role: RoleNode, PodName: "old-node", PodUID: "old-uid", NodeName: "worker-old", Safety: SafetyDestructive, Operation: OperationFilesystem}
	lease := heldLease(t, cfg, key, old, time.Now().Add(-10*time.Second), 1)
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: old.PodName, Namespace: testNamespace, UID: types.UID(old.PodUID)},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning},
	}
	client := fake.NewSimpleClientset(lease, pod)
	m := newTestManager(t, client, cfg, testIdentity("node-b", "uid-b"))
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	_, err := m.Acquire(ctx, key, AcquireOptions{Safety: SafetyDestructive, Operation: OperationFilesystem})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Acquire = %v, want deadline while live destructive holder exists", err)
	}
}

func TestDestructiveExpiredLeaseGoneHolderCanBeTaken(t *testing.T) {
	cfg := Config{Prefix: "fc-test", ShardCount: 32}
	key := "volume:format-gone"
	old := Holder{Role: RoleNode, PodName: "old-node", PodUID: "old-uid", NodeName: "worker-old", Safety: SafetyDestructive, Operation: OperationFilesystem}
	client := fake.NewSimpleClientset(heldLease(t, cfg, key, old, time.Now().Add(-10*time.Second), 1))
	m := newTestManager(t, client, cfg, testIdentity("node-b", "uid-b"))
	lock, err := m.Acquire(context.Background(), key, AcquireOptions{Safety: SafetyDestructive, Operation: OperationFilesystem})
	if err != nil {
		t.Fatalf("Acquire after holder disappeared: %v", err)
	}
	_ = lock.Release(context.Background())
}

func TestMalformedExpiredHolderFailsClosed(t *testing.T) {
	cfg := Config{Prefix: "fc-test", ShardCount: 32}
	key := "volume:malformed"
	name, _ := cfg.LeaseName(key)
	bad := "not-a-valid-holder"
	duration := int32(1)
	old := metav1.NewMicroTime(time.Now().Add(-10 * time.Second))
	client := fake.NewSimpleClientset(&coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace},
		Spec:       coordinationv1.LeaseSpec{HolderIdentity: &bad, LeaseDurationSeconds: &duration, RenewTime: &old},
	})
	m := newTestManager(t, client, cfg, testIdentity("node-b", "uid-b"))
	_, err := m.Acquire(context.Background(), key, testAcquireOptions())
	if !errors.Is(err, ErrUnsafeTakeover) {
		t.Fatalf("Acquire = %v, want ErrUnsafeTakeover", err)
	}
}

func findCollision(t *testing.T, cfg Config) (string, string, string) {
	t.Helper()
	seen := map[string]string{}
	for i := 0; i < 10000; i++ {
		key := fmt.Sprintf("volume:collision-%d", i)
		name, err := cfg.LeaseName(key)
		if err != nil {
			t.Fatal(err)
		}
		if previous, ok := seen[name]; ok {
			return previous, key, name
		}
		seen[name] = key
	}
	t.Fatal("no collision found")
	return "", "", ""
}

func multiLockFor(t *testing.T, set LockSet) *multiLock {
	t.Helper()
	multi, ok := set.(*multiLock)
	if !ok {
		t.Fatalf("lock set type = %T, want *multiLock", set)
	}
	return multi
}

func TestAcquireManyDeduplicatesPhysicalShardCollision(t *testing.T) {
	cfg := Config{Prefix: "iscsi-test", ShardCount: 32}
	keyA, keyB, wantLease := findCollision(t, cfg)
	client := fake.NewSimpleClientset(leasePoolObjects(t, cfg)...)
	m := newTestManager(t, client, cfg, testIdentity("node-a", "uid-a"))
	multi, err := m.AcquireMany(context.Background(), []string{keyA, keyB}, testAcquireOptions())
	if err != nil {
		t.Fatalf("AcquireMany: %v", err)
	}
	physical := multiLockFor(t, multi)
	if physical.PhysicalLockCount() != 1 {
		t.Fatalf("physical lock count = %d, want 1", physical.PhysicalLockCount())
	}
	if got := physical.LeaseNames(); len(got) != 1 || got[0] != wantLease {
		t.Fatalf("LeaseNames = %v, want [%s]", got, wantLease)
	}
	if err := multi.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestAcquireManyUsesPhysicalLeaseLexicalOrder(t *testing.T) {
	cfg := Config{Prefix: "fc-test", ShardCount: 32}
	client := fake.NewSimpleClientset(leasePoolObjects(t, cfg)...)
	m := newTestManager(t, client, cfg, testIdentity("node-a", "uid-a"))
	keys := []string{"fc-target:target-z", "volume:volume-a", "snapshot:snap-q"}
	multi, err := m.AcquireMany(context.Background(), keys, testAcquireOptions())
	if err != nil {
		t.Fatal(err)
	}
	names := multiLockFor(t, multi).LeaseNames()
	want := append([]string(nil), names...)
	sort.Strings(want)
	for i := range names {
		if names[i] != want[i] {
			t.Fatalf("physical acquisition order = %v, want lexical %v", names, want)
		}
	}
	_ = multi.Release(context.Background())
}

func findOrderedDistinctKeys(t *testing.T, cfg Config) (string, string) {
	t.Helper()
	for i := 0; i < 1000; i++ {
		for j := i + 1; j < 1000; j++ {
			a := fmt.Sprintf("volume:a-%d", i)
			b := fmt.Sprintf("volume:b-%d", j)
			an, _ := cfg.LeaseName(a)
			bn, _ := cfg.LeaseName(b)
			if an < bn {
				return a, b
			}
		}
	}
	t.Fatal("could not find ordered distinct keys")
	return "", ""
}

func TestAcquireManyPartialFailureRollsBack(t *testing.T) {
	cfg := Config{Prefix: "fc-test", ShardCount: 32}
	keyA, keyB := findOrderedDistinctKeys(t, cfg)
	client := fake.NewSimpleClientset(leasePoolObjects(t, cfg)...)
	holder := newTestManager(t, client, cfg, testIdentity("holder", "uid-holder"))
	contender := newTestManager(t, client, cfg, testIdentity("contender", "uid-contender"))
	observer := newTestManager(t, client, cfg, testIdentity("observer", "uid-observer"))

	blocked, err := holder.Acquire(context.Background(), keyB, testAcquireOptions())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	if _, err := contender.AcquireMany(ctx, []string{keyB, keyA}, testAcquireOptions()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("AcquireMany = %v, want deadline", err)
	}

	// keyA was acquired first by the contender and must have been rolled back.
	free, err := observer.Acquire(context.Background(), keyA, testAcquireOptions())
	if err != nil {
		t.Fatalf("rolled-back first shard remained locked: %v", err)
	}
	_ = free.Release(context.Background())
	_ = blocked.Release(context.Background())
}

func TestRenewalAPIErrorSignalsOwnershipLoss(t *testing.T) {
	cfg := Config{Prefix: "fc-test", ShardCount: 32}
	key := "volume:renew-error"
	name, _ := cfg.LeaseName(key)
	client := fake.NewSimpleClientset(emptyLease(name))
	var updateCount atomic.Int32
	client.PrependReactor("update", "leases", func(action ktesting.Action) (bool, runtime.Object, error) {
		// Update #1 is the initial claim. Update #2 is the first renewal and is
		// failed without mutating the fake client's reactor chain concurrently.
		if updateCount.Add(1) == 2 {
			return true, nil, errors.New("simulated apiserver outage")
		}
		return false, nil, nil
	})
	m := newTestManager(t, client, cfg, testIdentity("node-a", "uid-a"))
	lock, err := m.Acquire(context.Background(), key, testAcquireOptions())
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-lock.OwnershipLost():
	case <-time.After(500 * time.Millisecond):
		t.Fatal("renewal API error did not signal ownership loss")
	}
	_ = lock.Release(context.Background())
}

func TestMoreSemanticKeysThanShardsIsNotExhaustion(t *testing.T) {
	cfg := Config{Prefix: "iscsi-test", ShardCount: 32}
	client := fake.NewSimpleClientset(leasePoolObjects(t, cfg)...)
	m := newTestManager(t, client, cfg, testIdentity("node-a", "uid-a"))
	keys := make([]string, 0, 96)
	for i := 0; i < 96; i++ {
		keys = append(keys, fmt.Sprintf("volume:bulk-%d", i))
	}
	multi, err := m.AcquireMany(context.Background(), keys, testAcquireOptions())
	if err != nil {
		t.Fatalf("AcquireMany with >shard-count keys: %v", err)
	}
	if got := multiLockFor(t, multi).PhysicalLockCount(); got <= 0 || got > cfg.ShardCount {
		t.Fatalf("physical lock count = %d, want within 1..%d", got, cfg.ShardCount)
	}
	if err := multi.Release(context.Background()); err != nil {
		t.Fatalf("Release: %v", err)
	}
}

func TestRandomizedAcquireManyNoDeadlock(t *testing.T) {
	cfg := Config{Prefix: "fc-test", ShardCount: 32}
	client := fake.NewSimpleClientset(leasePoolObjects(t, cfg)...)
	m := newTestManager(t, client, cfg, testIdentity("node-a", "uid-a"))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	errCh := make(chan error, 12)
	for worker := 0; worker < 12; worker++ {
		worker := worker
		go func() {
			for iteration := 0; iteration < 40; iteration++ {
				keys := []string{
					fmt.Sprintf("volume:%d", (worker+iteration)%17),
					fmt.Sprintf("fc-target:%d", (worker*3+iteration)%11),
					fmt.Sprintf("snapshot:%d", (worker*5+iteration)%13),
				}
				lock, err := m.AcquireMany(ctx, keys, testAcquireOptions())
				if err != nil {
					errCh <- err
					return
				}
				if err := lock.AssertOwned(ctx); err != nil {
					errCh <- err
					_ = lock.Release(context.Background())
					return
				}
				if err := lock.Release(context.Background()); err != nil {
					errCh <- err
					return
				}
			}
			errCh <- nil
		}()
	}
	for i := 0; i < 12; i++ {
		if err := <-errCh; err != nil {
			t.Fatalf("randomized AcquireMany worker failed: %v", err)
		}
	}
}
