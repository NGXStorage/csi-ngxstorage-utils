package coord

import (
	"strings"
	"testing"
)

func TestHolderRoundTrip(t *testing.T) {
	h := Holder{
		Role:      RoleNode,
		PodName:   "iscsi-csi-ngxstorage-node-abc12",
		PodUID:    "12345678-1234-1234-1234-123456789abc",
		NodeName:  "worker0.ocp.example.test",
		Safety:    SafetyDestructive,
		Operation: OperationFilesystem,
	}
	encoded, err := EncodeHolder(h, "volume:backend-id-1")
	if err != nil {
		t.Fatalf("EncodeHolder: %v", err)
	}
	if len(encoded) > MaxHolderIdentityBytes {
		t.Fatalf("holder identity length = %d", len(encoded))
	}
	parsed, err := ParseHolder(encoded)
	if err != nil {
		t.Fatalf("ParseHolder: %v", err)
	}
	if parsed.PodName != h.PodName || parsed.PodUID != h.PodUID || parsed.Role != h.Role ||
		parsed.Safety != h.Safety || parsed.Operation != h.Operation {
		t.Fatalf("round trip mismatch: %+v", parsed)
	}
	if parsed.NodeHash != shortHash(h.NodeName) {
		t.Fatalf("node hash = %q, want %q", parsed.NodeHash, shortHash(h.NodeName))
	}
	if parsed.KeyHash != shortHash("volume:backend-id-1") {
		t.Fatalf("key hash mismatch: %q", parsed.KeyHash)
	}
}

func TestHolderWorstCaseFitsBound(t *testing.T) {
	h := Holder{
		Role:      RoleNode,
		PodName:   strings.Repeat("p", 63),
		PodUID:    strings.Repeat("u", 36),
		NodeName:  strings.Repeat("n", 253),
		Safety:    SafetyDestructive,
		Operation: OperationFilesystem,
	}
	encoded, err := EncodeHolder(h, strings.Repeat("k", 1024))
	if err != nil {
		t.Fatalf("worst-case holder should fit: %v", err)
	}
	if got := len(encoded); got > MaxHolderIdentityBytes {
		t.Fatalf("holder identity = %d bytes, max %d", got, MaxHolderIdentityBytes)
	}
}

func TestHolderRejectsUnboundedPodName(t *testing.T) {
	h := Holder{
		Role:      RoleNode,
		PodName:   strings.Repeat("p", 64),
		PodUID:    strings.Repeat("u", 36),
		NodeName:  "worker0",
		Safety:    SafetyDestructive,
		Operation: OperationFilesystem,
	}
	if _, err := EncodeHolder(h, "volume:x"); err == nil {
		t.Fatal("holder exceeding bound should fail")
	}
}

func TestParseHolderRejectsMalformedIdentity(t *testing.T) {
	for _, identity := range []string{"", "v1|n", "v2|n|d|p|u|00000000|00000000|f", "v1|x|d|p|u|00000000|00000000|f"} {
		if _, err := ParseHolder(identity); err == nil {
			t.Fatalf("malformed identity accepted: %q", identity)
		}
	}
}
