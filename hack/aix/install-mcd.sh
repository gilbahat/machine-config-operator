#!/usr/bin/sh
#
# Installs machine-config-daemon on an AIX node as the "mcd" SRC subsystem and
# starts it at boot from /etc/inittab. Run as root on the AIX node.
#
#   install-mcd.sh -b ./machine-config-daemon -k ./kubeconfig [-n NODE_NAME] [-- EXTRA_DAEMON_ARGS]
#
#   -b  machine-config-daemon binary built with `make machine-config-daemon-aix`
#   -k  kubeconfig for the machine-config-daemon service account
#       (create it with hack/aix/mcd-kubeconfig.sh)
#   -n  Kubernetes node name (default: hostname)
#   -u  uninstall instead
#
# Everything after "--" is appended to the daemon arguments, for example
# "-- --kubelet-healthz-enabled=false" on a test node without a kubelet.
set -eu

BIN_DIR=/opt/openshift/bin
BIN=$BIN_DIR/machine-config-daemon
CONF_DIR=/etc/machine-config-daemon
KUBECONFIG_PATH=$CONF_DIR/kubeconfig
LOG=/var/log/machine-config-daemon.log
SUBSYS=mcd
GROUP=kubernetes

binary=""
kubeconfig=""
node=$(hostname)
uninstall=0
while getopts "b:k:n:u" opt; do
	case $opt in
	b) binary=$OPTARG ;;
	k) kubeconfig=$OPTARG ;;
	n) node=$OPTARG ;;
	u) uninstall=1 ;;
	*) sed -n '3,18p' "$0"; exit 2 ;;
	esac
done
shift $((OPTIND - 1))
[ "${1:-}" = "--" ] && shift
extra="$*"

if [ "$(uname -s)" != "AIX" ]; then
	echo "this script must run on the AIX node" >&2
	exit 1
fi

if [ $uninstall -eq 1 ]; then
	stopsrc -s $SUBSYS >/dev/null 2>&1 || true
	rmitab $SUBSYS >/dev/null 2>&1 || true
	rmssys -s $SUBSYS >/dev/null 2>&1 || true
	echo "removed SRC subsystem $SUBSYS and its inittab entry (binary and $CONF_DIR left in place)"
	exit 0
fi

[ -n "$binary" ] && [ -n "$kubeconfig" ] || { echo "-b and -k are required" >&2; exit 2; }

mkdir -p $BIN_DIR $CONF_DIR
cp "$binary" $BIN.new
chmod 0755 $BIN.new
mv $BIN.new $BIN
cp "$kubeconfig" $KUBECONFIG_PATH
chmod 0600 $KUBECONFIG_PATH

args="start --node-name=$node --kubeconfig=$KUBECONFIG_PATH --v=2"
[ -n "$extra" ] && args="$args $extra"

# (Re)define the subsystem:
#   -S -n 15 -f 9   signal communication: SIGTERM to stop, SIGKILL to force
#   -w 600          give an in-progress update 10 minutes before SIGKILL
#                   (the same grace period as the Linux MCD pod)
#   -R              restart the daemon if it exits abnormally
#   -Q              single instance
lssrc -s $SUBSYS >/dev/null 2>&1 && { stopsrc -s $SUBSYS >/dev/null 2>&1 || true; rmssys -s $SUBSYS; }
mkssys -s $SUBSYS -G $GROUP -p $BIN -a "$args" -u 0 \
	-S -n 15 -f 9 -w 600 -R -Q \
	-i /dev/null -o $LOG -e $LOG

# Start at boot once TCP/IP is up (the daemon needs the API server).
entry="$SUBSYS:2:once:/usr/bin/startsrc -s $SUBSYS >/dev/console 2>&1"
if lsitab $SUBSYS >/dev/null 2>&1; then
	chitab "$entry"
else
	mkitab -i rctcpip "$entry"
fi

startsrc -s $SUBSYS
lssrc -s $SUBSYS
echo "machine-config-daemon installed; logs: $LOG"
