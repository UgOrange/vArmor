// Package common provides common functions for the status service
package common

import (
	"context"
	"reflect"

	v1 "k8s.io/api/core/v1"
	k8errors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"

	varmor "github.com/bytedance/vArmor/apis/varmor/v1beta1"
	varmortypes "github.com/bytedance/vArmor/internal/types"
	varmorinterface "github.com/bytedance/vArmor/pkg/client/clientset/versioned/typed/varmor/v1beta1"
)

func UpdateVarmorPolicyStatus(
	i varmorinterface.CrdV1beta1Interface,
	vp *varmor.VarmorPolicy,
	profileName string,
	ready bool,
	phase varmor.VarmorPolicyPhase,
	condType varmor.VarmorPolicyConditionType,
	status v1.ConditionStatus,
	reason, message string) error {

	// Prepare for condition
	condition := varmor.VarmorPolicyCondition{
		Type:               condType,
		Status:             status,
		LastTransitionTime: metav1.Now(),
		Reason:             reason,
		Message:            message,
	}

	regain := false
	update := func() (err error) {
		if regain {
			vp, err = i.VarmorPolicies(vp.Namespace).Get(context.Background(), vp.Name, metav1.GetOptions{})
			if err != nil {
				if k8errors.IsNotFound(err) {
					return nil
				}
				return err
			}
		}

		// Update condition
		exist := false
		switch condition.Type {
		case varmor.VarmorPolicyCreated:
			for i, c := range vp.Status.Conditions {
				if c.Type == varmor.VarmorPolicyCreated {
					condition.DeepCopyInto(&vp.Status.Conditions[i])
					exist = true
					break
				}
			}
		case varmor.VarmorPolicyUpdated:
			for i, c := range vp.Status.Conditions {
				if c.Type == varmor.VarmorPolicyUpdated {
					condition.DeepCopyInto(&vp.Status.Conditions[i])
					exist = true
					break
				}
			}
		case varmor.VarmorPolicyReady:
			for i, c := range vp.Status.Conditions {
				if c.Type == varmor.VarmorPolicyReady {
					condition.DeepCopyInto(&vp.Status.Conditions[i])
					exist = true
					break
				}
			}
		}
		if !exist {
			vp.Status.Conditions = append(vp.Status.Conditions, condition)
		}

		// Update profile name
		if profileName != "" {
			vp.Status.ProfileName = profileName
		}

		// Update status
		vp.Status.Ready = ready
		if phase != varmor.VarmorPolicyUnchanged {
			vp.Status.Phase = phase
		}

		_, err = i.VarmorPolicies(vp.Namespace).UpdateStatus(context.Background(), vp, metav1.UpdateOptions{})
		if err != nil {
			regain = true
		}
		return err
	}
	return retry.RetryOnConflict(retry.DefaultRetry, update)
}

func UpdateVarmorClusterPolicyStatus(
	i varmorinterface.CrdV1beta1Interface,
	vcp *varmor.VarmorClusterPolicy,
	profileName string,
	ready bool,
	phase varmor.VarmorPolicyPhase,
	condType varmor.VarmorPolicyConditionType,
	status v1.ConditionStatus,
	reason, message string) error {

	// Prepare for condition
	condition := varmor.VarmorPolicyCondition{
		Type:               condType,
		Status:             status,
		LastTransitionTime: metav1.Now(),
		Reason:             reason,
		Message:            message,
	}

	regain := false
	update := func() (err error) {
		if regain {
			vcp, err = i.VarmorClusterPolicies().Get(context.Background(), vcp.Name, metav1.GetOptions{})
			if err != nil {
				if k8errors.IsNotFound(err) {
					return nil
				}
				return err
			}
		}

		// Update condition
		exist := false
		switch condition.Type {
		case varmor.VarmorPolicyCreated:
			for i, c := range vcp.Status.Conditions {
				if c.Type == varmor.VarmorPolicyCreated {
					condition.DeepCopyInto(&vcp.Status.Conditions[i])
					exist = true
					break
				}
			}
		case varmor.VarmorPolicyUpdated:
			for i, c := range vcp.Status.Conditions {
				if c.Type == varmor.VarmorPolicyUpdated {
					condition.DeepCopyInto(&vcp.Status.Conditions[i])
					exist = true
					break
				}
			}
		case varmor.VarmorPolicyReady:
			for i, c := range vcp.Status.Conditions {
				if c.Type == varmor.VarmorPolicyReady {
					condition.DeepCopyInto(&vcp.Status.Conditions[i])
					exist = true
					break
				}
			}
		}
		if !exist {
			vcp.Status.Conditions = append(vcp.Status.Conditions, condition)
		}

		// Update profile name
		if profileName != "" {
			vcp.Status.ProfileName = profileName
		}

		// Update status
		vcp.Status.Ready = ready
		if phase != varmor.VarmorPolicyUnchanged {
			vcp.Status.Phase = phase
		}

		_, err = i.VarmorClusterPolicies().UpdateStatus(context.Background(), vcp, metav1.UpdateOptions{})
		if err != nil {
			regain = true
		}
		return err
	}
	return retry.RetryOnConflict(retry.DefaultRetry, update)
}

func newArmorProfileCondition(
	nodeName string,
	condType varmor.ArmorProfileConditionType,
	status v1.ConditionStatus,
	reason, message string) *varmor.ArmorProfileCondition {

	return &varmor.ArmorProfileCondition{
		NodeName:           nodeName,
		Type:               condType,
		Status:             status,
		LastTransitionTime: metav1.Now(),
		Reason:             reason,
		Message:            message,
	}
}

func UpdateArmorProfileStatus(
	i varmorinterface.CrdV1beta1Interface,
	ap *varmor.ArmorProfile,
	policyStatus *varmortypes.PolicyStatus,
	desiredNumber int32) error {

	var conditions []varmor.ArmorProfileCondition
	for nodeName, message := range policyStatus.NodeMessages {
		if message != string(varmor.ArmorProfileReady) {
			c := newArmorProfileCondition(nodeName, varmor.ArmorProfileReady, v1.ConditionFalse, "", message)
			conditions = append(conditions, *c)
		}
	}

	regain := false
	update := func() (err error) {
		if regain {
			ap, err = i.ArmorProfiles(ap.Namespace).Get(context.Background(), ap.Name, metav1.GetOptions{})
			if err != nil {
				if k8errors.IsNotFound(err) {
					return nil
				}
				return err
			}
		}
		// Nothing needs to be updated.
		if reflect.DeepEqual(ap.Status.Conditions, conditions) &&
			ap.Status.CurrentNumberLoaded == policyStatus.SuccessedNumber &&
			ap.Status.DesiredNumberLoaded == desiredNumber {
			return nil
		}
		ap.Status.DesiredNumberLoaded = desiredNumber
		ap.Status.CurrentNumberLoaded = policyStatus.SuccessedNumber
		if len(conditions) > 0 {
			ap.Status.Conditions = conditions
		} else {
			ap.Status.Conditions = nil
		}
		_, err = i.ArmorProfiles(ap.Namespace).UpdateStatus(context.Background(), ap, metav1.UpdateOptions{})
		if err != nil {
			regain = true
		}
		return err
	}
	return retry.RetryOnConflict(retry.DefaultRetry, update)
}

func newArmorProfileModelCondition(
	nodeName string,
	condType varmor.ArmorProfileModelConditionType,
	status v1.ConditionStatus,
	reason, message string) *varmor.ArmorProfileModelCondition {

	return &varmor.ArmorProfileModelCondition{
		NodeName:           nodeName,
		Type:               condType,
		Status:             status,
		LastTransitionTime: metav1.Now(),
		Reason:             reason,
		Message:            message,
	}
}

func UpdateArmorProfileModelStatus(
	i varmorinterface.CrdV1beta1Interface,
	apm *varmor.ArmorProfileModel,
	modelingStatus *varmortypes.ModelingStatus,
	desiredNumber int32,
	complete bool) error {

	var conditions []varmor.ArmorProfileModelCondition
	for nodeName, message := range modelingStatus.NodeMessages {
		if message != string(varmor.ArmorProfileModelReady) {
			c := newArmorProfileModelCondition(nodeName, varmor.ArmorProfileModelReady, v1.ConditionFalse, "", message)
			conditions = append(conditions, *c)
		}
	}

	if reflect.DeepEqual(apm.Status.Conditions, conditions) &&
		apm.Status.CompletedNumber == modelingStatus.CompletedNumber {
		return nil
	}

	apm.Status.DesiredNumber = desiredNumber
	apm.Status.CompletedNumber = modelingStatus.CompletedNumber
	if complete {
		apm.Status.Ready = true
	}
	if len(conditions) > 0 {
		apm.Status.Conditions = conditions
	} else {
		apm.Status.Conditions = nil
	}

	_, err := i.ArmorProfileModels(apm.Namespace).UpdateStatus(context.Background(), apm, metav1.UpdateOptions{})

	return err
}

func MergeAppArmorResult(apm *varmor.ArmorProfileModel, appArmor *varmor.AppArmor) {
	if appArmor == nil {
		return
	}

	if apm.Data.DynamicResult.AppArmor == nil {
		apm.Data.DynamicResult.AppArmor = &varmor.AppArmor{}
	}

	for _, newProfile := range appArmor.Profiles {
		find := false
		for _, profile := range apm.Data.DynamicResult.AppArmor.Profiles {
			if newProfile == profile {
				find = true
				break
			}
		}
		if !find {
			apm.Data.DynamicResult.AppArmor.Profiles = append(apm.Data.DynamicResult.AppArmor.Profiles, newProfile)
		}
	}

	for _, newExe := range appArmor.Executions {
		find := false
		for _, execution := range apm.Data.DynamicResult.AppArmor.Executions {
			if newExe == execution {
				find = true
				break
			}
		}
		if !find {
			apm.Data.DynamicResult.AppArmor.Executions = append(apm.Data.DynamicResult.AppArmor.Executions, newExe)
		}
	}

	for _, newFile := range appArmor.Files {
		findFile := false
		for index, file := range apm.Data.DynamicResult.AppArmor.Files {
			if newFile.Path == file.Path && newFile.Owner == file.Owner {
				findFile = true

				for _, newPerm := range newFile.Permissions {
					findPerm := false
					for _, perm := range file.Permissions {
						if newPerm == perm {
							findPerm = true
							break
						}
					}
					if !findPerm {
						apm.Data.DynamicResult.AppArmor.Files[index].Permissions = append(apm.Data.DynamicResult.AppArmor.Files[index].Permissions, newPerm)
					}
				}

				if file.OldPath == "" && newFile.OldPath != "" {
					apm.Data.DynamicResult.AppArmor.Files[index].OldPath = newFile.OldPath
				}
				break
			}
		}
		if !findFile {
			apm.Data.DynamicResult.AppArmor.Files = append(apm.Data.DynamicResult.AppArmor.Files, newFile)
		}
	}

	for _, newCap := range appArmor.Capabilities {
		find := false
		for _, cap := range apm.Data.DynamicResult.AppArmor.Capabilities {
			if newCap == cap {
				find = true
				break
			}
		}
		if !find {
			apm.Data.DynamicResult.AppArmor.Capabilities = append(apm.Data.DynamicResult.AppArmor.Capabilities, newCap)
		}
	}

	for _, newNet := range appArmor.Networks {
		find := false
		for _, net := range apm.Data.DynamicResult.AppArmor.Networks {
			if reflect.DeepEqual(newNet, net) {
				find = true
				break
			}
		}
		if !find {
			apm.Data.DynamicResult.AppArmor.Networks = append(apm.Data.DynamicResult.AppArmor.Networks, newNet)
		}
	}

	for _, newPtrace := range appArmor.Ptraces {
		find := false
		for index, ptrace := range apm.Data.DynamicResult.AppArmor.Ptraces {
			if newPtrace.Peer == ptrace.Peer {
				find = true

				for _, newPerm := range newPtrace.Permissions {
					findPerm := false
					for _, perm := range ptrace.Permissions {
						if newPerm == perm {
							findPerm = true
							break
						}
					}
					if !findPerm {
						apm.Data.DynamicResult.AppArmor.Ptraces[index].Permissions = append(apm.Data.DynamicResult.AppArmor.Ptraces[index].Permissions, newPerm)
					}
				}

				break
			}
		}
		if !find {
			apm.Data.DynamicResult.AppArmor.Ptraces = append(apm.Data.DynamicResult.AppArmor.Ptraces, newPtrace)
		}
	}

	for _, newSignal := range appArmor.Signals {
		find := false
		for index, signal := range apm.Data.DynamicResult.AppArmor.Signals {
			if newSignal.Peer == signal.Peer {
				find = true

				for _, newPerm := range newSignal.Permissions {
					findPerm := false
					for _, perm := range signal.Permissions {
						if newPerm == perm {
							findPerm = true
							break
						}
					}
					if !findPerm {
						apm.Data.DynamicResult.AppArmor.Signals[index].Permissions = append(apm.Data.DynamicResult.AppArmor.Signals[index].Permissions, newPerm)
					}
				}

				for _, newSig := range newSignal.Signals {
					findSig := false
					for _, sig := range signal.Signals {
						if newSig == sig {
							findSig = true
							break
						}
					}
					if !findSig {
						apm.Data.DynamicResult.AppArmor.Signals[index].Signals = append(apm.Data.DynamicResult.AppArmor.Signals[index].Signals, newSig)
					}
				}

				break
			}
		}
		if !find {
			apm.Data.DynamicResult.AppArmor.Signals = append(apm.Data.DynamicResult.AppArmor.Signals, newSignal)
		}
	}

	for _, newUnhandled := range appArmor.Unhandled {
		find := false
		for _, unhandled := range apm.Data.DynamicResult.AppArmor.Unhandled {
			if newUnhandled == unhandled {
				find = true
				break
			}
		}
		if !find {
			apm.Data.DynamicResult.AppArmor.Unhandled = append(apm.Data.DynamicResult.AppArmor.Unhandled, newUnhandled)
		}
	}
}

func MergeSeccompResult(apm *varmor.ArmorProfileModel, seccomp *varmor.Seccomp) {
	if seccomp == nil {
		return
	}

	if apm.Data.DynamicResult.Seccomp == nil {
		apm.Data.DynamicResult.Seccomp = &varmor.Seccomp{}
	}

	for _, newSyscall := range seccomp.Syscalls {
		find := false
		for _, syscall := range apm.Data.DynamicResult.Seccomp.Syscalls {
			if newSyscall == syscall {
				find = true
				break
			}
		}
		if !find {
			apm.Data.DynamicResult.Seccomp.Syscalls = append(apm.Data.DynamicResult.Seccomp.Syscalls, newSyscall)
		}
	}
}
