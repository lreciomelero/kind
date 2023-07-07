package workloadcluster

import (
	"context"
	"os/exec"

	"sigs.k8s.io/kind/pkg/log"

	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/kind/pkg/cluster/internal/delete/actions"
	"sigs.k8s.io/kind/pkg/commons"
	"sigs.k8s.io/kind/pkg/errors"
)

// Action implements and action for configuring and starting the
// external load balancer in front of the control-plane nodes.
type Action struct {
	descriptorPath         string
	explicitKubeconfigPath string
	workloadKubeconfigPath string
	logger                 log.Logger
	ctx                    *actions.ActionContext
}

// NewAction returns a new Action for configuring the load balancer
func NewAction(logger log.Logger, descriptorPath string, explicitKubeconfigPath string, workloadKubeconfigPath string, ctx *actions.ActionContext) actions.Action {
	return &Action{
		descriptorPath:         descriptorPath,
		explicitKubeconfigPath: explicitKubeconfigPath,
		workloadKubeconfigPath: workloadKubeconfigPath,
		logger:                 logger,
		ctx:                    ctx,
	}
}

// Execute runs the action
func (a *Action) Execute(opts actions.ClusterOptions) error {
	var err error

	a.ctx.Status.Start("Finding cluster object in local cluster 🔎")
	defer a.ctx.Status.End(false)

	// Parse the cluster descriptor
	keosCluster, err := commons.GetClusterDescriptor(a.descriptorPath)
	if err != nil {
		return errors.Wrap(err, "failed to parse cluster descriptor")
	}

	kindConfig, err := clientcmd.BuildConfigFromFlags("", a.explicitKubeconfigPath)
	if err != nil {
		return errors.Wrap(err, "Failed to get kindConfig: ")
	}

	dynamicClient, err := dynamic.NewForConfig(kindConfig)
	if err != nil {
		return errors.Wrap(err, "Failed to get dynamicClient: ")
	}

	gvr := schema.GroupVersionResource{
		Group:    "cluster.x-k8s.io",
		Version:  "v1beta1",
		Resource: "clusters",
	}
	resource := dynamicClient.Resource(gvr)
	obj, err := resource.Namespace("cluster-"+keosCluster.Metadata.Name).Get(context.TODO(), keosCluster.Metadata.Name, v1.GetOptions{})

	if err != nil {
		a.ctx.Status.End(true) // End Finding cluster object in local cluster

		a.ctx.Status.Start("Finding cluster object in workload cluster 🔎☁️")
		defer a.ctx.Status.End(false)

		workloadConfig, err := clientcmd.BuildConfigFromFlags("", a.workloadKubeconfigPath)
		if err != nil {
			return errors.Wrap(err, "Failed to get workloadConfig: ")
		}

		workloadDynamicClient, err := dynamic.NewForConfig(workloadConfig)
		if err != nil {
			return errors.Wrap(err, "Failed to get workloadDynamicClient: ")
		}

		resource = workloadDynamicClient.Resource(gvr)
		obj, err = resource.Namespace("cluster-"+keosCluster.Metadata.Name).Get(context.TODO(), keosCluster.Metadata.Name, v1.GetOptions{})
		if err != nil {
			return errors.New("Cluster object: " + keosCluster.Metadata.Name + " not found locally or in workload cluster")
		}
		a.ctx.Status.End(true) // End Finding cluster object in workload cluster

		a.ctx.Status.Start("Moving cluster object from workload cluster to local cluster 👇")
		defer a.ctx.Status.End(false)

		command := exec.Command("clusterctl", "move", "--kubeconfig", a.workloadKubeconfigPath, "--to-kubeconfig", a.explicitKubeconfigPath, "--namespace", obj.GetNamespace())
		_, err = command.CombinedOutput()
		if err != nil {
			return errors.Wrap(err, "Failed to delete cluster object in workload cluster: ")
		}
		a.ctx.Status.End(true) //Moving cluster object from workload cluster to local cluster

	} else {
		a.ctx.Status.End(true) // End Finding cluster object in local cluster
	}

	a.ctx.Status.Start("Deleting cluster object from local cluster 💥")
	defer a.ctx.Status.End(false)

	command := exec.Command("kubectl", "delete", "cluster", "--namespace", obj.GetNamespace(), obj.GetName(), "--kubeconfig", a.explicitKubeconfigPath)
	_, err = command.CombinedOutput()
	if err != nil {
		return errors.Wrap(err, "Failed to delete cluster object in local kind: ")
	}

	a.ctx.Status.End(true) // Deleting cluster object from local cluster
	return nil

}
