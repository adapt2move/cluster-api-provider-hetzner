/*
Copyright 2026 The Kubernetes Authors.

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

package loadbalancer

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/go-logr/logr"
	"github.com/hetznercloud/hcloud-go/v2/hcloud"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	clusterv1beta1 "sigs.k8s.io/cluster-api/api/core/v1beta1"
	v1beta2conditions "sigs.k8s.io/cluster-api/util/deprecated/v1beta1/conditions/v1beta2"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	infrav1 "github.com/syself/cluster-api-provider-hetzner/api/v1beta1"
	"github.com/syself/cluster-api-provider-hetzner/pkg/scope"
	"github.com/syself/cluster-api-provider-hetzner/pkg/services/hcloud/client/mocks"
)

const (
	testKubeAPIListenPort = 443
	testLBDestPort        = 6443
)

// Events go through the global recorder of sigs.k8s.io/cluster-api/util/record in this release,
// which cannot be swapped per test, so these tests check the HCloud calls and the conditions.
func newPublicInterfaceTestService(t *testing.T, publicInterface infrav1.LoadBalancerPublicInterface, mockClient *mocks.Client) *Service {
	t.Helper()
	hetznerCluster := &infrav1.HetznerCluster{
		Spec: infrav1.HetznerClusterSpec{
			ControlPlaneLoadBalancer: infrav1.LoadBalancerSpec{
				Port:            testLBDestPort,
				PublicInterface: publicInterface,
			},
			ControlPlaneEndpoint: &clusterv1beta1.APIEndpoint{Port: testKubeAPIListenPort},
		},
		Status: infrav1.HetznerClusterStatus{
			ControlPlaneLoadBalancer: &infrav1.LoadBalancerStatus{},
		},
	}
	return &Service{scope: &scope.ClusterScope{
		HetznerCluster: hetznerCluster,
		HCloudClient:   mockClient,
	}}
}

func attachedLB(publicEnabled bool) *hcloud.LoadBalancer {
	return &hcloud.LoadBalancer{
		ID:         1,
		PublicNet:  hcloud.LoadBalancerPublicNet{Enabled: publicEnabled},
		PrivateNet: []hcloud.LoadBalancerPrivateNet{{IP: net.ParseIP("10.0.0.2")}},
	}
}

func TestReconcilePublicInterface_DisabledInSpec_DisablesPublicInterface(t *testing.T) {
	mockClient := &mocks.Client{}
	svc := newPublicInterfaceTestService(t, infrav1.LoadBalancerPublicInterfaceDisabled, mockClient)
	lb := attachedLB(true)

	mockClient.On("DisableLoadBalancerPublicInterface", mock.Anything, lb).Return(nil).Once()

	res, err := svc.reconcilePublicInterface(context.Background(), lb)
	require.NoError(t, err)
	require.Equal(t, reconcile.Result{}, res)
	mockClient.AssertExpectations(t)
}

func TestReconcilePublicInterface_EnabledInSpec_EnablesPublicInterface(t *testing.T) {
	mockClient := &mocks.Client{}
	svc := newPublicInterfaceTestService(t, infrav1.LoadBalancerPublicInterfaceEnabled, mockClient)
	lb := attachedLB(false)

	mockClient.On("EnableLoadBalancerPublicInterface", mock.Anything, lb).Return(nil).Once()

	res, err := svc.reconcilePublicInterface(context.Background(), lb)
	require.NoError(t, err)
	require.Equal(t, reconcile.Result{}, res)
	mockClient.AssertExpectations(t)
}

func TestReconcilePublicInterface_AlreadyInDesiredState_NoChanges(t *testing.T) {
	tests := []struct {
		name            string
		publicInterface infrav1.LoadBalancerPublicInterface
		publicEnabled   bool
	}{
		{name: "disabled in spec and on the load balancer", publicInterface: infrav1.LoadBalancerPublicInterfaceDisabled, publicEnabled: false},
		{name: "enabled in spec and on the load balancer", publicInterface: infrav1.LoadBalancerPublicInterfaceEnabled, publicEnabled: true},
		// Not set means CAPH does not manage the public interface of an existing load balancer.
		{name: "not set, public interface enabled", publicInterface: "", publicEnabled: true},
		{name: "not set, public interface disabled", publicInterface: "", publicEnabled: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// The mock has no expectations, so any call to the HCloud API fails the test.
			mockClient := &mocks.Client{}
			svc := newPublicInterfaceTestService(t, tt.publicInterface, mockClient)

			res, err := svc.reconcilePublicInterface(context.Background(), attachedLB(tt.publicEnabled))
			require.NoError(t, err)
			require.Equal(t, reconcile.Result{}, res)
			mockClient.AssertExpectations(t)
		})
	}
}

func TestReconcilePublicInterface_DisabledInSpec_WaitsForNetworkAttachment(t *testing.T) {
	mockClient := &mocks.Client{}
	svc := newPublicInterfaceTestService(t, infrav1.LoadBalancerPublicInterfaceDisabled, mockClient)
	lb := &hcloud.LoadBalancer{ID: 1, PublicNet: hcloud.LoadBalancerPublicNet{Enabled: true}}

	res, err := svc.reconcilePublicInterface(context.Background(), lb)
	require.NoError(t, err)
	require.NotZero(t, res.RequeueAfter)
	mockClient.AssertExpectations(t)

	hc := svc.scope.HetznerCluster
	require.True(t, v1beta2conditions.IsFalse(hc, infrav1.HetznerClusterLoadBalancerReadyV1Beta2Condition))
	require.Equal(t, infrav1.HetznerClusterLoadBalancerWaitingForNetworkV1Beta2Reason, v1beta2conditions.Get(hc, infrav1.HetznerClusterLoadBalancerReadyV1Beta2Condition).Reason)
}

func TestReconcilePublicInterface_APIError_SetsConditionAndReturnsError(t *testing.T) {
	mockClient := &mocks.Client{}
	svc := newPublicInterfaceTestService(t, infrav1.LoadBalancerPublicInterfaceDisabled, mockClient)
	lb := attachedLB(true)

	mockClient.On("DisableLoadBalancerPublicInterface", mock.Anything, lb).Return(errors.New("boom")).Once()

	_, err := svc.reconcilePublicInterface(context.Background(), lb)
	require.ErrorContains(t, err, "failed to disable public interface of load balancer")

	hc := svc.scope.HetznerCluster
	require.True(t, v1beta2conditions.IsFalse(hc, infrav1.HetznerClusterLoadBalancerReadyV1Beta2Condition))
	require.Equal(t, infrav1.HetznerClusterLoadBalancerUpdateFailedV1Beta2Reason, v1beta2conditions.Get(hc, infrav1.HetznerClusterLoadBalancerReadyV1Beta2Condition).Reason)
	mockClient.AssertExpectations(t)
}

// TestReconcile_PublicInterfaceDisabled_WaitsForNetworkBeforeCreating checks that no load balancer
// is created without public interface while the network status is missing, because Hetzner rejects
// such a load balancer if it is not attached to a network.
func TestReconcile_PublicInterfaceDisabled_WaitsForNetworkBeforeCreating(t *testing.T) {
	mockClient := &mocks.Client{}
	svc := newPublicInterfaceTestService(t, infrav1.LoadBalancerPublicInterfaceDisabled, mockClient)
	hc := svc.scope.HetznerCluster
	hc.Name = "cluster"
	hc.Spec.ControlPlaneLoadBalancer.Enabled = true
	hc.Spec.HCloudNetwork.Enabled = true
	hc.Status.Network = nil

	mockClient.On("ListLoadBalancers", mock.Anything, mock.Anything).Return([]*hcloud.LoadBalancer{}, nil)

	res, err := svc.Reconcile(context.Background())
	require.NoError(t, err)
	require.NotZero(t, res.RequeueAfter)
	mockClient.AssertNotCalled(t, "CreateLoadBalancer", mock.Anything, mock.Anything)
	require.Equal(t, infrav1.HetznerClusterLoadBalancerWaitingForNetworkV1Beta2Reason, v1beta2conditions.Get(hc, infrav1.HetznerClusterLoadBalancerReadyV1Beta2Condition).Reason)
	mockClient.AssertExpectations(t)
}

// TestReconcile_ExistingLB_DisablesPublicInterfaceAfterNetworkAttachment runs the whole Reconcile
// for an existing load balancer that has a public interface and is attached to the network. The
// public interface gets disabled, and the load balancer is reported as ready.
func TestReconcile_ExistingLB_DisablesPublicInterfaceAfterNetworkAttachment(t *testing.T) {
	mockClient := &mocks.Client{}
	svc := newPublicInterfaceTestService(t, infrav1.LoadBalancerPublicInterfaceDisabled, mockClient)
	hc := svc.scope.HetznerCluster
	hc.Name = "cluster"
	hc.Spec.ControlPlaneLoadBalancer.Enabled = true
	hc.Spec.ControlPlaneLoadBalancer.Type = "lb11"
	hc.Spec.ControlPlaneLoadBalancer.Algorithm = infrav1.LoadBalancerAlgorithmTypeRoundRobin
	hc.Spec.HCloudNetwork.Enabled = true
	hc.Status.Network = &infrav1.NetworkStatus{ID: 4711}

	lb := attachedLB(true)
	lb.LoadBalancerType = &hcloud.LoadBalancerType{Name: "lb11"}
	lb.Algorithm = hcloud.LoadBalancerAlgorithm{Type: hcloud.LoadBalancerAlgorithmTypeRoundRobin}
	lb.PublicNet.IPv4.IP = net.ParseIP("1.2.3.4")
	lb.Services = []hcloud.LoadBalancerService{{ListenPort: testKubeAPIListenPort, DestinationPort: testLBDestPort}}

	mockClient.On("ListLoadBalancers", mock.Anything, mock.Anything).Return([]*hcloud.LoadBalancer{lb}, nil)
	mockClient.On("DisableLoadBalancerPublicInterface", mock.Anything, lb).Return(nil).Once()

	res, err := svc.Reconcile(context.Background())
	require.NoError(t, err)
	require.Equal(t, reconcile.Result{}, res)
	require.Equal(t, "10.0.0.2", hc.Status.ControlPlaneLoadBalancer.InternalIP)
	require.True(t, v1beta2conditions.IsTrue(hc, infrav1.HetznerClusterLoadBalancerReadyV1Beta2Condition))
	mockClient.AssertExpectations(t)
}

func TestStatusFromHCloudLB_NoPublicIP_LeavesIPsEmpty(t *testing.T) {
	lb := &hcloud.LoadBalancer{
		PrivateNet: []hcloud.LoadBalancerPrivateNet{{IP: net.ParseIP("10.0.0.2")}},
	}

	sts := statusFromHCloudLB(lb, true, testKubeAPIListenPort, logr.Discard())
	require.Empty(t, sts.IPv4)
	require.Empty(t, sts.IPv6)
	require.Equal(t, "10.0.0.2", sts.InternalIP)
}
