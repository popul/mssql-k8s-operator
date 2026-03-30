package v1alpha1

// This file documents the conversion strategy for the mssql.popul.io API.
//
// Current version: v1alpha1 (experimental)
// Next version: v1beta1 (planned)
//
// API Graduation Plan:
//
//   v1alpha1 → v1beta1:
//     - ServerReference.sqlServerRef becomes the recommended pattern
//     - Inline ServerReference fields (host/port/credentialsSecret) are deprecated
//       but remain functional for backward compatibility
//     - BackupType, RestorePhase, etc. remain unchanged
//     - New fields may be added; no existing fields will be removed or renamed
//
//   v1beta1 → v1:
//     - Deprecated inline ServerReference fields removed
//     - API considered stable; no breaking changes without a new version
//
// Conversion Strategy:
//
// The operator will use a conversion webhook served by the operator pod itself.
// When v1beta1 is introduced:
//   1. The "hub" version will be v1beta1 (internal storage version)
//   2. v1alpha1 types will implement ConvertTo/ConvertFrom methods
//   3. The webhook config will be deployed via cert-manager certificates
//
// The conversion functions are implemented as methods on the v1alpha1 types:
//
//   func (src *Database) ConvertTo(dstRaw conversion.Hub) error
//   func (dst *Database) ConvertFrom(srcRaw conversion.Hub) error
//
// These will be added when v1beta1 types are introduced. This file serves as
// documentation of the plan and a placeholder for future conversion logic.
//
// Migration Guide for Users:
//
//   1. v1alpha1 CRs will continue to work after upgrade to v1beta1
//   2. The conversion webhook will handle all transformations transparently
//   3. Users should migrate to v1beta1 before v1alpha1 is removed (2 releases later)
//   4. Use `kubectl convert` or `kubectl apply` with v1beta1 manifests to upgrade
//
// See: https://book.kubebuilder.io/multiversion-tutorial/conversion
