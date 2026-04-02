# Plan : Fencing anti split-brain pour CLUSTER_TYPE=NONE (v4)

## Problème

Avec `CLUSTER_TYPE=NONE`, quand l'ancien primary redémarre après un failover, il se considère toujours PRIMARY. Il n'y a pas de cluster manager (WSFC/Pacemaker) pour l'en empêcher.

### Causes racines

**1. Connexion aveugle à `Replicas[0]`** — `availabilitygroup_controller.go` ligne 89 :

```go
primaryReplica := ag.Spec.Replicas[0]
```

Le controller se connecte **toujours** au premier replica du spec, pas au primary connu. Quand `Replicas[0]` redémarre et se croit PRIMARY, le controller le croit aveuglément.

**2. Pas de détection de split-brain** — quand `GetAGStatus` retourne un primary différent de `status.PrimaryReplica`, le controller écrase le status sans vérifier la cohérence.

**3. Pas de rejoin des secondaries DISCONNECTED** — `joinSecondaries` n'est appelé qu'à la création de l'AG (dans `if !agExists`). Un secondary qui se déconnecte (crash, network, hard fencing) n'est jamais rejoint.

### Scénarios

```
Scénario 1 : Split-brain classique

  t=0  sql-0=PRIMARY, sql-1=SECONDARY              (normal)
  t=1  sql-0 crash → opérateur failover → sql-1     (status=sql-1)
  t=2  sql-0 redémarre, se croit PRIMARY
  t=3  Deux PRIMARY simultanés → split-brain

Scénario 2 : Status stale (operator crash)

  t=0  sql-0=PRIMARY, sql-1=SECONDARY              (status=sql-0)
  t=1  Opérateur crashe
  t=2  DBA fait ALTER AG FAILOVER manuellement      (sql-1 primary, status STALE=sql-0)
  t=3  Opérateur redémarre, status dit sql-0
  t=4  sql-1 est le seul PRIMARY → PAS un split-brain, juste un status en retard
       DANGER : si on fence sql-1, on tue le vrai primary !

Scénario 3 : Hard fencing → replica orphelin

  t=0  Fencing soft de sql-0 → SET ROLE=SECONDARY
  t=1  sql-0 re-claim PRIMARY (SQL Server le refait)
  t=2  Escalade : hard fencing → DROP AG sur sql-0
  t=3  sql-0 n'a plus l'AG
  t=4  Qui le rejoint ? joinSecondaries n'est appelé que dans if !agExists
       → sql-0 reste orphelin indéfiniment
```

## Principes de conception

### 1. Ne JAMAIS fencer un primary unique

Si un seul replica se voit PRIMARY, ce n'est pas un split-brain — c'est peut-être un status stale. Mettre à jour le status, ne pas fencer.

Le fencing ne se déclenche que quand **2+ replicas se voient PRIMARY simultanément**.

### 2. En cas de dual-primary, garder celui avec le LSN le plus élevé

Le LSN (Log Sequence Number) indique qui a les données les plus récentes. Fencer celui avec le LSN le plus bas minimise la perte de données.

### 3. Couper le trafic AVANT le fencing SQL

Retirer le label `primary` du rogue **immédiatement** (ms), puis exécuter le `SET ROLE=SECONDARY` (secondes). Ça réduit la fenêtre où deux pods reçoivent des écritures.

### 4. Circuit-breaker après N échecs

Si le fencing échoue N fois sur le même replica, arrêter et alerter l'humain.

### 5. Toujours reconcilier les replicas DISCONNECTED

Indépendamment du fencing, un secondary DISCONNECTED doit être rejoint.

## Algorithme

```
ENTRÉE de la réconciliation AG :

1. Fetch CR. Si NotFound → return.
2. Si DeletionTimestamp → handleDeletion (utilise status.PrimaryReplica, pas Replicas[0])
3. Finalizer
4. Résoudre le primary replica :
     Si status.PrimaryReplica != "" ET existe dans spec.Replicas → l'utiliser
     Sinon → fallback Replicas[0]
5. Se connecter au primary résolu
6. Si connexion échoue et autoFailover → handleAutoFailover (existant)
     NOTE : handleAutoFailover itère Replicas[1:] en dur.
     Après le fix ligne 89, le primary résolu n'est plus forcément Replicas[0].
     → handleAutoFailover doit itérer TOUS les replicas SAUF le primary résolu.
7. Ping

=== NOUVEAU : FENCING (si status.PrimaryReplica != "" ET ClusterType == None) ===

8. Guard : lister AGFailover CRs en phase Running → skip si oui
9. roles := collectReplicaRoles (connexion à chaque replica)
      NOTE : le replica sur lequel on est déjà connecté (étape 5) sera
      reconnecté dans collectReplicaRoles. C'est un double-connect mais
      c'est correct car le rôle peut avoir changé entre l'étape 5 et 10.
11. Analyser :

    primaries := replicas où role == "PRIMARY"
    (replicas injoignables sont exclus — role == "")

    Si len(primaries) == 0 :
      → Tous SECONDARY ou injoignables → pas de fencing, continuer normalement
        (le flux existant updateAGStatus gère ce cas)

    Si len(primaries) == 1 :
      Si primaries[0] == status.PrimaryReplica :
        → OK, pas de split-brain, continuer normalement
      Si primaries[0] != status.PrimaryReplica :
        → STATUS STALE, pas un split-brain
        → Patch status.PrimaryReplica = primaries[0].ServerName
        → updateReplicaRoleLabelsFromAG(ctx, ag, primaries[0].ServerName)
        → Event Normal "PrimaryChangedExternally"
        → Si la connexion courante (étape 5) est sur le VRAI primary → continuer
          Si la connexion courante est sur un SECONDARY → return early + requeue 5s

    Si len(primaries) >= 2 :
      → VRAI SPLIT-BRAIN
      → Event Warning "SplitBrainDetected"
      → lsns := collectReplicaLSNs(ctx, ag, primaries)
      → legitimate := replica avec le LSN le plus élevé
           si LSN égaux → garder celui == status.PrimaryReplica
           si status.PrimaryReplica pas dans primaries → garder le plus haut LSN
      → rogues := primaries - legitimate
      → Pour chaque rogue :
           a. IMMÉDIATEMENT : patcher label pod → secondary (coupe le trafic)
           b. Déterminer soft vs hard :
              hard si (lastFencedReplica == rogue.ServerName
                       ET time.Since(lastFencingTime) < cooldown)
           c. fenceReplica(ctx, ag, rogue, hard)
      → Patch status.PrimaryReplica = legitimate.ServerName
        (en cas de split-brain, le status doit pointer sur le légitime)
      → Patch LastFencingTime, FencingCount++, LastFencedReplica
      → Condition Ready=False, Reason=FencingExecuted|HardFencingExecuted
      → Return early + requeue 5s
        (la connexion courante est potentiellement sur un replica fencé)

=== FIN FENCING ===

12. Suite normale : HADR endpoint, AG exists, reconcile databases, listener
13. GetAGStatus → updateAGStatus

=== NOUVEAU : RECONCILIATION DES REPLICAS DISCONNECTED/ORPHELINS ===

14. Pour chaque replica dans spec.Replicas :
      Chercher ce replica dans agStatus.Replicas :
        Si trouvé ET connected == true → OK, skip
        Si trouvé ET connected == false ET role != PRIMARY → tryRejoinReplica
        Si NON trouvé (orphelin après hard fencing) → tryRejoinReplica
      NOTE : on itère sur spec.Replicas (pas agStatus.Replicas) pour attraper
      aussi les replicas droppés qui ne sont plus dans le status SQL.

15. Mettre à jour les labels (updateReplicaRoleLabelsFromAG)
16. Requeue
```

## Fichiers à modifier

### 1. SQL client — nouvelles méthodes

**`internal/sql/interface.go`** — ajouter :

```go
// SetAGRoleSecondary forces the local replica to become secondary.
// Only valid for CLUSTER_TYPE=NONE (SQL Server 2017+).
SetAGRoleSecondary(ctx context.Context, agName string) error

// GetLastHardenedLSN returns the last hardened LSN for the first database
// in the AG on this replica. Used as tiebreaker during split-brain resolution.
GetLastHardenedLSN(ctx context.Context, agName string) (int64, error)
```

**`internal/sql/client.go`** — implémenter :

```go
func (c *MSSQLClient) SetAGRoleSecondary(ctx context.Context, agName string) error {
    query := fmt.Sprintf(
        "ALTER AVAILABILITY GROUP %s SET (ROLE = SECONDARY)",
        QuoteName(agName))
    _, err := c.db.ExecContext(ctx, query)
    if err != nil {
        return fmt.Errorf("failed to set AG %s role to secondary: %w", agName, err)
    }
    return nil
}

func (c *MSSQLClient) GetLastHardenedLSN(ctx context.Context, agName string) (int64, error) {
    var lsn int64
    err := c.db.QueryRowContext(ctx,
        `SELECT TOP 1 ISNULL(drs.last_hardened_lsn, 0)
         FROM sys.dm_hadr_database_replica_states drs
         JOIN sys.availability_groups ag ON drs.group_id = ag.group_id
         WHERE ag.name = @p1 AND drs.is_local = 1
         ORDER BY drs.last_hardened_lsn DESC`, agName).Scan(&lsn)
    if err != nil {
        return 0, fmt.Errorf("failed to get last hardened LSN for AG %s: %w", agName, err)
    }
    return lsn, nil
}
```

**`internal/sql/mock_client.go`** — ajouter les mocks.

**`internal/sql/interface_test.go`** — ajouter les stubs.

### 2. Types API

**`api/v1alpha1/availabilitygroup_types.go`** — ajouter au `AvailabilityGroupStatus` :

```go
// LastFencingTime records when the last fencing operation was executed.
// +optional
LastFencingTime *metav1.Time `json:"lastFencingTime,omitempty"`

// FencingCount is the total number of fencing operations executed (cumulative).
// +optional
FencingCount int32 `json:"fencingCount,omitempty"`

// ConsecutiveFencingCount tracks how many times the same replica (LastFencedReplica)
// has been fenced consecutively. Resets to 1 when a different replica is fenced.
// Used by the circuit-breaker to detect infinite fencing loops.
// +optional
ConsecutiveFencingCount int32 `json:"consecutiveFencingCount,omitempty"`

// LastFencedReplica is the server name of the last replica that was fenced.
// Used with LastFencingTime to detect repeated re-claims and escalate to hard fencing.
// +optional
LastFencedReplica string `json:"lastFencedReplica,omitempty"`
```

**`api/v1alpha1/common_types.go`** — ajouter :

```go
ReasonSplitBrainDetected    = "SplitBrainDetected"
ReasonFencingExecuted       = "FencingExecuted"
ReasonHardFencingExecuted   = "HardFencingExecuted"
ReasonFencingFailed         = "FencingFailed"
ReasonFencingExhausted      = "FencingExhausted"
ReasonPrimaryChangedExternally = "PrimaryChangedExternally"
```

Puis `make generate`.

### 3. Nouveau fichier : `internal/controller/ag_fencing.go`

Constante :

```go
const maxFencingAttempts = 5
```

#### `collectReplicaRoles`

```go
func (r *AvailabilityGroupReconciler) collectReplicaRoles(
    ctx context.Context, ag *v1alpha1.AvailabilityGroup,
) (map[string]string, error)
```

- Itère sur `ag.Spec.Replicas`
- Pour chaque replica : se connecter, `GetAGReplicaRole(agName, serverName)`
- Replica injoignable → `""` (pas d'erreur fatale)
- Retourne `{"sql-0": "PRIMARY", "sql-1": "SECONDARY"}`

#### `collectReplicaLSNs`

```go
func (r *AvailabilityGroupReconciler) collectReplicaLSNs(
    ctx context.Context, ag *v1alpha1.AvailabilityGroup,
    candidates []v1alpha1.AGReplicaSpec,
) (map[string]int64, error)
```

- Appelé uniquement quand 2+ primaries détectés
- Pour chaque candidat : se connecter, `GetLastHardenedLSN(agName)`
- Erreur → LSN = 0 (pénalité : le replica sera fencé)

#### `detectAndResolveSplitBrain`

```go
func (r *AvailabilityGroupReconciler) detectAndResolveSplitBrain(
    ctx context.Context, ag *v1alpha1.AvailabilityGroup,
) (fenced bool, err error)
```

Algorithme complet (correspond aux étapes 8-11 de l'algorithme principal) :

```
 1. Guard: status.PrimaryReplica == "" → return (false, nil)
 2. Guard: ClusterType != nil && *ClusterType != "None" → return (false, nil)
 3. Guard: lister AGFailover CRs avec spec.agName == ag.Spec.AGName
           si l'un est en phase Running → return (false, nil)
 4. (réservé — circuit-breaker vérifié plus tard à l'étape 9b)

 5. roles := collectReplicaRoles(ctx, ag)
 6. primaries := []AGReplicaSpec pour chaque role == "PRIMARY"
    (exclure les replicas injoignables, role == "")

 7. Si len(primaries) == 0 → return (false, nil)

 8. Si len(primaries) == 1 :
      Si primaries[0].ServerName == status.PrimaryReplica → return (false, nil)
      Sinon :
        → STATUS STALE — corriger le status et les labels
        → Modifier ag.Status.PrimaryReplica in-place = primaries[0].ServerName
          (PAS de Status().Patch() ici — l'appelant fera le patch dans updateAGStatus
          plus tard, ou fera un return early qui laisse le prochain cycle patcher)
        → updateReplicaRoleLabelsFromAG(ctx, ag, primaries[0].ServerName)
        → Event Normal "PrimaryChangedExternally"
        → return (false, nil)
          L'appelant vérifie si `previousPrimary != ag.Status.PrimaryReplica`
          et return early + requeue. Au prochain cycle, updateAGStatus persistera
          le bon primary en status.

 9. len(primaries) >= 2 : VRAI SPLIT-BRAIN

 9b. Circuit-breaker : pour chaque rogue potentiel, vérifier si
     lastFencedReplica == rogue.ServerName
     ET ConsecutiveFencingCount >= maxFencingAttempts
     ET time.Since(LastFencingTime) < cooldown
     Si TOUS les rogues sont exhausted → poser condition FencingExhausted, return (false, nil)
     Sinon : ne fencer que les rogues non-exhausted.

      → Event Warning "SplitBrainDetected"
      → lsns := collectReplicaLSNs(ctx, ag, primaries)
      → legitimate := replica avec le LSN le plus élevé
           si LSN égaux → garder celui == status.PrimaryReplica
           si status.PrimaryReplica pas dans primaries → garder le plus haut LSN
      → rogues := primaries - legitimate

10. Pour chaque rogue (non-exhausted) :
      a. IMMÉDIATEMENT : patcher label pod → secondary (coupe le trafic)
      b. hard := (lastFencedReplica == rogue.ServerName
                  ET time.Since(lastFencingTime) < cooldown)
      c. err := fenceReplica(ctx, ag, rogue, hard)
         si err → logger, Event Warning "FencingFailed", continuer (label déjà retiré)

11. Patcher status (un seul appel Status().Patch()) :
      PrimaryReplica = legitimate.ServerName
      LastFencingTime = now
      FencingCount++ (cumulé, pour les métriques)
      lastRogue := dernier rogue fencé dans la boucle
      Si LastFencedReplica == lastRogue.ServerName :
        ConsecutiveFencingCount++ (même replica, on incrémente)
      Sinon :
        ConsecutiveFencingCount = 1 (nouveau replica, on reset)
      LastFencedReplica = lastRogue.ServerName
      Condition Ready=False, Reason=FencingExecuted|HardFencingExecuted

12. return (true, nil)
```

Notes :
- Étape 8 retourne `(false, nil)` car pas de fencing, juste une correction de status in-place.
  L'appelant vérifie `previousPrimary != ag.Status.PrimaryReplica` et return early.
- Étape 11 fait un seul `Status().Patch()` pour tout (PrimaryReplica + fencing fields + condition).
  Pas de double-patch dans le même cycle.
- Le circuit-breaker (étape 9b) est vérifié APRÈS collectReplicaRoles car il a besoin
  de connaître les rogues pour comparer avec LastFencedReplica.

#### `fenceReplica`

```go
func (r *AvailabilityGroupReconciler) fenceReplica(
    ctx context.Context, ag *v1alpha1.AvailabilityGroup,
    replica v1alpha1.AGReplicaSpec, hard bool,
) error
```

```
1. Récupérer credentials
2. Se connecter au replica (timeout 10s)
3. Si soft :
     SetAGRoleSecondary(agName) avec sqlContext (30s)
     Event Normal "FencingExecuted"
   Si hard :
     DropAG(agName) avec sqlContext (30s)
     Event Warning "HardFencingExecuted"
4. Fermer la connexion
5. Incrémenter métrique FencingTotal (labels: ag_name, namespace, fenced_replica, type)
```

Note : le label du pod est déjà retiré en étape 10a de `detectAndResolveSplitBrain`, AVANT l'appel à `fenceReplica`. Si `fenceReplica` échoue, le trafic est quand même coupé.

### 4. Nouveau dans la boucle principale : reconciliation des DISCONNECTED

Ajouter après `updateAGStatus` (ligne ~193), avant le requeue final :

```go
// 9b. Rejoin disconnected/orphan secondaries
// Iterate spec.Replicas (not agStatus.Replicas) to also catch
// replicas dropped by hard fencing that are absent from SQL status.
agStatusMap := make(map[string]sqlclient.AGReplicaState)
for _, rs := range agStatus.Replicas {
    agStatusMap[rs.ServerName] = rs
}
for _, specReplica := range ag.Spec.Replicas {
    if specReplica.ServerName == agStatus.PrimaryReplica {
        continue // Don't rejoin the primary
    }
    rs, found := agStatusMap[specReplica.ServerName]
    if found && rs.Connected {
        continue // Already connected
    }
    // Either disconnected or orphaned (not in SQL status at all)
    r.tryRejoinReplica(ctx, &ag, specReplica)
}
```

#### `tryRejoinReplica`

```go
func (r *AvailabilityGroupReconciler) tryRejoinReplica(
    ctx context.Context, ag *v1alpha1.AvailabilityGroup,
    replica v1alpha1.AGReplicaSpec,
) {
    logger := log.FromContext(ctx)

    username, password, err := getCredentialsFromSecret(ctx, r.Client,
        ag.Namespace, replica.Server.CredentialsSecret.Name)
    if err != nil {
        return // credentials unavailable, skip
    }

    conn, err := connectToSQL(replica.Server, username, password, r.SQLClientFactory)
    if err != nil {
        return // replica unreachable, skip
    }
    defer conn.Close()

    clusterType := "EXTERNAL"
    if ag.Spec.ClusterType != nil {
        clusterType = mapClusterType(*ag.Spec.ClusterType)
    }

    sqlCtx, cancel := sqlContext(ctx)
    defer cancel()
    err = conn.JoinAG(sqlCtx, ag.Spec.AGName, clusterType)
    if err != nil {
        // "already joined" errors are expected and harmless
        logger.V(1).Info("rejoin attempt", "replica", replica.ServerName, "result", err)
        return
    }

    r.Recorder.Event(ag, corev1.EventTypeNormal, "ReplicaRejoined",
        fmt.Sprintf("Replica %s rejoined AG %s", replica.ServerName, ag.Spec.AGName))
    logger.Info("disconnected replica rejoined", "replica", replica.ServerName)
}
```

### 5. Modifications de `availabilitygroup_controller.go`

#### Fix connexion au known primary (ligne 89)

```go
// AVANT
primaryReplica := ag.Spec.Replicas[0]

// APRÈS
primaryReplica := ag.Spec.Replicas[0]
if ag.Status.PrimaryReplica != "" {
    for i := range ag.Spec.Replicas {
        if ag.Spec.Replicas[i].ServerName == ag.Status.PrimaryReplica {
            primaryReplica = ag.Spec.Replicas[i]
            break
        }
    }
}
```

#### Fix `handleDeletion` (ligne 454)

Même logique.

#### Insertion du fencing (après le ping, ~ligne 130)

```go
// 4b. Fencing: detect and resolve split-brain
if ag.Status.PrimaryReplica != "" {
    previousPrimary := ag.Status.PrimaryReplica
    fenced, fenceErr := r.detectAndResolveSplitBrain(ctx, &ag)
    if fenceErr != nil {
        logger.Error(fenceErr, "fencing check failed")
    }
    if fenced {
        // Connexion courante potentiellement invalide (on vient de dégrader le nœud).
        return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
    }
    // Même sans fencing, le status peut avoir changé (status stale corrigé).
    // Si le primary résolu à l'étape 4 n'est plus le bon, return early.
    if ag.Status.PrimaryReplica != previousPrimary {
        return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
    }
}
```

#### Fix `handleAutoFailover` : ne plus hardcoder `Replicas[1:]`

`handleAutoFailover` itère `for i := 1; i < len(ag.Spec.Replicas)` (ligne 590),
ce qui assume que `Replicas[0]` est toujours le primary. Après le fix ligne 89,
le primary résolu peut être n'importe quel replica.

```go
// AVANT (ligne 590)
for i := 1; i < len(ag.Spec.Replicas); i++ {

// APRÈS : itérer tous les replicas sauf le primary connu
knownPrimary := ag.Status.PrimaryReplica
if knownPrimary == "" {
    knownPrimary = ag.Spec.Replicas[0].ServerName // fallback: skip [0] comme avant
}
for i := 0; i < len(ag.Spec.Replicas); i++ {
    if ag.Spec.Replicas[i].ServerName == knownPrimary {
        continue // skip le primary actuel (qu'on sait injoignable)
    }
    // ... reste identique
```

#### Insertion du rejoin DISCONNECTED (après `updateAGStatus`, ~ligne 193)

Voir section 4 ci-dessus.

#### RBAC

```go
// +kubebuilder:rbac:groups=mssql.popul.io,resources=agfailovers,verbs=get;list;watch
```

### 6. Métriques

**`internal/metrics/metrics.go`** :

```go
var FencingTotal = prometheus.NewCounterVec(
    prometheus.CounterOpts{
        Namespace: "mssql_operator",
        Name:      "fencing_total",
        Help:      "Total fencing actions to resolve split-brain",
    },
    []string{"ag_name", "namespace", "fenced_replica", "type"},
)
```

`type` = `"soft"` | `"hard"` | `"label_only"` (si fencing SQL échoue mais label retiré).

### 7. Helm chart RBAC

**`charts/mssql-operator/templates/rbac.yaml`** :

```yaml
- apiGroups: ["mssql.popul.io"]
  resources: ["agfailovers"]
  verbs: ["get", "list", "watch"]
```

## Tests (TDD)

### Infrastructure : multi-mock par host

```go
func newMultiMockAGReconciler(
    objs []runtime.Object,
    mocks map[string]*sqlclient.MockClient,
) (*AvailabilityGroupReconciler, *record.FakeRecorder) {
    scheme := newScheme()
    clientBuilder := fake.NewClientBuilder().WithScheme(scheme).
        WithStatusSubresource(&v1alpha1.AvailabilityGroup{}, &v1alpha1.AGFailover{}).
        WithRuntimeObjects(objs...)
    k8sClient := clientBuilder.Build()
    recorder := record.NewFakeRecorder(20)

    r := &AvailabilityGroupReconciler{
        Client:   k8sClient,
        Scheme:   scheme,
        Recorder: recorder,
        SQLClientFactory: func(host string, port int, username, password string, tlsEnabled bool) (sqlclient.SQLClient, error) {
            for key, mock := range mocks {
                if strings.Contains(host, key) {
                    if mock.ConnectError != nil {
                        return nil, mock.ConnectError
                    }
                    return mock, nil
                }
            }
            return nil, fmt.Errorf("no mock for host %s", host)
        },
    }
    return r, recorder
}
```

### Tests : `internal/controller/ag_fencing_test.go`

| # | Test | Scénario | Assertion |
|---|---|---|---|
| **Guards** | | | |
| 1 | `NoPrimaryInStatus` | `status.PrimaryReplica=""` | `fenced=false`, aucun appel SQL |
| 2 | `ClusterTypeWSFC_Skip` | `ClusterType=WSFC` | `fenced=false` |
| 3 | `ClusterTypeExternal_Skip` | `ClusterType=External` | `fenced=false` |
| 4 | `AGFailoverRunning_Skip` | AGFailover CR en `Phase=Running` | `fenced=false` |
| 5 | `FencingExhausted_Skip` | `LastFencedReplica=sql-0`, `ConsecutiveFencingCount=5`, dans le cooldown | `fenced=false`, condition `FencingExhausted` |
| **Pas de split-brain** | | | |
| 6 | `SinglePrimary_MatchesStatus` | sql-0=PRIMARY, status=sql-0 | `fenced=false` |
| 7 | `AllSecondary_NoFencing` | sql-0=SECONDARY, sql-1=SECONDARY | `fenced=false` |
| 8 | `ReplicaUnreachable_ExcludedFromAnalysis` | sql-0=ConnectError, sql-1=PRIMARY, status=sql-1 | `fenced=false` (sql-0 ignoré, sql-1 seul PRIMARY = status) |
| **Status stale** | | | |
| 9 | `StatusStale_UpdatesStatusAndLabels` | sql-1=PRIMARY, sql-0=SECONDARY, status=sql-0 | status corrigé → sql-1, labels mis à jour, Event `PrimaryChangedExternally`, `fenced=false` |
| 10 | `StatusStale_ReturnEarlyIfWrongNode` | Connexion sur sql-0, sql-1 vrai primary | `previousPrimary != status` → return early dans l'appelant |
| **Split-brain** | | | |
| 11 | `DualPrimary_FencesLowerLSN` | sql-0=PRIMARY LSN=100, sql-1=PRIMARY LSN=200, status=sql-0 | Fencing soft de sql-0, sql-1 gardé, status→sql-1 |
| 12 | `DualPrimary_EqualLSN_KeepsStatus` | sql-0=PRIMARY LSN=100, sql-1=PRIMARY LSN=100, status=sql-1 | Fencing soft de sql-0, sql-1 gardé (== status) |
| 13 | `DualPrimary_StatusNotInPrimaries` | sql-0=PRIMARY LSN=50, sql-1=PRIMARY LSN=200, status=sql-2 | Garder sql-1 (LSN haut), fencer sql-0 |
| 14 | `TriplePrimary_FencesAllButBestLSN` | 3 replicas PRIMARY, LSNs variés | Fencer les 2 avec LSN plus bas |
| **Comportement du fencing** | | | |
| 15 | `FenceSoft_CallsSetRoleSecondary` | Premier fencing | `SetAGRoleSecondary` appelé, pas `DropAG` |
| 16 | `FenceHard_CallsDropAG` | Même replica (sql-0) re-claim dans le cooldown | `DropAG` appelé, pas `SetAGRoleSecondary` |
| 17 | `Fence_LabelRemovedEvenIfSQLFails` | `SetAGRoleSecondary` échoue | Label `secondary` posé, erreur loggée, `fenced=true` |
| 18 | `Fence_StatusUpdated` | Après fencing | `LastFencingTime` set, `FencingCount++`, `ConsecutiveFencingCount` set, `LastFencedReplica` set, `PrimaryReplica` = légitime |
| 19 | `Fence_Idempotent` | sql-0=SECONDARY, sql-1=PRIMARY, status=sql-1 | `fenced=false` (pas de rogue) |
| **Recovery (rejoin)** | | | |
| 20 | `Rejoin_DisconnectedSecondary` | agStatus: sql-1 DISCONNECTED, sql-1 dans spec | `JoinAG` appelé |
| 21 | `Rejoin_OrphanAfterHardFencing` | sql-0 dans spec mais absent de agStatus | `JoinAG` appelé |
| 22 | `Rejoin_Unreachable_NoError` | sql-1 injoignable | Pas de crash, skip silencieux |
| 23 | `Rejoin_AlreadyConnected_Skip` | sql-1 CONNECTED | `JoinAG` pas appelé |
| 24 | `Rejoin_PrimaryDisconnected_Skip` | sql-0=PRIMARY DISCONNECTED | Pas de rejoin (c'est le primary, pas un secondary) |
| **Connexion primary** | | | |
| 25 | `ConnectsToKnownPrimary` | status=sql-1 | Factory appelé avec host contenant "sql-1" |
| 26 | `FallsBackToReplicas0_IfStatusEmpty` | status="" | Factory appelé avec host contenant "sql-0" |
| 27 | `HandleDeletion_UsesKnownPrimary` | status=sql-1, DeletionTimestamp set | `DropAG` exécuté via sql-1 |
| **handleAutoFailover fix** | | | |
| 28 | `AutoFailover_IteratesAllExceptKnownPrimary` | status=sql-1, sql-1 injoignable | Tente failover sur sql-0 |
| 29 | `AutoFailover_FallbackSkipsReplica0_IfStatusEmpty` | status="" | Itère Replicas[1:] comme avant |

## Matrice de couverture des edge cases

| Edge case | Test(s) | Mécanisme |
|---|---|---|
| Premier déploiement | 1, 26 | Guard `status.PrimaryReplica==""`, fallback `Replicas[0]` |
| Un seul primary correct | 6 | No-op |
| Status stale (primary changé en externe) | 9, 10 | Correction du status, return early si connexion invalide |
| Vrai split-brain (2 primaries) | 11, 12, 13 | Fencing du lower-LSN |
| Replica injoignable | 8, 22 | `role=""`, skip gracieux |
| Fencing SQL échoue | 17 | Label déjà retiré, erreur loggée, `fenced=true` |
| Idempotence | 19 | Déjà SECONDARY = no-op |
| Re-claim en boucle | 16 | Escalade hard fencing (DropAG) |
| Rejoin après hard fencing | 21 | `tryRejoinReplica` itère spec.Replicas (pas agStatus) |
| Secondary DISCONNECTED | 20 | `tryRejoinReplica` dans la boucle principale |
| Tous SECONDARY | 7 | Pas de fencing, requeue |
| AGFailover en cours | 4 | Skip fencing |
| WSFC / External | 2, 3 | Guard `ClusterType` |
| Circuit-breaker | 5 | `maxFencingAttempts` + condition `FencingExhausted` |
| Fenêtre de vulnérabilité | 17 | Label retiré même si fencing SQL échoue |
| Tiebreaker dual-primary | 11, 12, 13 | LSN comparé, fallback sur status |
| Connexion invalide après fencing | Plan §algo étape 12 | Return early + requeue 5s |
| `handleDeletion` hardcode Replicas[0] | 27 | Fix utilisant `status.PrimaryReplica` |
| `handleAutoFailover` hardcode Replicas[1:] | 28, 29 | Fix itérant tous sauf le known primary |
| Operator restart pendant fencing | Level-triggered | Re-détecté au prochain cycle |
| Race fencing vs auto-failover | 4 | AGFailover CR check |
| SA password rotation | Connexion échoue | Requeue, retry au prochain cycle |
| AG CR deleted pendant fencing | `DeletionTimestamp` check | `handleDeletion` appelé avant fencing |
| Network partition | Fencing échoue | Label retiré, SQL retry au prochain cycle |
| Transactions en cours sur le fencé | `sqlContext()` | Timeout SQL 30s |
| Scale-down retire le primary | 13 | LSN tiebreaker si status.Primary pas dans les primaries |
| Scale-down retire un secondary | 20, 21 | Rejoin ne s'applique qu'aux replicas du spec |
| Connexion invalide après status stale | 10 | Return early si primary résolu ≠ status après correction |
| 3+ replicas, multi-rogue | 14 | Même algorithme, fencing de chaque rogue |
| Primary DISCONNECTED pas rejoint | 24 | Guard `role != PRIMARY` dans le rejoin |

## Passe finale : reste-t-il quelque chose ?

### Vérifié et couvert :

- [x] Connexion au known primary (pas Replicas[0])
- [x] Fencing uniquement si 2+ primaries (pas de fencing sur status stale)
- [x] Tiebreaker par LSN
- [x] Label retiré avant le fencing SQL (fenêtre réduite)
- [x] Circuit-breaker déplacé après collectReplicaRoles (besoin de connaître les rogues)
- [x] Circuit-breaker par rogue : skip individuellement si exhausted, pas global
- [x] Escalade soft → hard
- [x] Rejoin des DISCONNECTED (indépendant du fencing)
- [x] Rejoin après hard fencing
- [x] Guards : DeletionTimestamp, ClusterType, AGFailover Running, status vide
- [x] handleDeletion fixé
- [x] RBAC pour agfailovers
- [x] Métriques
- [x] Return early après fencing (connexion invalide)
- [x] Return early après correction status stale (connexion sur mauvais nœud)
- [x] Fix `handleAutoFailover` qui hardcode `Replicas[1:]`
- [x] Patch `status.PrimaryReplica = legitimate` en cas de split-brain
- [x] Rejoin itère sur `spec.Replicas` (pas `agStatus.Replicas`) pour couvrir les orphelins
- [x] `collectReplicaRoles` exclut les injoignables (role="") de l'analyse des primaries
- [x] Erreur dans `fenceReplica` est non-fatale (label déjà retiré, on continue)
- [x] Pas de double Status().Patch() : status stale modifie in-place, seul le fencing ou updateAGStatus patch
- [x] Rejoin itère spec.Replicas avec lookup dans agStatusMap (type sqlclient.AGReplicaState)
- [x] LastFencedReplica = dernier rogue fencé (pas rogues[0] hardcodé)
- [x] ConsecutiveFencingCount par replica (pas FencingCount global) pour le circuit-breaker
- [x] WithStatusSubresource inclut AGFailover pour les tests avec AGFailover CR
- [x] handleAutoFailover fallback quand status vide (compatibilité Replicas[1:])
- [x] Primary DISCONNECTED pas rejoint (guard dans le rejoin)
- [x] 3+ replicas (test 14 avec triple primary)
- [x] 29 tests couvrant tous les cas

### Hors scope (accepté) :

- **Fencing réseau (STONITH)** : on ne peut pas couper le réseau d'un pod K8s sans NetworkPolicy. Le fencing SQL + label est suffisant pour CLUSTER_TYPE=NONE.
- **Protection write-write pendant les quelques ms de split-brain** : entre la détection et le retrait du label, 2 pods peuvent recevoir des écritures. C'est inhérent à l'absence de cluster manager. Le LSN tiebreaker minimise la perte.
- **Automatic seeding après hard fencing** : le `JoinAG` rejoint le replica, mais les données doivent être re-synchronisées par SQL Server (automatic seeding). Ça peut prendre du temps selon la taille des DBs.

## Ordre d'implémentation

```
 1. ag_fencing_test.go              (29 tests, TDD red)
 2. internal/sql/interface.go       (SetAGRoleSecondary, GetLastHardenedLSN)
 3. internal/sql/client.go          (implémentations)
 4. internal/sql/mock_client.go     (mocks)
 5. internal/sql/interface_test.go  (stubs)
 6. api/v1alpha1/availabilitygroup_types.go  (status fields)
 7. api/v1alpha1/common_types.go    (Reasons)
 8. make generate
 9. ag_fencing.go                   (collectReplicaRoles, collectReplicaLSNs,
                                     detectAndResolveSplitBrain, fenceReplica,
                                     tryRejoinReplica)
10. availabilitygroup_controller.go :
      - fix ligne 89 (connexion au known primary)
      - fix ligne 454 (handleDeletion)
      - fix ligne 590 (handleAutoFailover itère tous sauf known primary)
      - insertion fencing + return early si status changé
      - insertion rejoin DISCONNECTED
      - RBAC markers
11. internal/metrics/metrics.go     (FencingTotal)
12. charts/mssql-operator/templates/rbac.yaml (agfailovers RBAC)
13. make test                       (29 tests green + existants verts)
14. make docker-build + test e2e kind
```
