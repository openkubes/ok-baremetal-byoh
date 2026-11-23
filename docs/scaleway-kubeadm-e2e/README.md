> **Provider-specific E2E lab records.** Everything in this directory documents the OpenKubes
> end-to-end lab, which runs on Scaleway: plans, runbooks, results and evidence of real runs. It is
> kept as historical and test evidence. It is **not** a product requirement and **not** a default:
> BYOH works on any bare-metal host or virtual machine that meets the host requirements. Current lab
> operation lives in the separate `ok-scaleway-lab` repository.

> **Verified two-node E2E baseline (20 November 2026):** [Reproducible baseline and explicit limitations](two-node-e2e-baseline-2026-11-20.md). Control-plane + worker Kubernetes v1.36.6 Ready, MD 1/1 and Cilium 2/2; cross-node policy tests and generalized orchestration remain open.
>
> **Current Make replay:** See [Make lifecycle runbook](make-lifecycle-runbook.md) for the
> live-verified management, enrollment, CP, network, and Cilium steps
> (19 November 2026). The worker Make orchestration is still pending;
> this page also contains historical manual-E2E evidence and plans,
> which must not be mistaken for today's complete live replay.
>
# OpenKubes BYOH – Manual Scaleway kubeadm E2E → Make → `ok up`

## Aktueller Stand – 19. November 2026

**Status: Scaleway BYOH E2E erfolgreich – Automatisierung in Vorbereitung.**

Der ursprünglich geplante Provisionierungsweg wurde erfolgreich
auf zwei vorhandenen Scaleway-Bare-Metal-Servern validiert:

- Management: lokaler kind-Cluster `ok-byoh-make-test`
- Workload-Cluster: `ok-byoh-scw1`
- Control Plane: `ok-bm-lab-01`, Kubernetes Ready
- Worker: `ok-bm-lab-02`, Kubernetes Ready
- Kubernetes: `v1.36.6`
- Cilium: `v1.19.6`
- containerd: `2.3.7`
- CAPI Cluster: `Available=True`
- MachineDeployment: `1/1 Ready`

Der erfolgreiche Test belegt Provisionierung, Host-Registrierung,
kubeadm-Bootstrap und grundlegende Cluster-Readiness.

**Nicht nachgewiesen:** HA, vollständige Cilium-Konnektivität,
Failure-Recovery, produktionsreife Netzwerkarchitektur und
unbeaufsichtigte Host-Provisionierung.

### Betriebsgrenzen der bestehenden Testumgebung

Die laufende Umgebung verwendet temporäre SSH-Tunnel und
DNAT-Regeln für die Kommunikation zwischen Management-Cluster,
BYOH-Hosts und Workload-Cluster.

Diese Verbindungen dürfen nicht automatisch beendet oder
verändert werden, solange die Testumgebung verwendet wird.

**Keine Änderungen an Scaleway, OpenTofu oder den laufenden
Cluster-Ressourcen im Rahmen der Automatisierungsintegration.**

### Dokumentation

- [Erfolgreicher E2E und Automatisierung](e2e-results.md)
- [Netzwerkpfade und temporäre Tunnel](network-paths.md)
- [Cilium-Installation und Voraussetzungen](cilium.md)

### Automatisierung

Die Skripte unter `scripts/scaleway-e2e/` bilden die
kontrollierten Schritte des erfolgreichen E2E ab.

Die Automatisierung befindet sich weiterhin in Entwicklung.
Mutation Commands dürfen nicht ohne gesonderte Prüfung und
Freigabe gegen den bestehenden Cluster ausgeführt werden.

Worker-Replikate sind beim Manifest-Rendering konfigurierbar.
Das automatisierte Multi-Worker-Onboarding ist noch nicht
freigegeben. Control-Plane-HA und ClusterClass bleiben
separate spätere Erweiterungen.

---


**Status:** Plan / Vorbereitung · **Stand:** 2026-11-18  
**Repositories:** [`openkubes/ok-baremetal-byoh`](https://github.com/openkubes/ok-baremetal-byoh) und [`openkubes/ok-cluster`](https://github.com/openkubes/ok-cluster)  
**Vorgeschlagener Ablageort:** `ok-baremetal-byoh/docs/scaleway-kubeadm-e2e/README.md`  
**Umgebungen:** lokaler macOS-M5-Rechner mit vorhandenem Kind-Cluster; später **neu** bereitgestellte Scaleway-Ubuntu-Hosts.

> **Grundsatz:** **Erst jeden Schritt manuell verifizieren, danach mit Make automatisieren, zuletzt in `ok up` integrieren.** Keine zweite Cluster-Provisionierungs- oder Lifecycle-Engine. Kubernetes-Cluster werden deklarativ über Cluster API (CAPI) und den BYOH Infrastructure Provider verwaltet. Scaleway-Hosts werden getrennt von BYOH bereitgestellt.

## 1. Ziel und Nichtziele

### Ziel

Ein reproduzierbarer und überprüfbarer End-to-End-Pfad:

1. Altumgebung mit OpenTofu vollständig und nachweisbar bereinigen.
2. Separate, frische Scaleway-Ubuntu-Hosts über den ausdrücklich freigegebenen Infrastruktur-Workflow bereitstellen.
3. Lokalen Kind-Management-Cluster prüfen und mit einem **verifizierten OpenKubes-BYOH-Build** betreiben.
4. Neue Linux-Hosts sicher registrieren.
5. Ein kubeadm-basiertes Kubernetes-Workload-Cluster aus CAPI/BYOH-Ressourcen erstellen (initial: eine Control Plane, ein Worker).
6. Zustand, Fehlerfälle, Reconciliation und kontrollierten Cleanup anhand von Evidence prüfen.
7. Genau diesen getesteten Ablauf in Make-Targets und anschließend in den bestehenden `ok up`-Vertrag übernehmen.

### Vorerst nicht im Umfang

- Kein Same-Plane-Management-Pivot im ersten E2E.
- Keine RKE2-/Kubespray-Provisionierung innerhalb des BYOH-Pfads: diese bleiben separate Implementierungswege.
- Keine automatische Provisionierung oder Löschung von Scaleway-Ressourcen durch BYOH.
- Kein öffentlicher Kind-API-Endpunkt ohne private Netz-/Zugriffsschranken.
- Kein produktionsreifes Multi-Cluster-/HA-Versprechen aus einem ersten Zwei-Host-Smoke-Test.

## 2. Bekannter Ausgangszustand (2026-11-18)

Der lokale Kind-Cluster **`ok-bm-bootstrap` existiert bereits** und wurde aus der Shell-History nachweislich **manuell über `kind create cluster --name ok-bm-bootstrap ...`** erstellt; anschließend wurde CAPI mit `clusterctl init` installiert. Der ursprüngliche vollständige Kind-Befehl samt Netzparametern ist noch zu archivieren (`~/.zsh_history`, ungefähr Zeilen 14455 ff.; `clusterctl init` ungefähr 14595 ff.).

Read-only-Beobachtungen:

| Komponente | Beobachteter Zustand |
| --- | --- |
| Kind-Node | `ok-bm-bootstrap-control-plane`, `Ready`, Kubernetes `v1.35.0` |
| CAPI Core Controller | `1/1` |
| kubeadm Bootstrap Controller | `1/1` |
| kubeadm Control Plane Controller | `1/1` |
| cert-manager | Bereit |
| Alter BYOH Controller | Deployment `byoh-system/byoh-controller-manager`, **`0/0`**, bewusst deaktiviert |
| `BootstrapKubeconfig` / `ByoHost` | Keine Objekte gefunden |
| Bootstrap-Token-Secrets (`kube-system`) | **0** |

**Historischer Vorfall:** Der zuvor eingesetzte `mensylisir`-BYOH-Controller erzeugte unerwartet zahlreiche Bootstrap-Token-Secrets; nach der Untersuchung wurde der Controller auf null Replicas skaliert und die Testressourcen/Tokens bereinigt. Dieser Fehler ist ein **Release-Blocker**, bis Idempotenz und Cleanup im konkret zu installierenden OpenKubes-Build nachgewiesen sind. Der vorhandene Zustand ist kein Nachweis, dass der OpenKubes-Fork bereits installiert ist.

**Kubeconfig-Falle:** Auf dem Mac zeigte `KUBECONFIG` auf `.../hetzner/ok-rke2/kubeconfig/scw1.yaml`, weshalb `kubectl --context kind-ok-bm-bootstrap` fehlschlug. Daher im Runbook **immer explizite `--kubeconfig`-Pfade** verwenden; nie stillschweigend den aktiven Context.

Eine lokal extrahierte Kind-Kubeconfig liegt aktuell unter `/tmp/ok-bm-bootstrap-kubeconfig` (Modus `0600`), ist aber flüchtig und muss vor einem späteren Test neu verifiziert werden. **Kubeconfigs, Tokens und Raw-Logs nie committen oder im Issue posten.**

## 3. Verbindliche Schutzgrenzen

- **Keine Änderungen** an bestehenden Scaleway-Hosts (`scw1`, `scw2`) oder OpenTofu-Ressourcen ohne gesonderte ausdrückliche Freigabe.
- Der vorhandene Kind-Cluster `ok-bm-bootstrap` bleibt bis zur gesonderten Freigabe **read-only**. **Keine** automatische Wiederaktivierung des bisherigen Controllers, keine `clusterctl init`-/`kubectl apply`-Versuche und keine Test-Specs gegen ihn.
- Vor jedem Schreibzugriff: Cluster-Identität, Kubeconfig-Datei, API-Endpunkt, Namespace, erwartete Ressourcen und geplante Änderung sichtbar prüfen.
- Keine automatischen Sicherheits-Bypässe (`--insecure-skip-tls-verify`, ungeschützter Internet-Port, pauschale CSR-Autoapproval-Berechtigung, privilegierter Zugriff auf fremde Hosts).
- Der BYOH-Agent hat Root-Rechte auf dem Host. Nur **dedizierte Testmaschinen** verwenden, deren Datenverlust ausdrücklich akzeptiert und freigegeben ist.
- **Keine automatischen Löschungen** von Kubernetes-Ressourcen, Host-State oder lokalen Lifecycle-Receipts bei Fehlern; Fehlerzustände für die Analyse erhalten.
- Kein GitHub-Merge und keine destruktiven Infrastrukturaktionen ohne menschliche Freigabe.

## 4. Phasen und Exit-Kriterien

### Phase 0 – Quellen, Inventar, Freigabe (nur read-only)

**Arbeiten**

- Exakte Git-SHAs von `ok-baremetal-byoh`, `ok-cluster`, allen Images und CAPI-Komponenten dokumentieren.
- Den vollständigen historischen Kind-Erstellungsbefehl und die tatsächlichen Netzwerk-/Port-Parameter rekonstruieren.
- Deployment-Image, ProviderInventory, CRDs, Webhooks, Namespaces, Cluster-Identität und Token-Anzahl des vorhandenen Kind-Clusters erfassen.
- Im BYOH-Fork die Bootstrap-Token-Idempotenz, CSR-Authorisierung, Host-Reset und Cleanup-Risiken aus Code **und** Tests prüfen. Den früheren Vorfall reproduzierbar im isolierten Test erfassen; kein Versuch mit produktiven Hosts.
- Upstream-Reste in `test/e2e/config/provider.yaml`, `Makefile`, `docs/getting_started.md` prüfen: aktuell gibt es noch `superbrothers`-Image-/Release-Referenzen. Ein installationsfähiges, verifiziertes **OpenKubes-Artefakt** muss klar bestimmt sein.
- Den OpenTofu-Rebuild-/Bereinigungsplan mit Bestands-, Git- und State-Abgleich festhalten. Vor jeglichem `destroy` die bisher außerhalb von Git liegenden Inputs und Artefakte sichern; vorhandene Preservation-Audit-Gaps zuerst schließen.

**Exit:** freigegebenes Testinventar, nachvollziehbare Quellen-/Image-Pins, akzeptiertes Netz-/Sicherheitsdesign, keine ungeklärte Token-Flut. **Kein** Infrastruktur- oder Cluster-Write bis dahin.

### Phase 1 – Alte Scaleway-Umgebung sichern und bereinigen (Freigabe erforderlich)

**Arbeiten**

- Gültigen OpenTofu-Workspace, State, Ressourceninventar und alle externen Abhängigkeiten read-only prüfen.
- Notwendige Artefakte gemäß Preservation-Audit sichern und Wiederherstellbarkeit dokumentieren.
- Konkreten `tofu plan -destroy` unabhängig reviewen und **explizit freigeben lassen**.
- Erst dann OpenTofu-Destroy ausführen; danach mit OpenTofu und Scaleway-Provider/API nachweisen, dass **die ausgewählten alten Ressourcen** wirklich entfernt sind.
- Verbliebene externe Ressourcen (z. B. IPs, Volumes, Security Groups, DNS) gesondert erfassen; nichts implizit löschen.

**Exit:** von Nutzer bestätigter Cleanup mit Belegen. **Ohne Exit keine Neu-Provisionierung für diesen Test.**

### Phase 2 – Neue Scaleway-Testhosts und Netz (Freigabe erforderlich)

**Arbeiten**

- Frische, dedizierte Ubuntu-24.04-Hosts bereitstellen: initial **2 Hosts** mit eindeutigen Hostnamen; Architektur/Größe nach Provider- und kubeadm-Anforderungen festlegen.
- Firewall, SSH, Zeit-Synchronisation, Kernel-/Containerd-Voraussetzungen und API-Erreichbarkeit prüfen.
- Einen privaten, authentifizierten Kommunikationsweg zwischen Mac/Kind-Management-API und Scaleway-Hosts festlegen (z. B. VPN); **nicht** pauschal `0.0.0.0:6443` exponieren.
- Auch Host↔Host-, Control-Plane-Endpunkt-, kubelet-, CNI- und etcd-Verkehr für den geplanten Workload-Cluster vorab klären.
- Das Risiko eines **lokalen Mac-Laptops als langlebiger Management-API** dokumentieren (Schlafmodus, wechselnde IP, Tunnelunterbrechung): erster E2E nur unter kontrollierten Bedingungen; keine Betriebsfreigabe daraus ableiten.

**Exit:** beide Hosts erreichbar, Netzwerkpfade/Firewall und Credential-Scope dokumentiert, OpenTofu-Owner und Destroy-Pfad bekannt.

### Phase 3 – CAPI/OpenKubes-BYOH Management vorbereiten (Freigabe erforderlich)

**Arbeiten**

- Entscheidung dokumentieren: vorhandenen `ok-bm-bootstrap` nach gesonderter Freigabe wiederverwenden **oder** einen neuen, eindeutig benannten isolierten Kind-Cluster erstellen. Keine stillschweigende Mutation des Bestandsclusters.
- Vorhandene CAPI-Version, CAPI-Verträge, CRDs, Controller und Webhooks abgleichen. Kein zweites paralleles BYOH-Deployment über den alten Controller installieren.
- BYOH-Manager aus eindeutigem OpenKubes-Commit bauen/veröffentlichen oder verifiziertes Release-Artefakt mit **immutable Image-Digest** nutzen. `clusterctl`-Provider-Konfiguration muss **explizit unseren Fork** referenzieren, nicht ein implizites Upstream-Release.
- Token-/CSR-Freigabe initial **manuell** und mit eingeschränktem Namespace/Host-Scope. Historische BYOH-Limitierungen (bootstrap-token nicht hostgebunden; mögliche Cross-Namespace-CSR-Registrierung) vor Live-Verwendung als Risiko akzeptieren oder beheben.
- Controller vor Aktivierung durch isolierte Tests auf Token-Idempotenz und Restart-Reconciliation prüfen; direkt danach Tokenzahl engmaschig beobachten.

**Exit:** CAPI/Controller healthy, Installation und Image-Herkunft überprüft, keine unerwarteten Secrets/CSRs, Providerzustand dokumentiert.

### Phase 4 – Host Registration und manueller kubeadm-Cluster (Freigabe erforderlich)

**Arbeiten**

1. Dedizierten Namespace, erwartete Hosts und CAPI-/BYOH-Objekte deklarativ planen.
2. Nach bestätigter API-Erreichbarkeit pro Host den kurzlebigen `BootstrapKubeconfig` kontrolliert erzeugen, geschützt übertragen und registrieren. **Keine Tokens in Konsolenausgaben oder Git.**
3. CSR-Identität, Hostname, Namespace und Signing Requests **vor Genehmigung** vergleichen.
4. `ByoHost`-Registrierung und die für die Testmaschinen tatsächlich erteilten Berechtigungen bestätigen.
5. Zunächst Cluster-/Control-Plane-/Machine-/Bootstrap-/Infrastructure-Ressourcen gemäß dem versionierten BYOH-CAPI-Template anwenden; ein Control-Plane- und ein Worker-Host.
6. Independent Readiness-Checks: Cluster, Machines, BYOH-Mappings, kubeadm, Nodes, CNI, Workload-Pods, Zertifikate und Events.
7. Bei Timeouts oder Reconciliation-Problemen **stoppen**; Ressourcen nicht blind erneut erstellen oder per Force löschen.

**Exit:** beide Workload-Nodes `Ready`, Clusterfunktion und eindeutige Hostzuordnung belegt; keine Tokenflut und kein unkontrollierter Hostzugriff.

### Phase 5 – Lifecycle-, Failure- und Cleanup-Tests (separate Freigaben)

**Arbeiten**

- Kontrollierte Controller-/Agent-Neustarts, Reconciliation-Idempotenz, wiederholte Lesetests und Fehlerzustände prüfen.
- Bewusst autorisierte Testfälle für fehlende Verbindung, abgelaufene Bootstrap-Tokens und doppelte Hostidentität durchführen.
- Geplanten Node-/Machine-Delete, `kubeadm reset`, Installer-Uninstall und Host-Wiederverwendung einzeln prüfen; **immer zuerst Review der konkreten Zielhosts**.
- CAPI-Ressourcen, CSR/Secrets, Netzwerk und Hostzustand nach Teardown prüfen. Unvollständige Cleanup-Zustände als Fehler dokumentieren, nicht durch manuelles Löschen kaschieren.

**Exit:** nachvollziehbare Lifecycle-Receipts, keine unerwarteten Ressourcen, definierter Recovery-Pfad und freigegebener Teardown-Nachweis.

### Phase 6 – Make-Automatisierung (erst nach manuellem E2E)

Ziel: reine, nachvollziehbare Orchestrierung **derselben geprüften Schritte**, kein zweiter Controller.

**Kandidaten** (Namen sind Vorschläge, noch nicht implementiert):

| Target | Aufgabe | Default |
| --- | --- | --- |
| `byoh-preflight` | Quellen, Identitäten, Netzwerk, Berechtigungen und Provider-Status prüfen | Read-only |
| `byoh-plan` | Versionierte CAPI-/BYOH-Manifeste und Änderungen darstellen | Read-only |
| `byoh-provider-install` | Pinned OpenKubes-Controller installieren | Explizite Freigabe |
| `byoh-host-register` | Host-Registrierung begleiten | Explizite Freigabe; Credentials privat |
| `byoh-cluster-create` | Geprüfte CAPI-Manifeste anwenden | Explizite Freigabe |
| `byoh-verify` | Independent Cluster/Node-Readiness und Evidence | Read-only |
| `byoh-cluster-delete` | Kontrollierter Cluster-Teardown | Separate destruktive Freigabe |

**Guardrails:** immutable Digests, explizite Kubeconfig/Context-/UID-Pins, fail-closed Defaults, kein `kubectl --force`, kein impliziter OpenTofu-Destroy, keine Secret-Ausgabe, idempotente Resume-Semantik mit erhaltenen Receipts.

**Exit:** Make-Ablauf reproduziert den manuellen E2E auf **neuen freigegebenen Testressourcen**, mit Negativtests und nachvollziehbarem Cleanup.

### Phase 7 – Integration in `ok up`

- `ok up` bleibt der **eine** Nutzer-Einstiegspunkt; BYOH wird als weitere CAPI-Infrastructure-Provider-Option modelliert.
- Bestehende ClusterRequest-, CAPI-, Zielidentitäts-, Confirmation-, Plan-, Evidence- und Lifecycle-Contracts **vor** Umsetzung überprüfen; heute ist der einfache Same-Plane-Pfad in `ok-cluster` explizit auf **Talos/KubeVirt** begrenzt.
- Kind-Erzeugung nur bei Bedarf und nur über einen vorhandenen, **sicher abgegrenzten** Kind-Lifecycle-Baustein. `ok local` ist kein erforderlicher Nutzerbefehl für BYOH.
- Kein blindes Aufrufen von Make als alternative Authority-Engine: CLI muss dieselben Sicherheits-/Autorisierungsgrenzen beibehalten und den ausführenden CAPI-Pfad klar besitzen.
- Same-Plane-Pivot als **eigenen späteren** Meilenstein behandeln; Kind-Bootstrap wird vorher nicht automatisch entfernt.

**Exit:** CLI erreicht mit expliziter Providerwahl denselben geprüften E2E und erzeugt gleichwertige Evidence; bestehende Talos/KubeVirt-Tests bleiben grün.

## 5. Sofort ausführbare Read-only-Prüfungen

Diese Befehle **verändern keine Kubernetes-Ressourcen**. `kind get kubeconfig` schreibt nur an die explizit angegebene lokale Datei; deren Inhalt geheim halten.

```bash
kind get clusters
kind get kubeconfig --name ok-bm-bootstrap > /tmp/ok-bm-bootstrap-kubeconfig
chmod 600 /tmp/ok-bm-bootstrap-kubeconfig
BM_KUBECONFIG=/tmp/ok-bm-bootstrap-kubeconfig

kubectl --kubeconfig "$BM_KUBECONFIG" get nodes -o wide
kubectl --kubeconfig "$BM_KUBECONFIG" get deployments -A
kubectl --kubeconfig "$BM_KUBECONFIG" get providers.clusterctl.cluster.x-k8s.io -A
kubectl --kubeconfig "$BM_KUBECONFIG" get crds | grep -Ei 'byoh|bootstrapkubeconfig' || true
kubectl --kubeconfig "$BM_KUBECONFIG" -n byoh-system get deployment byoh-controller-manager \
  -o jsonpath='{.spec.template.spec.containers[*].image}{"\n"}'
kubectl --kubeconfig "$BM_KUBECONFIG" get bootstrapkubeconfigs,byohosts -A
kubectl --kubeconfig "$BM_KUBECONFIG" -n kube-system get secrets \
  --field-selector type=bootstrap.kubernetes.io/token --no-headers | wc -l
```

**Nicht ausführen, bevor Phase 0 und die jeweilige Freigabe vollständig sind:** `kubectl apply`, `clusterctl init`, `kubectl scale`, `kind delete`, `tofu destroy`, Host-Agent-Installation, `kubeadm init/reset` und alle Make-Targets mit Schreibwirkung.

## 6. Evidenz und Entscheidungsprotokoll

Für jeden Testlauf ein privates Evidence-Verzeichnis außerhalb von Git anlegen, mit:

- Datum, Operator, Host- und Cluster-Inventar, exakten Git-SHAs, Tool-Versionen, Image-Digests und CAPI-Kompatibilität.
- Geprüften Change-Plan inklusive Ziel-Identität und **wer** den Schritt freigegeben hat.
- Nicht geheime Kommandos/Exit-Codes, Ressourcen-UIDs, Readiness-Ergebnisse und Zeitpunkte.
- **Vorher-/Nachher-Anzahlen** von Bootstrap-Tokens, CSRs, `BootstrapKubeconfig`, `ByoHost`, Cluster und Machines.
- Fehler, Stop-Bedingungen, noch offene Cleanup-Aufgaben und Abschlussentscheidung.

Keine ungefilterten Kubeconfigs, Tokenwerte, privaten Schlüssel oder potenziell sensitiven Installations-/Make-Logs in Tickets, PRs oder Chat posten.

## 7. Offene Entscheidungen vor der ersten Mutation

1. Soll der vorhandene, historisch belastete `ok-bm-bootstrap` überhaupt wiederverwendet werden – oder isolieren wir den neuen Test mit einem separaten Kind-Cluster? **Bis zur Entscheidung unverändert lassen.**
2. Welcher **konkrete** OpenKubes-BYOH-Commit und welches immutable Controller-Image sind freigegeben? Ist der Token-Idempotenz-Fix nachgewiesen?
3. Wie wird die lokale Kind-Management-API aus den Scaleway-Hosts ausschließlich privat erreicht? Wo liegt der kubeadm-Control-Plane-Endpunkt?
4. Welche zwei dedizierten Scaleway-Host-SKUs/Netzwerke werden verwendet, und wie wird ihr OpenTofu-Owner/State getrennt?
5. Ist der Preservation-Audit vor dem alten OpenTofu-Destroy vollständig geschlossen?
6. Welche CAPI-/Kubernetes-Patch-Versionen, Pod-/Service-CIDRs und CNI werden für den ersten Test festgehalten?
7. Wo werden nicht geheime E2E-Receipts und die dazugehörigen Freigaben abgelegt?

## 8. Nächste konkrete Aktion

**Jetzt ausschließlich Phase 0:** Read-only-Provider-Inventur auf `ok-bm-bootstrap`, Review des aktuellen Forks auf Token-Idempotenz und OpenKubes-Image-Pinning, Sicherung des exakten historischen Kind-Bootstrap-Kommandos und der Scaleway/OpenTofu-Artefakt-/State-Planung.

Erst nach dokumentierten Ergebnissen einen **separaten freizugebenden Change-Plan für Phase 1** erstellen. Keine automatischen Änderungen allein durch dieses Dokument.
