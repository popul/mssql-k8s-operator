package v1alpha1

import (
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

// Default implements webhook.Defaulter.
func (r *Schema) Default() {
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
func (r *Schema) ValidateCreate() ([]string, error) {
	return nil, r.validateSchema()
}

// ValidateUpdate implements webhook.Validator.
func (r *Schema) ValidateUpdate(old runtime.Object) ([]string, error) {
	oldSchema, ok := old.(*Schema)
	if !ok {
		return nil, fmt.Errorf("expected Schema, got %T", old)
	}

	var allErrs field.ErrorList

	// schemaName is immutable
	if r.Spec.SchemaName != oldSchema.Spec.SchemaName {
		allErrs = append(allErrs, field.Forbidden(
			field.NewPath("spec", "schemaName"),
			"schemaName is immutable after creation"))
	}

	// databaseName is immutable
	if r.Spec.DatabaseName != oldSchema.Spec.DatabaseName {
		allErrs = append(allErrs, field.Forbidden(
			field.NewPath("spec", "databaseName"),
			"databaseName is immutable after creation"))
	}

	// server reference is immutable
	if r.Spec.Server.Host != oldSchema.Spec.Server.Host {
		allErrs = append(allErrs, field.Forbidden(
			field.NewPath("spec", "server", "host"),
			"server host is immutable"))
	}

	if !int32PtrEqual(r.Spec.Server.Port, oldSchema.Spec.Server.Port) {
		allErrs = append(allErrs, field.Forbidden(
			field.NewPath("spec", "server", "port"),
			"server port is immutable"))
	}

	if !boolPtrEqual(r.Spec.Server.TLS, oldSchema.Spec.Server.TLS) {
		allErrs = append(allErrs, field.Forbidden(
			field.NewPath("spec", "server", "tls"),
			"server tls is immutable"))
	}

	if len(allErrs) > 0 {
		return nil, allErrs.ToAggregate()
	}
	return nil, r.validateSchema()
}

// ValidateDelete implements webhook.Validator.
func (r *Schema) ValidateDelete() ([]string, error) {
	return nil, nil
}

func (r *Schema) validateSchema() error {
	var allErrs field.ErrorList

	if r.Spec.SchemaName == "" {
		allErrs = append(allErrs, field.Required(
			field.NewPath("spec", "schemaName"),
			"schemaName is required"))
	}

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
