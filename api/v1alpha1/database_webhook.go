package v1alpha1

import (
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

// Default implements webhook.Defaulter.
func (r *Database) Default() {
	if r.Spec.DeletionPolicy == nil {
		policy := DeletionPolicyRetain
		r.Spec.DeletionPolicy = &policy
	}
	if r.Spec.Server.Port == nil {
		port := int32(1433)
		r.Spec.Server.Port = &port
	}
}

// ValidateCreate implements webhook.Validator.
func (r *Database) ValidateCreate() ([]string, error) {
	return nil, r.validateDatabase()
}

// ValidateUpdate implements webhook.Validator.
func (r *Database) ValidateUpdate(old runtime.Object) ([]string, error) {
	oldDB, ok := old.(*Database)
	if !ok {
		return nil, fmt.Errorf("expected Database, got %T", old)
	}

	var allErrs field.ErrorList

	// databaseName is immutable
	if r.Spec.DatabaseName != oldDB.Spec.DatabaseName {
		allErrs = append(allErrs, field.Forbidden(
			field.NewPath("spec", "databaseName"),
			"databaseName is immutable after creation"))
	}

	// collation is immutable
	if !stringPtrEqual(r.Spec.Collation, oldDB.Spec.Collation) {
		allErrs = append(allErrs, field.Forbidden(
			field.NewPath("spec", "collation"),
			"collation is immutable after creation"))
	}

	// server reference is immutable
	if r.Spec.Server.Host != oldDB.Spec.Server.Host {
		allErrs = append(allErrs, field.Forbidden(
			field.NewPath("spec", "server", "host"),
			"server host is immutable"))
	}

	if !int32PtrEqual(r.Spec.Server.Port, oldDB.Spec.Server.Port) {
		allErrs = append(allErrs, field.Forbidden(
			field.NewPath("spec", "server", "port"),
			"server port is immutable"))
	}

	if !boolPtrEqual(r.Spec.Server.TLS, oldDB.Spec.Server.TLS) {
		allErrs = append(allErrs, field.Forbidden(
			field.NewPath("spec", "server", "tls"),
			"server tls is immutable"))
	}

	if len(allErrs) > 0 {
		return nil, allErrs.ToAggregate()
	}
	return nil, r.validateDatabase()
}

// ValidateDelete implements webhook.Validator.
func (r *Database) ValidateDelete() ([]string, error) {
	return nil, nil
}

func (r *Database) validateDatabase() error {
	var allErrs field.ErrorList

	if r.Spec.DatabaseName == "" {
		allErrs = append(allErrs, field.Required(
			field.NewPath("spec", "databaseName"),
			"databaseName is required"))
	}

	if r.Spec.Server.Host == "" {
		allErrs = append(allErrs, field.Required(
			field.NewPath("spec", "server", "host"),
			"server host is required"))
	}

	if r.Spec.Server.CredentialsSecret.Name == "" {
		allErrs = append(allErrs, field.Required(
			field.NewPath("spec", "server", "credentialsSecret", "name"),
			"credentialsSecret name is required"))
	}

	if len(allErrs) > 0 {
		return allErrs.ToAggregate()
	}
	return nil
}

func stringPtrEqual(a, b *string) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

func int32PtrEqual(a, b *int32) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

func boolPtrEqual(a, b *bool) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}
