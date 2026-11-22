package blockmount

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	mount "k8s.io/mount-utils"
)

type publishFakeFileInfo struct {
	mode os.FileMode
	rdev uint64
}

func (f publishFakeFileInfo) Name() string       { return "fake" }
func (f publishFakeFileInfo) Size() int64        { return 0 }
func (f publishFakeFileInfo) Mode() os.FileMode  { return f.mode }
func (f publishFakeFileInfo) ModTime() time.Time { return time.Time{} }
func (f publishFakeFileInfo) IsDir() bool        { return f.mode.IsDir() }
func (f publishFakeFileInfo) Sys() interface{}   { return &syscall.Stat_t{Rdev: f.rdev} }

func swapPublishStatFile(t *testing.T, fn func(string) (os.FileInfo, error)) {
	t.Helper()
	original := StatFile
	StatFile = fn
	t.Cleanup(func() { StatFile = original })
}

type listErrorMounter struct {
	mount.Interface
}

func (listErrorMounter) List() ([]mount.MountPoint, error) {
	return nil, errors.New("list unavailable")
}

type publishInspectionErrorMounter struct {
	mount.Interface
}

func (publishInspectionErrorMounter) IsLikelyNotMountPoint(string) (bool, error) {
	return false, errors.New("inspection unavailable")
}

func makePublishPaths(t *testing.T) (staging, target string) {
	t.Helper()
	dir := t.TempDir()
	staging = filepath.Join(dir, "staging")
	target = filepath.Join(dir, "target")
	if err := os.MkdirAll(staging, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	return staging, target
}

func TestPublishTargetNotMountedProceeds(t *testing.T) {
	staging, target := makePublishPaths(t)
	mounter := mount.NewFakeMounter([]mount.MountPoint{{Device: "/dev/dm-1", Path: staging}})
	matched, err := PublishTargetMatchesStaging(mounter, target, staging, false, false)
	if err != nil || matched {
		t.Fatalf("matched=%v err=%v, want (false, nil) for a not-mounted target", matched, err)
	}
}

func TestPublishTargetAbsentProceeds(t *testing.T) {
	staging, _ := makePublishPaths(t)
	absent := filepath.Join(t.TempDir(), "absent-target")
	mounter := mount.NewFakeMounter(nil)
	matched, err := PublishTargetMatchesStaging(mounter, absent, staging, false, false)
	if err != nil || matched {
		t.Fatalf("matched=%v err=%v, want (false, nil) for an absent target", matched, err)
	}
}

func TestPublishTargetInspectionErrorFailsClosed(t *testing.T) {
	staging, target := makePublishPaths(t)
	mounter := publishInspectionErrorMounter{Interface: mount.NewFakeMounter(nil)}
	if _, err := PublishTargetMatchesStaging(mounter, target, staging, false, false); err == nil {
		t.Fatal("no error on a mount-point inspection failure")
	}
}

func TestPublishTargetListErrorFailsClosed(t *testing.T) {
	staging, target := makePublishPaths(t)
	mounter := listErrorMounter{Interface: mount.NewFakeMounter([]mount.MountPoint{
		{Device: "/dev/dm-1", Path: staging},
		{Device: "/dev/dm-1", Path: target, Opts: []string{"bind"}},
	})}
	if _, err := PublishTargetMatchesStaging(mounter, target, staging, false, false); err == nil {
		t.Fatal("no error on a mount list failure")
	}
}

func TestPublishFilesystemSameDeviceSucceeds(t *testing.T) {
	staging, target := makePublishPaths(t)
	mounter := mount.NewFakeMounter([]mount.MountPoint{
		{Device: "/dev/dm-1", Path: staging, Opts: []string{"rw"}},
		{Device: "/dev/dm-1", Path: target, Opts: []string{"bind"}},
	})
	matched, err := PublishTargetMatchesStaging(mounter, target, staging, false, false)
	if err != nil || !matched {
		t.Fatalf("matched=%v err=%v, want idempotent success for the same staging device", matched, err)
	}
}

func TestPublishFilesystemMapperAliasSucceeds(t *testing.T) {
	staging, target := makePublishPaths(t)
	dir := t.TempDir()
	device := filepath.Join(dir, "dm-1")
	if err := os.WriteFile(device, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(dir, "mapper-alias")
	if err := os.Symlink(device, alias); err != nil {
		t.Fatal(err)
	}
	mounter := mount.NewFakeMounter([]mount.MountPoint{
		{Device: device, Path: staging, Opts: []string{"rw"}},
		{Device: alias, Path: target, Opts: []string{"bind"}},
	})
	matched, err := PublishTargetMatchesStaging(mounter, target, staging, false, false)
	if err != nil || !matched {
		t.Fatalf("matched=%v err=%v, want success through a mapper alias of the same device", matched, err)
	}
}

func TestPublishFilesystemForeignDeviceFailsClosed(t *testing.T) {
	staging, target := makePublishPaths(t)
	mounter := mount.NewFakeMounter([]mount.MountPoint{
		{Device: "/dev/dm-1", Path: staging, Opts: []string{"rw"}},
		{Device: "/dev/dm-2", Path: target, Opts: []string{"bind"}},
	})
	if _, err := PublishTargetMatchesStaging(mounter, target, staging, false, false); err == nil {
		t.Fatal("no error for a target bound from a different volume")
	}
}

func TestPublishFilesystemStagingNotMountedFailsClosed(t *testing.T) {
	staging, target := makePublishPaths(t)
	mounter := mount.NewFakeMounter([]mount.MountPoint{
		{Device: "/dev/dm-1", Path: target, Opts: []string{"bind"}},
	})
	if _, err := PublishTargetMatchesStaging(mounter, target, staging, false, false); err == nil {
		t.Fatal("no error when the staging mount is missing")
	}
}

func TestPublishFilesystemUnresolvedSourceFailsClosed(t *testing.T) {
	staging, target := makePublishPaths(t)
	mounter := mount.NewFakeMounter([]mount.MountPoint{
		{Device: "", Path: staging, Opts: []string{"rw"}},
		{Device: "", Path: target, Opts: []string{"bind"}},
	})
	if _, err := PublishTargetMatchesStaging(mounter, target, staging, false, false); err == nil {
		t.Fatal("no error for an unresolved mount source")
	}
}

func TestPublishReadOnlyMismatchFailsClosed(t *testing.T) {
	staging, target := makePublishPaths(t)
	for _, tc := range []struct {
		name   string
		opts   []string
		wantRO bool
	}{
		{"mounted-rw-requested-ro", []string{"bind"}, true},
		{"mounted-ro-requested-rw", []string{"bind", "ro"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mounter := mount.NewFakeMounter([]mount.MountPoint{
				{Device: "/dev/dm-1", Path: staging, Opts: []string{"rw"}},
				{Device: "/dev/dm-1", Path: target, Opts: tc.opts},
			})
			if _, err := PublishTargetMatchesStaging(mounter, target, staging, false, tc.wantRO); err == nil {
				t.Fatalf("no error for incompatible read-only state (opts=%v wantRO=%v)", tc.opts, tc.wantRO)
			}
		})
	}
}

func TestPublishReadOnlyMatchSucceeds(t *testing.T) {
	staging, target := makePublishPaths(t)
	mounter := mount.NewFakeMounter([]mount.MountPoint{
		{Device: "/dev/dm-1", Path: staging, Opts: []string{"ro"}},
		{Device: "/dev/dm-1", Path: target, Opts: []string{"bind", "ro"}},
	})
	matched, err := PublishTargetMatchesStaging(mounter, target, staging, false, true)
	if err != nil || !matched {
		t.Fatalf("matched=%v err=%v, want idempotent success for a compatible ro publish", matched, err)
	}
}

func TestPublishBlockSameRdevSucceeds(t *testing.T) {
	dir := t.TempDir()
	staging := filepath.Join(dir, "block-staging")
	target := filepath.Join(dir, "block-target")
	for _, p := range []string{staging, target} {
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	mounter := mount.NewFakeMounter([]mount.MountPoint{{Device: "devtmpfs", Path: target, Opts: []string{"bind"}}})
	swapPublishStatFile(t, func(string) (os.FileInfo, error) {
		return publishFakeFileInfo{mode: os.ModeDevice | 0o660, rdev: 42}, nil
	})
	matched, err := PublishTargetMatchesStaging(mounter, target, staging, true, false)
	if err != nil || !matched {
		t.Fatalf("matched=%v err=%v, want idempotent success for the same device rdev", matched, err)
	}
}

func TestPublishBlockDifferentRdevFailsClosed(t *testing.T) {
	dir := t.TempDir()
	staging := filepath.Join(dir, "block-staging")
	target := filepath.Join(dir, "block-target")
	for _, p := range []string{staging, target} {
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	mounter := mount.NewFakeMounter([]mount.MountPoint{{Device: "devtmpfs", Path: target, Opts: []string{"bind"}}})
	swapPublishStatFile(t, func(path string) (os.FileInfo, error) {
		if path == target {
			return publishFakeFileInfo{mode: os.ModeDevice | 0o660, rdev: 43}, nil
		}
		return publishFakeFileInfo{mode: os.ModeDevice | 0o660, rdev: 42}, nil
	})
	if _, err := PublishTargetMatchesStaging(mounter, target, staging, true, false); err == nil {
		t.Fatal("no error for a block target bound from a different device")
	}
}

func TestPublishBlockStagingRegularFileFailsClosed(t *testing.T) {
	dir := t.TempDir()
	staging := filepath.Join(dir, "block-staging")
	target := filepath.Join(dir, "block-target")
	for _, p := range []string{staging, target} {
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	mounter := mount.NewFakeMounter([]mount.MountPoint{{Device: "devtmpfs", Path: target, Opts: []string{"bind"}}})
	swapPublishStatFile(t, func(path string) (os.FileInfo, error) {
		if path == target {
			return publishFakeFileInfo{mode: os.ModeDevice | 0o660, rdev: 42}, nil
		}
		return publishFakeFileInfo{mode: 0o600}, nil
	})
	if _, err := PublishTargetMatchesStaging(mounter, target, staging, true, false); err == nil {
		t.Fatal("no error when the staging path is a regular file, not a device node")
	}
}

func TestStagingReadyForPublishNotMountedFails(t *testing.T) {
	staging, _ := makePublishPaths(t)
	mounter := mount.NewFakeMounter(nil)
	if err := StagingReadyForPublish(mounter, staging, false); err == nil {
		t.Fatal("no error for an unmounted staging directory")
	}
}

func TestStagingReadyForPublishMountedFilesystemSucceeds(t *testing.T) {
	staging, _ := makePublishPaths(t)
	mounter := mount.NewFakeMounter([]mount.MountPoint{{Device: "/dev/dm-1", Path: staging}})
	if err := StagingReadyForPublish(mounter, staging, false); err != nil {
		t.Fatalf("mounted filesystem staging: %v", err)
	}
}

func TestStagingReadyForPublishBlockDeviceSucceeds(t *testing.T) {
	dir := t.TempDir()
	staging := filepath.Join(dir, "block-staging")
	if err := os.WriteFile(staging, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	mounter := mount.NewFakeMounter([]mount.MountPoint{{Device: "devtmpfs", Path: staging}})
	swapPublishStatFile(t, func(string) (os.FileInfo, error) {
		return publishFakeFileInfo{mode: os.ModeDevice | 0o660, rdev: 42}, nil
	})
	if err := StagingReadyForPublish(mounter, staging, true); err != nil {
		t.Fatalf("mounted block staging device: %v", err)
	}
}

func TestStagingReadyForPublishBlockRegularFileFails(t *testing.T) {
	dir := t.TempDir()
	staging := filepath.Join(dir, "block-staging")
	if err := os.WriteFile(staging, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	mounter := mount.NewFakeMounter([]mount.MountPoint{{Device: "devtmpfs", Path: staging}})
	swapPublishStatFile(t, func(string) (os.FileInfo, error) {
		return publishFakeFileInfo{mode: 0o600}, nil
	})
	if err := StagingReadyForPublish(mounter, staging, true); err == nil {
		t.Fatal("no error when block staging is not a device node")
	}
}

func TestStagingReadyForPublishInspectionErrorFails(t *testing.T) {
	staging, _ := makePublishPaths(t)
	mounter := publishInspectionErrorMounter{Interface: mount.NewFakeMounter(nil)}
	if err := StagingReadyForPublish(mounter, staging, false); err == nil {
		t.Fatal("no error on a staging mount-point inspection failure")
	}
}
