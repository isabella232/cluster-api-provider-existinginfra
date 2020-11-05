/*
Copyright 2020 Weaveworks.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controllers

import (
	"context"
	"errors"

	"github.com/go-logr/logr"
	gerrors "github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	clusterweaveworksv1alpha3 "github.com/weaveworks/cluster-api-provider-existinginfra/apis/cluster.weave.works/v1alpha3"
	corev1 "k8s.io/api/core/v1"
	apierrs "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/cluster-api/util"
	"sigs.k8s.io/cluster-api/util/patch"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
)

const (
	LocalController = "wks.weave.works/local-controller"
	Creating        = "wks.weave.works/is-creating"
)

// ExistingInfraClusterReconciler reconciles a ExistingInfraCluster object
type ExistingInfraClusterReconciler struct {
	client.Client
	Log           logr.Logger
	Scheme        *runtime.Scheme
	eventRecorder record.EventRecorder
}

// +kubebuilder:rbac:groups=cluster.weave.works,resources=existinginfraclusters,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=cluster.weave.works,resources=existinginfraclusters/status,verbs=get;update;patch

func (r *ExistingInfraClusterReconciler) Reconcile(req ctrl.Request) (_ ctrl.Result, reterr error) {
	ctx := context.TODO() // upstream will add this eventually
	contextLog := log.WithField("name", req.NamespacedName)

	// request only contains the name of the object, so fetch it from the api-server
	eic := &clusterweaveworksv1alpha3.ExistingInfraCluster{}
	err := r.Get(ctx, req.NamespacedName, eic)
	if err != nil {
		if apierrs.IsNotFound(err) { // isn't there; give in
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if _, found := eic.Annotations[LocalController]; !found {
		if _, found = eic.Annotations[Creating]; !found {
			if err := r.setClusterAnnotation(ctx, eic, Creating, "true"); err != nil {
				return ctrl.Result{}, err
			}
			if err := r.setupGitDir(eic); err != nil {
				return ctrl.Result{}, err
			}
			if err := r.setupInitialWorkloadCluster(eic); err != nil {
				return ctrl.Result{}, err
			}
		}
	}

	// Get Cluster via OwnerReferences
	cluster, err := util.GetOwnerCluster(ctx, r, eic.ObjectMeta)
	if err != nil {
		return ctrl.Result{}, err
	}
	if cluster == nil {
		contextLog.Info("Cluster Controller has not yet set ownerReferences")
		return ctrl.Result{}, nil
	}
	contextLog = contextLog.WithField("cluster", cluster.Name)

	if util.IsPaused(cluster, eic) {
		contextLog.Info("ExistingInfraCluster or linked Cluster is marked as paused. Won't reconcile")
		return ctrl.Result{}, nil
	}

	// Initialize the patch helper
	patchHelper, err := patch.NewHelper(eic, r)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Attempt to Patch the ExistingInfraMachine object and status after each reconciliation.
	defer func() {
		if err := patchHelper.Patch(ctx, eic); err != nil {
			contextLog.Errorf("failed to patch ExistingInfraCluster: %v", err)
			if reterr == nil {
				reterr = err
			}
		}
	}()

	// Object still there but with deletion timestamp => run our finalizer
	if !eic.ObjectMeta.DeletionTimestamp.IsZero() {
		r.recordEvent(cluster, corev1.EventTypeNormal, "Delete", "Deleted cluster %v", cluster.Name)
		return ctrl.Result{}, errors.New("ClusterReconciler#Delete not implemented")
	}

	eic.Status.Ready = true // TODO: know whether it is really ready

	return ctrl.Result{}, nil
}

func (r *ExistingInfraClusterReconciler) setupGitDir(eic *clusterweaveworksv1alpha3.ExistingInfraCluster) error {
	// XXX
	return nil
}

func (r *ExistingInfraClusterReconciler) setupInitialWorkloadCluster(eic *clusterweaveworksv1alpha3.ExistingInfraCluster) error {
	// XXX
	return nil
}

func (r *ExistingInfraClusterReconciler) newBuilderWithMgr(mgr ctrl.Manager) *builder.Builder {
	return ctrl.NewControllerManagedBy(mgr).
		For(&clusterweaveworksv1alpha3.ExistingInfraCluster{}).
		WithEventFilter(pausedPredicates())
}

func (r *ExistingInfraClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return r.newBuilderWithMgr(mgr).Complete(r)
}

func (r *ExistingInfraClusterReconciler) SetupWithManagerOptions(mgr ctrl.Manager, options controller.Options) error {
	return r.newBuilderWithMgr(mgr).WithOptions(options).Complete(r)
}

func (a *ExistingInfraClusterReconciler) setClusterAnnotation(ctx context.Context, eic *clusterweaveworksv1alpha3.ExistingInfraCluster, key, value string) error {
	err := a.modifyCluster(ctx, eic, func(node *clusterweaveworksv1alpha3.ExistingInfraCluster) {
		eic.Annotations[key] = value
	})
	if err != nil {
		return gerrors.Wrapf(err, "Failed to set annotation: %s for cluster: %s", key, eic.Name)
	}
	return nil
}

func (a *ExistingInfraClusterReconciler) modifyCluster(ctx context.Context, eic *clusterweaveworksv1alpha3.ExistingInfraCluster, updater func(*clusterweaveworksv1alpha3.ExistingInfraCluster)) error {
	contextLog := log.WithFields(log.Fields{"cluster": eic.Name})
	retryErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var result clusterweaveworksv1alpha3.ExistingInfraCluster
		getErr := a.Client.Get(ctx, client.ObjectKey{Name: eic.Name}, &result)
		if getErr != nil {
			contextLog.Errorf("failed to read cluster info, assuming unsafe to update: %v", getErr)
			return getErr
		}
		updater(&result)
		updateErr := a.Client.Update(ctx, &result)
		if updateErr != nil {
			contextLog.Errorf("failed attempt to update cluster annotation: %v", updateErr)
			return updateErr
		}
		return nil
	})
	if retryErr != nil {
		contextLog.Errorf("failed to update cluster annotation: %v", retryErr)
		return gerrors.Wrapf(retryErr, "Could not mark cluster %s as updated", eic.Name)
	}
	return nil
}

func (r *ExistingInfraClusterReconciler) recordEvent(object runtime.Object, eventType, reason, messageFmt string, args ...interface{}) {
	r.eventRecorder.Eventf(object, eventType, reason, messageFmt, args...)
	switch eventType {
	case corev1.EventTypeWarning:
		log.Warnf(messageFmt, args...)
	case corev1.EventTypeNormal:
		log.Infof(messageFmt, args...)
	default:
		log.Debugf(messageFmt, args...)
	}
}

func setupInitialWorkloadCluster(eic *clusterweaveworksv1alpha3.ExistingInfraCluster) {

}
