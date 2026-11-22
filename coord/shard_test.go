package coord

import (
	"testing"
)

func TestDefaultShardCount(t *testing.T) {
	if DefaultShardCount != 128 {
		t.Fatalf("DefaultShardCount = %d, want 128", DefaultShardCount)
	}
}

func TestSupportedShardCounts(t *testing.T) {
	for _, count := range []int{32, 64, 128, 256} {
		if !SupportedShardCount(count) {
			t.Fatalf("SupportedShardCount(%d) = false", count)
		}
	}
	for _, count := range []int{-1, 0, 1, 16, 33, 63, 129, 512} {
		if SupportedShardCount(count) {
			t.Fatalf("SupportedShardCount(%d) = true, want false", count)
		}
	}
}

func TestConfigValidation(t *testing.T) {
	valid := Config{Prefix: "iscsi-csi-ngxstorage", ShardCount: 128}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}

	cases := []Config{
		{Prefix: "", ShardCount: 128},
		{Prefix: " iscsi-csi-ngxstorage", ShardCount: 128},
		{Prefix: "Invalid_Upper", ShardCount: 128},
		{Prefix: "bad..name", ShardCount: 128},
		{Prefix: "iscsi-csi-ngxstorage", ShardCount: 64 + 1},
	}
	for _, cfg := range cases {
		if err := cfg.Validate(); err == nil {
			t.Fatalf("invalid config accepted: %+v", cfg)
		}
	}
}

func TestLeasePoolDefaultHas128UniqueNames(t *testing.T) {
	cfg := Config{Prefix: "iscsi-csi-ngxstorage", ShardCount: DefaultShardCount}
	names, err := cfg.LeaseNames()
	if err != nil {
		t.Fatalf("LeaseNames: %v", err)
	}
	if len(names) != 128 {
		t.Fatalf("LeaseNames count = %d, want 128", len(names))
	}
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		if _, exists := seen[name]; exists {
			t.Fatalf("duplicate Lease name %q", name)
		}
		seen[name] = struct{}{}
	}
	if names[0] != "iscsi-csi-ngxstorage-operation-lock-000" {
		t.Fatalf("first Lease = %q", names[0])
	}
	if names[127] != "iscsi-csi-ngxstorage-operation-lock-127" {
		t.Fatalf("last Lease = %q", names[127])
	}
}

func TestGoldenShardMapping(t *testing.T) {
	cfg := Config{Prefix: "iscsi-csi-ngxstorage", ShardCount: 128}
	cases := map[string]string{
		"volume:backend-id-1":                  "iscsi-csi-ngxstorage-operation-lock-097",
		"volume:fc.csi.ngxstorage.com.example": "iscsi-csi-ngxstorage-operation-lock-088",
		"fc-target:target-1":                   "iscsi-csi-ngxstorage-operation-lock-095",
	}
	for key, want := range cases {
		got, err := cfg.LeaseName(key)
		if err != nil {
			t.Fatalf("LeaseName(%q): %v", key, err)
		}
		if got != want {
			t.Fatalf("LeaseName(%q) = %q, want %q; hash/modulus contract changed", key, got, want)
		}
	}
}

func TestSameKeyAlwaysMapsToSameShard(t *testing.T) {
	cfg := Config{Prefix: "fc-csi-ngxstorage", ShardCount: 128}
	const key = "volume:fc.csi.ngxstorage.com.example"
	first, err := cfg.LeaseName(key)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		got, err := cfg.LeaseName(key)
		if err != nil {
			t.Fatal(err)
		}
		if got != first {
			t.Fatalf("non-deterministic mapping: %q != %q", got, first)
		}
	}
}

func TestDifferentPrefixesHaveDifferentLockDomains(t *testing.T) {
	key := "volume:backend-id-1"
	a := Config{Prefix: "release-a-iscsi", ShardCount: 128}
	b := Config{Prefix: "release-b-iscsi", ShardCount: 128}
	an, err := a.LeaseName(key)
	if err != nil {
		t.Fatal(err)
	}
	bn, err := b.LeaseName(key)
	if err != nil {
		t.Fatal(err)
	}
	if an == bn {
		t.Fatalf("different prefixes mapped to same Lease name %q", an)
	}
}

func TestEmptySemanticKeyRejected(t *testing.T) {
	cfg := Config{Prefix: "fc-csi-ngxstorage", ShardCount: 128}
	if _, err := cfg.LeaseName(""); err == nil {
		t.Fatal("empty semantic key should fail")
	}
}
