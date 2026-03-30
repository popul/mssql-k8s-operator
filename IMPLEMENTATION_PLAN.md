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
    TLS               *bool           `json:"tls,omitempty"`        // +optional, +kubebuilder:default=false
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
    UserOwnsObjects(ctx context.Context, dbName, userName string) (bool, error)

    // Cross-reference checks
    LoginHasUsers(ctx context.Context, loginName string) (bool, error)

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

AC-5.1.1, AC-5.1.2, AC-5.1.3, AC-5.1.4, AC-5.2.1, AC-5.2.2, AC-5.2.4

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

AC-2.1.1 → AC-2.1.5, AC-2.2.1, AC-2.3.1 → AC-2.3.3, AC-2.4.1 → AC-2.4.3, AC-2.5.1, AC-2.5.2, AC-2.6.1 → AC-2.6.3, AC-5.1.3, AC-5.1.4, AC-5.2.3, AC-5.4.1, AC-5.4.2, AC-5.5.1

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

- Stocker le `ResourceVersion` du Secret password dans `status.passwordSecretResourceVersion`.
- À chaque réconciliation, comparer le `ResourceVersion` actuel du Secret avec celui stocké dans le status.
- Si différent → `ALTER LOGIN ... WITH PASSWORD`, mettre à jour le status.
- Ajouter un watch sur les Secrets référencés par les CRs Login pour déclencher une réconciliation lorsque le Secret est modifié (même si `spec` n'a pas changé, car `GenerationChangedPredicate` ne filtre que les changements sur la CR Login elle-même, pas sur les Secrets).

> **Choix de design** : on utilise `status.passwordSecretResourceVersion` plutôt qu'une annotation pour éviter de muter les metadata de la CR (ce qui causerait un event de reconciliation supplémentaire, même si `GenerationChangedPredicate` le filtrerait).

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
│     ├─ Fetch la CR Login par `loginRef.name` (nom K8s) dans le même namespace
│     ├─ Extraire `spec.loginName` de la CR Login (nom SQL Server, ex: "myapp_user")
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

## Étape 7 — Tests d'intégration

### Objectif

Valider chaque couche avec des dépendances réelles : le client SQL contre un vrai SQL Server (testcontainers), les contrôleurs contre un vrai API Server (envtest), et les deux ensemble (full-stack).

### 7A — Tests d'intégration du client SQL (testcontainers)

#### Fichiers créés

- `internal/sql/testhelper_test.go` — setup/teardown du container SQL Server
- `internal/sql/client_integration_test.go` — tests de toutes les méthodes

#### Setup

```go
// testhelper_test.go
// Build tag: //go:build integration

func setupSQLServer(t *testing.T) (host string, port int, cleanup func()) {
    ctx := context.Background()
    req := testcontainers.ContainerRequest{
        Image:        "mcr.microsoft.com/mssql/server:2022-latest",
        ExposedPorts: []string{"1433/tcp"},
        Env: map[string]string{
            "ACCEPT_EULA":     "Y",
            "MSSQL_SA_PASSWORD": "T3stP@ssw0rd!",
        },
        WaitingFor: wait.ForSQL("1433/tcp", "sqlserver",
            func(host string, port nat.Port) string {
                return fmt.Sprintf("sqlserver://sa:T3stP@ssw0rd!@%s:%s", host, port.Port())
            }).WithStartupTimeout(60 * time.Second),
    }
    container, _ := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
        ContainerRequest: req, Started: true,
    })
    // ... retourner host, port mappé, cleanup
}
```

#### Cas de test

```
TestDatabaseLifecycle
├── CreateDatabase avec collation
├── CreateDatabase sans collation (défaut)
├── DatabaseExists → true
├── GetDatabaseCollation → valeur attendue
├── SetDatabaseOwner + GetDatabaseOwner
├── DropDatabase → DatabaseExists → false
└── DropDatabase idempotent (re-drop)

TestLoginLifecycle
├── CreateLogin
├── LoginExists → true
├── UpdateLoginPassword → connexion avec nouveau mdp OK
├── SetLoginDefaultDatabase + GetLoginDefaultDatabase
├── AddLoginToServerRole + GetLoginServerRoles
├── RemoveLoginFromServerRole
├── LoginHasUsers → false (pas d'user)
├── DropLogin → LoginExists → false
└── DropLogin idempotent

TestDatabaseUserLifecycle
├── Setup : CreateDatabase + CreateLogin
├── CreateUser
├── UserExists → true
├── AddUserToDatabaseRole + GetUserDatabaseRoles
├── RemoveUserFromDatabaseRole
├── UserOwnsObjects → false
├── DropUser → UserExists → false
└── Cleanup : DropLogin + DropDatabase

TestUserOwnsObjects
├── CreateDatabase + CreateLogin + CreateUser
├── CREATE SCHEMA owned by user (T-SQL direct)
├── UserOwnsObjects → true
└── Cleanup

TestLoginHasUsers
├── CreateDatabase + CreateLogin + CreateUser
├── LoginHasUsers → true
└── DropUser → LoginHasUsers → false

TestQuoteNameInjection
├── CreateDatabase("test]db") → succès, nom correctement échappé
├── CreateDatabase("test[db") → succès
├── CreateDatabase("'; DROP DATABASE master; --") → succès, pas d'injection
└── Cleanup

TestConnectionFailure
├── Arrêter le container
├── Appeler Ping() → erreur retournée, pas de panic/hang
└── Timeout < 10s
```

#### Commande

```bash
go test -tags=integration -v -count=1 ./internal/sql/...
```

### 7B — Tests d'intégration des contrôleurs (envtest)

#### Fichiers créés

- `internal/controller/suite_test.go` — setup envtest (déjà scaffoldé, à enrichir)
- `internal/controller/database_controller_integration_test.go`
- `internal/controller/login_controller_integration_test.go`
- `internal/controller/databaseuser_controller_integration_test.go`

#### Setup

```go
// suite_test.go
var (
    k8sClient  client.Client
    testEnv    *envtest.Environment
    ctx        context.Context
    cancel     context.CancelFunc
    mockSQL    *sql.MockSQLClient
)

func TestControllers(t *testing.T) {
    RegisterFailHandler(Fail)
    RunSpecs(t, "Controller Suite")
}

var _ = BeforeSuite(func() {
    testEnv = &envtest.Environment{
        CRDDirectoryPaths: []string{
            filepath.Join("..", "..", "config", "crd", "bases"),
        },
    }
    cfg, _ := testEnv.Start()

    // Créer le mock SQL client
    mockSQL = sql.NewMockSQLClient()

    // Enregistrer le manager + les 3 contrôleurs avec le mock
    mgr, _ := ctrl.NewManager(cfg, ctrl.Options{Scheme: scheme})
    (&DatabaseReconciler{
        Client:       mgr.GetClient(),
        Scheme:       mgr.GetScheme(),
        SQLFactory:   func(...) (sql.SQLClient, error) { return mockSQL, nil },
        Recorder:     mgr.GetEventRecorderFor("database-controller"),
    }).SetupWithManager(mgr)
    // ... idem Login, DatabaseUser

    go mgr.Start(ctx)
})
```

#### Cas de test — Database

```
Describe("Database Controller")
├── Context("Création")
│   ├── It("crée la base et passe en Ready=True")
│   │   → Créer Secret SA + CR Database
│   │   → Eventually: status.conditions[Ready]=True
│   │   → Expect: mockSQL.CreateDatabaseCalled == true
│   │
│   └── It("adopte une base existante sans erreur")
│       → Configurer mockSQL.DatabaseExists = true
│       → Créer CR Database
│       → Eventually: Ready=True, CreateDatabase NOT called
│
├── Context("Mise à jour")
│   ├── It("met à jour le owner")
│   │   → Modifier spec.owner
│   │   → Eventually: mockSQL.SetDatabaseOwnerCalled == true
│   │
│   └── It("rejette un changement de databaseName")
│       → Modifier spec.databaseName
│       → Eventually: Ready=False, Reason=ImmutableFieldChanged
│
├── Context("Suppression")
│   ├── It("supprime la base avec deletionPolicy=Delete")
│   │   → Delete CR
│   │   → Eventually: mockSQL.DropDatabaseCalled == true
│   │   → Eventually: CR n'existe plus dans l'API Server
│   │
│   ├── It("conserve la base avec deletionPolicy=Retain")
│   │   → Delete CR
│   │   → Eventually: CR n'existe plus
│   │   → Expect: mockSQL.DropDatabaseCalled == false
│   │
│   └── It("supprime proprement même si la base n'existe plus")
│       → Configurer mockSQL.DatabaseExists = false
│       → Delete CR → pas d'erreur
│
├── Context("Erreurs")
│   ├── It("Secret manquant → SecretNotFound")
│   ├── It("Connexion échouée → ConnectionFailed + retry")
│   └── It("Émet un event Warning sur erreur")
│
└── Context("Idempotence")
    └── It("2 réconciliations identiques = même résultat, 0 mutation")
```

#### Cas de test — Login

```
Describe("Login Controller")
├── Context("Création + rôles")
│   ├── It("crée le login avec les server roles")
│   └── It("adopte un login existant et corrige les rôles")
│
├── Context("Rotation du mot de passe")
│   ├── It("détecte un changement de Secret et appelle UpdateLoginPassword")
│   └── It("émet un event LoginPasswordRotated")
│
├── Context("Gestion des rôles")
│   ├── It("ajoute un rôle sans toucher aux existants")
│   └── It("retire un rôle supprimé du spec")
│
├── Context("Suppression")
│   ├── It("bloque si LoginHasUsers=true → LoginInUse")
│   └── It("supprime si pas d'users dépendants")
│
└── Context("Idempotence")
    └── It("2 réconciliations identiques = 0 mutation")
```

#### Cas de test — DatabaseUser

```
Describe("DatabaseUser Controller")
├── Context("Référence croisée")
│   ├── It("LoginRefNotFound si la CR Login n'existe pas")
│   ├── It("Passe en Ready quand la CR Login est créée ensuite")
│   └── It("Crée l'utilisateur avec les rôles spécifiés")
│
├── Context("Gestion des rôles")
│   ├── It("ajoute un rôle")
│   └── It("retire un rôle")
│
├── Context("Suppression")
│   ├── It("bloque si UserOwnsObjects=true")
│   └── It("supprime sinon")
│
└── Context("Idempotence")
    └── It("2 réconciliations identiques = 0 mutation")
```

#### Cas de test — Transverse

```
Describe("Transverse")
├── It("Les events sont visibles via l'API Events")
├── It("GenerationChangedPredicate filtre les updates de status")
└── It("ObservedGeneration correspond à metadata.generation")
```

### 7C — Tests full-stack (envtest + testcontainers)

#### Fichier créé

- `test/integration/fullstack_test.go`

#### Objectif

Combiner un vrai API Server (envtest) avec un vrai SQL Server (testcontainers). Les contrôleurs utilisent le **vrai** client SQL, pas un mock.

#### Setup

```go
// Build tag: //go:build fullstack

var (
    testEnv       *envtest.Environment
    sqlHost       string
    sqlPort       int
    sqlCleanup    func()
)

func TestFullStack(t *testing.T) {
    // 1. Démarrer testcontainers SQL Server
    sqlHost, sqlPort, sqlCleanup = setupSQLServer(t)
    defer sqlCleanup()

    // 2. Démarrer envtest
    testEnv = &envtest.Environment{...}
    cfg, _ := testEnv.Start()

    // 3. Enregistrer les contrôleurs avec le VRAI SQLClientFactory
    mgr, _ := ctrl.NewManager(cfg, ...)
    realFactory := func(host string, port int, user, pass string) (sql.SQLClient, error) {
        return sql.NewMSSQLClient(host, port, user, pass)
    }
    // ... enregistrer les 3 contrôleurs avec realFactory

    // 4. Lancer les tests
    RegisterFailHandler(Fail)
    RunSpecs(t, "Full Stack Suite")
}
```

#### Scénarios

```
Describe("Full Stack — Database")
├── It("CR Database → base créée sur SQL Server réel")
│   → kubectl create Secret SA (avec vrais credentials SQL)
│   → kubectl create Database CR
│   → Eventually: status Ready=True
│   → Vérifier via connexion SQL directe: SELECT name FROM sys.databases WHERE name = 'testdb'
│   → Vérifier collation, owner
│
├── It("Suppression avec Delete → base supprimée sur SQL Server")
│   → kubectl delete Database CR
│   → Vérifier: base n'existe plus dans sys.databases
│
└── It("Suppression avec Retain → base conservée")
    → kubectl delete → vérifier: base existe toujours

Describe("Full Stack — Login")
├── It("CR Login → login créé sur SQL Server réel")
├── It("Rotation mot de passe → connexion avec ancien mdp échoue, nouveau réussit")
└── It("Suppression → login supprimé")

Describe("Full Stack — DatabaseUser")
├── It("Cycle complet: Database + Login + DatabaseUser → user avec rôles")
│   → Créer les 3 CRs en séquence
│   → Vérifier sur SQL Server: user existe, rôles attribués
│
├── It("Modifier les rôles → convergence sur SQL Server")
│   → Ajouter/retirer des rôles dans le spec
│   → Vérifier sur SQL Server
│
└── It("Supprimer dans l'ordre inverse → cleanup complet")
    → Delete DatabaseUser → user supprimé
    → Delete Login → login supprimé
    → Delete Database → base supprimée

Describe("Full Stack — Résilience")
├── It("SQL Server restart → réconciliation reprend")
│   → Créer des CRs → Ready
│   → Arrêter le container SQL Server
│   → Eventually: conditions passent à Ready=False
│   → Redémarrer le container
│   → Eventually: conditions repassent à Ready=True
│
└── It("Drift detection → re-création automatique")
    → Créer une CR Database → Ready
    → DROP DATABASE directement via SQL
    → Attendre le prochain cycle de réconciliation (≤30s)
    → Vérifier: base recréée sur SQL Server
```

#### Commande

```bash
go test -tags=fullstack -v -count=1 -timeout=5m ./test/integration/...
```

### Résumé des fichiers de test

```
internal/sql/
├── client_integration_test.go      # 7A — testcontainers, //go:build integration
└── testhelper_test.go              # Helper shared

internal/controller/
├── suite_test.go                   # 7B — envtest setup
├── database_controller_integration_test.go
├── login_controller_integration_test.go
└── databaseuser_controller_integration_test.go

test/integration/
└── fullstack_test.go               # 7C — envtest + testcontainers, //go:build fullstack
```

### Makefile targets

```makefile
.PHONY: test test-integration test-fullstack

test:                               ## Tests unitaires + envtest
	go test ./... -count=1

test-integration:                   ## Tests d'intégration SQL (nécessite Docker)
	go test -tags=integration -v -count=1 -timeout=5m ./internal/sql/...

test-fullstack:                     ## Tests full-stack envtest + SQL Server (nécessite Docker)
	go test -tags=fullstack -v -count=1 -timeout=10m ./test/integration/...

test-all: test test-integration test-fullstack  ## Tous les tests
```

### Critères couverts

AC-9.2.1 → AC-9.2.18, AC-9.3.1 → AC-9.3.16, AC-9.4.1 → AC-9.4.4

---

## Étape 8 — Observabilité

### Fichiers modifiés

- `cmd/main.go` — configuration zap JSON, health endpoints
- `internal/controller/*.go` — audit des logs et events

### Actions

- Configurer le logger zap en mode JSON production.
- Vérifier que `/healthz` et `/readyz` sont exposés (scaffoldé par Kubebuilder).
- Auditer que l'`EventRecorder` est utilisé dans chaque contrôleur (implémenté dans les étapes 4-6, cette étape fait l'audit de complétude).
- Vérifier qu'aucun credential n'apparaît dans les logs quel que soit le niveau de verbosité.

### Critères couverts

AC-5.2.1, AC-5.4.3, AC-6.1.1 → AC-6.1.3, AC-6.3.1, AC-6.3.2

> **Note** : AC-5.4.1 et AC-5.4.2 (events Normal et Warning) sont implémentés dans les étapes 4-6 (contrôleurs). Cette étape fait la vérification et ajoute les events manquants.

---

## Étape 9 — Helm Chart

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

## Étape 10 — CI/CD (GitHub Actions)

### Fichiers créés

```
.github/workflows/
├── ci.yaml          # test + lint sur push/PR
├── release.yaml     # build + push image sur tag
```

### Critères couverts

AC-1.2.1, AC-1.2.2, AC-1.2.3

---

## Étape 11 — Tests E2E

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

AC-9.5.1, AC-5.3.2

---

## Étape 12 — Haute disponibilité & Résilience [P2]

### Actions

- Activer le leader election dans `cmd/main.go` (déjà scaffoldé, juste activer le flag).
- Ajouter le `PodDisruptionBudget` conditionnel dans le Helm chart.
- Tests de résilience : kill le pod, vérifier le failover.

### Critères couverts

AC-7.1 → AC-7.4, AC-8.1 → AC-8.3

---

## Étape 13 — Métriques Prometheus [P2]

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
    │       │       ├── Étape 3 (mock)
    │       │       │       │
    │       │       │   ┌───┼───────────┐
    │       │       │   │   │           │
    │       │       │ Ét.4  Ét.5      Ét.6
    │       │       │ (DB)  (Login)  (DBUser)
    │       │       │   │   │           │
    │       │       │   └───┼───────────┘
    │       │       │       │
    │       │       ├── Étape 7A (tests intégration SQL — testcontainers)
    │       │       │
    │       │       └── Étape 7B (tests intégration contrôleurs — envtest) ← dépend de 3-6
    │       │               │
    │       │               └── Étape 7C (tests full-stack) ← dépend de 7A + 7B
    │       │
    │       │── Étape 8 (observabilité) ← dépend de 4-6
    │       │
    │       └── Étape 9 (Helm chart)
    │
    ├── Étape 10 (CI/CD)
    │
    └── Étape 11 (E2E) ← dépend de 7-9
            │
            ├── Étape 12 (HA) [P2]
            └── Étape 13 (métriques) [P2]
```

---

## Ordre d'exécution recommandé

| # | Étape | Parallélisable avec |
|---|---|---|
| 1 | Étape 0 — Scaffolding | — |
| 2 | Étape 1 — Types API | — |
| 3 | Étape 2 — Client SQL | Étape 10 (CI) |
| 4 | Étape 3 — Mock | Étape 7A (tests intégration SQL) |
| 5 | Étape 4 — Contrôleur Database | — |
| 6 | Étape 5 — Contrôleur Login | — |
| 7 | Étape 6 — Contrôleur DatabaseUser | Étape 9 (Helm) |
| 8 | Étape 7B — Tests intégration envtest | Étape 8 (observabilité) |
| 9 | Étape 7C — Tests full-stack | — |
| 10 | Étape 11 — Tests E2E | — |
| 11 | Étape 12 — HA [P2] | Étape 13 (métriques) |
