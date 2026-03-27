package v1alpha1

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// --- Common Types ---

func TestDeletionPolicyConstants(t *testing.T) {
	if DeletionPolicyDelete != DeletionPolicy("Delete") {
		t.Errorf("expected DeletionPolicyDelete to be 'Delete', got %q", DeletionPolicyDelete)
	}
	if DeletionPolicyRetain != DeletionPolicy("Retain") {
		t.Errorf("expected DeletionPolicyRetain to be 'Retain', got %q", DeletionPolicyRetain)
	}
}

func TestConditionConstants(t *testing.T) {
	if ConditionReady != "Ready" {
		t.Errorf("expected ConditionReady to be 'Ready', got %q", ConditionReady)
	}
}

func TestReasonConstants(t *testing.T) {
	reasons := []string{
		ReasonReady,
		ReasonConnectionFailed,
		ReasonSecretNotFound,
		ReasonInvalidCredentialsSecret,
		ReasonImmutableFieldChanged,
		ReasonCollationChangeNotSupported,
		ReasonLoginInUse,
		ReasonLoginRefNotFound,
		ReasonLoginNotReady,
		ReasonUserOwnsObjects,
		ReasonInvalidServerRole,
		ReasonDatabaseProvisioning,
	}
	for _, r := range reasons {
		if r == "" {
			t.Error("reason constant should not be empty")
		}
	}
}

func TestServerReferenceDefaults(t *testing.T) {
	ref := ServerReference{
		Host: "localhost",
		CredentialsSecret: SecretReference{Name: "sa-creds"},
	}
	if ref.Host != "localhost" {
		t.Errorf("expected Host 'localhost', got %q", ref.Host)
	}
	if ref.Port != nil {
		t.Errorf("expected Port nil when not set, got %v", ref.Port)
	}
	if ref.TLS != nil {
		t.Errorf("expected TLS nil when not set, got %v", ref.TLS)
	}
}

// --- Database Types ---

func TestDatabaseSpecFields(t *testing.T) {
	collation := "SQL_Latin1_General_CP1_CI_AS"
	owner := "myuser"
	policy := DeletionPolicyRetain

	spec := DatabaseSpec{
		Server: ServerReference{
			Host:              "mssql.svc",
			CredentialsSecret: SecretReference{Name: "sa-secret"},
		},
		DatabaseName:   "mydb",
		Collation:      &collation,
		Owner:          &owner,
		DeletionPolicy: &policy,
	}

	if spec.DatabaseName != "mydb" {
		t.Errorf("expected DatabaseName 'mydb', got %q", spec.DatabaseName)
	}
	if *spec.Collation != collation {
		t.Errorf("expected Collation %q, got %q", collation, *spec.Collation)
	}
	if *spec.Owner != owner {
		t.Errorf("expected Owner %q, got %q", owner, *spec.Owner)
	}
	if *spec.DeletionPolicy != DeletionPolicyRetain {
		t.Errorf("expected DeletionPolicy Retain, got %q", *spec.DeletionPolicy)
	}
}

func TestDatabaseSpecOptionalFieldsNil(t *testing.T) {
	spec := DatabaseSpec{
		Server: ServerReference{
			Host:              "mssql.svc",
			CredentialsSecret: SecretReference{Name: "sa-secret"},
		},
		DatabaseName: "mydb",
	}

	if spec.Collation != nil {
		t.Error("Collation should be nil when not set")
	}
	if spec.Owner != nil {
		t.Error("Owner should be nil when not set")
	}
	if spec.DeletionPolicy != nil {
		t.Error("DeletionPolicy should be nil when not set")
	}
}

func TestDatabaseStatusConditions(t *testing.T) {
	status := DatabaseStatus{
		ObservedGeneration: 3,
		Conditions: []metav1.Condition{
			{
				Type:               ConditionReady,
				Status:             metav1.ConditionTrue,
				Reason:             ReasonReady,
				Message:            "Database is ready",
				ObservedGeneration: 3,
			},
		},
	}

	if status.ObservedGeneration != 3 {
		t.Errorf("expected ObservedGeneration 3, got %d", status.ObservedGeneration)
	}
	if len(status.Conditions) != 1 {
		t.Fatalf("expected 1 condition, got %d", len(status.Conditions))
	}
	if status.Conditions[0].Type != ConditionReady {
		t.Errorf("expected condition type %q, got %q", ConditionReady, status.Conditions[0].Type)
	}
}

func TestDatabaseObjectMeta(t *testing.T) {
	db := Database{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "mydb",
			Namespace: "default",
		},
		Spec: DatabaseSpec{
			DatabaseName: "mydb",
		},
	}

	if db.Name != "mydb" {
		t.Errorf("expected name 'mydb', got %q", db.Name)
	}
	if db.Namespace != "default" {
		t.Errorf("expected namespace 'default', got %q", db.Namespace)
	}
}

func TestDatabaseListItems(t *testing.T) {
	list := DatabaseList{
		Items: []Database{
			{ObjectMeta: metav1.ObjectMeta{Name: "db1"}},
			{ObjectMeta: metav1.ObjectMeta{Name: "db2"}},
		},
	}
	if len(list.Items) != 2 {
		t.Errorf("expected 2 items, got %d", len(list.Items))
	}
}

// --- Login Types ---

func TestLoginSpecFields(t *testing.T) {
	defaultDB := "master"
	policy := DeletionPolicyDelete

	spec := LoginSpec{
		Server: ServerReference{
			Host:              "mssql.svc",
			CredentialsSecret: SecretReference{Name: "sa-secret"},
		},
		LoginName:       "mylogin",
		PasswordSecret:  SecretReference{Name: "login-password"},
		DefaultDatabase: &defaultDB,
		ServerRoles:     []string{"dbcreator", "securityadmin"},
		DeletionPolicy:  &policy,
	}

	if spec.LoginName != "mylogin" {
		t.Errorf("expected LoginName 'mylogin', got %q", spec.LoginName)
	}
	if spec.PasswordSecret.Name != "login-password" {
		t.Errorf("expected PasswordSecret.Name 'login-password', got %q", spec.PasswordSecret.Name)
	}
	if *spec.DefaultDatabase != "master" {
		t.Errorf("expected DefaultDatabase 'master', got %q", *spec.DefaultDatabase)
	}
	if len(spec.ServerRoles) != 2 {
		t.Errorf("expected 2 server roles, got %d", len(spec.ServerRoles))
	}
	if *spec.DeletionPolicy != DeletionPolicyDelete {
		t.Errorf("expected DeletionPolicy Delete, got %q", *spec.DeletionPolicy)
	}
}

func TestLoginStatusPasswordTracking(t *testing.T) {
	status := LoginStatus{
		ObservedGeneration:            2,
		PasswordSecretResourceVersion: "12345",
	}

	if status.PasswordSecretResourceVersion != "12345" {
		t.Errorf("expected PasswordSecretResourceVersion '12345', got %q", status.PasswordSecretResourceVersion)
	}
}

func TestLoginObjectMeta(t *testing.T) {
	login := Login{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "mylogin",
			Namespace: "default",
		},
	}
	if login.Name != "mylogin" {
		t.Errorf("expected name 'mylogin', got %q", login.Name)
	}
}

// --- DatabaseUser Types ---

func TestDatabaseUserSpecFields(t *testing.T) {
	spec := DatabaseUserSpec{
		Server: ServerReference{
			Host:              "mssql.svc",
			CredentialsSecret: SecretReference{Name: "sa-secret"},
		},
		DatabaseName:  "mydb",
		UserName:      "myuser",
		LoginRef:      LoginReference{Name: "mylogin-cr"},
		DatabaseRoles: []string{"db_datareader", "db_datawriter"},
	}

	if spec.DatabaseName != "mydb" {
		t.Errorf("expected DatabaseName 'mydb', got %q", spec.DatabaseName)
	}
	if spec.UserName != "myuser" {
		t.Errorf("expected UserName 'myuser', got %q", spec.UserName)
	}
	if spec.LoginRef.Name != "mylogin-cr" {
		t.Errorf("expected LoginRef.Name 'mylogin-cr', got %q", spec.LoginRef.Name)
	}
	if len(spec.DatabaseRoles) != 2 {
		t.Errorf("expected 2 database roles, got %d", len(spec.DatabaseRoles))
	}
}

func TestDatabaseUserHasNoDeletionPolicy(t *testing.T) {
	// DatabaseUser has no DeletionPolicy field — always DROP USER on delete.
	// This test documents the design decision.
	spec := DatabaseUserSpec{
		DatabaseName: "mydb",
		UserName:     "myuser",
		LoginRef:     LoginReference{Name: "mylogin"},
	}
	_ = spec // compiles without DeletionPolicy field
}

func TestDatabaseUserObjectMeta(t *testing.T) {
	user := DatabaseUser{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "myuser",
			Namespace: "default",
		},
	}
	if user.Name != "myuser" {
		t.Errorf("expected name 'myuser', got %q", user.Name)
	}
}

// --- GroupVersion ---

func TestGroupVersion(t *testing.T) {
	if GroupVersion.Group != "mssql.popul.io" {
		t.Errorf("expected group 'mssql.popul.io', got %q", GroupVersion.Group)
	}
	if GroupVersion.Version != "v1alpha1" {
		t.Errorf("expected version 'v1alpha1', got %q", GroupVersion.Version)
	}
}
