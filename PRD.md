# PRD — MSSQL Kubernetes Operator

## 1. Vue d'ensemble

### 1.1 Objectif

Développer un opérateur Kubernetes permettant de gérer le cycle de vie complet d'instances Microsoft SQL Server et de leurs objets (bases de données, logins, utilisateurs, permissions) de manière déclarative via des Custom Resources (CR).

### 1.2 Problème adressé

Aujourd'hui, la gestion de SQL Server dans Kubernetes repose sur des scripts manuels, des jobs Helm/Init ou des outils externes. Il n'existe pas de solution native Kubernetes permettant de :

- Déclarer l'état souhaité d'une base de données SQL Server et laisser un contrôleur le réconcilier
- Gérer les credentials de manière sécurisée via les Secrets Kubernetes
- Avoir une boucle de réconciliation continue garantissant la cohérence entre l'état déclaré et l'état réel

### 1.3 Utilisateurs cibles

| Persona | Besoin principal |
|---|---|
| **Platform Engineer** | Exposer SQL Server en self-service aux équipes via des CRDs |
| **DBA / Ops** | Gérer les objets SQL Server via GitOps (ArgoCD, Flux) |
| **Développeur** | Provisionner rapidement une base de données pour un environnement de dev/test |

---

## 2. Périmètre fonctionnel

### 2.1 Phase 1 — MVP

#### CRD `Database`

Gestion déclarative des bases de données sur une instance SQL Server existante.

```yaml
apiVersion: mssql.popul.io/v1alpha1
kind: Database
metadata:
  name: myapp-db
  namespace: default
spec:
  server:
    host: mssql.database.svc.cluster.local
    port: 1433
    credentialsSecret:
      name: mssql-sa-credentials    # Secret contenant les clés `username` et `password`
  databaseName: myapp
  collation: SQL_Latin1_General_CP1_CI_AS   # optionnel, défaut SQL Server
  owner: myapp_user                          # optionnel
```

Fonctionnalités :

- Création de la base de données si elle n'existe pas
- Mise à jour des propriétés modifiables (owner, collation si vide)
- Suppression de la base lorsque la CR est supprimée (configurable via `spec.deletionPolicy: Delete | Retain`)
- Status conditions (`Ready`, `Error`) avec messages détaillés
- Finalizer pour contrôler la suppression

#### CRD `Login`

Gestion des logins SQL Server (authentification SQL).

```yaml
apiVersion: mssql.popul.io/v1alpha1
kind: Login
metadata:
  name: myapp-login
spec:
  server:
    host: mssql.database.svc.cluster.local
    port: 1433
    credentialsSecret:
      name: mssql-sa-credentials
  loginName: myapp_user
  passwordSecret:
    name: myapp-login-password      # Secret contenant la clé `password`
  defaultDatabase: myapp            # optionnel
  serverRoles:                      # optionnel
    - dbcreator
```

Fonctionnalités :

- Création / mise à jour / suppression du login
- Rotation du mot de passe lors de la mise à jour du Secret référencé
- Gestion des server roles
- `deletionPolicy: Delete | Retain`

#### CRD `DatabaseUser`

Gestion des utilisateurs au sein d'une base de données.

```yaml
apiVersion: mssql.popul.io/v1alpha1
kind: DatabaseUser
metadata:
  name: myapp-dbuser
spec:
  server:
    host: mssql.database.svc.cluster.local
    port: 1433
    credentialsSecret:
      name: mssql-sa-credentials
  databaseName: myapp
  userName: myapp_user
  loginRef:
    name: myapp-login               # référence à une CR Login
  databaseRoles:
    - db_datareader
    - db_datawriter
```

Fonctionnalités :

- Création / suppression de l'utilisateur dans la base cible
- Attribution des database roles
- Référence croisée vers la CR `Login` (avec validation)

### 2.2 Phase 2 — Fonctionnalités avancées

| Fonctionnalité | Description |
|---|---|
| **CRD `Permission`** | Gestion fine des permissions (GRANT / DENY / REVOKE) sur des objets spécifiques |
| **CRD `Schema`** | Création et gestion de schémas SQL |
| **CRD `AgentJob`** | Gestion déclarative des SQL Server Agent Jobs |
| **Multi-instance** | Support de `ServerRef` pour pointer vers différentes instances SQL Server |
| **Backup / Restore** | Déclenchement de backups et restores via CRDs |
| **Monitoring** | Exposition de métriques Prometheus (réconciliations, erreurs, latence) |

### 2.3 Hors périmètre

- Déploiement / gestion du cycle de vie de l'instance SQL Server elle-même (utiliser l'opérateur officiel Microsoft ou un StatefulSet)
- Support d'Azure SQL Database ou SQL Managed Instance (envisageable en phase ultérieure)
- Migration de schéma applicatif (utiliser Flyway, Liquibase, etc.)

---

## 3. Architecture technique

### 3.1 Stack technologique

| Composant | Choix |
|---|---|
| Langage | **Go** |
| Framework opérateur | **Kubebuilder** (controller-runtime) |
| Driver SQL Server | `github.com/microsoft/go-mssqldb` |
| Tests | Go testing + envtest (controller-runtime) + testcontainers (SQL Server) |
| CI/CD | GitHub Actions |
| Distribution | Image Docker + Helm chart |

### 3.2 Diagramme de haut niveau

```
┌──────────────────────────────────────────────┐
│                Kubernetes API                 │
│  ┌──────────┐ ┌───────┐ ┌──────────────┐    │
│  │ Database  │ │ Login │ │ DatabaseUser │    │
│  └────┬─────┘ └───┬───┘ └──────┬───────┘    │
│       │            │            │             │
│  ┌────▼────────────▼────────────▼──────────┐ │
│  │         mssql-k8s-operator              │ │
│  │  ┌────────────┐ ┌────────────────────┐  │ │
│  │  │ Controllers│ │ SQL Server Client   │  │ │
│  │  └──────┬─────┘ └─────────┬──────────┘  │ │
│  └─────────┼─────────────────┼─────────────┘ │
└────────────┼─────────────────┼───────────────┘
             │                 │
             │            ┌────▼──────────────┐
             │            │  SQL Server (TDS)  │
             │            │  Port 1433         │
             │            └───────────────────┘
             │
        ┌────▼───────────┐
        │ Kubernetes      │
        │ Secrets         │
        └────────────────┘
```

### 3.3 Boucle de réconciliation

Chaque contrôleur suit le pattern standard :

1. **Observer** — Recevoir l'événement (create/update/delete) sur la CR
2. **Comparer** — Lire l'état actuel sur SQL Server via requête T-SQL
3. **Agir** — Exécuter les commandes DDL/DCL nécessaires pour converger
4. **Rapporter** — Mettre à jour le `.status` de la CR avec les conditions

Intervalle de re-queue par défaut : **30 secondes** (configurable).

### 3.4 Gestion des erreurs

- Backoff exponentiel en cas d'erreur de connexion SQL Server
- Events Kubernetes émis pour chaque action significative
- Conditions de status normalisées (`Ready`, `Degraded`, `Error`)
- Les erreurs SQL transitoires (timeouts, connexion perdue) déclenchent un requeue, pas un échec définitif

### 3.5 Sécurité

- L'opérateur ne stocke jamais de credentials en clair — uniquement des références à des Secrets Kubernetes
- Support du chiffrement TLS pour la connexion à SQL Server
- RBAC minimal : l'opérateur n'a besoin que des permissions sur ses CRDs et les Secrets référencés
- Option pour utiliser un ServiceAccount SQL Server au lieu de SA

---

## 4. Exigences non fonctionnelles

| Exigence | Cible |
|---|---|
| **Disponibilité** | L'opérateur doit supporter le leader election pour le HA |
| **Performance** | Réconciliation < 5s pour les opérations courantes |
| **Idempotence** | Toute réconciliation doit être idempotente et safe à re-exécuter |
| **Observabilité** | Logs structurés (JSON), métriques Prometheus, events K8s |
| **Compatibilité** | SQL Server 2019+, Kubernetes 1.27+ |
| **Tests** | Couverture > 80% sur les contrôleurs, tests d'intégration avec SQL Server réel |

---

## 5. Plan de livraison

### Phase 1 — MVP (8 semaines)

| Semaine | Livrable |
|---|---|
| S1-S2 | Scaffolding Kubebuilder, CRDs, types API, validation webhooks |
| S3-S4 | Contrôleur `Database` + tests unitaires et d'intégration |
| S5-S6 | Contrôleurs `Login` et `DatabaseUser` + tests |
| S7 | Helm chart, documentation, CI/CD (build, test, push image) |
| S8 | Tests end-to-end, stabilisation, release v0.1.0 |

### Phase 2 — Hardening (4 semaines)

- Métriques Prometheus
- Leader election
- Tests de chaos (perte de connexion SQL, redémarrage de pods)
- Documentation utilisateur complète

### Phase 3 — Fonctionnalités avancées (backlog)

- CRDs supplémentaires (`Permission`, `Schema`, `AgentJob`)
- Backup / Restore
- Support multi-instance avancé

---

## 6. Critères de succès

| Critère | Mesure |
|---|---|
| Un utilisateur peut créer une DB via `kubectl apply` | Test E2E passant |
| La suppression de la CR supprime la DB (si `deletionPolicy: Delete`) | Test E2E passant |
| La rotation d'un mot de passe de login se propage en < 60s | Test d'intégration |
| L'opérateur récupère après une perte de connexion SQL Server | Test de résilience |
| Le Helm chart s'installe sans configuration manuelle | Test d'installation |

---

## 7. Risques et mitigations

| Risque | Impact | Mitigation |
|---|---|---|
| Suppression accidentelle d'une base de données | Critique | `deletionPolicy: Retain` par défaut |
| Credentials SA exposés | Élevé | Référence exclusive via Secrets K8s, RBAC strict |
| Drift entre état déclaré et état réel | Moyen | Réconciliation périodique + détection de drift dans le status |
| Incompatibilité entre versions SQL Server | Moyen | Matrice de compatibilité testée en CI |

---

## 8. Questions ouvertes

1. **Faut-il supporter Windows Authentication (Kerberos) dès la phase 1 ?** — Recommandation : non, le reporter en phase 2+.
2. **Doit-on gérer la création de Secrets (pour les mots de passe générés) ou uniquement les consommer ?** — Recommandation : uniquement consommer en phase 1, option de génération en phase 2.
3. **Quel niveau de granularité pour les permissions (CRD `Permission`) ?** — À définir lors de la conception de la phase 2.
4. **Faut-il supporter les bases de données contenues (contained databases) ?** — À évaluer.
