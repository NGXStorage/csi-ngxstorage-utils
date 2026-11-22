# csi-ngxstorage-utils

Shared, protocol-neutral Go utilities used by the NGX Storage FC, iSCSI and NFS
CSI drivers and related tools.

## What it provides

- `node` — process-local per-volume mutation locks and a published-target registry.
- `names` — deterministic NGX Storage CSI volume-name normalization.
- `blockmount` — block-device probe, format, verification, mount/unmount, and
  staging/publish identity checks (FC and iSCSI).
- `kubernetes` — PV, VolumeAttachment, and Node lookup with fail-closed errors.
- `coord` — Kubernetes Lease-based distributed operation locks with startup preflight.

These cover CSI node operations (`NodeStageVolume` / `NodePublishVolume` mount
and identity handling), controller-side attachment and node lookup, backend
resource naming, and serialization of cross-node mutations such as exactly-once
filesystem initialization.

## Install

```bash
go get github.com/NGXStorage/csi-ngxstorage-utils
```

Import only the packages you need.

## Example

```go
package main

import (
	"fmt"

	"github.com/NGXStorage/csi-ngxstorage-utils/names"
	"github.com/NGXStorage/csi-ngxstorage-utils/node"
)

func main() {
	backend := names.ModifyVolName("pvc-0f3a9c")

	var state node.State
	state.SetPublished("vol-1", "/var/lib/kubelet/pods/vol-1/globalmount")
	target, ok := state.PublishedTarget("vol-1")

	fmt.Println(backend, target, ok)
}
```

## Test

```bash
go test ./...
go vet ./...
```

## License

Apache-2.0
