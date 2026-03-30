package v1alpha1

import (
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

// Default implements webhook.Defaulter.
func (r *AGFailover) Default() {
	if r.Spec.Force == nil {
		f := false
		r.Spec.Force = &f
	}
	if r.Spec.Server.Port == nil {
		port := int32(1433)
		r.Spec.Server.Port = &port
	}
}

// ValidateCreate implements webhook.Validator.
func (r *AGFailover) ValidateCreate() ([]string, error) {
	return nil, r.validateAGFailover()
}

// ValidateUpdate implements webhook.Validator.
func (r *AGFailover) ValidateUpdate(old runtime.Object) ([]string, error) {
	oldFO, ok := old.(*AGFailover)
	if !ok {
		return nil, fmt.Errorf("expected AGFailover, got %T", old)
	}

	var allErrs field.ErrorList

	// Spec is fully immutable (one-shot operation)
	if r.Spec.AGName != oldFO.Spec.AGName {
		allErrs = append(allErrs, field.Forbidden(
			field.NewPath("spec", "agName"),
			"agName is immutable after creation"))
	}
	if r.Spec.TargetReplica != oldFO.Spec.TargetReplica {
		allErrs = append(allErrs, field.Forbidden(
			field.NewPath("spec", "targetReplica"),
			"targetReplica is immutable after creation"))
	}
	if r.Spec.Server.Host != oldFO.Spec.Server.Host {
		allErrs = append(allErrs, field.Forbidden(
			field.NewPath("spec", "server", "host"),
			"server host is immutable after creation"))
	}
	if boolValue(r.Spec.Force) != boolValue(oldFO.Spec.Force) {
		allErrs = append(allErrs, field.Forbidden(
			field.NewPath("spec", "force"),
			"force is immutable after creation"))
	}

	if len(allErrs) > 0 {
		return nil, allErrs.ToAggregate()
	}
	return nil, nil
}

// ValidateDelete implements webhook.Validator.
func (r *AGFailover) ValidateDelete() ([]string, error) {
	return nil, nil
}

func (r *AGFailover) validateAGFailover() error {
	var allErrs field.ErrorList

	specPath := field.NewPath("spec")

	if r.Spec.AGName == "" {
		allErrs = append(allErrs, field.Required(
			specPath.Child("agName"),
			"agName is required"))
	}
	if r.Spec.TargetReplica == "" {
		allErrs = append(allErrs, field.Required(
			specPath.Child("targetReplica"),
			"targetReplica is required"))
	}
	if r.Spec.Server.Host == "" {
		allErrs = append(allErrs, field.Required(
			specPath.Child("server", "host"),
			"server host is required"))
	}
	if r.Spec.Server.CredentialsSecret.Name == "" {
		allErrs = append(allErrs, field.Required(
			specPath.Child("server", "credentialsSecret", "name"),
			"credentialsSecret name is required"))
	}

	// force=true requires the confirm-data-loss annotation
	if r.Spec.Force != nil && *r.Spec.Force {
		annotation := r.GetAnnotations()[ConfirmDataLossAnnotation]
		if annotation != "yes" {
			allErrs = append(allErrs, field.Required(
				field.NewPath("metadata", "annotations", ConfirmDataLossAnnotation),
				"force failover requires annotation mssql.popul.io/confirm-data-loss: \"yes\" to acknowledge potential data loss"))
		}
	}

	if len(allErrs) > 0 {
		return allErrs.ToAggregate()
	}
	return nil
}

func boolValue(b *bool) bool {
	if b == nil {
		return false
	}
	return *b
}
