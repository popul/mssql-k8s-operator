# CLAUDE.md — mssql-k8s-operator

## Projet

Opérateur Kubernetes (Go + Kubebuilder) pour gérer des objets SQL Server (databases, logins, users) de manière déclarative via des Custom Resources. Voir `PRD.md` pour le périmètre fonctionnel complet.

## Stack

- **Go 1.22+**, **Kubebuilder v4**, **controller-runtime v0.18+**
- Driver SQL Server : `github.com/microsoft/go-mssqldb`
- Distribution : image Docker multi-stage + Helm chart
- CI : GitHub Actions

## Commandes courantes

```bash
make generate        # Générer les DeepCopy, CRDs, RBAC
make manifests       # Régénérer les manifests (CRDs, webhooks, RBAC)
make test            # Tests unitaires + envtest
make docker-build    # Build de l'image Docker
make docker-push     # Push de l'image
make install         # Installer les CRDs dans le cluster courant
make deploy          # Déployer l'opérateur dans le cluster courant
```

## Conventions de code

### Structure des fichiers

```
api/v1alpha1/          # Types API (CRDs), groupversion_info.go
internal/controller/   # Contrôleurs (un fichier par CRD)
internal/sql/          # Client SQL Server (abstraction sur go-mssqldb)
config/                # Kustomize (CRDs, RBAC, manager, webhook)
charts/                # Helm chart
```

### Design des APIs (CRDs)

- Toujours séparer `spec` (état souhaité, écrit par l'utilisateur) et `status` (état observé, écrit par le contrôleur uniquement).
- Utiliser le status subresource : `+kubebuilder:subresource:status`.
- Utiliser des types pointeurs (`*int32`, `*string`) pour les champs optionnels afin de distinguer "non défini" de "valeur zéro". Annoter avec `// +optional`.
- Fournir des valeurs par défaut via `+kubebuilder:default=` ou un webhook de defaulting.
- Ajouter des markers de validation (`+kubebuilder:validation:Enum=`, `Required`, `MinLength=`, etc.) directement dans les types Go.
- Nommer les champs JSON en camelCase. Un CRD = une responsabilité.
- Versionner en `v1alpha1` → `v1beta1` → `v1`. Ne jamais supprimer ou renommer un champ dans une version publiée.

### Boucle de réconciliation

Chaque `Reconcile()` doit suivre cette structure :

1. **Fetch** la CR. Si `NotFound`, retourner sans erreur (supprimée).
2. **Finalizer** : ajouter si absent, traiter si `DeletionTimestamp != nil`.
3. **Observer** l'état actuel sur SQL Server (requête T-SQL).
4. **Comparer** état désiré vs état actuel.
5. **Agir** : exécuter les DDL/DCL nécessaires pour converger.
6. **Status** : mettre à jour les conditions et `ObservedGeneration`.

Règles impératives :

- **Idempotent** : exécuter N fois avec le même input produit le même résultat. Toujours vérifier l'existence avant de créer.
- **Level-triggered** : comparer l'état désiré à l'état observé, ne pas réagir aux événements individuels.
- **Ne jamais bloquer** : les opérations longues doivent être asynchrones (lancer, requeue, vérifier au prochain cycle).
- **Respecter `context.Context`** : le context est annulé lors du shutdown du manager.
- **Ne jamais muter le `spec`** dans le contrôleur. Utiliser un webhook mutant pour les defaults.
- **Séparer les updates** : `Status().Update()` pour le status, `Update()` pour spec/metadata. Ne jamais mixer.

### Requeue

| Situation | Retour |
|---|---|
| Réconciliation réussie, rien à faire | `ctrl.Result{}` |
| Polling périodique nécessaire | `ctrl.Result{RequeueAfter: 30 * time.Second}` |
| Erreur transitoire (connexion SQL perdue) | `ctrl.Result{}, err` (back-off auto) |
| Erreur permanente (config invalide) | Set condition `Error`, `ctrl.Result{}` (pas d'erreur, pas de retry infini) |

Ne jamais utiliser `ctrl.Result{Requeue: true}` en boucle serrée.

### Filtrage des événements

Utiliser `predicate.GenerationChangedPredicate{}` pour éviter de réconcilier sur les mises à jour de status ou metadata uniquement. Cela évite les boucles infinies.

### Finalizers

- Nom : `mssql.popul.io/finalizer`
- Utiliser `controllerutil.AddFinalizer()` / `RemoveFinalizer()`.
- Le cleanup doit être idempotent.
- Ne jamais bloquer la suppression indéfiniment : logger et alerter si le cleanup échoue de manière permanente.

### Status Conditions

Utiliser le type standard `metav1.Condition` :

```go
// Condition types
const (
    ConditionReady    = "Ready"
    ConditionDegraded = "Degraded"
)
```

- `ObservedGeneration` doit correspondre à `metadata.generation`.
- `Reason` : PascalCase, stable, machine-readable (ex: `DatabaseProvisioning`, `ConnectionFailed`).
- `Message` : texte libre, lisible par un humain.
- Utiliser `meta.SetStatusCondition()` pour gérer les transitions.
- Ne mettre à jour `LastTransitionTime` que quand `Status` change réellement.

### Gestion des erreurs

- Wrapper les erreurs : `fmt.Errorf("failed to create database %s: %w", name, err)`.
- Erreurs transitoires → retourner `err` (retry automatique avec back-off).
- Erreurs permanentes → set condition, **ne pas retourner d'erreur** (sinon retry infini).
- Émettre des Events Kubernetes (`Warning` / `Normal`) pour les transitions visibles par l'utilisateur.

### Client SQL Server

- Centraliser toute interaction SQL dans `internal/sql/` derrière une interface (facilite le mocking).
- Ne jamais construire de requêtes par concaténation de strings → utiliser des requêtes paramétrées ou des identifiers quotés via `quotename()`.
- Toujours fermer les connexions / utiliser un pool avec des limites raisonnables.
- Ne jamais logger de credentials ou données sensibles.

### RBAC

- Utiliser les markers `// +kubebuilder:rbac:` pour générer le RBAC minimal.
- Principe du moindre privilège : uniquement les verbes et resources nécessaires.
- L'opérateur ne doit jamais nécessiter `cluster-admin`.
- ServiceAccount dédié (pas `default`).

### Owner References

- Toujours utiliser `controllerutil.SetControllerReference()` pour les ressources créées par l'opérateur.
- Cela garantit le garbage collection automatique à la suppression du parent.

## Tests

### Pyramide de tests

1. **Tests unitaires** (majorité) : logique métier pure, validation, diffing. Mocker le client K8s avec `fake.NewClientBuilder()` et le client SQL avec une interface mockée.
2. **Tests d'intégration** (modérés) : `envtest` (API server + etcd réels, sans kubelet). Tester la création de CRs, la réconciliation, le status.
3. **Tests E2E** (peu) : cluster réel (kind/k3d), déploiement via Helm chart, validation du cycle de vie complet.

### Commandes

```bash
make test                    # Unit + envtest
make test-e2e                # E2E (nécessite un cluster)
go test ./internal/sql/...   # Tests du client SQL uniquement
```

### Bonnes pratiques

- Tester les cas d'erreur, pas seulement le happy path.
- Tester l'idempotence : appeler `Reconcile()` deux fois de suite doit donner le même résultat.
- Tester le comportement de suppression (finalizer + cleanup).
- Utiliser `testcontainers-go` avec l'image `mcr.microsoft.com/mssql/server:2022-latest` pour les tests d'intégration SQL.

## Helm Chart

- CRDs dans `charts/mssql-operator/crds/` (attention : Helm ne met pas à jour les CRDs après l'install initial — gérer les upgrades via un Job pre-upgrade ou séparément).
- Rendre configurable : image, tag, replicas, resources, nodeSelector, tolerations, affinity.
- Inclure un `ServiceMonitor` optionnel pour Prometheus.
- Inclure un `PodDisruptionBudget` si replicas > 1.

## Observabilité

- **Logs** : structurés (JSON via zap), utiliser `logr`. `log.Info()` pour les transitions, `log.V(1).Info()` pour le debug. Ne jamais logger de secrets.
- **Métriques** : controller-runtime expose automatiquement les métriques du work queue et de la réconciliation. Ajouter des métriques métier custom via `prometheus.NewGaugeVec` enregistrées dans `metrics.Registry`.
- **Events** : émettre des events K8s sur les CRs pour chaque action significative (`DatabaseCreated`, `LoginPasswordRotated`, `ReconciliationFailed`).
- **Health** : exposer `/healthz` et `/readyz`.

## Sécurité

- Credentials SQL Server uniquement via références à des Secrets Kubernetes — jamais en dur, jamais en clair dans les logs.
- Support TLS pour la connexion SQL Server.
- Pas d'injection SQL : utiliser `quotename()` pour les identifiers dynamiques, requêtes paramétrées pour les valeurs.
- `deletionPolicy: Retain` par défaut pour éviter les suppressions accidentelles de bases.

## Production

- Activer le leader election (`--leader-elect`) avec 2+ replicas.
- Définir des resource requests/limits sur le pod opérateur.
- Tuning lease : 15s lease / 10s renew / 2s retry (défauts raisonnables).
- Les webhooks sont servis par tous les replicas (pas seulement le leader).

## Pièges à éviter

| Piège | Solution |
|---|---|
| Boucle de réconciliation infinie | `GenerationChangedPredicate`, comparer `ObservedGeneration` |
| Retry infini sur erreur permanente | Ne pas retourner `err`, poser une condition status |
| Ressources orphelines | Toujours poser des owner references |
| Comparaison d'objets complets | Comparer uniquement les champs pertinents (K8s mute les objets) |
| RBAC trop large | Markers `+kubebuilder:rbac:` avec les permissions minimales |
| Update status + spec en un seul appel | Séparer : `Status().Update()` et `Update()` |
| Hardcoder des namespaces/noms | Utiliser la configuration ou les labels |
| Ignorer l'annulation du context | Respecter `ctx.Done()` dans les opérations longues |
