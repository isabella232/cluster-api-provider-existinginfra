package recipe

import (
	"fmt"

	"github.com/weaveworks/cluster-api-provider-existinginfra/pkg/plan"
	"github.com/weaveworks/cluster-api-provider-existinginfra/pkg/plan/resource"
	"github.com/weaveworks/cluster-api-provider-existinginfra/pkg/utilities/object"
	"github.com/weaveworks/cluster-api-provider-existinginfra/pkg/utilities/version"
)

type NodeType int

const (
	OriginalMaster NodeType = iota
	SecondaryMaster
	Worker
)

// BuildUpgradePlan creates a sub-plan to run upgrade using respective package management commands.
func BuildUpgradePlan(pkgType resource.PkgType, k8sVersion string, ntype NodeType) (plan.Resource, error) {
	b := plan.NewBuilder()

	// install new packages - kubelet and kubectl need to be installed before kubeadm
	switch pkgType {
	case resource.PkgTypeRPM, resource.PkgTypeRHEL:
		b.AddResource(
			"upgrade:node-unlock-kubernetes",
			&resource.Run{Script: object.String("yum versionlock delete 'kube*' || true")})
		b.AddResource(
			"upgrade:node-kubelet",
			&resource.RPM{Name: "kubelet", Version: k8sVersion, DisableExcludes: "kubernetes"},
			plan.DependOn("upgrade:node-unlock-kubernetes"))
		b.AddResource(
			"upgrade:node-kubectl",
			&resource.RPM{Name: "kubectl", Version: k8sVersion, DisableExcludes: "kubernetes"},
			plan.DependOn("upgrade:node-kubelet"))
		b.AddResource(
			"upgrade:node-install-kubeadm",
			&resource.RPM{Name: "kubeadm", Version: k8sVersion, DisableExcludes: "kubernetes"},
			plan.DependOn("upgrade:node-kubectl"))
		b.AddResource(
			"upgrade:node-lock-kubernetes",
			&resource.Run{Script: object.String("yum versionlock add 'kube*' || true")},
			plan.DependOn("upgrade:node-install-kubeadm"))
	case resource.PkgTypeDeb:
		b.AddResource(
			"upgrade:node-unlock-kubernetes",
			&resource.Run{Script: object.String("apt-mark unhold 'kube*' || true")})
		b.AddResource(
			"upgrade:node-kubelet",
			&resource.Deb{Name: "kubelet", Suffix: "=" + k8sVersion + "-00"},
			plan.DependOn("upgrade:node-unlock-kubernetes"))
		b.AddResource(
			"upgrade:node-kubectl",
			&resource.Deb{Name: "kubectl", Suffix: "=" + k8sVersion + "-00"},
			plan.DependOn("upgrade:node-kubelet"))
		b.AddResource(
			"upgrade:node-install-kubeadm",
			&resource.Deb{Name: "kubeadm", Suffix: "=" + k8sVersion + "-00"},
			plan.DependOn("upgrade:node-kubectl"))
		b.AddResource(
			"upgrade:node-lock-kubernetes",
			&resource.Run{Script: object.String("apt-mark hold 'kube*' || true")},
			plan.DependOn("upgrade:node-install-kubeadm"))
	}
	//
	// For secondary masters
	// version >= 1.16.0 uses: kubeadm upgrade node
	// version >= 1.14.0 && < 1.16.0 uses: kubeadm upgrade node experimental-control-plane
	//
	secondaryMasterUpgradeControlPlaneFlag := ""
	if lt, err := version.LessThan(k8sVersion, "v1.16.0"); err == nil && lt {
		secondaryMasterUpgradeControlPlaneFlag = "experimental-control-plane"
	}

	switch ntype {
	case OriginalMaster:
		b.AddResource(
			"upgrade:node-kubeadm-upgrade",
			&resource.Run{Script: object.String(fmt.Sprintf("kubeadm upgrade plan && kubeadm upgrade apply -y %s", k8sVersion))},
			plan.DependOn("upgrade:node-install-kubeadm"))
	case SecondaryMaster:
		b.AddResource(
			"upgrade:node-kubeadm-upgrade",
			&resource.Run{Script: object.String(fmt.Sprintf("kubeadm upgrade node %s", secondaryMasterUpgradeControlPlaneFlag))},
			plan.DependOn("upgrade:node-install-kubeadm"))
	case Worker:
		b.AddResource(
			"upgrade:node-kubeadm-upgrade",
			// From kubeadm upgrade node phase kubelet-config --help
			// > kubeadm uses the KuberneteVersion field in the kubeadm-config ConfigMap to determine what the _desired_ kubelet version is.
			&resource.Run{Script: object.String("kubeadm upgrade node phase kubelet-config")},
			plan.DependOn("upgrade:node-install-kubeadm"))
	}

	switch pkgType {
	case resource.PkgTypeRPM, resource.PkgTypeRHEL:
		b.AddResource(
			"upgrade:node-restart-kubelet",
			&resource.Run{Script: object.String("systemctl restart kubelet")},
			plan.DependOn("upgrade:node-kubeadm-upgrade"))
	case resource.PkgTypeDeb:
		b.AddResource(
			"upgrade:node-restart-kubelet",
			&resource.Run{Script: object.String("systemctl restart kubelet")},
			plan.DependOn("upgrade:node-kubeadm-upgrade"))
	}

	p, err := b.Plan()
	if err != nil {
		return nil, err
	}
	return &p, nil
}
