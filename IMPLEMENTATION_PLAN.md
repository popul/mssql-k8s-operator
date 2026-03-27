# Plan d'implémentation — mssql-k8s-operator

Ce document décrit les étapes d'implémentation ordonnées par dépendances. Chaque étape référence les critères d'acceptation (AC-X.X.X) qu'elle couvre.

---

## Étape 0 — Scaffolding Kubebuilder

### Actions

```bash
kubebuilder init --domain popul.io --repo github.com/popul/mssql-k8s-operator
kubebuilder create api --group mssql --version v1alpha1 --kind Database --resource --controller
kubebuilder create api --group mssql --version v1alpha1 --kind Login --resource --controller
kubebuilder create api --group mssql --version v1alpha1 --kind DatabaseUser --resource --controller
```

### Fichiers créés

```
cmd/main.go                              # Entrypoint
api/v1alpha1/database_types.go           # Types Database
api/v1alpha1/login_types.go              # Types Login
api/v1alpha1/databaseuser_types.go       # Types DatabaseUser
api/v1alpha1/groupversion_info.go        # GVK registration
internal/controller/database_controller.go
internal/controller/login_controller.go
internal/controller/databaseuser_controller.go
config/                                  # Kustomize (CRDs, RBAC, manager)
Makefile, Dockerfile, go.mod, go.sum
```

### Critères couverts

AC-1.1.1, AC-1.1.2, AC-1.1.3, AC-1.1.4

---

## Étape 1 — Types API (CRDs)

### Objectif

Définir les structs Go pour les 3 CRDs avec markers Kubebuilder pour la validation et les défauts.

### Fichiers modifiés

- `api/v1alpha1/database_types.go`
- `api/v1alpha1/login_types.go`
- `api/v1alpha1/databaseuser_types.go`
- `api/v1alpha1/common_types.go` (nouveau — types partagés)

### Design

```go
// common_types.go — types réutilisés par les 3 CRDs
type ServerReference struct {
    Host              string          `json:"host"`
    Port              *int32          `json:"port,omitempty"`       // +kubebuilder:default=1433
    CredentialsSecret SecretReference `json:"credentialsSecret"`
}

type SecretReference struct {
    Name string `json:"name"`
}

// +kubebuilder:validation:Enum=Delete;Retain
// +kubebuilder:default=Retain
type DeletionPolicy string
```

```go
// database_types.go
type DatabaseSpec struct {
    Server         ServerReference `json:"server"`
    DatabaseName   string         `json:"databaseName"`        // +kubebuilder:validation:MinLength=1
    Collation      *string        `json:"collation,omitempty"`
    Owner          *string        `json:"owner,omitempty"`
    DeletionPolicy DeletionPolicy `json:"deletionPolicy,omitempty"`
}

type DatabaseStatus struct {
    Conditions         []metav1.Condition `json:"conditions,omitempty"`
    ObservedGeneration int64              `json:"observedGeneration,omitempty"`
}
```

```go
// login_types.go
type LoginSpec struct {
    Server          ServerReference `json:"server"`
    LoginName       string          `json:"loginName"`           // +kubebuilder:validation:MinLength=1
    PasswordSecret  SecretReference `json:"passwordSecret"`
    DefaultDatabase *string         `json:"defaultDatabase,omitempty"`
    ServerRoles     []string        `json:"serverRoles,omitempty"`
    DeletionPolicy  DeletionPolicy  `json:"deletionPolicy,omitempty"`
}
```

```go
// databaseuser_types.go
type DatabaseUserSpec struct {
    Server        ServerReference `json:"server"`
    DatabaseName  string          `json:"databaseName"`        // +kubebuilder:validation:MinLength=1
    UserName      string          `json:"userName"`            // +kubebuilder:validation:MinLength=1
    LoginRef      LoginReference  `json:"loginRef"`
    DatabaseRoles []string        `json:"databaseRoles,omitempty"`
}

type LoginReference struct {
    Name string `json:"name"`
}
```

### Commande

```bash
make generate && make manifests
```

### Tests

- Test unitaire : vérifier que les CRDs générées contiennent les validations attendues (enum, required, defaults).

### Critères couverts

AC-2.7.1, AC-2.7.2, AC-2.7.3, AC-3.5.1, AC-3.5.2, AC-4.5.1, AC-4.5.2, AC-4.5.3

---

## Étape 2 — Client SQL Server (interface + implémentation)

### Objectif

Créer l'abstraction SQL pour toutes les interactions avec SQL Server, derrière une interface mockable.

### Fichiers créés

- `internal/sql/client.go` — interface
- `internal/sql/mssql_client.go` — implémentation go-mssqldb
- `internal/sql/mssql_client_test.go` — tests avec testcontainers

### Design

```go
// client.go
type SQLClient interface {
    // Database operations
    DatabaseExists(ctx context.Context, name string) (bool, error)
    CreateDatabase(ctx context.Context, name string, collation *string) error
    DropDatabase(ctx context.Context, name string) error
    GetDatabaseOwner(ctx context.Context, name string) (string, error)
    SetDatabaseOwner(ctx context.Context, dbName, owner string) error
    GetDatabaseCollation(ctx context.Context, name string) (string, error)

    // Login operations
    LoginExists(ctx context.Context, name string) (bool, error)
    CreateLogin(ctx context.Context, name, password string) error
    DropLogin(ctx context.Context, name string) error
    UpdateLoginPassword(ctx context.Context, name, password string) error
    GetLoginDefaultDatabase(ctx context.Context, name string) (string, error)
    SetLoginDefaultDatabase(ctx context.Context, name, dbName string) error
    GetLoginServerRoles(ctx context.Context, name string) ([]string, error)
    AddLoginToServerRole(ctx context.Context, login, role string) error
    RemoveLoginFromServerRole(ctx context.Context, login, role string) error

    // DatabaseUser operations
    UserExists(ctx context.Context, dbName, userName string) (bool, error)
    CreateUser(ctx context.Context, dbName, userName, loginName string) error
    DropUser(ctx context.Context, dbName, userName string) error
    GetUserDatabaseRoles(ctx context.Context, dbName, userName string) ([]string, error)
    AddUserToDatabaseRole(ctx context.Context, dbName, userName, role string) error
    RemoveUserFromDatabaseRole(ctx context.Context, dbName, userName, role string) error

    // Connection
    Close() error
    Ping(ctx context.Context) error
}
```

### Décisions clés

- Toutes les requêtes utilisent `quotename()` côté SQL pour les identifiers — pas de concaténation Go.
- Les mots de passe sont passés en paramètres (`sp_executesql` / `@p1`) — jamais en clair dans le SQL.
- Pool de connexions avec `sql.DB` (max 10 idle, 30s timeout).
- L'implémentation est **sans état** : chaque appel ouvre une connexion du pool, exécute, retourne.

### Tests

- Tests d'intégration avec `testcontainers-go` + `mcr.microsoft.com/mssql/server:2022-latest`.
- Chaque méthode testée : happy path + erreur (base inexistante, login déjà existant, etc.).

### Critères couverts

AC-5.1.1, AC-5.1.2, AC-5.2.1, AC-5.2.2, AC-9.2.2

---

## Étape 3 — Mock du client SQL

### Objectif

Créer un mock du `SQLClient` pour les tests unitaires des contrôleurs.

### Fichiers créés

- `internal/sql/mock_client.go`

### Design

```go
type MockSQLClient struct {
    Databases    map[string]MockDatabase
    Logins       map[string]MockLogin
    Users        map[string]MockUser // key: "dbName/userName"
    ConnectError error               // simuler une panne de connexion
}
```

Ou bien utiliser `github.com/stretchr/testify/mock` / `go.uber.org/mock` (gomock) pour générer le mock depuis l'interface.

### Critères couverts

Pré-requis pour AC-5.3.1, AC-5.3.2, AC-9.1.1, AC-9.1.2

---

## Étape 4 — Contrôleur Database

### Objectif

Implémenter la boucle de réconciliation complète pour le CRD `Database`.

### Fichier modifié

- `internal/controller/database_controller.go`

### Flux de réconciliation

```
Reconcile(ctx, req)
│
├─ 1. Fetch Database CR
│     └─ NotFound → return (supprimée)
│
├─ 2. Finalizer
│     ├─ Pas de DeletionTimestamp + pas de finalizer → AddFinalizer, Update
│     └─ DeletionTimestamp set → goto cleanup
│
├─ 3. Lire le Secret credentialsSecret
│     └─ Erreur → condition Ready=False, Reason=SecretNotFound
│
├─ 4. Créer le SQLClient (connexion)
│     └─ Erreur → condition Ready=False, Reason=ConnectionFailed, return err (retry)
│
├─ 5. Observer : DatabaseExists() + GetCollation() + GetOwner()
│
├─ 6. Comparer & Agir
│     ├─ N'existe pas → CreateDatabase(), event DatabaseCreated
│     ├─ Owner différent → SetDatabaseOwner()
│     └─ Tout conforme → rien
│
├─ 7. Status : condition Ready=True, ObservedGeneration = generation
│
└─ return RequeueAfter(30s)

Cleanup (DeletionTimestamp set):
├─ deletionPolicy == Delete → DropDatabase()
├─ RemoveFinalizer, Update
└─ return
```

### RBAC markers

```go
// +kubebuilder:rbac:groups=mssql.popul.io,resources=databases,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=mssql.popul.io,resources=databases/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=mssql.popul.io,resources=databases/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
```

### Setup avec predicates

```go
func (r *DatabaseReconciler) SetupWithManager(mgr ctrl.Manager) error {
    return ctrl.NewControllerManagedBy(mgr).
        For(&v1alpha1.Database{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
        Complete(r)
}
```

### Tests

- **Unitaires** (mock SQL client) : création, idempotence, mise à jour owner, suppression Delete, suppression Retain, Secret manquant, connexion échouée, champ immutable.
- **Intégration** (envtest) : cycle de vie complet create → update → delete.

### Critères couverts

AC-2.1.1 → AC-2.1.5, AC-2.2.1, AC-2.3.1, AC-2.3.2, AC-2.4.1 → AC-2.4.3, AC-2.5.1, AC-2.5.2, AC-2.6.1 → AC-2.6.3, AC-5.4.1, AC-5.4.2, AC-5.5.1

---

## Étape 5 — Contrôleur Login

### Objectif

Même pattern que Database, avec la rotation de mot de passe et les server roles.

### Fichier modifié

- `internal/controller/login_controller.go`

### Flux de réconciliation

```
Reconcile(ctx, req)
│
├─ 1-4. Fetch, Finalizer, Secret SA, Connect (identique à Database)
│
├─ 5. Lire le Secret passwordSecret
│     └─ Calculer hash du password pour détecter les changements
│
├─ 6. Observer : LoginExists() + GetDefaultDatabase() + GetServerRoles()
│
├─ 7. Comparer & Agir
│     ├─ N'existe pas → CreateLogin(), event LoginCreated
│     ├─ Password changé (hash dans annotation) → UpdateLoginPassword(), event LoginPasswordRotated
│     ├─ DefaultDatabase différent → SetLoginDefaultDatabase()
│     ├─ Rôles à ajouter → AddLoginToServerRole()
│     └─ Rôles à retirer → RemoveLoginFromServerRole()
│
├─ 8. Status : Ready=True, ObservedGeneration
│
└─ return RequeueAfter(30s)
```

### Détection de rotation du mot de passe

- Stocker un hash SHA-256 du password dans une annotation de la CR (`mssql.popul.io/password-hash`).
- À chaque réconciliation, comparer le hash actuel du Secret avec l'annotation.
- Si différent → `ALTER LOGIN ... WITH PASSWORD`, mettre à jour l'annotation.

### Tests

- Unitaires : création, rotation de mot de passe, ajout/retrait de rôle, suppression avec LoginInUse.
- Intégration (testcontainers) : rotation réelle du mot de passe.

### Critères couverts

AC-3.1.1 → AC-3.1.4, AC-3.2.1, AC-3.2.2, AC-3.3.1, AC-3.3.2, AC-3.4.1 → AC-3.4.3, AC-3.5.3

---

## Étape 6 — Contrôleur DatabaseUser

### Objectif

Gestion des utilisateurs dans une base, avec référence croisée vers la CR Login.

### Fichier modifié

- `internal/controller/databaseuser_controller.go`

### Flux de réconciliation

```
Reconcile(ctx, req)
│
├─ 1-4. Fetch, Finalizer, Secret SA, Connect
│
├─ 5. Résoudre loginRef
│     ├─ Fetch la CR Login par nom dans le même namespace
│     └─ Pas trouvée → condition Ready=False, Reason=LoginRefNotFound, RequeueAfter(10s)
│
├─ 6. Observer : UserExists() + GetUserDatabaseRoles()
│
├─ 7. Comparer & Agir
│     ├─ N'existe pas → CreateUser(), event DatabaseUserCreated
│     ├─ Rôles à ajouter → AddUserToDatabaseRole()
│     └─ Rôles à retirer → RemoveUserFromDatabaseRole()
│
├─ 8. Status : Ready=True
│
└─ return RequeueAfter(30s)

Cleanup:
├─ DropUser() — si échoue (owns objects) → condition UserOwnsObjects, ne pas retirer le finalizer
└─ Réussit → RemoveFinalizer
```

### Watch sur Login (pour détecter la création d'un Login référencé)

```go
func (r *DatabaseUserReconciler) SetupWithManager(mgr ctrl.Manager) error {
    return ctrl.NewControllerManagedBy(mgr).
        For(&v1alpha1.DatabaseUser{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
        Watches(&v1alpha1.Login{}, handler.EnqueueRequestsFromMapFunc(r.findDatabaseUsersForLogin)).
        Complete(r)
}
```

### Tests

- Unitaires : création, LoginRefNotFound puis résolu, ajout/retrait de rôle, suppression, UserOwnsObjects.

### Critères couverts

AC-4.1.1, AC-4.1.2, AC-4.2.1, AC-4.2.2, AC-4.3.1, AC-4.3.2, AC-4.4.1, AC-4.4.2

---

## Étape 7 — Observabilité

### Fichiers modifiés

- `cmd/main.go` — configuration zap JSON, health endpoints
- `internal/controller/*.go` — ajout des `recorder.Event()`

### Actions

- Configurer le logger zap en mode JSON production.
- Vérifier que `/healthz` et `/readyz` sont exposés (scaffoldé par Kubebuilder).
- Ajouter `EventRecorder` dans chaque contrôleur, émettre les events listés dans les AC.

### Critères couverts

AC-5.4.1, AC-5.4.2, AC-5.4.3, AC-6.1.1 → AC-6.1.3, AC-6.3.1, AC-6.3.2

---

## Étape 8 — Helm Chart

### Fichiers créés

```
charts/mssql-operator/
├── Chart.yaml
├── values.yaml
├── crds/                    # CRDs copiées depuis config/crd/bases/
├── templates/
│   ├── deployment.yaml
│   ├── serviceaccount.yaml
│   ├── clusterrole.yaml
│   ├── clusterrolebinding.yaml
│   ├── service.yaml          # métriques
│   ├── servicemonitor.yaml   # optionnel
│   └── pdb.yaml              # si replicas > 1
└── tests/
    └── test-connection.yaml
```

### Critères couverts

AC-1.3.1 → AC-1.3.4

---

## Étape 9 — CI/CD (GitHub Actions)

### Fichiers créés

```
.github/workflows/
├── ci.yaml          # test + lint sur push/PR
├── release.yaml     # build + push image sur tag
```

### Critères couverts

AC-1.2.1, AC-1.2.2, AC-1.2.3

---

## Étape 10 — Tests E2E

### Fichier créé

- `test/e2e/e2e_test.go`

### Scénario

1. Déployer SQL Server dans kind via un manifest.
2. Installer l'opérateur via Helm.
3. Créer une CR Database → vérifier la base sur SQL Server.
4. Créer une CR Login → vérifier le login.
5. Créer une CR DatabaseUser → vérifier l'utilisateur et les rôles.
6. Modifier les rôles → vérifier la convergence.
7. Supprimer dans l'ordre inverse → vérifier le cleanup.

### Critères couverts

AC-9.3.1, AC-5.3.2

---

## Étape 11 — Haute disponibilité & Résilience [P2]

### Actions

- Activer le leader election dans `cmd/main.go` (déjà scaffoldé, juste activer le flag).
- Ajouter le `PodDisruptionBudget` conditionnel dans le Helm chart.
- Tests de résilience : kill le pod, vérifier le failover.

### Critères couverts

AC-7.1 → AC-7.4, AC-8.1 → AC-8.3

---

## Étape 12 — Métriques Prometheus [P2]

### Actions

- Ajouter des métriques custom dans `internal/metrics/metrics.go` :
  - `mssql_operator_managed_databases` (gauge)
  - `mssql_operator_managed_logins` (gauge)
  - `mssql_operator_managed_users` (gauge)
  - `mssql_operator_reconciliation_errors_total` (counter, par type de CR)
- Activer le `ServiceMonitor` dans le Helm chart.

### Critères couverts

AC-6.2.1 → AC-6.2.3

---

## Graphe de dépendances

```
Étape 0 (scaffold)
    │
    ├── Étape 1 (types API)
    │       │
    │       ├── Étape 2 (SQL client)
    │       │       │
    │       │       └── Étape 3 (mock)
    │       │               │
    │       │       ┌───────┼───────┐
    │       │       │       │       │
    │       │    Étape 4  Étape 5  Étape 6
    │       │   (Database) (Login) (DBUser)
    │       │       │       │       │
    │       │       └───────┼───────┘
    │       │               │
    │       │           Étape 7 (observabilité)
    │       │
    │       └── Étape 8 (Helm chart)
    │
    ├── Étape 9 (CI/CD)
    │
    └── Étape 10 (E2E) ← dépend de 4-8
            │
            ├── Étape 11 (HA) [P2]
            └── Étape 12 (métriques) [P2]
```

---

## Ordre d'exécution recommandé

| # | Étape | Parallélisable avec |
|---|---|---|
| 1 | Étape 0 — Scaffolding | — |
| 2 | Étape 1 — Types API | — |
| 3 | Étape 2 — Client SQL | Étape 9 (CI) |
| 4 | Étape 3 — Mock | — |
| 5 | Étape 4 — Contrôleur Database | — |
| 6 | Étape 5 — Contrôleur Login | — |
| 7 | Étape 6 — Contrôleur DatabaseUser | Étape 8 (Helm) |
| 8 | Étape 7 — Observabilité | — |
| 9 | Étape 10 — Tests E2E | — |
| 10 | Étape 11 — HA [P2] | Étape 12 (métriques) |
