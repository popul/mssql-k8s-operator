package v1alpha1

import (
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

// Default implements webhook.Defaulter.
func (r *Restore) Default() {
	if r.Spec.Server.Port == nil {
		port := int32(1433)
		r.Spec.Server.Port = &port
	}
}

// ValidateCreate implements webhook.Validator.
func (r *Restore) ValidateCreate() ([]string, error) {
	return nil, r.validateRestore()
}

// ValidateUpdate implements webhook.Validator.
func (r *Restore) ValidateUpdate(old runtime.Object) ([]string, error) {
	oldRestore, ok := old.(*Restore)
	if !ok {
		return nil, fmt.Errorf("expected Restore, got %T", old)
	}

	var allErrs field.ErrorList

	// Spec is fully immutable — restore is a one-shot operation
	if r.Spec.DatabaseName != oldRestore.Spec.DatabaseName {
		allErrs = append(allErrs, field.Forbidden(
			field.NewPath("spec", "databaseName"),
			"spec is immutable after creation"))
	}
	if r.Spec.Source != oldRestore.Spec.Source {
		allErrs = append(allErrs, field.Forbidden(
			field.NewPath("spec", "source"),
			"spec is immutable after creation"))
	}
	if r.Spec.Server.Host != oldRestore.Spec.Server.Host {
		allErrs = append(allErrs, field.Forbidden(
			field.NewPath("spec", "server", "host"),
			"spec is immutable after creation"))
	}

	if len(allErrs) > 0 {
		return nil, allErrs.ToAggregate()
	}
	return nil, nil
}

// ValidateDelete implements webhook.Validator.
func (r *Restore) ValidateDelete() ([]string, error) {
	return nil, nil
}

func (r *Restore) validateRestore() error {
	var allErrs field.ErrorList

	if r.Spec.DatabaseName == "" {
		allErrs = append(allErrs, field.Required(
			field.NewPath("spec", "databaseName"),
			"databaseName is required"))
	}
	if r.Spec.Source == "" {
		allErrs = append(allErrs, field.Required(
			field.NewPath("spec", "source"),
			"source is required"))
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
