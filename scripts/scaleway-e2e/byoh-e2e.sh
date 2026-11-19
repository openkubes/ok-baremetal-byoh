#!/usr/bin/env bash
# OpenKubes BYOH Scaleway lab helper. Run on Mac from ok-baremetal-byoh repo.
set -uo pipefail

CLUSTER="${CLUSTER:-ok-byoh-scw1}"
NAMESPACE="${NAMESPACE:-default}"
HOST_CP="${HOST_CP:-ok-bm-lab-01}"
HOST_WORKER="${HOST_WORKER:-ok-bm-lab-02}"
WORKER_SSH="${WORKER_SSH:-ubuntu@51.158.62.216}"
WORKER_ROLE="${WORKER_ROLE:-scaleway-worker}"
WORKER_REPLICAS="${WORKER_REPLICAS:-1}"
MGMT_KUBECONFIG="${MGMT_KUBECONFIG:-$HOME/.config/ok/byoh-make-test/kubeconfig}"
WORKLOAD_KUBECONFIG="${WORKLOAD_KUBECONFIG:-$HOME/.config/ok/byoh-scw1/kubeconfig-tunnel}"
WORKER_TEMPLATE="${WORKER_TEMPLATE:-test/e2e/data/infrastructure-provider-byoh/v1beta1/templates/vm/md.yaml}"
WORKER_MANIFEST="${WORKER_MANIFEST:-/tmp/ok-byoh-scw1-worker.yaml}"
CILIUM_CHART="${CILIUM_CHART:-$HOME/temp/kubernauts/ok/ok-cluster/.tools/cilium-1.19.6.tgz}"
AGENT_BINARY="${AGENT_BINARY:-bin/byoh-hostagent-linux-amd64}"
MGMT_FORWARD_URL="${MGMT_FORWARD_URL:-https://127.0.0.1:16443}"
WORKER_ENDPOINT="${WORKER_ENDPOINT:-51.158.62.217:6443}"

say() { printf '%s\n' "$*"; }
fail() { say "ERROR: $*" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || fail "missing command: $1"; }
mgmt() { kubectl --kubeconfig "$MGMT_KUBECONFIG" -n "$NAMESPACE" "$@"; }
workload() { kubectl --kubeconfig "$WORKLOAD_KUBECONFIG" "$@"; }
confirm() {
  local typed
  printf 'Type %s to proceed: ' "$1" >&2
  IFS= read -r typed
  [ "$typed" = "$1" ] || fail 'Confirmation not given; no action taken'
}
requirements() {
  need kubectl; [ -s "$MGMT_KUBECONFIG" ] || fail "missing management kubeconfig: $MGMT_KUBECONFIG"
  [ "$(kubectl --kubeconfig "$MGMT_KUBECONFIG" config current-context 2>/dev/null)" = 'kind-ok-byoh-make-test' ] || fail 'Unexpected management context'
}

cmd=${1:-help}
case "$cmd" in
  status)
    requirements
    say '=== Management Cluster ==='
    mgmt get cluster "$CLUSTER" || exit 1
    mgmt get machinedeployment "${CLUSTER}-md-0" || true
    mgmt get machines -l "cluster.x-k8s.io/cluster-name=$CLUSTER" || true
    mgmt get byohost "$HOST_CP" "$HOST_WORKER" -o wide || true
    if [ -s "$WORKLOAD_KUBECONFIG" ]; then
      say '=== Workload nodes ==='
      workload get nodes -o wide || true
    fi
    ;;
  render-worker)
    requirements; need envsubst
    [ -r "$WORKER_TEMPLATE" ] || fail "worker template not found: $WORKER_TEMPLATE (run from repository root)"
    case "$WORKER_REPLICAS" in
      ''|*[!0-9]*) fail 'WORKER_REPLICAS must be a positive integer' ;;
    esac
    [ "$WORKER_REPLICAS" -ge 1 ] ||
      fail 'WORKER_REPLICAS must be at least 1'

    export CLUSTER_NAME="$CLUSTER" NAMESPACE
    export WORKER_MACHINE_COUNT="$WORKER_REPLICAS"
    export KUBERNETES_VERSION=v1.36.6
    envsubst '${CLUSTER_NAME} ${NAMESPACE} ${WORKER_MACHINE_COUNT} ${KUBERNETES_VERSION}' < "$WORKER_TEMPLATE" \
      | sed "s/role: worker/role: $WORKER_ROLE/" > "$WORKER_MANIFEST" || fail 'render failed'
    if grep -n '\${' "$WORKER_MANIFEST"; then fail 'unresolved template variables'; fi
    say "Rendered $WORKER_MANIFEST"
    mgmt apply --dry-run=server -f "$WORKER_MANIFEST" || fail 'server dry-run failed'
    ;;
  apply-worker)
    # Multi-worker rendering is supported, but host onboarding
    # remains single-worker until inventory validation is implemented.
    case "$WORKER_REPLICAS" in
      1) ;;
      *) fail 'apply-worker currently supports exactly one worker; multi-worker onboarding requires host inventory preflight' ;;
    esac

    requirements
    [ -s "$WORKER_MANIFEST" ] || fail 'render-worker first'
    need ssh
    say 'Checking worker API path (TLS reachability only; not a credential test)'
    ssh "$WORKER_SSH" "curl -ksSf --connect-timeout 5 --max-time 8 https://$WORKER_ENDPOINT/readyz" || fail 'worker cannot reach workload API'
    say; say 'Checking agent is stopped before binding'
    current=$(ssh "$WORKER_SSH" 'systemctl is-active byoh-hostagent.service 2>/dev/null || true')
    [ "$current" != 'active' ] || fail 'worker agent must be stopped before applying'
    mgmt get byohost "$HOST_WORKER" >/dev/null || fail 'worker not enrolled'
    mgmt apply --dry-run=server -f "$WORKER_MANIFEST" || fail 'dry-run failed'
    say "Planned changes:"
    say "  Cluster: $CLUSTER"
    say "  Namespace: $NAMESPACE"
    say "  Worker host: $HOST_WORKER"
    say "  Host label: role=$WORKER_ROLE"
    say "  Manifest: $WORKER_MANIFEST"
    confirm "apply-worker-$CLUSTER"
    mgmt label byohost "$HOST_WORKER" "role=$WORKER_ROLE" --overwrite || fail 'label failed'
    mgmt apply -f "$WORKER_MANIFEST" || fail 'apply failed'
    say 'Worker desired state submitted. Inspect status/binding before starting agent.'
    ;;
  enroll-create)
    requirements
    [ "$(mgmt get bootstrapkubeconfig "$HOST_WORKER" --ignore-not-found -o name)" = '' ] || fail 'bootstrap object already exists'
    ca=$(kubectl --kubeconfig "$MGMT_KUBECONFIG" config view --raw --minify -o jsonpath='{.clusters[0].cluster.certificate-authority-data}')
    [ -n "$ca" ] || fail 'management CA data absent'
    confirm "create-bootstrap-$HOST_WORKER"
    printf 'apiVersion: infrastructure.cluster.x-k8s.io/v1beta1\nkind: BootstrapKubeconfig\nmetadata:\n  name: %s\n  namespace: %s\nspec:\n  apiserver: %s\n  certificate-authority-data: %s\n' \
      "$HOST_WORKER" "$NAMESPACE" "$MGMT_FORWARD_URL" "$ca" | mgmt create -f -
    unset ca
    say 'Bootstrap credential created; valid for a limited time. Never share status output.'
    ;;
  enroll-transfer)
    requirements; need ssh

    # Check readiness without retrieving or displaying the credential.
    ready=$(mgmt get bootstrapkubeconfig "$HOST_WORKER" \
      -o go-template='{{if .status.bootstrapKubeconfigData}}ready{{end}}') \
      || fail 'bootstrap credential lookup failed'
    [ "$ready" = ready ] || fail 'bootstrap credential not populated'

    # Never replace an existing remote bootstrap credential.
    ssh -T "$WORKER_SSH" \
      'sudo -n test ! -e /root/.byoh/bootstrap.kubeconfig' \
      || fail 'remote credential exists or preflight failed'

    say "Target host: $WORKER_SSH"
    say "Credential destination: /root/.byoh/bootstrap.kubeconfig"
    confirm "transfer-bootstrap-$HOST_WORKER"

    ssh -T "$WORKER_SSH" \
      'sudo -n install -d -m 700 /root/.byoh' \
      || fail 'remote directory creation failed'

    # Stream directly; never keep credentials in a shell variable.
    # pipefail ensures kubectl and SSH errors are propagated.
    mgmt get bootstrapkubeconfig "$HOST_WORKER" \
      -o jsonpath='{.status.bootstrapKubeconfigData}' |
      ssh -T "$WORKER_SSH" 'sudo -n sh -c '"'"'
        set -eu
        umask 077
        dir=/root/.byoh
        test ! -e "$dir/bootstrap.kubeconfig"
        tmp=$(mktemp "$dir/.bootstrap.kubeconfig.XXXXXX")
        cleanup() {
          rm -f -- "$tmp"
        }
        trap cleanup EXIT
        cat > "$tmp"
        test -s "$tmp"
        chmod 600 "$tmp"
        # Atomic no-clobber creation on the same filesystem.
        # ln fails if the destination already exists.
        ln "$tmp" "$dir/bootstrap.kubeconfig"
        rm -f "$tmp" || exit 1
        trap - EXIT
      '"'"'' || fail 'credential transfer failed'

    say 'Credential installed with restricted permissions.'
    ;;
  inspect-csr)
    requirements; need openssl; need base64
    csr="byoh-csr-default.$HOST_WORKER"
    mgmt get csr "$csr" -o jsonpath='Username={.spec.username}{"\n"}Signer={.spec.signerName}{"\n"}Usages={.spec.usages}{"\n"}Groups={.spec.groups}{"\n"}' || exit 1
    mgmt get csr "$csr" -o jsonpath='{.spec.request}' | base64 -d | openssl req -inform PEM -noout -subject -nameopt RFC2253
    say 'Approve only after confirming identity, usages, signer, bootstrap group and requestor.'
    ;;
  approve-csr)
    requirements
    need openssl
    need base64
    need jq

    csr="byoh-csr-$NAMESPACE.$HOST_WORKER"

    # Validate CSR metadata without exposing credentials.
    mgmt get csr "$csr" -o json |
      jq -e \
        --arg namespace "$NAMESPACE" \
        --arg host "$HOST_WORKER" '
        .spec.signerName == "kubernetes.io/kube-apiserver-client"
        and (.spec.usages == ["client auth"])
        and (.spec.groups | index("system:bootstrappers:byoh") != null)
        and (.spec.username | startswith("system:bootstrap:"))
        and ((.status.conditions // []) | length == 0)
      ' >/dev/null || fail 'CSR metadata validation failed'

    subject=$(
      mgmt get csr "$csr" -o jsonpath='{.spec.request}' |
        base64 -d |
        openssl req -inform PEM -noout -subject -nameopt RFC2253
    ) || fail 'CSR subject parsing failed'

    expected="CN=byoh:host:$NAMESPACE:$HOST_WORKER,O=byoh:hosts"

    case "$subject" in
      "subject=$expected"|"subject= $expected")
        ;;
      *)
        fail "Unexpected CSR subject: $subject"
        ;;
    esac

    say "CSR validated: $csr"
    say "Subject: $subject"
    say "Signer, usages, bootstrap group and pending state validated."

    confirm "approve-$HOST_WORKER"
    mgmt certificate approve "$csr" || fail 'CSR approval failed'
    ;;
  enroll-revoke)
    requirements
    need ssh
    ssh "$WORKER_SSH" 'sudo test -s /root/.byoh/config' || fail 'permanent client kubeconfig missing'
    confirm "revoke-$HOST_WORKER"
    mgmt delete bootstrapkubeconfig "$HOST_WORKER" --wait=true || fail 'could not revoke'
    ssh "$WORKER_SSH" 'sudo rm -f /root/.byoh/bootstrap.kubeconfig' || fail 'remote file removal failed'
    say 'Bootstrap resource deleted and remote token file removed.'
    ;;
  agent-install)
    requirements; need ssh; need scp
    [ -s "$AGENT_BINARY" ] || fail "missing agent binary $AGENT_BINARY"
    # Systemd unit is installed but NOT started. Local SSH-config aliases are not used.
    confirm "install-agent-$HOST_WORKER"
    scp "$AGENT_BINARY" "$WORKER_SSH:/tmp/byoh-hostagent" || fail 'scp failed'
    ssh "$WORKER_SSH" 'sudo install -m 755 /tmp/byoh-hostagent /usr/local/bin/byoh-hostagent && rm /tmp/byoh-hostagent' || fail 'binary install failed'
    ssh "$WORKER_SSH" 'sudo install -d -m 755 /etc/systemd/system; sudo tee /etc/systemd/system/byoh-hostagent.service >/dev/null <<"UNIT"
[Unit]
Description=OpenKubes BYOH Host Agent
After=network-online.target
Wants=network-online.target
[Service]
Type=simple
User=root
WorkingDirectory=/root
ExecStart=/usr/local/bin/byoh-hostagent --namespace default --skip-installation
Restart=on-failure
RestartSec=5
[Install]
WantedBy=multi-user.target
UNIT
sudo systemctl daemon-reload; sudo systemctl enable byoh-hostagent.service' || fail 'unit install failed'
    say 'Agent binary and stopped enrollment-phase unit installed. No agent started.'
    ;;
  agent-arm-worker)
    requirements; need ssh
    ssh "$WORKER_SSH" 'sudo test -s /root/.byoh/config' || fail 'permanent client kubeconfig missing'
    agent_state=$(ssh -T "$WORKER_SSH"       'sudo -n systemctl is-active byoh-hostagent.service') || {
      [ "$agent_state" = inactive ] ||
        fail 'cannot confirm agent is inactive'
    }

    [ "$agent_state" = inactive ] ||
      fail "agent must be inactive (current state: $agent_state)"
    ref=$(mgmt get byohost "$HOST_WORKER" -o jsonpath='{.spec.byoMachineRef.name}')
    [ -n "$ref" ] || fail 'worker host has no ByoMachine binding'
    case "$ref" in "$CLUSTER"-md-0-*) ;; *) fail "unexpected binding $ref";; esac
    confirm "arm-worker-$HOST_WORKER"
    ssh "$WORKER_SSH" 'sudo -n cp -a /etc/systemd/system/byoh-hostagent.service /etc/systemd/system/byoh-hostagent.service.pre-worker && sudo -n sed -i "s/ --skip-installation//" /etc/systemd/system/byoh-hostagent.service && sudo -n systemctl daemon-reload && sudo -n systemctl start byoh-hostagent.service' || fail 'agent start failed'
    say 'Worker agent started; installation/bootstrap may execute as root now.'
    ;;
  verify)
    requirements
    [ -s "$WORKLOAD_KUBECONFIG" ] || fail 'workload kubeconfig missing'
    say '=== Nodes ==='
    workload get nodes -o wide || exit 1
    say '=== Cluster ==='
    mgmt get cluster "$CLUSTER" || exit 1
    say '=== MachineDeployment ==='
    mgmt get machinedeployment "${CLUSTER}-md-0" || exit 1
    say '=== Machines ==='
    mgmt get machines -l "cluster.x-k8s.io/cluster-name=$CLUSTER" || exit 1
    say '=== Cilium ==='
    workload -n kube-system get daemonset cilium || exit 1
    ;;
  help|*)
    cat <<'HELP'
Usage: scripts/byoh-e2e.sh COMMAND
  status           Read-only management and workload status
  render-worker    Render local repo vm/md.yaml and server-side dry-run
  apply-worker     Check host/API, label worker and create four resources (confirmation)
  agent-install    Copy agent, install stopped unit with WorkingDirectory=/root
  enroll-create    Create short-lived BootstrapKubeconfig (confirmation)
  enroll-transfer  Transfer credential directly to worker via SSH
  inspect-csr     Inspect CSR without revealing secrets
  approve-csr     Validate subject/signer/usages/groups then explicitly approve
  enroll-revoke   Delete bootstrap resource/token and remote bootstrap file
  agent-arm-worker Start stopped, bound worker agent for installation (confirmation)
  verify           Read-only final validation
Environment overrides: CLUSTER, NAMESPACE, WORKER_SSH, MGMT_KUBECONFIG,
WORKLOAD_KUBECONFIG, WORKER_MANIFEST, WORKER_TEMPLATE, WORKER_ROLE,
HOST_WORKER, AGENT_BINARY, MGMT_FORWARD_URL, WORKER_ENDPOINT.
HELP
    ;;
esac
