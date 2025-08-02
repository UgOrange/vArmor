// Copyright 2023 vArmor Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package policy

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-logr/logr"
	apicorev1 "k8s.io/api/core/v1"
	k8errors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"

	// informers "k8s.io/client-go/informers/core/v1"

	varmor "github.com/bytedance/vArmor/apis/varmor/v1beta1"
	varmorconfig "github.com/bytedance/vArmor/internal/config"
	varmorprofile "github.com/bytedance/vArmor/internal/profile"
	statusmanager "github.com/bytedance/vArmor/internal/status/apis/v1"
	statuscommon "github.com/bytedance/vArmor/internal/status/common"
	varmortypes "github.com/bytedance/vArmor/internal/types"
	varmorutils "github.com/bytedance/vArmor/internal/utils"
	varmorinterface "github.com/bytedance/vArmor/pkg/client/clientset/versioned/typed/varmor/v1beta1"
	varmorinformer "github.com/bytedance/vArmor/pkg/client/informers/externalversions/varmor/v1beta1"
	varmorlister "github.com/bytedance/vArmor/pkg/client/listers/varmor/v1beta1"
)

type ClusterPolicyController struct {
	kubeClient                    *kubernetes.Clientset
	varmorInterface               varmorinterface.CrdV1beta1Interface
	vcpInformer                   varmorinformer.VarmorClusterPolicyInformer
	vcpLister                     varmorlister.VarmorClusterPolicyLister
	vcpInformerSynced             cache.InformerSynced
	queue                         workqueue.RateLimitingInterface
	statusManager                 *statusmanager.StatusManager
	egressCache                   map[string]varmortypes.EgressInfo
	egressCacheMutex              *sync.RWMutex
	restartExistWorkloads         bool
	enableBehaviorModeling        bool
	enablePodServiceEgressControl bool
	bpfExclusiveMode              bool
	log                           logr.Logger
}

// NewClusterPolicyController create a new ClusterPolicyController
func NewClusterPolicyController(
	kubeClient *kubernetes.Clientset,
	varmorInterface varmorinterface.CrdV1beta1Interface,
	vcpInformer varmorinformer.VarmorClusterPolicyInformer,
	statusManager *statusmanager.StatusManager,
	egressCache map[string]varmortypes.EgressInfo,
	egressCacheMutex *sync.RWMutex,
	restartExistWorkloads bool,
	enableBehaviorModeling bool,
	enablePodServiceEgressControl bool,
	bpfExclusiveMode bool,
	log logr.Logger) (*ClusterPolicyController, error) {

	c := ClusterPolicyController{
		kubeClient:                    kubeClient,
		varmorInterface:               varmorInterface,
		vcpInformer:                   vcpInformer,
		vcpLister:                     vcpInformer.Lister(),
		vcpInformerSynced:             vcpInformer.Informer().HasSynced,
		queue:                         workqueue.NewNamedRateLimitingQueue(workqueue.DefaultControllerRateLimiter(), "clusterpolicy"),
		statusManager:                 statusManager,
		egressCache:                   egressCache,
		egressCacheMutex:              egressCacheMutex,
		restartExistWorkloads:         restartExistWorkloads,
		enableBehaviorModeling:        enableBehaviorModeling,
		enablePodServiceEgressControl: enablePodServiceEgressControl,
		bpfExclusiveMode:              bpfExclusiveMode,
		log:                           log,
	}

	return &c, nil
}

func (c *ClusterPolicyController) enqueueClusterPolicy(vcp *varmor.VarmorClusterPolicy, logger logr.Logger) {
	key, err := cache.MetaNamespaceKeyFunc(vcp)
	if err != nil {
		logger.Error(err, "cache.MetaNamespaceKeyFunc()")
		return
	}
	c.queue.Add(key)
}

func (c *ClusterPolicyController) addVarmorClusterPolicy(obj interface{}) {
	logger := c.log.WithName("AddFunc()")

	vcp := obj.(*varmor.VarmorClusterPolicy)

	logger.V(2).Info("enqueue VarmorClusterPolicy")
	c.enqueueClusterPolicy(vcp, logger)
}

func (c *ClusterPolicyController) deleteVarmorClusterPolicy(obj interface{}) {
	logger := c.log.WithName("DeleteFunc()")

	vcp := obj.(*varmor.VarmorClusterPolicy)

	logger.V(2).Info("enqueue VarmorClusterPolicy")
	c.enqueueClusterPolicy(vcp, logger)
}

func (c *ClusterPolicyController) updateVarmorClusterPolicy(oldObj, newObj interface{}) {
	logger := c.log.WithName("UpdateFunc()")

	oldVcp := oldObj.(*varmor.VarmorClusterPolicy)
	newVcp := newObj.(*varmor.VarmorClusterPolicy)

	if newVcp.ResourceVersion == oldVcp.ResourceVersion ||
		reflect.DeepEqual(newVcp.Spec, oldVcp.Spec) ||
		!reflect.DeepEqual(newVcp.Status, oldVcp.Status) {
		logger.V(2).Info("nothing need to be updated")
	} else {
		logger.V(2).Info("enqueue VarmorClusterPolicy")
		c.enqueueClusterPolicy(newVcp, logger)
	}
}

func (c *ClusterPolicyController) handleDeleteVarmorClusterPolicy(name string) error {
	logger := c.log.WithName("handleDeleteVarmorClusterPolicy()")
	logger.Info("VarmorClusterPolicy", "name", name)

	apName := varmorprofile.GenerateArmorProfileName(varmorconfig.Namespace, name, true)
	logger.Info("retrieve ArmorProfile", "namespace", varmorconfig.Namespace, "name", apName)
	ap, err := c.varmorInterface.ArmorProfiles(varmorconfig.Namespace).Get(context.Background(), apName, metav1.GetOptions{})
	if err != nil {
		if k8errors.IsNotFound(err) {
			logger.Error(err, "namespace", varmorconfig.Namespace, "name", apName)
		} else {
			logger.Error(err, "c.varmorInterface.ArmorProfiles().Get()")
			return err
		}
	} else {
		if c.restartExistWorkloads && ap.Spec.UpdateExistingWorkloads {
			// This will trigger the rolling upgrade of the target workloads
			logger.Info("delete annotations of target workloads to trigger a rolling upgrade asynchronously")
			go updateWorkloadAnnotationsAndEnv(
				c.kubeClient.AppsV1(),
				metav1.NamespaceAll,
				ap.Spec.Profile.Enforcer,
				"",
				ap.Spec.Target,
				"", false, logger)
		}

		logger.Info("remove the ArmorProfile's finalizers")
		err := varmorutils.RemoveArmorProfileFinalizers(c.varmorInterface, varmorconfig.Namespace, apName)
		if err != nil {
			logger.Error(err, "failed to remove the ArmorProfile's finalizers")
		}

		// Cleanup the policy from the egress information cache
		policyKey := name
		c.egressCacheMutex.Lock()
		delete(c.egressCache, policyKey)
		c.egressCacheMutex.Unlock()
	}

	// Cleanup the PolicyStatus and ModelingStatus of status manager for the deleted VarmorClusterPolicy/ArmorProfile object
	logger.Info("cleanup the policy status (and if any modeling status) of statusmanager.policystatuses")
	c.statusManager.DeleteCh <- name

	return nil
}

func (c *ClusterPolicyController) ignoreAdd(vcp *varmor.VarmorClusterPolicy, logger logr.Logger) (bool, error) {
	if vcp.Spec.Target.Kind != "Deployment" && vcp.Spec.Target.Kind != "StatefulSet" && vcp.Spec.Target.Kind != "DaemonSet" && vcp.Spec.Target.Kind != "Pod" {
		err := fmt.Errorf("the target kind is not supported")
		logger.Error(err, "update the policy status with forbidden info")
		err = statuscommon.UpdateVarmorClusterPolicyStatus(c.varmorInterface, vcp, "", false, varmor.VarmorPolicyError, varmor.VarmorPolicyCreated, apicorev1.ConditionFalse,
			"Forbidden",
			"The target kind is not supported.")
		return true, err
	}

	if vcp.Spec.Target.Name == "" && vcp.Spec.Target.Selector == nil {
		err := fmt.Errorf("the target name and selector are empty")
		logger.Error(err, "update the policy status with forbidden info")
		err = statuscommon.UpdateVarmorClusterPolicyStatus(c.varmorInterface, vcp, "", false, varmor.VarmorPolicyError, varmor.VarmorPolicyCreated, apicorev1.ConditionFalse,
			"Forbidden",
			"You should specify the target workload either by name or selector.")
		return true, err
	}

	if vcp.Spec.Target.Name != "" && vcp.Spec.Target.Selector != nil {
		err := fmt.Errorf("the target name and selector are exclusive")
		logger.Error(err, "update the policy status with forbidden info")
		err = statuscommon.UpdateVarmorClusterPolicyStatus(c.varmorInterface, vcp, "", false, varmor.VarmorPolicyError, varmor.VarmorPolicyCreated, apicorev1.ConditionFalse,
			"Forbidden",
			"You shouldn't specify the target workload using both name and selector.")
		return true, err
	}

	if vcp.Spec.Policy.Mode == varmor.EnhanceProtectMode && vcp.Spec.Policy.EnhanceProtect == nil {
		err := fmt.Errorf("the enhanceProtect field is not set when the policy runs in the EnhanceProtect mode")
		logger.Error(err, "update the policy status with forbidden info")
		err = statuscommon.UpdateVarmorClusterPolicyStatus(c.varmorInterface, vcp, "", false, varmor.VarmorPolicyError, varmor.VarmorPolicyCreated, apicorev1.ConditionFalse,
			"Forbidden",
			"The enhanceProtect field should be set when the policy runs in the EnhanceProtect mode.")
		return true, err
	}

	if !c.enableBehaviorModeling && vcp.Spec.Policy.Mode == varmor.BehaviorModelingMode {
		err := fmt.Errorf("the BehaviorModeling mode is not enabled")
		logger.Error(err, "update the policy status with forbidden info")
		err = statuscommon.UpdateVarmorClusterPolicyStatus(c.varmorInterface, vcp, "", false, varmor.VarmorPolicyError, varmor.VarmorPolicyCreated, apicorev1.ConditionFalse,
			"Forbidden",
			"The BehaviorModeling feature is not enabled.")
		return true, err
	}

	if c.enableBehaviorModeling && vcp.Spec.Policy.Mode == varmor.BehaviorModelingMode && vcp.Spec.Policy.ModelingOptions == nil {
		err := fmt.Errorf("the modelingOptions field is not set when the policy runs in the BehaviorModeling mode")
		logger.Error(err, "update the policy status with forbidden info")
		err = statuscommon.UpdateVarmorClusterPolicyStatus(c.varmorInterface, vcp, "", false, varmor.VarmorPolicyError, varmor.VarmorPolicyCreated, apicorev1.ConditionFalse,
			"Forbidden",
			"The modelingOptions field should be set when the policy runs in the BehaviorModeling mode.")
		return true, err
	}

	// Do not exceed the length of a standard Kubernetes name (63 characters)
	// Note: The advisory length of AppArmor profile name is 100 (See https://bugs.launchpad.net/apparmor/+bug/1499544).
	profileName := varmorprofile.GenerateArmorProfileName(varmorconfig.Namespace, vcp.Name, true)
	if len(profileName) > 63 {
		err := fmt.Errorf("the length of ArmorProfile name is exceed 63. name: %s, length: %d", profileName, len(profileName))
		logger.Error(err, "update the policy status with forbidden info")
		msg := fmt.Sprintf("The length of policy object name is too long, please limit it to %d bytes.", 63-len(varmorprofile.ClusterProfileNameTemplate)+4-len(varmorconfig.Namespace))
		err = statuscommon.UpdateVarmorClusterPolicyStatus(c.varmorInterface, vcp, "", false, varmor.VarmorPolicyError, varmor.VarmorPolicyCreated, apicorev1.ConditionFalse,
			"Forbidden",
			msg)
		return true, err
	}

	return false, nil
}

func (c *ClusterPolicyController) handleAddVarmorClusterPolicy(vcp *varmor.VarmorClusterPolicy) error {
	logger := c.log.WithName("handleAddVarmorClusterPolicy()")

	logger.Info("VarmorClusterPolicy created", "name", vcp.Name, "labels", vcp.Labels, "target", vcp.Spec.Target)

	if ignore, err := c.ignoreAdd(vcp, logger); ignore {
		if err != nil {
			logger.Error(err, "ignoreAdd()")
		}
		return err
	}

	ap, egressInfo, err := varmorprofile.NewArmorProfile(c.kubeClient, c.varmorInterface, vcp, true, c.enablePodServiceEgressControl, logger)
	if err != nil {
		logger.Error(err, "NewArmorProfile()")
		err = statuscommon.UpdateVarmorClusterPolicyStatus(c.varmorInterface, vcp, "", false, varmor.VarmorPolicyError, varmor.VarmorPolicyCreated, apicorev1.ConditionFalse,
			"Error",
			err.Error())
		if err != nil {
			logger.Error(err, "statuscommon.UpdateVarmorClusterPolicyStatus()")
			return err
		}
		return nil
	}

	logger.Info("update VarmorClusterPolicy/status (created=true)")
	err = statuscommon.UpdateVarmorClusterPolicyStatus(c.varmorInterface, vcp, ap.Spec.Profile.Name, false, varmor.VarmorPolicyPending, varmor.VarmorPolicyCreated, apicorev1.ConditionTrue, "", "")
	if err != nil {
		logger.Error(err, "statuscommon.UpdateVarmorClusterPolicyStatus()")
		return err
	}

	if vcp.Spec.Policy.Mode == varmor.BehaviorModelingMode {
		err = resetArmorProfileModelStatus(c.varmorInterface, varmorconfig.Namespace, ap.Name)
		if err != nil {
			logger.Error(err, "resetArmorProfileModelStatus()")
		}
	}

	atomic.StoreInt32(&c.statusManager.UpdateDesiredNumber, 1)

	logger.Info("create ArmorProfile")
	ap, err = c.varmorInterface.ArmorProfiles(varmorconfig.Namespace).Create(context.Background(), ap, metav1.CreateOptions{})
	if err != nil {
		logger.Error(err, "ArmorProfile().Create()")
		if varmorutils.IsRequestSizeError(err) {
			return statuscommon.UpdateVarmorClusterPolicyStatus(
				c.varmorInterface, vcp, "", false, varmor.VarmorPolicyError, varmor.VarmorPolicyCreated, apicorev1.ConditionFalse,
				"Error",
				"The profiles are too large to create an ArmorProfile object.")
		}
		return err
	}

	// Cache the egress information for the policy which has network egress rules with toPods and toService fields
	if egressInfo != nil && (len(egressInfo.ToPods) > 0 || len(egressInfo.ToServices) > 0) {
		policyKey := vcp.Name
		c.egressCacheMutex.Lock()
		c.egressCache[policyKey] = *egressInfo
		c.egressCacheMutex.Unlock()
		logger.Info("egress cache added", "policy key", policyKey, "egress info", egressInfo)
	}

	if c.restartExistWorkloads && vcp.Spec.UpdateExistingWorkloads {
		// This will trigger the rolling upgrade of the target workloads
		logger.Info("add annotations to target workloads to trigger a rolling upgrade asynchronously")
		go updateWorkloadAnnotationsAndEnv(
			c.kubeClient.AppsV1(),
			metav1.NamespaceAll,
			vcp.Spec.Policy.Enforcer,
			vcp.Spec.Policy.Mode,
			vcp.Spec.Target,
			ap.Name,
			c.bpfExclusiveMode,
			logger)
	}

	return nil
}

func (c *ClusterPolicyController) ignoreUpdate(newVcp *varmor.VarmorClusterPolicy, oldAp *varmor.ArmorProfile, logger logr.Logger) (bool, error) {
	newEnforcers := varmortypes.GetEnforcerType(newVcp.Spec.Policy.Enforcer)
	oldEnforcers := varmortypes.GetEnforcerType(oldAp.Spec.Profile.Enforcer)

	// Disallow modifying the target field of a policy.
	if !reflect.DeepEqual(newVcp.Spec.Target, oldAp.Spec.Target) {
		err := fmt.Errorf("disallow modifying the target field of a policy")
		logger.Error(err, "update the policy status with forbidden info")
		err = statuscommon.UpdateVarmorClusterPolicyStatus(c.varmorInterface, newVcp, "", false, varmor.VarmorPolicyUnchanged, varmor.VarmorPolicyUpdated, apicorev1.ConditionFalse,
			"Forbidden",
			"Modifying the target field of a policy is not allowed. You need to recreate the policy object.")
		return true, err
	}

	// Disallow switching the mode of a policy from BehaviorModeling to others when behavior modeling is still incomplete.
	if newVcp.Spec.Policy.Mode != varmor.BehaviorModelingMode &&
		newVcp.Status.Phase == varmor.VarmorPolicyModeling {
		err := fmt.Errorf("disallow switching the mode of a policy from BehaviorModeling to others when behavior modeling is still incomplete")
		logger.Error(err, "update the policy status with forbidden info")
		err = statuscommon.UpdateVarmorClusterPolicyStatus(c.varmorInterface, newVcp, "", false, varmor.VarmorPolicyUnchanged, varmor.VarmorPolicyUpdated, apicorev1.ConditionFalse,
			"Forbidden",
			"Switching the mode of a policy from BehaviorModeling to others is not allowed when behavior modeling is still incomplete.")
		return true, err
	}

	// Disallow modifying the enforcer field of a policy when behavior modeling is still incomplete.
	if newVcp.Spec.Policy.Mode == varmor.BehaviorModelingMode &&
		newVcp.Status.Phase == varmor.VarmorPolicyModeling &&
		newEnforcers != oldEnforcers {
		err := fmt.Errorf("disallow modifying the enforcer field of a policy when behavior modeling is still incomplete")
		logger.Error(err, "update the policy status with forbidden info")
		err = statuscommon.UpdateVarmorClusterPolicyStatus(c.varmorInterface, newVcp, "", false, varmor.VarmorPolicyUnchanged, varmor.VarmorPolicyUpdated, apicorev1.ConditionFalse,
			"Forbidden",
			"Modifying the enforcer field of a policy is not allowed when behavior modeling is still incomplete.")
		return true, err
	}

	// Disallow removing the activated AppArmor or Seccomp enforcer.
	if (newEnforcers&oldEnforcers != oldEnforcers) && (newEnforcers|varmortypes.BPF != oldEnforcers) {
		err := fmt.Errorf("disallow removing the activated AppArmor or Seccomp enforcer")
		logger.Error(err, "update the policy status with forbidden info")
		err = statuscommon.UpdateVarmorClusterPolicyStatus(c.varmorInterface, newVcp, "", false, varmor.VarmorPolicyUnchanged, varmor.VarmorPolicyUpdated, apicorev1.ConditionFalse,
			"Forbidden",
			"Modifying a policy to remove the AppArmor or Seccomp enforcer is not allowed. To remove them, you need to recreate the policy object.")
		return true, err
	}

	// Make sure the enhanceProtect field has been set when the policy runs in the EnhanceProtect mode.
	if newVcp.Spec.Policy.Mode == varmor.EnhanceProtectMode &&
		newVcp.Spec.Policy.EnhanceProtect == nil {
		err := fmt.Errorf("the enhanceProtect field is not set when the policy runs in the EnhanceProtect mode")
		logger.Error(err, "update the policy status with forbidden info")
		err = statuscommon.UpdateVarmorClusterPolicyStatus(c.varmorInterface, newVcp, "", false, varmor.VarmorPolicyError, varmor.VarmorPolicyUpdated, apicorev1.ConditionFalse,
			"Forbidden",
			"The enhanceProtect field should be set when the policy runs in the EnhanceProtect mode.")
		return true, err
	}

	// Make sure the modelingOptions field has been set when the policy runs in BehaviorModeling mode.
	if newVcp.Spec.Policy.Mode == varmor.BehaviorModelingMode &&
		newVcp.Spec.Policy.ModelingOptions == nil {
		err := fmt.Errorf("the modelingOptions field is not set when the policy runs in the BehaviorModeling mode")
		logger.Error(err, "update the policy status with forbidden info")
		err = statuscommon.UpdateVarmorClusterPolicyStatus(c.varmorInterface, newVcp, "", false, varmor.VarmorPolicyError, varmor.VarmorPolicyUpdated, apicorev1.ConditionFalse,
			"Forbidden",
			"The modelingOptions field should be set when the policy runs in the BehaviorModeling mode.")
		return true, err
	}

	return false, nil
}

func (c *ClusterPolicyController) handleUpdateVarmorClusterPolicy(newVcp *varmor.VarmorClusterPolicy, oldAp *varmor.ArmorProfile) error {
	logger := c.log.WithName("handleUpdateVarmorClusterPolicy()")

	logger.Info("VarmorClusterPolicy updated", "name", newVcp.Name, "labels", newVcp.Labels, "target", newVcp.Spec.Target)

	if ignore, err := c.ignoreUpdate(newVcp, oldAp, logger); ignore {
		if err != nil {
			logger.Error(err, "ignoreUpdate()")
		}
		return err
	}

	statusKey := newVcp.Name

	// First, reset VarmorClusterPolicy/status
	logger.Info("1. reset VarmorClusterPolicy/status (updated=true)", "name", newVcp.Name)
	err := statuscommon.UpdateVarmorClusterPolicyStatus(c.varmorInterface, newVcp, "", false, varmor.VarmorPolicyPending, varmor.VarmorPolicyUpdated, apicorev1.ConditionTrue, "", "")
	if err != nil {
		logger.Error(err, "statuscommon.UpdateVarmorClusterPolicyStatus()")
		return err
	}

	// Second, create a new ArmorProfileSpec with the updated policy
	complete := false
	if newVcp.Spec.Policy.Mode == varmor.BehaviorModelingMode {
		if newVcp.Spec.Policy.ModelingOptions != nil {
			createTime := oldAp.CreationTimestamp.Time
			Duration := time.Duration(newVcp.Spec.Policy.ModelingOptions.Duration) * time.Minute
			if time.Now().After(createTime.Add(Duration)) {
				complete = true
			}
		}
	}

	newProfile, egressInfo, err := varmorprofile.GenerateProfile(c.kubeClient, c.varmorInterface, newVcp.Spec.Policy, oldAp.Name, varmorconfig.Namespace, complete, c.enablePodServiceEgressControl, logger)
	if err != nil {
		logger.Error(err, "GenerateProfile()")
		err = statuscommon.UpdateVarmorClusterPolicyStatus(c.varmorInterface, newVcp, "", false, varmor.VarmorPolicyError, varmor.VarmorPolicyUpdated, apicorev1.ConditionFalse,
			"Error",
			err.Error())
		if err != nil {
			logger.Error(err, "statuscommon.UpdateVarmorClusterPolicyStatus()")
			return err
		}
		return nil
	}

	newApSpec := oldAp.Spec.DeepCopy()
	newApSpec.Profile = *newProfile
	newApSpec.UpdateExistingWorkloads = newVcp.Spec.UpdateExistingWorkloads
	if newVcp.Spec.Policy.Mode == varmor.BehaviorModelingMode {
		// Update the BehaviorModeling duration
		if newVcp.Spec.Policy.ModelingOptions != nil {
			newApSpec.BehaviorModeling.Duration = newVcp.Spec.Policy.ModelingOptions.Duration
		}

		// Reset the status cache if the BehaviorModeling duration has not expired
		if !complete {
			newApSpec.BehaviorModeling.Enable = true
			logger.Info("reset the status cache", "status key", statusKey)
			atomic.StoreInt32(&c.statusManager.UpdateDesiredNumber, 1)
			c.statusManager.ResetCh <- statusKey
		}
	}

	// Third, cache the egress information for the policy which has network egress rules with toPods and toService fields
	if egressInfo != nil {
		policyKey := statusKey
		c.egressCacheMutex.Lock()
		delete(c.egressCache, policyKey)
		if len(egressInfo.ToPods) > 0 || len(egressInfo.ToServices) > 0 {
			c.egressCache[policyKey] = *egressInfo
		}
		c.egressCacheMutex.Unlock()
		logger.Info("egress cache updated", "policy key", policyKey, "egress info", egressInfo)
	}

	// Last, do update
	if !reflect.DeepEqual(oldAp.Spec, *newApSpec) {
		// Update the objects and their statuses if the spec of ArmorProfile has changed
		logger.Info("2. update the object and its status")

		logger.Info("2.1. reset ArmorProfile/status and ArmorProfileModel/Status", "namespace", varmorconfig.Namespace, "name", oldAp.Name)
		oldAp.Status.CurrentNumberLoaded = 0
		oldAp.Status.Conditions = nil
		oldAp, err = c.varmorInterface.ArmorProfiles(varmorconfig.Namespace).UpdateStatus(context.Background(), oldAp, metav1.UpdateOptions{})
		if err != nil {
			logger.Error(err, "ArmorProfile().UpdateStatus()")
			return err
		}

		if newVcp.Spec.Policy.Mode == varmor.BehaviorModelingMode {
			err = resetArmorProfileModelStatus(c.varmorInterface, varmorconfig.Namespace, oldAp.Name)
			if err != nil {
				logger.Error(err, "resetArmorProfileModelStatus()")
			}
		}

		logger.Info("2.2. update ArmorProfile")
		oldAp.Spec = *newApSpec
		forceSetOwnerReference(oldAp, newVcp, true)
		_, err = c.varmorInterface.ArmorProfiles(varmorconfig.Namespace).Update(context.Background(), oldAp, metav1.UpdateOptions{})
		if err != nil {
			logger.Error(err, "ArmorProfile().Update()")
			if varmorutils.IsRequestSizeError(err) {
				return statuscommon.UpdateVarmorClusterPolicyStatus(
					c.varmorInterface, newVcp, "", false, varmor.VarmorPolicyError, varmor.VarmorPolicyUpdated, apicorev1.ConditionFalse,
					"Error",
					"The profiles are too large to update the existing ArmorProfile object.")
			}
			return err
		}
	} else if len(oldAp.OwnerReferences) == 0 {
		// Forward compatibility, add an ownerReference to the existing ArmorProfile object
		forceSetOwnerReference(oldAp, newVcp, true)
		_, err = c.varmorInterface.ArmorProfiles(varmorconfig.Namespace).Update(context.Background(), oldAp, metav1.UpdateOptions{})
		if err != nil {
			logger.Error(err, "ArmorProfile().Update()")
			if varmorutils.IsRequestSizeError(err) {
				return statuscommon.UpdateVarmorClusterPolicyStatus(
					c.varmorInterface, newVcp, "", false, varmor.VarmorPolicyError, varmor.VarmorPolicyUpdated, apicorev1.ConditionFalse,
					"Error",
					"The profiles are too large to update the existing ArmorProfile object.")
			}
			return err
		}
	} else {
		// Update the objects' statuses
		logger.Info("2. update the object' status")

		logger.Info("2.1. update VarmorClusterPolicy/status and ArmorProfile/status", "status key", statusKey)
		c.statusManager.UpdateStatusCh <- statusKey
	}
	return nil
}

func (c *ClusterPolicyController) syncClusterPolicy(key string) error {
	logger := c.log.WithName("syncClusterPolicy()")

	startTime := time.Now()
	logger.V(2).Info("started syncing policy", "key", key, "startTime", startTime)
	defer func() {
		logger.V(2).Info("finished syncing policy", "key", key, "processingTime", time.Since(startTime).String())
	}()

	_, name, err := cache.SplitMetaNamespaceKey(key)
	if err != nil {
		logger.Error(err, "cache.SplitMetaNamespaceKey()")
		return err
	}

	vcp, err := c.varmorInterface.VarmorClusterPolicies().Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		if k8errors.IsNotFound(err) {
			// VarmorClusterPolicy delete event
			logger.V(2).Info("processing VarmorClusterPolicy delete event")
			return c.handleDeleteVarmorClusterPolicy(name)
		} else {
			logger.Error(err, "c.varmorInterface.VarmorClusterPolicies().Get()")
			return err
		}
	}

	newPolicy := false
	apName := varmorprofile.GenerateArmorProfileName(varmorconfig.Namespace, vcp.Name, true)
	ap, err := c.varmorInterface.ArmorProfiles(varmorconfig.Namespace).Get(context.Background(), apName, metav1.GetOptions{})
	if err == nil {
		if policyOwnArmorProfile(vcp, ap, true) {
			// VarmorClusterPolicy update event
			logger.V(2).Info("processing VarmorClusterPolicy update event")
			return c.handleUpdateVarmorClusterPolicy(vcp, ap)
		} else {
			logger.Info("remove the finalizers of zombie ArmorProfile", "namespace", ap.Namespace, "name", ap.Name)
			err := varmorutils.RemoveArmorProfileFinalizers(c.varmorInterface, ap.Namespace, ap.Name)
			if err != nil {
				return err
			}
			newPolicy = true
		}
	}

	if k8errors.IsNotFound(err) || newPolicy {
		// VarmorClusterPolicy create event
		logger.V(2).Info("processing VarmorClusterPolicy create event")
		return c.handleAddVarmorClusterPolicy(vcp)
	}

	return err
}

func (c *ClusterPolicyController) handleErr(err error, key interface{}) {
	logger := c.log
	if err == nil {
		c.queue.Forget(key)
		return
	}

	if c.queue.NumRequeues(key) < maxRetries {
		logger.Error(err, "failed to sync policy", "key", key)
		c.queue.AddRateLimited(key)
		return
	}

	utilruntime.HandleError(err)
	logger.V(2).Info("dropping policy out of queue", "key", key)
	c.queue.Forget(key)
}

func (c *ClusterPolicyController) processNextWorkItem() bool {
	key, quit := c.queue.Get()
	if quit {
		return false
	}
	defer c.queue.Done(key)
	err := c.syncClusterPolicy(key.(string))
	c.handleErr(err, key)

	return true
}

func (c *ClusterPolicyController) worker() {
	for c.processNextWorkItem() {
	}
}

// Run begins watching and syncing.
func (c *ClusterPolicyController) Run(workers int, stopCh <-chan struct{}) {
	logger := c.log
	logger.Info("starting")

	defer utilruntime.HandleCrash()

	if !cache.WaitForCacheSync(stopCh, c.vcpInformerSynced) {
		logger.Error(fmt.Errorf("failed to sync informer cache"), "cache.WaitForCacheSync()")
		return
	}

	c.vcpInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    c.addVarmorClusterPolicy,
		UpdateFunc: c.updateVarmorClusterPolicy,
		DeleteFunc: c.deleteVarmorClusterPolicy,
	})

	for i := 0; i < workers; i++ {
		go wait.Until(c.worker, time.Second, stopCh)
	}

	<-stopCh
}

func (c *ClusterPolicyController) CleanUp() {
	c.log.Info("cleaning up")
	c.queue.ShutDown()
}
