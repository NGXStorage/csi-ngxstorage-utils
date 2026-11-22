package blockmount

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	mount "k8s.io/mount-utils"
)

func TestPrepareBlockTargetReplacesDirectoryWithFile(t *testing.T) {
	target := filepath.Join(t.TempDir(), "staging", "volume")
	if err := os.MkdirAll(target, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := PrepareBlockTarget(target); err != nil {
		t.Fatalf("PrepareBlockTarget: %v", err)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("target mode = %s, want regular file", info.Mode())
	}
}

func TestProbeFilesystemDistinguishesEmptyExistingAndError(t *testing.T) {
	original := getDiskFormat
	t.Cleanup(func() { getDiskFormat = original })

	getDiskFormat = func(string) (string, error) { return "", nil }
	probe, err := ProbeFilesystem("/dev/test")
	if err != nil || probe.State != FilesystemEmpty {
		t.Fatalf("empty probe = %+v, %v", probe, err)
	}

	getDiskFormat = func(string) (string, error) { return "ext4", nil }
	probe, err = ProbeFilesystem("/dev/test")
	if err != nil || probe.State != FilesystemExisting || probe.FSType != "ext4" {
		t.Fatalf("existing probe = %+v, %v", probe, err)
	}

	getDiskFormat = func(string) (string, error) { return "", errors.New("I/O error") }
	if probe, err = ProbeFilesystem("/dev/test"); err == nil || probe.State != FilesystemUnknown {
		t.Fatalf("probe error must stay unknown: %+v, %v", probe, err)
	}
}

func TestFormatFilesystemUsesExpectedMkfsArguments(t *testing.T) {
	original := runFilesystemCommand
	t.Cleanup(func() { runFilesystemCommand = original })

	var gotName string
	var gotArgs []string
	runFilesystemCommand = func(name string, args ...string) ([]byte, error) {
		gotName = name
		gotArgs = append([]string(nil), args...)
		return nil, nil
	}

	if err := FormatFilesystem("/dev/test", "ext4"); err != nil {
		t.Fatal(err)
	}
	if gotName != "mkfs.ext4" || !reflect.DeepEqual(gotArgs, []string{"-F", "-m0", "/dev/test"}) {
		t.Fatalf("ext4 command = %s %v", gotName, gotArgs)
	}
	if err := FormatFilesystem("/dev/test", "xfs"); err != nil {
		t.Fatal(err)
	}
	if gotName != "mkfs.xfs" || !reflect.DeepEqual(gotArgs, []string{"-f", "/dev/test"}) {
		t.Fatalf("xfs command = %s %v", gotName, gotArgs)
	}
	if err := FormatFilesystem("/dev/test", "../../evil"); err == nil {
		t.Fatal("unsafe filesystem type should be rejected")
	}
}

func TestVerifyFilesystemRequiresExpectedSignature(t *testing.T) {
	original := getDiskFormat
	t.Cleanup(func() { getDiskFormat = original })
	getDiskFormat = func(string) (string, error) { return "", nil }
	if err := VerifyFilesystem("/dev/test", "ext4"); err == nil {
		t.Fatal("empty device should fail verification")
	}
	getDiskFormat = func(string) (string, error) { return "xfs", nil }
	if err := VerifyFilesystem("/dev/test", "ext4"); err == nil {
		t.Fatal("filesystem mismatch should fail verification")
	}
	getDiskFormat = func(string) (string, error) { return "ext4", nil }
	if err := VerifyFilesystem("/dev/test", "ext4"); err != nil {
		t.Fatalf("matching signature rejected: %v", err)
	}
}

func TestMountFilesystemReaderOnlyStagesReadOnly(t *testing.T) {
	original := Mounter
	t.Cleanup(func() { Mounter = original })
	fake := mount.NewFakeMounter(nil)
	Mounter = fake

	if err := MountFilesystem("/dev/test", "/stage", "ext4", true, nil); err != nil {
		t.Fatal(err)
	}
	points, err := fake.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 1 {
		t.Fatalf("mount points = %#v", points)
	}
	if !reflect.DeepEqual(points[0].Opts, []string{"ro", "defaults"}) {
		t.Fatalf("mount options = %v, want [ro defaults]", points[0].Opts)
	}
}
