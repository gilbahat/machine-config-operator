# machine-config-daemon on AIX

This fork runs the machine-config-daemon (MCD) on IBM AIX 7.2+ nodes, so an
OpenShift cluster can manage AIX nodes' configuration with MachineConfigs and
MachineConfigPools the same way it manages RHCOS nodes. It is part of the
Kubernetes-on-AIX effort built on
[containerd-aix](https://github.com/reuna-labs/containerd-aix) and assumes an
AIX kubelet port will follow.

Only the daemon is ported. The operator, controllers and machine-config-server
keep running on the Linux control plane unchanged.

> **Status:** the daemon cross-compiles for `aix/ppc64` and the AIX host layer
> is unit-tested on Linux against recorded AIX command output. It has not yet
> been run on AIX hardware or LPARs. The first runs should use the test modes
> described below.

## How the daemon runs on an AIX node

On RHCOS the MCD is a privileged DaemonSet pod. It shares the host PID
namespace, mounts the host root at `/rootfs` and `chroot`s into it. None of
that is possible under containerd-aix:

- WPARs cannot share the global environment's process space: `hostPID` is rejected.
- `hostPath` volumes are limited to an allowlist of namefs mounts.
- A process inside a WPAR cannot run `shutdown`, `chpasswd` or `startsrc` for the global environment.

So on AIX the MCD runs in the global environment as the **`mcd` SRC
subsystem**, alongside containerd and (later) the kubelet:

| | RHCOS | AIX |
|---|---|---|
| Runs as | DaemonSet pod | SRC subsystem `mcd`, group `kubernetes`, started from `/etc/inittab` |
| Root filesystem | `--root-mount=/rootfs` + chroot | `--root-mount=/` (default on AIX) |
| API credentials | pod service account | `--kubeconfig=/etc/machine-config-daemon/kubeconfig` (default on AIX) holding a `machine-config-daemon` SA token |
| Logs | pod logs / journal | `/var/log/machine-config-daemon.log` and syslog (`logger -t machine-config-daemon`) |
| Metrics | `127.0.0.1:8797` behind kube-rbac-proxy | `127.0.0.1:8797` (local only) |

The MCD DaemonSet already has `nodeSelector: kubernetes.io/os: linux`, and an
AIX kubelet reports `kubernetes.io/os=aix`, so the DaemonSet never schedules
onto AIX nodes. No manifest changes are needed.

### SRC subsystem conventions

The daemon relies on these SRC subsystem names, all in group `kubernetes`.
The kubelet port and containerd-aix packaging should register under the same
names:

| Service | SRC subsystem | Notes |
|---|---|---|
| containerd-aix | `containerd` | plays CRI-O's role; `crio.service` in policies maps here |
| kubelet (port) | `kubelet` | restarted for kubelet CA rotation and re-bootstrap |
| this daemon | `mcd` | registered by `hack/aix/install-mcd.sh` |

## What a MachineConfig can do on AIX

| MachineConfig content | AIX behaviour |
|---|---|
| `storage.files` | Written atomically with owner and mode. Package-owned files get a `.orig` backup. Ownership is checked with `lslpp -w`, plus `rpm -qf` when the AIX Toolbox is installed. |
| `passwd.users[core].sshAuthorizedKeys` | Written to `/home/core/.ssh/authorized_keys`; AIX OpenSSH has no `authorized_keys.d`. |
| `passwd.users[core].passwordHash` | Set with `chpasswd -e -c`. The hash must be an AIX format: `{ssha512}`, `{ssha256}`, `{ssha1}`, `{smd5}` or DES crypt. Linux `$6$` hashes are rejected with an explanation, because AIX would store them and silently lock the user out. |
| Node disruption policy `Restart` / `Reload` | Mapped to SRC through `aix.SubsystemForUnit`:<br>• `foo.service` becomes subsystem `foo`; `crio.service` becomes `containerd`.<br>• Restart is `stopsrc` (escalating to `stopsrc -f`), then `startsrc`, waiting on `lssrc` at each step.<br>• Reload is `refresh -s`, falling back to `SIGHUP` for signal-only subsystems.<br>• containerd and kubelet cannot reload, so a reload restarts them.<br>• Targets, sockets, timers and mounts are rejected. |
| Node disruption policy `DaemonReload` | No-op: SRC reads definitions from the ODM on every command. |
| Reboot | `nohup shutdown -Fr` runs detached, as `systemd-run` does on Linux. `/etc/rc.shutdown` stops the SRC subsystems. |
| `update-ca-trust` (CA bundle changes) | PEM anchors from `/etc/pki/ca-trust/source/anchors` are mirrored into `/var/ssl/certs` as `mco-anchor-*.pem`. This is the directory Go's `crypto/x509` reads on AIX, so containerd-aix, the kubelet and the MCD trust them. Only `mco-anchor-*` files are ever removed. |
| `osImageURL` | Ignored. The render controller stamps the RHCOS image on every rendered config, so a cluster upgrade does not reboot AIX nodes. |
| `kernelArguments`, `extensions`, `kernelType` (non-default), `fips: true`, `systemd.units`, on-cluster layering images | **Rejected.** The node goes Degraded with one error listing everything to remove from the pool. |

Removed users are deconfigured by locking their password (`*`). On Linux the MCD
clears the hash, which on AIX would allow login without a password.

Other host mechanics:

- **Boot ID:** AIX has no `/proc/sys/kernel/random/boot_id`. The daemon derives
  a stable ID from the `who -b` boot record, which is enough to tell whether
  the node rebooted.
- **Config drift:** fsnotify has no AIX backend. Instead, the on-disk state is
  re-validated every minute with the same checks the inotify monitor runs.
  Each distinct drift is reported once.
- **OS detection:** AIX has no os-release file, so the release comes from
  `oslevel -s`. AIX older than 7.2 is refused. AIX is never a CoreOS variant,
  so every rpm-ostree, bootupd, bootc and extension path stays off.
- **Disabled managers:**
  - PinnedImageSets drive CRI-O's drop-in config and podman.
  - The internal release image registry lives on control-plane nodes.

  Neither manager is started on AIX.

## Setting up an AIX pool

```sh
oc apply -f docs/aix/machineconfigpool.yaml
oc label node <aix-node> node-role.kubernetes.io/aix=
```

AIX nodes must **not** carry `node-role.kubernetes.io/worker`. Otherwise they
would join the worker pool and render the RHCOS worker configs. Put
AIX-specific content in MachineConfigs labelled
`machineconfiguration.openshift.io/role: aix`; see
[docs/aix/example-machineconfigs.yaml](aix/example-machineconfigs.yaml).

## Building and installing

```sh
make machine-config-daemon-aix        # -> _output/aix/ppc64/machine-config-daemon (XCOFF64)
hack/aix/mcd-kubeconfig.sh > mcd.kubeconfig    # cluster-admin oc; long-lived SA token
```

Copy both files to the node, then as root on AIX:

```sh
./install-mcd.sh -b ./machine-config-daemon -k ./mcd.kubeconfig
lssrc -s mcd
tail -f /var/log/machine-config-daemon.log
```

`install-mcd.sh -u` removes the subsystem and its inittab entry.

The daemon tightens the mode of the kubelet's `/etc/kubernetes/kubeconfig` on
every update. On RHCOS a missing file is an error. On AIX it is skipped, so the
daemon can run before the kubelet port is installed.

## Test modes (before the kubelet port lands)

**1. Standalone, no cluster.** Apply a MachineConfig or Ignition file
directly against the node, with no API server involved:

```sh
machine-config-daemon start --once-from=/tmp/50-aix-ssh-banner.yaml --skip-reboot --v=4
```

`--once-from` also takes an Ignition JSON file, or an Ignition config served
over HTTPS (for example by the machine-config-server); a remote MachineConfig
needs cluster access. A local MachineConfig goes through the full update path:
validation, file writing, SSH keys, passwords and the post-config action. A
bare Ignition file only writes files, after the same validation.

**2. Cluster-attached, no kubelet.** Create a `Node` object by hand, labelled
`node-role.kubernetes.io/aix` and `kubernetes.io/os=aix`. Then install with
the kubelet health check off:

```sh
./install-mcd.sh -b ./machine-config-daemon -k ./mcd.kubeconfig -n <node> -- --kubelet-healthz-enabled=false
```

The daemon then reads desired configs from the pool and writes node
annotations and MachineConfigNode status like any node. Drains are empty,
since no pods run there.

On cloud platforms, the cloud node lifecycle controller may delete a Node
object that has no backing instance, so use this mode on platform `None` or
bare metal clusters.

**3. Production.** Once the kubelet port registers the node, install
without the extra flags.

## Build notes

- `CGO_ENABLED=0 GOOS=aix GOARCH=ppc64` with the `containers_image_openpgp` tag,
  which swaps the cgo gpgme signature backend for the pure-Go one. This matches
  the other reuna-labs ports.
- Six small vendored-dependency changes are needed. They are kept in
  `hack/aix/vendor-aix.patch` and applied in `vendor/`:
  - containers/storage: AIX `Stat_t` conversion, fileutils `Exists`, and lockfile `SEEK_SET`.
  - godbus: AIX transport.
  - moby/sys/user: `/etc/passwd` lookup on AIX.

  `go mod vendor` drops them, so run `hack/aix/vendor-patch.sh apply` afterwards.
  `make machine-config-daemon-aix` refuses to build if the patch is missing.
- Linux behaviour is unchanged. The Linux code the AIX build replaces now lives,
  unmodified, in `pkg/daemon/platform_default.go` (`//go:build !aix`), and
  `pkg/daemon/platform_aix.go` holds the AIX versions. Every AIX command is run
  by absolute path through `pkg/daemon/aix`. That package is not build-tagged,
  so its tests run on Linux CI with a scripted runner.

## Known gaps and next steps

- **Controller side.** The render controller does not know a pool is AIX, so
  unsupported content is reported by the node (Degraded) rather than rejected
  at render time. A pool-level OS marker would let it reject such content
  before rollout.
- **Bootstrap.** Joining an AIX node, including first-boot config from the
  machine-config-server, belongs to the kubelet port. `--once-from <MCS URL>`
  is the likely building block.
- **OS updates.** There is no AIX equivalent of `osImageURL` yet. A natural
  mapping is a TL/SP level applied with `suma`/`install_all_updates` plus a
  reboot.
- **Kernel tuning.** Tuning maps to `vmo`/`no`/`ioo`/`schedo -p` and `chdev`, not
  kernel arguments. That needs a MachineConfig extension or a dedicated
  file-plus-action convention.
- **PinnedImageSets** with containerd's CRI.
- **Metrics.** Not exposed off-node yet. Linux nodes use kube-rbac-proxy as a
  pod sidecar for this.
