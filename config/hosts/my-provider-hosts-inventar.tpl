# OpenKubes BYOH host inventory template (proposal; parser not yet implemented).
# Copy to config/hosts/scaleway.yaml or another local *.yaml file.
# Do not commit the filled inventory. No private keys, tokens or kubeconfigs here.
#
# cp config/hosts/my-provider-hosts-inventar.tpl config/hosts/scaleway.yaml
#
# Host addresses and SSH users are illustrative placeholders.
apiVersion: openkubes.io/v1alpha1
kind: BYOHHostInventory
metadata:
  name: my-provider-lab
spec:
  provider: my-provider
  management:
    kindCluster: ok-byoh-make-test
    # The automation will resolve this local path explicitly; do not store contents.
    kubeconfig: ~/.config/ok/byoh-make-test/kubeconfig
    accessMode: ssh-reverse-tunnel
    remoteAPIPort: 16443
  hosts:
    - name: my-control-plane
      role: control-plane
      ssh:
        user: ubuntu
        address: REPLACE_WITH_CONTROL_PLANE_ADDRESS
    - name: my-worker
      role: worker
      ssh:
        user: ubuntu
        address: REPLACE_WITH_WORKER_ADDRESS
