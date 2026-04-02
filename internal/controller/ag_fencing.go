package controller

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	v1alpha1 "github.com/popul/mssql-k8s-operator/api/v1alpha1"
	opmetrics "github.com/popul/mssql-k8s-operator/internal/metrics"
)

const maxFencingAttempts = 5

// collectReplicaRoles connects to each replica and returns a map of serverName -> role.
// Unreachable replicas are mapped to "" (empty string).
func (r *AvailabilityGroupReconciler) collectReplicaRoles(
	ctx context.Context, ag *v1alpha1.AvailabilityGroup,
) (map[string]string, error) {
	roles := make(map[string]string, len(ag.Spec.Replicas))

	for _, replica := range ag.Spec.Replicas {
		username, password, err := getCredentialsFromSecret(ctx, r.Client, ag.Namespace, replica.Server.CredentialsSecret.Name)
		if err != nil {
			roles[replica.ServerName] = ""
			continue
		}

		conn, err := connectToSQL(replica.Server, username, password, r.SQLClientFactory)
		if err != nil {
			roles[replica.ServerName] = ""
			continue
		}

		sqlCtx, cancel := sqlContext(ctx)
		role, err := conn.GetAGReplicaRole(sqlCtx, ag.Spec.AGName, replica.ServerName)
		cancel()
		conn.Close()

		if err != nil {
			roles[replica.ServerName] = ""
			continue
		}
		roles[replica.ServerName] = role
	}

	return roles, nil
}

// collectReplicaLSNs returns the last hardened LSN for each candidate replica.
// On error, LSN defaults to 0 (penalizes the replica in tiebreaker).
func (r *AvailabilityGroupReconciler) collectReplicaLSNs(
	ctx context.Context, ag *v1alpha1.AvailabilityGroup,
	candidates []v1alpha1.AGReplicaSpec,
) (map[string]int64, error) {
	lsns := make(map[string]int64, len(candidates))

	for _, replica := range candidates {
		username, password, err := getCredentialsFromSecret(ctx, r.Client, ag.Namespace, replica.Server.CredentialsSecret.Name)
		if err != nil {
			lsns[replica.ServerName] = 0
			continue
		}

		conn, err := connectToSQL(replica.Server, username, password, r.SQLClientFactory)
		if err != nil {
			lsns[replica.ServerName] = 0
			continue
		}

		sqlCtx, cancel := sqlContext(ctx)
		lsn, err := conn.GetLastHardenedLSN(sqlCtx, ag.Spec.AGName)
		cancel()
		conn.Close()

		if err != nil {
			lsns[replica.ServerName] = 0
			continue
		}
		lsns[replica.ServerName] = lsn
	}

	return lsns, nil
}

// detectAndResolveSplitBrain checks all replicas for role conflicts and resolves them.
// Returns true if fencing was performed (caller should return early and requeue).
func (r *AvailabilityGroupReconciler) detectAndResolveSplitBrain(
	ctx context.Context, ag *v1alpha1.AvailabilityGroup,
) (fenced bool, err error) {
	logger := log.FromContext(ctx)

	// Guard 1: no primary known yet (first deployment)
	if ag.Status.PrimaryReplica == "" {
		return false, nil
	}

	// Guard 2: fencing only applies to CLUSTER_TYPE=NONE
	if ag.Spec.ClusterType != nil && *ag.Spec.ClusterType != "None" {
		return false, nil
	}

	// Guard 3: skip if an AGFailover CR is running
	var failovers v1alpha1.AGFailoverList
	if err := r.List(ctx, &failovers, client.InNamespace(ag.Namespace)); err == nil {
		for i := range failovers.Items {
			if failovers.Items[i].Spec.AGName == ag.Spec.AGName &&
				failovers.Items[i].Status.Phase == v1alpha1.FailoverPhaseRunning {
				return false, nil
			}
		}
	}

	// Step 5: collect actual roles from each replica
	roles, err := r.collectReplicaRoles(ctx, ag)
	if err != nil {
		return false, fmt.Errorf("failed to collect replica roles: %w", err)
	}

	// Step 6: identify primaries (exclude unreachable replicas)
	var primaries []v1alpha1.AGReplicaSpec
	for _, replica := range ag.Spec.Replicas {
		if roles[replica.ServerName] == "PRIMARY" {
			primaries = append(primaries, replica)
		}
	}

	// Step 7: no primary found → nothing to do
	if len(primaries) == 0 {
		return false, nil
	}

	// Step 8: single primary
	if len(primaries) == 1 {
		if primaries[0].ServerName == ag.Status.PrimaryReplica {
			return false, nil // all good
		}
		// Status stale — correct in-place (no Status().Patch here, caller handles it)
		logger.Info("primary changed externally, correcting status",
			"old", ag.Status.PrimaryReplica, "new", primaries[0].ServerName)
		ag.Status.PrimaryReplica = primaries[0].ServerName
		r.updateReplicaRoleLabelsFromAG(ctx, ag, primaries[0].ServerName)
		r.Recorder.Event(ag, corev1.EventTypeNormal, v1alpha1.ReasonPrimaryChangedExternally,
			fmt.Sprintf("Primary changed externally to %s", primaries[0].ServerName))
		return false, nil
	}

	// Step 9: 2+ primaries → REAL SPLIT-BRAIN

	// Step 9b: circuit-breaker check per rogue
	cooldown := 60 * time.Second
	if ag.Spec.FailoverCooldown != nil {
		if d, err := time.ParseDuration(*ag.Spec.FailoverCooldown); err == nil {
			cooldown = d
		}
	}

	// Determine the legitimate primary via LSN tiebreaker
	lsns, _ := r.collectReplicaLSNs(ctx, ag, primaries)

	legitimate := primaries[0]
	highestLSN := lsns[primaries[0].ServerName]
	for _, p := range primaries[1:] {
		pLSN := lsns[p.ServerName]
		if pLSN > highestLSN {
			legitimate = p
			highestLSN = pLSN
		} else if pLSN == highestLSN && p.ServerName == ag.Status.PrimaryReplica {
			// Equal LSN → prefer the one matching current status
			legitimate = p
		}
	}

	// Build rogue list
	var rogues []v1alpha1.AGReplicaSpec
	allExhausted := true
	for _, p := range primaries {
		if p.ServerName == legitimate.ServerName {
			continue
		}
		// Check circuit-breaker for this specific rogue
		isExhausted := ag.Status.LastFencedReplica == p.ServerName &&
			ag.Status.ConsecutiveFencingCount >= maxFencingAttempts &&
			ag.Status.LastFencingTime != nil &&
			time.Since(ag.Status.LastFencingTime.Time) < cooldown
		if isExhausted {
			continue
		}
		allExhausted = false
		rogues = append(rogues, p)
	}

	if len(rogues) == 0 {
		if allExhausted {
			// All rogues exhausted — alert human
			patch := client.MergeFrom(ag.DeepCopy())
			meta.SetStatusCondition(&ag.Status.Conditions, metav1.Condition{
				Type:               v1alpha1.ConditionReady,
				Status:             metav1.ConditionFalse,
				Reason:             v1alpha1.ReasonFencingExhausted,
				Message:            "Fencing exhausted: split-brain persists, manual intervention required",
				ObservedGeneration: ag.Generation,
			})
			_ = r.Status().Patch(ctx, ag, patch)
		}
		return false, nil
	}

	r.Recorder.Event(ag, corev1.EventTypeWarning, v1alpha1.ReasonSplitBrainDetected,
		fmt.Sprintf("Split-brain detected: %d primaries, legitimate=%s", len(primaries), legitimate.ServerName))

	// Step 10: fence each rogue
	var lastRogue v1alpha1.AGReplicaSpec
	for _, rogue := range rogues {
		lastRogue = rogue

		// 10a: immediately remove primary label (cut traffic before SQL fencing)
		r.setPodRoleLabel(ctx, ag.Namespace, rogue.ServerName, RoleSecondary)

		// 10b: determine soft vs hard
		hard := ag.Status.LastFencedReplica == rogue.ServerName &&
			ag.Status.LastFencingTime != nil &&
			time.Since(ag.Status.LastFencingTime.Time) < cooldown

		// 10c: execute fencing
		if fenceErr := r.fenceReplica(ctx, ag, rogue, hard); fenceErr != nil {
			logger.Error(fenceErr, "fencing failed (label already removed)", "replica", rogue.ServerName, "hard", hard)
			r.Recorder.Event(ag, corev1.EventTypeWarning, v1alpha1.ReasonFencingFailed,
				fmt.Sprintf("Fencing %s failed: %v (label removed, traffic cut)", rogue.ServerName, fenceErr))
		}
	}

	// Step 11: patch status (single Status().Patch call)
	patch := client.MergeFrom(ag.DeepCopy())
	ag.Status.PrimaryReplica = legitimate.ServerName
	now := metav1.Now()
	ag.Status.LastFencingTime = &now
	ag.Status.FencingCount++
	if ag.Status.LastFencedReplica == lastRogue.ServerName {
		ag.Status.ConsecutiveFencingCount++
	} else {
		ag.Status.ConsecutiveFencingCount = 1
	}
	ag.Status.LastFencedReplica = lastRogue.ServerName

	reason := v1alpha1.ReasonFencingExecuted
	// Check if any hard fencing was done
	for _, rogue := range rogues {
		if ag.Status.LastFencedReplica == rogue.ServerName &&
			ag.Status.LastFencingTime != nil {
			// If this was a hard fence, use the hard reason
			// (simplified: use hard reason if consecutive count > 1)
			if ag.Status.ConsecutiveFencingCount > 1 {
				reason = v1alpha1.ReasonHardFencingExecuted
			}
		}
	}

	meta.SetStatusCondition(&ag.Status.Conditions, metav1.Condition{
		Type:               v1alpha1.ConditionReady,
		Status:             metav1.ConditionFalse,
		Reason:             reason,
		Message:            fmt.Sprintf("Fenced rogue primary, legitimate primary is %s", legitimate.ServerName),
		ObservedGeneration: ag.Generation,
	})
	_ = r.Status().Patch(ctx, ag, patch)

	return true, nil
}

// fenceReplica executes soft or hard fencing on a single replica.
func (r *AvailabilityGroupReconciler) fenceReplica(
	ctx context.Context, ag *v1alpha1.AvailabilityGroup,
	replica v1alpha1.AGReplicaSpec, hard bool,
) error {
	logger := log.FromContext(ctx)

	username, password, err := getCredentialsFromSecret(ctx, r.Client, ag.Namespace, replica.Server.CredentialsSecret.Name)
	if err != nil {
		return fmt.Errorf("failed to get credentials for %s: %w", replica.ServerName, err)
	}

	conn, err := connectToSQL(replica.Server, username, password, r.SQLClientFactory)
	if err != nil {
		return fmt.Errorf("failed to connect to %s: %w", replica.ServerName, err)
	}
	defer conn.Close()

	sqlCtx, cancel := sqlContext(ctx)
	defer cancel()

	fenceType := "soft"
	if hard {
		fenceType = "hard"
		err = conn.DropAG(sqlCtx, ag.Spec.AGName)
		if err == nil {
			r.Recorder.Event(ag, corev1.EventTypeWarning, v1alpha1.ReasonHardFencingExecuted,
				fmt.Sprintf("Hard fencing: dropped AG on %s", replica.ServerName))
		}
	} else {
		err = conn.SetAGRoleSecondary(sqlCtx, ag.Spec.AGName)
		if err == nil {
			r.Recorder.Event(ag, corev1.EventTypeNormal, v1alpha1.ReasonFencingExecuted,
				fmt.Sprintf("Soft fencing: set %s to SECONDARY", replica.ServerName))
		}
	}

	opmetrics.FencingTotal.WithLabelValues(ag.Spec.AGName, ag.Namespace, replica.ServerName, fenceType).Inc()
	logger.Info("fencing executed", "replica", replica.ServerName, "type", fenceType, "error", err)

	return err
}

// setPodRoleLabel patches a single pod's role label.
func (r *AvailabilityGroupReconciler) setPodRoleLabel(ctx context.Context, namespace, podName, role string) {
	var pod corev1.Pod
	if err := r.Get(ctx, client.ObjectKey{Name: podName, Namespace: namespace}, &pod); err != nil {
		return
	}
	if pod.Labels[LabelRole] == role {
		return
	}
	patch := client.MergeFrom(pod.DeepCopy())
	if pod.Labels == nil {
		pod.Labels = make(map[string]string)
	}
	pod.Labels[LabelRole] = role
	_ = r.Patch(ctx, &pod, patch)
}

// tryRejoinReplica attempts to rejoin a disconnected or orphaned replica to the AG.
func (r *AvailabilityGroupReconciler) tryRejoinReplica(
	ctx context.Context, ag *v1alpha1.AvailabilityGroup,
	replica v1alpha1.AGReplicaSpec,
) {
	logger := log.FromContext(ctx)

	username, password, err := getCredentialsFromSecret(ctx, r.Client, ag.Namespace, replica.Server.CredentialsSecret.Name)
	if err != nil {
		return
	}

	conn, err := connectToSQL(replica.Server, username, password, r.SQLClientFactory)
	if err != nil {
		return
	}
	defer conn.Close()

	clusterType := "EXTERNAL"
	if ag.Spec.ClusterType != nil {
		clusterType = mapClusterType(*ag.Spec.ClusterType)
	}

	sqlCtx, cancel := sqlContext(ctx)
	defer cancel()
	err = conn.JoinAG(sqlCtx, ag.Spec.AGName, clusterType)
	if err != nil {
		logger.V(1).Info("rejoin attempt", "replica", replica.ServerName, "result", err)
		return
	}

	r.Recorder.Event(ag, corev1.EventTypeNormal, "ReplicaRejoined",
		fmt.Sprintf("Replica %s rejoined AG %s", replica.ServerName, ag.Spec.AGName))
	logger.Info("disconnected replica rejoined", "replica", replica.ServerName)
}
