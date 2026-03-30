# Critères d'acceptation — mssql-k8s-operator

## Légende

- **[P1]** Phase 1 — MVP
- **[P2]** Phase 2 — Hardening
- Chaque critère est formulé en Given / When / Then et peut être directement traduit en test.

---

## 1. Scaffolding & Infrastructure

### 1.1 Initialisation du projet [P1]

- [ ] **AC-1.1.1** — Le projet est scaffoldé avec Kubebuilder v4, compile sans erreur (`make build`), et les CRDs sont générées (`make manifests`).
- [ ] **AC-1.1.2** — Le `Makefile` expose les cibles : `generate`, `manifests`, `test`, `docker-build`, `docker-push`, `install`, `deploy`.
- [ ] **AC-1.1.3** — Le `Dockerfile` produit une image multi-stage minimale (scratch ou distroless) contenant uniquement le binaire.
- [ ] **AC-1.1.4** — Le groupe API est `mssql.popul.io`, la version est `v1alpha1`.

### 1.2 CI/CD [P1]

- [ ] **AC-1.2.1** — Un workflow GitHub Actions exécute `make test` sur chaque push et pull request.
- [ ] **AC-1.2.2** — Un workflow build et push l'image Docker sur un tag Git.
- [ ] **AC-1.2.3** — Les tests d'intégration SQL (testcontainers) s'exécutent en CI.

### 1.3 Helm Chart [P1]

- [ ] **AC-1.3.1** — `helm install mssql-operator ./charts/mssql-operator` déploie l'opérateur sans configuration manuelle supplémentaire.
- [ ] **AC-1.3.2** — Les CRDs sont installées automatiquement lors du `helm install`.
- [ ] **AC-1.3.3** — Les paramètres suivants sont configurables via `values.yaml` : image, tag, replicas, resources, nodeSelector, tolerations, affinity.
- [ ] **AC-1.3.4** — `helm uninstall` supprime toutes les ressources créées par le chart (hors CRDs, convention Helm).

---

## 2. CRD `Database`

### 2.1 Création [P1]

- [ ] **AC-2.1.1** — **Given** un Secret `mssql-sa-credentials` existant avec les clés `username` et `password`, **When** je crée une CR `Database` avec `databaseName: myapp`, **Then** la base de données `myapp` est créée sur l'instance SQL Server et la condition `Ready=True` est positionnée dans le status.
- [ ] **AC-2.1.2** — **Given** une CR `Database` avec `collation: SQL_Latin1_General_CP1_CI_AS`, **When** la base est créée, **Then** la collation de la base correspond à la valeur spécifiée.
- [ ] **AC-2.1.3** — **Given** une CR `Database` sans `collation`, **When** la base est créée, **Then** la collation par défaut de l'instance SQL Server est utilisée.
- [ ] **AC-2.1.4** — **Given** une CR `Database` avec `owner: myapp_user`, **When** la base est créée et le login existe, **Then** le propriétaire de la base est `myapp_user`.
- [ ] **AC-2.1.5** — **Given** une base de données `myapp` qui existe déjà sur SQL Server, **When** je crée une CR `Database` avec le même `databaseName`, **Then** le contrôleur adopte la base existante sans erreur et passe en `Ready=True`.

### 2.2 Idempotence [P1]

- [ ] **AC-2.2.1** — **Given** une CR `Database` en état `Ready=True`, **When** le contrôleur réconcilie une seconde fois sans changement, **Then** aucune requête DDL n'est exécutée et le status reste inchangé.

### 2.3 Mise à jour [P1]

- [ ] **AC-2.3.1** — **Given** une CR `Database` existante, **When** je modifie `owner` dans le spec, **Then** le propriétaire de la base est mis à jour sur SQL Server via `ALTER AUTHORIZATION`.
- [ ] **AC-2.3.2** — **Given** une CR `Database` existante avec une collation non définie initialement, **When** je définis `collation` dans le spec, **Then** le contrôleur refuse la mise à jour (la collation ne peut pas être changée sur une base existante) et positionne `Ready=False` avec `Reason: CollationChangeNotSupported`.
- [ ] **AC-2.3.3** — **Given** une CR `Database` existante, **When** je modifie `databaseName`, **Then** le contrôleur refuse la mise à jour (champ immutable) et positionne une condition `Ready=False` avec un `Reason: ImmutableFieldChanged`.

### 2.4 Suppression [P1]

- [ ] **AC-2.4.1** — **Given** une CR `Database` avec `deletionPolicy: Delete`, **When** je supprime la CR (`kubectl delete`), **Then** la base de données est supprimée (DROP DATABASE) sur SQL Server, le finalizer est retiré, et la CR disparaît.
- [ ] **AC-2.4.2** — **Given** une CR `Database` avec `deletionPolicy: Retain` (ou non spécifié, car c'est le défaut), **When** je supprime la CR, **Then** la base de données est conservée sur SQL Server, le finalizer est retiré, et la CR disparaît.
- [ ] **AC-2.4.3** — **Given** une CR `Database` dont la base a déjà été supprimée manuellement sur SQL Server, **When** je supprime la CR, **Then** le finalizer est retiré sans erreur (cleanup idempotent).

### 2.5 Finalizer [P1]

- [ ] **AC-2.5.1** — **Given** une nouvelle CR `Database` créée, **Then** le finalizer `mssql.popul.io/finalizer` est ajouté à la CR lors de la première réconciliation.
- [ ] **AC-2.5.2** — **Given** une CR `Database` avec finalizer et `DeletionTimestamp` positionné, **When** le cleanup est terminé, **Then** le finalizer est retiré et la CR est effectivement supprimée.

### 2.6 Status [P1]

- [ ] **AC-2.6.1** — Le champ `status.conditions` contient une condition de type `Ready` avec `Status` ∈ {`True`, `False`, `Unknown`}, un `Reason` PascalCase, un `Message` lisible et `ObservedGeneration` correspondant à `metadata.generation`.
- [ ] **AC-2.6.2** — **Given** un SQL Server injoignable, **When** le contrôleur réconcilie, **Then** la condition `Ready=False` est positionnée avec `Reason: ConnectionFailed` et un event `Warning` est émis.
- [ ] **AC-2.6.3** — **Given** le Secret `credentialsSecret` référencé n'existe pas, **When** le contrôleur réconcilie, **Then** la condition `Ready=False` est positionnée avec `Reason: SecretNotFound`.

### 2.7 Validation [P1]

- [ ] **AC-2.7.1** — **Given** une CR `Database` sans `spec.server.credentialsSecret.name`, **When** je tente de la créer, **Then** l'API Server rejette la CR avec une erreur de validation.
- [ ] **AC-2.7.2** — **Given** une CR `Database` sans `spec.databaseName`, **When** je tente de la créer, **Then** l'API Server rejette la CR.
- [ ] **AC-2.7.3** — **Given** une CR `Database` avec `deletionPolicy: InvalidValue`, **When** je tente de la créer, **Then** l'API Server rejette la CR (enum validation).

---

## 3. CRD `Login`

### 3.1 Création [P1]

- [ ] **AC-3.1.1** — **Given** un Secret `mssql-sa-credentials` et un Secret `myapp-login-password` (clé `password`), **When** je crée une CR `Login` avec `loginName: myapp_user`, **Then** le login SQL `myapp_user` est créé sur l'instance et la condition `Ready=True` est positionnée.
- [ ] **AC-3.1.2** — **Given** une CR `Login` avec `defaultDatabase: myapp`, **When** le login est créé, **Then** la base par défaut du login est `myapp`.
- [ ] **AC-3.1.3** — **Given** une CR `Login` avec `serverRoles: [dbcreator, securityadmin]`, **When** le login est créé, **Then** le login est membre des rôles serveur `dbcreator` et `securityadmin`.
- [ ] **AC-3.1.4** — **Given** un login `myapp_user` qui existe déjà sur SQL Server, **When** je crée une CR `Login` avec le même `loginName`, **Then** le contrôleur adopte le login existant et met à jour le mot de passe et les rôles si nécessaire.

### 3.2 Rotation du mot de passe [P1]

- [ ] **AC-3.2.1** — **Given** une CR `Login` en état `Ready=True`, **When** le contenu du Secret `passwordSecret` est modifié (nouvelle valeur pour la clé `password`), **Then** le mot de passe du login est mis à jour sur SQL Server via `ALTER LOGIN ... WITH PASSWORD`.
- [ ] **AC-3.2.2** — **Given** une rotation de mot de passe réussie, **Then** un event `Normal` de type `LoginPasswordRotated` est émis sur la CR.

### 3.3 Gestion des rôles [P1]

- [ ] **AC-3.3.1** — **Given** une CR `Login` existante avec `serverRoles: [dbcreator]`, **When** je modifie pour `serverRoles: [dbcreator, securityadmin]`, **Then** le login est ajouté au rôle `securityadmin` sans toucher à `dbcreator`.
- [ ] **AC-3.3.2** — **Given** une CR `Login` existante avec `serverRoles: [dbcreator, securityadmin]`, **When** je modifie pour `serverRoles: [dbcreator]`, **Then** le login est retiré du rôle `securityadmin`.

### 3.4 Suppression [P1]

- [ ] **AC-3.4.1** — **Given** une CR `Login` avec `deletionPolicy: Delete`, **When** je supprime la CR, **Then** le login est supprimé (DROP LOGIN) sur SQL Server.
- [ ] **AC-3.4.2** — **Given** une CR `Login` avec `deletionPolicy: Retain`, **When** je supprime la CR, **Then** le login est conservé sur SQL Server.
- [ ] **AC-3.4.3** — **Given** une CR `Login` dont le login est utilisé par un `DatabaseUser` actif, **When** je supprime la CR `Login` avec `deletionPolicy: Delete`, **Then** la suppression échoue avec une condition `Ready=False` et `Reason: LoginInUse`, et un event `Warning` est émis.

### 3.5 Validation [P1]

- [ ] **AC-3.5.1** — **Given** une CR `Login` sans `spec.loginName`, **Then** l'API Server rejette la CR.
- [ ] **AC-3.5.2** — **Given** une CR `Login` sans `spec.passwordSecret.name`, **Then** l'API Server rejette la CR.
- [ ] **AC-3.5.3** — **Given** une CR `Login` avec un `serverRoles` contenant une valeur invalide, **Then** le contrôleur positionne `Ready=False` avec `Reason: InvalidServerRole`.

---

## 4. CRD `DatabaseUser`

### 4.1 Création [P1]

- [ ] **AC-4.1.1** — **Given** une base `myapp` et un login `myapp_user` existants, **When** je crée une CR `DatabaseUser` avec `databaseName: myapp`, `userName: myapp_user`, `loginRef.name: myapp-login`, **Then** l'utilisateur `myapp_user` est créé dans la base `myapp` et la condition `Ready=True` est positionnée.
- [ ] **AC-4.1.2** — **Given** une CR `DatabaseUser` avec `databaseRoles: [db_datareader, db_datawriter]`, **When** l'utilisateur est créé, **Then** il est membre des rôles `db_datareader` et `db_datawriter`.

### 4.2 Référence croisée [P1]

- [ ] **AC-4.2.1** — **Given** une CR `DatabaseUser` avec `loginRef.name: myapp-login`, **When** la CR `Login` nommée `myapp-login` n'existe pas dans le namespace, **Then** la condition `Ready=False` est positionnée avec `Reason: LoginRefNotFound`.
- [ ] **AC-4.2.2** — **Given** une CR `DatabaseUser` en attente (`LoginRefNotFound`), **When** la CR `Login` référencée est créée ultérieurement, **Then** le contrôleur détecte le changement, crée l'utilisateur, et passe en `Ready=True`.

### 4.3 Gestion des rôles [P1]

- [ ] **AC-4.3.1** — **Given** une CR `DatabaseUser` existante, **When** j'ajoute un rôle dans `databaseRoles`, **Then** l'utilisateur est ajouté au nouveau rôle sans perdre les rôles existants.
- [ ] **AC-4.3.2** — **Given** une CR `DatabaseUser` existante, **When** je retire un rôle de `databaseRoles`, **Then** l'utilisateur est retiré de ce rôle.

### 4.4 Suppression [P1]

> **Note** : contrairement à `Database` et `Login`, `DatabaseUser` n'a pas de `deletionPolicy`. La suppression d'un utilisateur SQL est une opération peu risquée (l'utilisateur n'a pas de données propres). Le comportement est toujours `DROP USER`.

- [ ] **AC-4.4.1** — **Given** une CR `DatabaseUser`, **When** je supprime la CR, **Then** l'utilisateur est supprimé (DROP USER) de la base de données cible.
- [ ] **AC-4.4.2** — **Given** une CR `DatabaseUser` dont l'utilisateur possède des objets (schéma, tables), **When** je supprime la CR, **Then** la suppression échoue avec `Reason: UserOwnsObjects` et un event `Warning`.

### 4.5 Validation [P1]

- [ ] **AC-4.5.1** — **Given** une CR `DatabaseUser` sans `spec.databaseName`, **Then** l'API Server rejette la CR.
- [ ] **AC-4.5.2** — **Given** une CR `DatabaseUser` sans `spec.userName`, **Then** l'API Server rejette la CR.
- [ ] **AC-4.5.3** — **Given** une CR `DatabaseUser` sans `spec.loginRef.name`, **Then** l'API Server rejette la CR.

---

## 5. Comportements transverses

### 5.1 Connexion SQL Server [P1]

- [ ] **AC-5.1.1** — **Given** un Secret `credentialsSecret` contenant `username` et `password`, **When** le contrôleur se connecte à SQL Server, **Then** il utilise ces credentials et la connexion est établie via TDS sur le port spécifié.
- [ ] **AC-5.1.2** — **Given** un Secret `credentialsSecret` dont la clé `username` ou `password` est absente, **When** le contrôleur réconcilie, **Then** la condition `Ready=False` est positionnée avec `Reason: InvalidCredentialsSecret`.
- [ ] **AC-5.1.3** — **Given** une connexion SQL Server qui échoue (instance injoignable), **When** le contrôleur réconcilie, **Then** l'erreur est transitoire, un requeue avec back-off exponentiel est effectué, et un event `Warning` est émis.
- [ ] **AC-5.1.4** — **Given** une connexion SQL Server qui revient après une coupure, **When** le contrôleur réconcilie, **Then** la réconciliation reprend normalement et la condition repasse en `Ready=True`.

### 5.2 Sécurité [P1]

- [ ] **AC-5.2.1** — Aucune credential (mot de passe, token) n'apparaît dans les logs de l'opérateur, quel que soit le niveau de verbosité.
- [ ] **AC-5.2.2** — Toutes les requêtes SQL utilisant des identifiers dynamiques (noms de base, login, user) utilisent `quotename()` ou un mécanisme équivalent pour prévenir l'injection SQL.
- [ ] **AC-5.2.3** — Le ServiceAccount de l'opérateur n'a que les permissions RBAC nécessaires : CRDs custom (get, list, watch, create, update, patch, delete), Secrets (get, list, watch), Events (create, patch).
- [ ] **AC-5.2.4** — **Given** `spec.server` avec TLS activé, **When** le contrôleur se connecte, **Then** la connexion est chiffrée.

### 5.3 Idempotence globale [P1]

- [ ] **AC-5.3.1** — Pour chaque type de CR (Database, Login, DatabaseUser), appeler `Reconcile()` deux fois consécutives avec le même état produit exactement le même résultat sans effet de bord.
- [ ] **AC-5.3.2** — **Given** un opérateur redémarré, **When** il réconcilie toutes les CRs existantes, **Then** aucune modification n'est apportée aux objets SQL Server déjà conformes.

### 5.4 Events Kubernetes [P1]

- [ ] **AC-5.4.1** — Un event `Normal` est émis lors de chaque création réussie (ex: `DatabaseCreated`, `LoginCreated`, `DatabaseUserCreated`).
- [ ] **AC-5.4.2** — Un event `Warning` est émis lors de chaque erreur de réconciliation (ex: `ReconciliationFailed`, `ConnectionFailed`).
- [ ] **AC-5.4.3** — Les events sont visibles via `kubectl describe <resource>`.

### 5.5 Filtrage des événements [P1]

- [ ] **AC-5.5.1** — **Given** une mise à jour du status uniquement (sans changement de `spec`), **When** la CR est modifiée, **Then** le contrôleur ne déclenche pas de réconciliation (grâce à `GenerationChangedPredicate`).

---

## 6. Observabilité

### 6.1 Logs [P1]

- [ ] **AC-6.1.1** — Les logs sont structurés au format JSON.
- [ ] **AC-6.1.2** — Chaque entrée de log inclut le `namespace/name` de la CR concernée.
- [ ] **AC-6.1.3** — Les transitions d'état importantes sont loggées au niveau `Info`.

### 6.2 Métriques [P2]

- [ ] **AC-6.2.1** — L'endpoint `/metrics` expose les métriques standard controller-runtime (work queue depth, reconcile duration, reconcile errors).
- [ ] **AC-6.2.2** — Des métriques custom sont exposées : nombre de databases/logins/users gérés, nombre de réconciliations réussies/échouées par type de CR.
- [ ] **AC-6.2.3** — Un `ServiceMonitor` optionnel est disponible dans le Helm chart.

### 6.3 Health [P1]

- [ ] **AC-6.3.1** — L'endpoint `/healthz` retourne 200 quand l'opérateur est fonctionnel.
- [ ] **AC-6.3.2** — L'endpoint `/readyz` retourne 200 quand l'opérateur est prêt à réconcilier (caches synchronisés).

---

## 7. Haute disponibilité [P2]

- [ ] **AC-7.1** — **Given** 2 replicas de l'opérateur déployés avec `--leader-elect`, **Then** un seul replica réconcilie à la fois (le leader).
- [ ] **AC-7.2** — **Given** le leader qui crash, **When** le lease expire, **Then** le standby prend le relais en < 30 secondes.
- [ ] **AC-7.3** — **Given** 2+ replicas, **Then** les webhooks de validation sont servis par tous les replicas (pas seulement le leader).
- [ ] **AC-7.4** — Un `PodDisruptionBudget` est déployé quand `replicas > 1` dans le Helm chart.

---

## 8. Résilience [P2]

- [ ] **AC-8.1** — **Given** SQL Server qui devient injoignable pendant 5 minutes puis revient, **When** le contrôleur réconcilie, **Then** toutes les CRs convergent vers leur état désiré sans intervention manuelle.
- [ ] **AC-8.2** — **Given** un drift manuel sur SQL Server (ex: base supprimée hors opérateur), **When** le prochain cycle de réconciliation se déclenche (≤ 30s), **Then** l'opérateur détecte le drift et recrée la base (si la CR existe toujours).
- [ ] **AC-8.3** — **Given** le pod opérateur qui redémarre (OOMKill, rolling update), **When** il reprend, **Then** il réconcilie toutes les CRs existantes sans duplication ni erreur.

---

## 9. Tests

### 9.1 Couverture [P1]

- [ ] **AC-9.1.1** — La couverture de code sur les contrôleurs est ≥ 80%.
- [ ] **AC-9.1.2** — Chaque contrôleur a des tests unitaires couvrant : création, mise à jour, suppression, erreurs de connexion, Secret manquant, idempotence.

### 9.2 Tests d'intégration — Client SQL (testcontainers) [P1]

- [ ] **AC-9.2.1** — Les tests avec testcontainers démarrent une instance `mcr.microsoft.com/mssql/server:2022-latest` et valident toutes les méthodes du `SQLClient` contre SQL Server réel.
- [ ] **AC-9.2.2** — **Given** un appel à `CreateDatabase("testdb", "SQL_Latin1_General_CP1_CI_AS")`, **When** on appelle `DatabaseExists("testdb")`, **Then** le retour est `true` et `GetDatabaseCollation("testdb")` retourne la bonne collation.
- [ ] **AC-9.2.3** — **Given** un appel à `CreateDatabase("testdb", nil)`, **Then** la base est créée avec la collation par défaut de l'instance.
- [ ] **AC-9.2.4** — **Given** une base existante, **When** on appelle `SetDatabaseOwner("testdb", "testlogin")`, **Then** `GetDatabaseOwner("testdb")` retourne `testlogin`.
- [ ] **AC-9.2.5** — **Given** un appel à `DropDatabase("testdb")`, **Then** `DatabaseExists("testdb")` retourne `false`.
- [ ] **AC-9.2.6** — **Given** un appel à `DropDatabase("nonexistent")`, **Then** aucune erreur n'est retournée (idempotent).
- [ ] **AC-9.2.7** — **Given** un appel à `CreateLogin("testlogin", "P@ssw0rd123")`, **When** on appelle `LoginExists("testlogin")`, **Then** le retour est `true`.
- [ ] **AC-9.2.8** — **Given** un login existant, **When** on appelle `UpdateLoginPassword("testlogin", "NewP@ss456")`, **Then** la connexion avec le nouveau mot de passe réussit.
- [ ] **AC-9.2.9** — **Given** un login existant, **When** on appelle `AddLoginToServerRole("testlogin", "dbcreator")` puis `GetLoginServerRoles("testlogin")`, **Then** la liste contient `dbcreator`.
- [ ] **AC-9.2.10** — **Given** un login avec le rôle `dbcreator`, **When** on appelle `RemoveLoginFromServerRole("testlogin", "dbcreator")`, **Then** `GetLoginServerRoles("testlogin")` ne contient plus `dbcreator`.
- [ ] **AC-9.2.10b** — **Given** un login existant, **When** on appelle `SetLoginDefaultDatabase("testlogin", "testdb")` puis `GetLoginDefaultDatabase("testlogin")`, **Then** le retour est `testdb`.
- [ ] **AC-9.2.11** — **Given** un appel à `DropLogin("nonexistent")`, **Then** aucune erreur (idempotent).
- [ ] **AC-9.2.12** — **Given** une base et un login existants, **When** on appelle `CreateUser("testdb", "testuser", "testlogin")`, **Then** `UserExists("testdb", "testuser")` retourne `true`.
- [ ] **AC-9.2.13** — **Given** un utilisateur existant, **When** on appelle `AddUserToDatabaseRole("testdb", "testuser", "db_datareader")`, **Then** `GetUserDatabaseRoles("testdb", "testuser")` contient `db_datareader`.
- [ ] **AC-9.2.13b** — **Given** un utilisateur membre de `db_datareader`, **When** on appelle `RemoveUserFromDatabaseRole("testdb", "testuser", "db_datareader")`, **Then** `GetUserDatabaseRoles("testdb", "testuser")` ne contient plus `db_datareader`.
- [ ] **AC-9.2.14** — **Given** un utilisateur sans objets, **When** on appelle `DropUser("testdb", "testuser")`, **Then** `UserExists("testdb", "testuser")` retourne `false`.
- [ ] **AC-9.2.15** — **Given** un utilisateur qui possède un schéma, **When** on appelle `UserOwnsObjects("testdb", "testuser")`, **Then** le retour est `true`.
- [ ] **AC-9.2.16** — **Given** un login associé à un user dans une base, **When** on appelle `LoginHasUsers("testlogin")`, **Then** le retour est `true`.
- [ ] **AC-9.2.17** — **Given** un identifier avec des caractères spéciaux (`test]db`, `test[db`, `test"db`), **When** on crée une base avec ce nom, **Then** la base est créée sans injection SQL et le nom est correctement échappé.
- [ ] **AC-9.2.18** — **Given** le container SQL Server arrêté, **When** on appelle n'importe quelle méthode du client, **Then** une erreur de connexion est retournée (pas de panic ni hang).

### 9.3 Tests d'intégration — Contrôleurs (envtest) [P1]

- [ ] **AC-9.3.1** — L'environnement envtest démarre un API Server + etcd, installe les CRDs, et enregistre les 3 contrôleurs avec un mock SQL client.

#### Database (envtest)

- [ ] **AC-9.3.2** — **Given** un Secret SA et un mock SQL configuré, **When** je crée une CR `Database` dans envtest, **Then** après réconciliation le status contient `Ready=True` et `ObservedGeneration == metadata.generation`.
- [ ] **AC-9.3.3** — **Given** une CR `Database` déjà `Ready`, **When** je modifie `spec.owner`, **Then** la génération incrémente, le contrôleur réconcilie, et le mock reçoit un appel `SetDatabaseOwner`.
- [ ] **AC-9.3.4** — **Given** une CR `Database` avec finalizer, **When** je la supprime (`kubectl delete`), **Then** le mock reçoit un appel `DropDatabase` (si deletionPolicy=Delete) et la CR disparaît de l'API Server.
- [ ] **AC-9.3.5** — **Given** un mock SQL qui retourne une erreur de connexion, **When** le contrôleur réconcilie, **Then** le status contient `Ready=False, Reason=ConnectionFailed` et un event `Warning` est enregistré.
- [ ] **AC-9.3.6** — **Given** le Secret `credentialsSecret` qui n'existe pas, **When** le contrôleur réconcilie, **Then** le status contient `Ready=False, Reason=SecretNotFound`.

#### Login (envtest)

- [ ] **AC-9.3.7** — **Given** un Secret SA et un Secret password, **When** je crée une CR `Login`, **Then** le mock reçoit `CreateLogin` et le status passe à `Ready=True`.
- [ ] **AC-9.3.8** — **Given** une CR `Login` `Ready`, **When** je modifie le contenu du Secret password (nouvelle ResourceVersion), **Then** le contrôleur détecte le changement et appelle `UpdateLoginPassword` sur le mock.
- [ ] **AC-9.3.9** — **Given** une CR `Login` avec `serverRoles: [dbcreator]`, **When** je modifie pour `[dbcreator, securityadmin]`, **Then** le mock reçoit `AddLoginToServerRole("securityadmin")` sans toucher `dbcreator`.
- [ ] **AC-9.3.10** — **Given** un mock qui retourne `LoginHasUsers=true`, **When** je supprime la CR `Login` avec `deletionPolicy: Delete`, **Then** le status contient `Ready=False, Reason=LoginInUse` et le finalizer n'est PAS retiré.

#### DatabaseUser (envtest)

- [ ] **AC-9.3.11** — **Given** une CR `Login` existante en `Ready`, **When** je crée une CR `DatabaseUser` référençant ce Login, **Then** le mock reçoit `CreateUser` et le status passe à `Ready=True`.
- [ ] **AC-9.3.12** — **Given** une CR `DatabaseUser` avec `loginRef` vers un Login inexistant, **When** le contrôleur réconcilie, **Then** le status contient `Ready=False, Reason=LoginRefNotFound`.
- [ ] **AC-9.3.13** — **Given** une CR `DatabaseUser` en `LoginRefNotFound`, **When** je crée la CR `Login` référencée, **Then** le contrôleur reconcilie le DatabaseUser et le status passe à `Ready=True`.
- [ ] **AC-9.3.14** — **Given** un mock qui retourne `UserOwnsObjects=true`, **When** je supprime la CR `DatabaseUser`, **Then** le status contient `Ready=False, Reason=UserOwnsObjects` et le finalizer n'est PAS retiré.

#### Transverse (envtest)

- [ ] **AC-9.3.15** — **Given** une CR déjà `Ready` sans changement, **When** le contrôleur réconcilie une seconde fois, **Then** le mock ne reçoit aucun appel de mutation (idempotence).
- [ ] **AC-9.3.16** — Les events Kubernetes (`Normal` et `Warning`) sont visibles via `client.List(&corev1.EventList{})` après chaque action significative.

### 9.4 Tests d'intégration — Full stack (testcontainers + envtest) [P1]

- [ ] **AC-9.4.1** — Un test combine envtest (API Server réel) + testcontainers (SQL Server réel) pour valider le cycle complet : créer une CR Database → vérifier la base sur SQL Server → supprimer la CR → vérifier que la base est supprimée.
- [ ] **AC-9.4.2** — Même test full-stack pour Login : créer → vérifier le login sur SQL Server → rotation du mot de passe → vérifier la connexion avec le nouveau mot de passe → supprimer.
- [ ] **AC-9.4.3** — Même test full-stack pour DatabaseUser : créer Database + Login + DatabaseUser → vérifier l'utilisateur et les rôles sur SQL Server → modifier les rôles → vérifier → supprimer dans l'ordre inverse.
- [ ] **AC-9.4.4** — **Given** le container SQL Server qui est arrêté puis redémarré, **When** le contrôleur réconcilie, **Then** toutes les CRs convergent vers `Ready=True` après le redémarrage.

### 9.5 Tests E2E [P1]

- [ ] **AC-9.5.1** — Un test E2E déploie l'opérateur via Helm dans un cluster kind, crée une Database + Login + DatabaseUser, vérifie l'état sur SQL Server, puis supprime tout et vérifie le cleanup.

---

## Résumé

| Catégorie | Nombre de critères |
|---|---|
| Infrastructure & CI | 11 |
| CRD Database | 20 |
| CRD Login | 14 |
| CRD DatabaseUser | 11 |
| Comportements transverses | 14 |
| Observabilité | 8 |
| Haute disponibilité | 4 |
| Résilience | 3 |
| Tests — Couverture | 2 |
| Tests — Intégration SQL (testcontainers) | 20 |
| Tests — Intégration Contrôleurs (envtest) | 16 |
| Tests — Full stack (envtest + testcontainers) | 4 |
| Tests — E2E | 1 |
| **Total** | **128** |
