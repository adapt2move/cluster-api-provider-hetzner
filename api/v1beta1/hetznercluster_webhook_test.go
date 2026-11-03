/*
Copyright 2024 The Kubernetes Authors.

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

package v1beta1

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/utils/ptr"
	clusterv1beta1 "sigs.k8s.io/cluster-api/api/core/v1beta1"
)

func validHetznerCluster(lb LoadBalancerSpec) *HetznerCluster {
	return &HetznerCluster{
		Spec: HetznerClusterSpec{
			ControlPlaneLoadBalancer: lb,
			HetznerSecret: HetznerSecretRef{
				Key: HetznerSecretKeyRef{
					HCloudToken: "token",
				},
			},
		},
	}
}

func TestValidateUpdateProxyProtocol(t *testing.T) {
	webhook := &hetznerClusterWebhook{}

	tests := []struct {
		name        string
		oldLB       LoadBalancerSpec
		newLB       LoadBalancerSpec
		expectError bool
	}{
		{
			name:        "disabling proxy protocol is not allowed",
			oldLB:       LoadBalancerSpec{EnableProxyProtocol: true},
			newLB:       LoadBalancerSpec{EnableProxyProtocol: false},
			expectError: true,
		},
		{
			name:        "enabling proxy protocol is allowed",
			oldLB:       LoadBalancerSpec{EnableProxyProtocol: false},
			newLB:       LoadBalancerSpec{EnableProxyProtocol: true},
			expectError: false,
		},
		{
			name:        "keeping proxy protocol enabled is allowed",
			oldLB:       LoadBalancerSpec{EnableProxyProtocol: true},
			newLB:       LoadBalancerSpec{EnableProxyProtocol: true},
			expectError: false,
		},
		{
			name:        "keeping proxy protocol disabled is allowed",
			oldLB:       LoadBalancerSpec{EnableProxyProtocol: false},
			newLB:       LoadBalancerSpec{EnableProxyProtocol: false},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := webhook.ValidateUpdate(context.Background(), validHetznerCluster(tt.oldLB), validHetznerCluster(tt.newLB))
			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "proxy protocol cannot be disabled once enabled")
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestValidateLoadBalancerHealthCheck(t *testing.T) {
	webhook := &hetznerClusterWebhook{}

	tests := []struct {
		name        string
		healthCheck *LoadBalancerHealthCheckSpec
		wantErr     string
	}{
		{
			name:        "no health check is allowed",
			healthCheck: nil,
		},
		{
			name:        "tcp without the http fields is allowed",
			healthCheck: &LoadBalancerHealthCheckSpec{Protocol: "tcp", IntervalSeconds: ptr.To(5)},
		},
		{
			name:        "http with a path is allowed",
			healthCheck: &LoadBalancerHealthCheckSpec{Protocol: "http", Path: ptr.To("/readyz")},
		},
		{
			name:        "https with a domain and a response is allowed",
			healthCheck: &LoadBalancerHealthCheckSpec{Protocol: "https", Domain: ptr.To("example.com"), Response: ptr.To("ok")},
		},
		{
			name:        "tcp with a path is rejected",
			healthCheck: &LoadBalancerHealthCheckSpec{Protocol: "tcp", Path: ptr.To("/readyz")},
			wantErr:     "path must not be set when protocol is tcp",
		},
		{
			name:        "tcp with a domain is rejected",
			healthCheck: &LoadBalancerHealthCheckSpec{Protocol: "tcp", Domain: ptr.To("example.com")},
			wantErr:     "domain must not be set when protocol is tcp",
		},
		{
			name:        "tcp with a response is rejected",
			healthCheck: &LoadBalancerHealthCheckSpec{Protocol: "tcp", Response: ptr.To("ok")},
			wantErr:     "response must not be set when protocol is tcp",
		},
		{
			name:        "tcp with status codes is rejected",
			healthCheck: &LoadBalancerHealthCheckSpec{Protocol: "tcp", StatusCodes: []string{"200"}},
			wantErr:     "statusCodes must not be set when protocol is tcp",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cluster := validHetznerCluster(LoadBalancerSpec{HealthCheck: tt.healthCheck})

			_, err := webhook.ValidateUpdate(context.Background(), validHetznerCluster(LoadBalancerSpec{}), cluster)

			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

// TestValidatePublicInterface checks that a load balancer without public interface needs an enabled
// load balancer and the private network, on create and on update.
func TestValidatePublicInterface(t *testing.T) {
	webhook := &hetznerClusterWebhook{}
	cluster := func(lbEnabled, networkEnabled bool, publicInterface LoadBalancerPublicInterface) *HetznerCluster {
		c := validHetznerCluster(LoadBalancerSpec{
			Enabled:         lbEnabled,
			Region:          "fsn1",
			PublicInterface: publicInterface,
		})
		c.Spec.ControlPlaneRegions = []Region{"fsn1"}
		c.Spec.HCloudNetwork.Enabled = networkEnabled
		if !lbEnabled {
			c.Spec.ControlPlaneEndpoint = &clusterv1beta1.APIEndpoint{Host: "example.com", Port: 6443}
		}
		return c
	}

	tests := []struct {
		name    string
		cluster *HetznerCluster
		wantErr string
	}{
		{name: "not set without network is accepted", cluster: cluster(true, false, "")},
		{name: "enabled without network is accepted", cluster: cluster(true, false, LoadBalancerPublicInterfaceEnabled)},
		{name: "disabled with network is accepted", cluster: cluster(true, true, LoadBalancerPublicInterfaceDisabled)},
		{
			name:    "disabled without network is rejected",
			cluster: cluster(true, false, LoadBalancerPublicInterfaceDisabled),
			wantErr: "publicInterface can only be disabled if hcloudNetwork.enabled is true",
		},
		{
			name:    "disabled without load balancer is rejected",
			cluster: cluster(false, true, LoadBalancerPublicInterfaceDisabled),
			wantErr: "publicInterface can only be disabled if controlPlaneLoadBalancer.enabled is true",
		},
	}

	for _, tt := range tests {
		t.Run("create: "+tt.name, func(t *testing.T) {
			_, err := webhook.ValidateCreate(context.Background(), tt.cluster)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}
		})
		t.Run("update: "+tt.name, func(t *testing.T) {
			_, err := webhook.ValidateUpdate(context.Background(), tt.cluster, tt.cluster)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// TestValidateUpdatePublicInterfaceToggle checks that the public interface can be switched in both
// directions on an existing cluster, and that disabling it warns about an endpoint that is not changed.
func TestValidateUpdatePublicInterfaceToggle(t *testing.T) {
	webhook := &hetznerClusterWebhook{}
	cluster := func(publicInterface LoadBalancerPublicInterface) *HetznerCluster {
		c := validHetznerCluster(LoadBalancerSpec{Enabled: true, PublicInterface: publicInterface})
		c.Spec.HCloudNetwork.Enabled = true
		c.Spec.ControlPlaneEndpoint = &clusterv1beta1.APIEndpoint{Host: "1.2.3.4", Port: 6443}
		return c
	}

	warnings, err := webhook.ValidateUpdate(context.Background(), cluster(""), cluster(LoadBalancerPublicInterfaceDisabled))
	require.NoError(t, err)
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], `spec.controlPlaneEndpoint.host "1.2.3.4" is not changed`)

	warnings, err = webhook.ValidateUpdate(context.Background(), cluster(LoadBalancerPublicInterfaceDisabled), cluster(LoadBalancerPublicInterfaceDisabled))
	require.NoError(t, err)
	require.Empty(t, warnings)

	warnings, err = webhook.ValidateUpdate(context.Background(), cluster(LoadBalancerPublicInterfaceDisabled), cluster(LoadBalancerPublicInterfaceEnabled))
	require.NoError(t, err)
	require.Empty(t, warnings)

	// An empty host is defaulted by the controller, so there is nothing to warn about.
	c := cluster(LoadBalancerPublicInterfaceDisabled)
	c.Spec.ControlPlaneEndpoint = nil
	warnings, err = webhook.ValidateUpdate(context.Background(), cluster(""), c)
	require.NoError(t, err)
	require.Empty(t, warnings)
}
