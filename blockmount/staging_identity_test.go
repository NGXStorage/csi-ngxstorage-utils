package blockmount

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	mount "k8s.io/mount-utils"
)

func TestSameBlockDeviceResolvesAliases(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "dm-7")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(dir, "mapper-alias")
	if err := os.Symlink(target, alias); err != nil {
		t.Fatal(err)
	}
	if !SameBlockDevice(alias, target) {
		t.Fatal("symlink alias must compare equal to its target")
	}
	if SameBlockDevice(target, filepath.Join(dir, "absent")) {
		t.Fatal("unresolvable path must not compare equal")
	}
	if !SameBlockDevice(target, target) {
		t.Fatal("identical paths must compare equal")
	}
}

func TestBlockStagingMatchesDeviceIdentity(t *testing.T) {
	original := StatFile
	t.Cleanup(func() { StatFile = original })
	staging := filepath.Join(t.TempDir(), "block-staging")
	if err := os.WriteFile(staging, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	dev := &fakeFileInfo{mode: os.ModeDevice, rdev: 42}
	StatFile = func(string) (os.FileInfo, error) { return dev, nil }
	mounter := mount.NewFakeMounter([]mount.MountPoint{{Device: "devtmpfs", Path: staging}})
	ok, err := BlockStagingMatchesDevice(mounter, staging, "/dev/dm-7")
	if err != nil || !ok {
		t.Fatalf("same rdev must match: %v %v", ok, err)
	}
	StatFile = func(path string) (os.FileInfo, error) {
		if path == staging {
			return &fakeFileInfo{mode: os.ModeDevice, rdev: 43}, nil
		}
		return &fakeFileInfo{mode: os.ModeDevice, rdev: 42}, nil
	}
	if _, err := BlockStagingMatchesDevice(mounter, staging, "/dev/dm-7"); err == nil {
		t.Fatal("different rdev must fail closed")
	}
	StatFile = func(path string) (os.FileInfo, error) {
		if path == staging {
			return &fakeFileInfo{mode: 0, rdev: 42}, nil
		}
		return &fakeFileInfo{mode: os.ModeDevice, rdev: 42}, nil
	}
	if _, err := BlockStagingMatchesDevice(mounter, staging, "/dev/dm-7"); err == nil {
		t.Fatal("regular file is not a device identity")
	}
}

func TestBlockStagingNotMountedProceeds(t *testing.T) {
	mounter := mount.NewFakeMounter(nil)
	ok, err := BlockStagingMatchesDevice(mounter, "/absent-staging", "/dev/dm-7")
	if err != nil || ok {
		t.Fatalf("absent path must proceed to first mount: %v %v", ok, err)
	}
}

type fakeFileInfo struct {
	mode os.FileMode
	rdev uint64
}

func (f *fakeFileInfo) Name() string       { return "fake" }
func (f *fakeFileInfo) Size() int64        { return 0 }
func (f *fakeFileInfo) Mode() os.FileMode  { return f.mode }
func (f *fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (f *fakeFileInfo) IsDir() bool        { return false }
func (f *fakeFileInfo) Sys() interface{}   { return &syscall.Stat_t{Rdev: f.rdev} }
