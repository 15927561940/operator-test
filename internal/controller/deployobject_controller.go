/*
Copyright 2024.

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

package controller

import (
	"context"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	appv1 "my.domain/api/v1"
)

// DeployObjectReconciler reconciles a DeployObject object
type DeployObjectReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=app.my.domain,resources=deployobjects,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=app.my.domain,resources=deployobjects/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=app.my.domain,resources=deployobjects/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the DeployObject object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.19.0/pkg/reconcile
func (r *DeployObjectReconciler) Reconcile(req ctrl.Request) (ctrl.Result, error) {
	ctx := context.Background()
	log := r.Log.WithValues("deployobject", req.NamespacedName)

	// 1. 获取 NS.DeployObject 实例
	var deployObject apiv1.DeployObject
	if err := r.Get(ctx, req.NamespacedName, &deployObject); err != nil {
		log.Info("unable to fetch DeployObject")
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// 2. DeployObject 实例处于删除状态, 则退出
	if deployObject.DeletionTimestamp != nil {
		log.Info("DeployObject/%s is deleting", req.Name)
		return ctrl.Result{}, nil
	}

	// 3. 检测 NS 下是否有已创建的关联 Deployment
	var deployment appsv1.Deployment
	if err := r.Get(ctx, req.NamespacedName, &deployment); err != nil && apierrors.IsNotFound(err) {
		// create deployment
		deployment := newDeployment(&deployObject)
		if err := r.Create(ctx, deployment); err != nil {
			log.Error(err, "create deployment failed")
			return ctrl.Result{}, err
		}

		// create service
		service := newService(&deployObject)
		if err := r.Create(ctx, service); err != nil {
			log.Error(err, "create service failed")
			return ctrl.Result{}, err
		}

		// update DeployObject spec annotation
		setDeployObjectSpecAnnotation(&deployObject)
		if err := r.Update(ctx, &deployObject); err != nil {
			log.Error(err, "update deployobject failed")
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// 若 deployment 已存在, 则取出 DeployObject.Annotation 中之前的 DeployObject.Spec, 与当前DeployObject.Spec对比
	var prevSpec apiv1.DeployObjectSpec
	if err := json.Unmarshal([]byte(deployObject.Annotations["spec"]), &prevSpec); err != nil {
		log.Error(err, "parse spec annotation failed")
		return ctrl.Result{}, err
	}

	if !reflect.DeepEqual(deployObject.Spec, prevSpec) {
		// 与上一次的 Spec 有差异, 则需要更新 deployment & service
		newDeploy := newDeployment(&deployObject)
		var curDeploy appsv1.Deployment
		if err := r.Get(ctx, req.NamespacedName, &curDeploy); err != nil {
			log.Error(err, "get current deployment failed")
			return ctrl.Result{}, err
		}
		curDeploy.Spec = newDeploy.Spec
		if err := r.Update(ctx, &curDeploy); err != nil {
			log.Error(err, "update current deployment failed")
			return ctrl.Result{}, err
		}

		newService := newService(&deployObject)
		var curService corev1.Service
		if err := r.Get(ctx, req.NamespacedName, &curService); err != nil {
			log.Error(err, "get service failed")
			return ctrl.Result{}, err
		}
		clusterIP := curService.Spec.ClusterIP
		curService.Spec = newService.Spec
		curService.Spec.ClusterIP = clusterIP
		if err := r.Update(ctx, &curService); err != nil {
			log.Error(err, "update service failed")
			return ctrl.Result{}, err
		}

		// update DeployObject spec annotation
		setDeployObjectSpecAnnotation(&deployObject)
		if err := r.Update(ctx, &deployObject); err != nil {
			log.Error(err, "update deployobject failed")
			return ctrl.Result{}, err
		}
	} else {
		// 新旧 DeployObject.Spec 相同, 但 DeployObject 与 Deployment & Service 的设置有不同, 则需纠正 Deployment & Service 的设置
		// 对比 replicas / image / port/
		newDeploy := newDeployment(&deployObject)
		var curDeploy appsv1.Deployment
		if err := r.Get(ctx, req.NamespacedName, &curDeploy); err != nil {
			log.Error(err, "same spec, get current deployment failed")
			return ctrl.Result{}, err
		}

		newService := newService(&deployObject)
		var curService corev1.Service
		if err := r.Get(ctx, req.NamespacedName, &curService); err != nil {
			log.Error(err, "same spec, get current service failed")
			return ctrl.Result{}, err
		}

		if (*deployObject.Spec.Replicas != *curDeploy.Spec.Replicas) ||
			(deployObject.Spec.Image != curDeploy.Spec.Template.Spec.Containers[0].Image) {
			log.Info("same spec, update current deployment ...")
			curDeploy.Spec = newDeploy.Spec
			if err := r.Update(ctx, &curDeploy); err != nil {
				log.Error(err, "same spec, update current deployment failed")
				return ctrl.Result{}, err
			}
		}
		if !reflect.DeepEqual(deployObject.Spec.Ports, curService.Spec.Ports) {
			log.Info("same spec, update current service ...")
			clusterIP := curService.Spec.ClusterIP
			curService.Spec = newService.Spec
			curService.Spec.ClusterIP = clusterIP
			if err := r.Update(ctx, &curService); err != nil {
				log.Error(err, "same spec, update service deployment failed")
				return ctrl.Result{}, err
			}
		}
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *DeployObjectReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&appv1.DeployObject{}).
		Complete(r)
}
