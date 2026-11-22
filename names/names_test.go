package names

import (
	"strings"
	"testing"
)

func TestModifyVolNamePreservesContract(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		noPVC bool
		want  string
	}{
		{name: "strips and hashes long PVC name", in: "pvc-12345678-1234-1234-1234-123456789abc", want: "pvc1234567812341234123" + hashNameSuffix("pvc12345678123412341234123456789abc")},
		{name: "removes separators and restores prefix", in: "my.volume-name_v2", want: "pvcmyvolumenamev2"},
		{name: "optional prefix omission", in: "my.volume-name_v2", noPVC: true, want: "myvolumenamev2"},
		{name: "existing prefix is not duplicated", in: "pvc-claim", want: "pvcclaim"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got string
			if tt.noPVC {
				got = ModifyVolName(tt.in, true)
			} else {
				got = ModifyVolName(tt.in)
			}
			if got != tt.want {
				t.Fatalf("ModifyVolName(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestModifyVolNameTruncatesAndIsDeterministic(t *testing.T) {
	got := ModifyVolName(strings.Repeat("a", 40))
	if len(got) != 30 {
		t.Fatalf("ModifyVolName length = %d, want 30", len(got))
	}
	if got != ModifyVolName(strings.Repeat("a", 40)) {
		t.Fatal("ModifyVolName is not deterministic")
	}
}

func TestModifyVolNameDistinguishesLongSharedPrefixes(t *testing.T) {
	base := "pvc" + strings.Repeat("a", 27)
	a := ModifyVolName(base + "ZZZ1")
	b := ModifyVolName(base + "ZZZ2")
	if a == b {
		t.Fatalf("distinct normalized names alias: %q", a)
	}
	if len(a) != 30 || len(b) != 30 {
		t.Fatalf("normalized names must be 30 chars: %q / %q", a, b)
	}
}
