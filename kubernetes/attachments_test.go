package kubernetes

import (
	"context"
	"errors"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubeFake "k8s.io/client-go/kubernetes/fake"
)

type fakeAttachmentAPI struct {
	persistentVolumes []corev1.PersistentVolume
	volumeAttachments []storagev1.VolumeAttachment
	nodes             map[string]*corev1.Node
	persistentErr     error
	attachmentsErr    error
	nodesErr          error
}

func (f fakeAttachmentAPI) PersistentVolumes(context.Context) ([]corev1.PersistentVolume, error) {
	return f.persistentVolumes, f.persistentErr
}

func (f fakeAttachmentAPI) VolumeAttachments(context.Context) ([]storagev1.VolumeAttachment, error) {
	return f.volumeAttachments, f.attachmentsErr
}

func (f fakeAttachmentAPI) Node(_ context.Context, name string) (*corev1.Node, error) {
	if f.nodesErr != nil {
		return nil, f.nodesErr
	}
	if node, ok := f.nodes[name]; ok {
		return node, nil
	}
	return nil, apierrors.NewNotFound(corev1.Resource("nodes"), name)
}

func TestNodeExists(t *testing.T) {
	lookup := &Lookup{api: fakeAttachmentAPI{
		nodes: map[string]*corev1.Node{
			"node-a": {ObjectMeta: metav1.ObjectMeta{Name: "node-a"}},
		},
	}}
	if ok, err := lookup.NodeExists(context.Background(), "node-a"); err != nil || !ok {
		t.Fatalf("NodeExists(node-a) = (%v, %v), want (true, nil)", ok, err)
	}
	if ok, err := lookup.NodeExists(context.Background(), "node-missing"); err != nil || ok {
		t.Fatalf("NodeExists(node-missing) = (%v, %v), want (false, nil)", ok, err)
	}
	failing := &Lookup{api: fakeAttachmentAPI{nodesErr: errors.New("api down")}}
	if _, err := failing.NodeExists(context.Background(), "node-a"); err == nil {
		t.Fatal("NodeExists must propagate API errors (fail closed)")
	}
}

func TestClientsetNodeLookupUsesGetAndNotFoundSemantics(t *testing.T) {
	client := kubeFake.NewSimpleClientset(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-a"}})
	lookup := &Lookup{api: clientsetAttachmentAPI{client: client}}

	if exists, err := lookup.NodeExists(context.Background(), "node-a"); err != nil || !exists {
		t.Fatalf("NodeExists(node-a) = (%v, %v), want (true, nil)", exists, err)
	}
	if exists, err := lookup.NodeExists(context.Background(), "node-missing"); err != nil || exists {
		t.Fatalf("NodeExists(node-missing) = (%v, %v), want (false, nil)", exists, err)
	}
}

func TestRemainingNodesFiltersDeduplicatesAndSorts(t *testing.T) {
	pvName := "pv-one"
	lookup := &Lookup{api: fakeAttachmentAPI{
		persistentVolumes: []corev1.PersistentVolume{
			persistentVolume(pvName, "fc.csi.ngxstorage.com", "volume-one"),
			persistentVolume("other-driver", "other.csi.example", "volume-one"),
		},
		volumeAttachments: []storagev1.VolumeAttachment{
			volumeAttachment("attachment-z", pvName, "node-z"),
			volumeAttachment("attachment-a", pvName, "node-a"),
			volumeAttachment("attachment-a-duplicate", pvName, "node-a"),
			volumeAttachment("attachment-excluded", pvName, "node-excluded"),
		},
	}}

	got, err := lookup.RemainingNodes(context.Background(), "fc.csi.ngxstorage.com", "volume-one", "node-excluded")
	if err != nil {
		t.Fatalf("RemainingNodes() error = %v", err)
	}
	want := []string{"node-a", "node-z"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("RemainingNodes() = %#v, want %#v", got, want)
	}
}

func TestRemainingNodesRejectsAmbiguousPersistentVolumes(t *testing.T) {
	lookup := &Lookup{api: fakeAttachmentAPI{persistentVolumes: []corev1.PersistentVolume{
		persistentVolume("pv-one", "fc.csi.ngxstorage.com", "volume-one"),
		persistentVolume("pv-two", "fc.csi.ngxstorage.com", "volume-one"),
	}}}

	_, err := lookup.RemainingNodes(context.Background(), "fc.csi.ngxstorage.com", "volume-one", "")
	var ambiguous ErrAmbiguousVolumePV
	if !errors.As(err, &ambiguous) || ambiguous.Count != 2 {
		t.Fatalf("RemainingNodes() error = %v, want ErrAmbiguousVolumePV{Count: 2}", err)
	}
}

func TestRemainingNodesPropagatesListErrors(t *testing.T) {
	t.Run("persistent volumes", func(t *testing.T) {
		wantErr := errors.New("persistent volume list failed")
		lookup := &Lookup{api: fakeAttachmentAPI{persistentErr: wantErr}}
		_, err := lookup.RemainingNodes(context.Background(), "fc.csi.ngxstorage.com", "volume-one", "")
		if !errors.Is(err, wantErr) {
			t.Fatalf("RemainingNodes() error = %v, want %v", err, wantErr)
		}
	})

	t.Run("volume attachments", func(t *testing.T) {
		wantErr := errors.New("volume attachment list failed")
		lookup := &Lookup{api: fakeAttachmentAPI{
			persistentVolumes: []corev1.PersistentVolume{
				persistentVolume("pv-one", "fc.csi.ngxstorage.com", "volume-one"),
			},
			attachmentsErr: wantErr,
		}}
		_, err := lookup.RemainingNodes(context.Background(), "fc.csi.ngxstorage.com", "volume-one", "")
		if !errors.Is(err, wantErr) {
			t.Fatalf("RemainingNodes() error = %v, want %v", err, wantErr)
		}
	})
}

func persistentVolume(name, driver, handle string) corev1.PersistentVolume {
	return corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: corev1.PersistentVolumeSpec{PersistentVolumeSource: corev1.PersistentVolumeSource{
			CSI: &corev1.CSIPersistentVolumeSource{Driver: driver, VolumeHandle: handle},
		}},
	}
}

func volumeAttachment(name, pvName, node string) storagev1.VolumeAttachment {
	return storagev1.VolumeAttachment{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: storagev1.VolumeAttachmentSpec{
			Attacher: "fc.csi.ngxstorage.com",
			NodeName: node,
			Source:   storagev1.VolumeAttachmentSource{PersistentVolumeName: &pvName},
		},
	}
}
