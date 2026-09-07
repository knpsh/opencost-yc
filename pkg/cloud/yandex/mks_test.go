package yandex

import (
	"context"
	"errors"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/opencost/opencost/core/pkg/clustercache"
	k8s "github.com/yandex-cloud/go-genproto/yandex/cloud/k8s/v1"
	"google.golang.org/grpc"
)

type fakeNodeGroupClient struct {
	groups map[string]*k8s.NodeGroup
	err    error
	calls  int
}

func (f *fakeNodeGroupClient) Get(_ context.Context, request *k8s.GetNodeGroupRequest, _ ...grpc.CallOption) (*k8s.NodeGroup, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	group := f.groups[request.GetNodeGroupId()]
	if group == nil {
		return nil, errors.New("node group not found")
	}
	return group, nil
}

type fakeClusterClient struct {
	cluster *k8s.Cluster
	err     error
	calls   int
}

func (f *fakeClusterClient) Get(_ context.Context, _ *k8s.GetClusterRequest, _ ...grpc.CallOption) (*k8s.Cluster, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.cluster, nil
}

type fakeResourcePresetClient struct {
	presets map[string]*k8s.ResourcePreset
	err     error
	calls   int
}

func (f *fakeResourcePresetClient) Get(_ context.Context, request *k8s.GetResourcePresetRequest, _ ...grpc.CallOption) (*k8s.ResourcePreset, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	preset := f.presets[request.GetResourcePresetId()]
	if preset == nil {
		return nil, errors.New("preset not found")
	}
	return preset, nil
}

func mksTestProvider(cluster *k8s.Cluster) (*Yandex, *fakeNodeGroupClient, *fakeClusterClient, *fakeResourcePresetClient) {
	nodeGroups := &fakeNodeGroupClient{groups: map[string]*k8s.NodeGroup{
		"group-a": {Id: "group-a", ClusterId: "cluster-a"},
	}}
	clusters := &fakeClusterClient{cluster: cluster}
	presets := &fakeResourcePresetClient{presets: map[string]*k8s.ResourcePreset{
		"s-c2-m8": {Id: "s-c2-m8", Cores: 2, CoreFraction: 100, Memory: 8 * 1024 * 1024 * 1024},
	}}
	provider := &Yandex{
		Clientset: &clustercache.MockClusterCache{Nodes: []*clustercache.Node{
			{Labels: map[string]string{nodeGroupIDLabel: "group-a"}},
			{Labels: map[string]string{nodeGroupIDLabel: "group-a"}},
		}},
		nodeGroups: nodeGroups, clusters: clusters, resourcePresets: presets,
		mapping: testMapping(), prices: map[string]unitPrice{
			"cpu": {Hourly: 1.76}, "ram": {Hourly: 0.46},
		},
		mksPresets: map[string]masterResources{}, now: time.Now,
	}
	return provider, nodeGroups, clusters, presets
}

func autoScaleCluster(status k8s.Cluster_Status, masters, cores, memoryGiB int64) *k8s.Cluster {
	return &k8s.Cluster{Id: "cluster-a", Status: status, Master: &k8s.Master{
		EtcdClusterSize: masters,
		Resources:       &k8s.MasterResources{Cores: cores, CoreFraction: 100, Memory: memoryGiB * 1024 * 1024 * 1024},
		ScalePolicy: &k8s.MasterScalePolicy{ScaleType: &k8s.MasterScalePolicy_AutoScale_{
			AutoScale: &k8s.MasterScalePolicy_AutoScale{MinResourcePresetId: "s-c2-m8"},
		}},
	}}
}

func TestDiscoverMKSClusterID(t *testing.T) {
	provider, nodeGroups, _, _ := mksTestProvider(autoScaleCluster(k8s.Cluster_RUNNING, 1, 2, 8))
	got, err := provider.discoverMKSClusterID(context.Background())
	if err != nil || got != "cluster-a" {
		t.Fatalf("discoverMKSClusterID = %q, %v", got, err)
	}
	if nodeGroups.calls != 1 {
		t.Fatalf("duplicate node-group label caused %d calls, want 1", nodeGroups.calls)
	}
	if _, err := provider.discoverMKSClusterID(context.Background()); err != nil || nodeGroups.calls != 1 {
		t.Fatalf("cached discovery made another API call: calls=%d err=%v", nodeGroups.calls, err)
	}
}

func TestDiscoverMKSClusterIDRejectsMissingAndConflictingLabels(t *testing.T) {
	provider, _, _, _ := mksTestProvider(autoScaleCluster(k8s.Cluster_RUNNING, 1, 2, 8))
	provider.Clientset = &clustercache.MockClusterCache{}
	if _, err := provider.discoverMKSClusterID(context.Background()); err == nil || !strings.Contains(err.Error(), nodeGroupIDLabel) {
		t.Fatalf("missing-label error = %v", err)
	}

	provider, nodeGroups, _, _ := mksTestProvider(autoScaleCluster(k8s.Cluster_RUNNING, 1, 2, 8))
	provider.Clientset = &clustercache.MockClusterCache{Nodes: []*clustercache.Node{
		{Labels: map[string]string{nodeGroupIDLabel: "group-a"}},
		{Labels: map[string]string{nodeGroupIDLabel: "group-b"}},
	}}
	nodeGroups.groups["group-b"] = &k8s.NodeGroup{Id: "group-b", ClusterId: "cluster-b"}
	if _, err := provider.discoverMKSClusterID(context.Background()); err == nil || !strings.Contains(err.Error(), "multiple MKS clusters") {
		t.Fatalf("conflicting-cluster error = %v", err)
	}
}

func TestCalculateMKSPricingMinimumAndScaledResources(t *testing.T) {
	provider, _, clusters, _ := mksTestProvider(autoScaleCluster(k8s.Cluster_RUNNING, 1, 2, 8))
	state, err := provider.calculateMKSPricing(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(state.HourlyCost-7.2) > 1e-12 || state.MasterCount != 1 || state.PresetID != "s-c2-m8" {
		t.Fatalf("minimum MKS state = %#v", state)
	}

	clusters.cluster = autoScaleCluster(k8s.Cluster_RUNNING, 3, 4, 16)
	state, err = provider.calculateMKSPricing(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(state.HourlyCost-43.2) > 1e-12 || state.Billable.Cores != 4 || state.Billable.MemoryBytes != 16*1024*1024*1024 {
		t.Fatalf("scaled MKS state = %#v", state)
	}
}

func TestCalculateMKSPricingAppliesIndependentMinimumFloors(t *testing.T) {
	provider, _, clusters, _ := mksTestProvider(autoScaleCluster(k8s.Cluster_RUNNING, 1, 4, 4))
	state, err := provider.calculateMKSPricing(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state.Billable.Cores != 4 || state.Billable.MemoryBytes != 8*1024*1024*1024 {
		t.Fatalf("billable resources = %#v", state.Billable)
	}
	clusters.cluster.Master.Resources = &k8s.MasterResources{Cores: 1, CoreFraction: 100, Memory: 16 * 1024 * 1024 * 1024}
	state, err = provider.calculateMKSPricing(context.Background())
	if err != nil || state.Billable.Cores != 2 || state.Billable.MemoryBytes != 16*1024*1024*1024 {
		t.Fatalf("independent minimum floors = %#v, %v", state.Billable, err)
	}
}

func TestCalculateMKSPricingFixedStoppedAndPresetCache(t *testing.T) {
	cluster := autoScaleCluster(k8s.Cluster_STOPPED, 3, 2, 8)
	cluster.Master.ScalePolicy = &k8s.MasterScalePolicy{ScaleType: &k8s.MasterScalePolicy_FixedScale_{
		FixedScale: &k8s.MasterScalePolicy_FixedScale{ResourcePresetId: "s-c2-m8"},
	}}
	provider, _, _, presets := mksTestProvider(cluster)
	state, err := provider.calculateMKSPricing(context.Background())
	if err != nil || state.HourlyCost != 0 || state.ClusterStatus != "STOPPED" {
		t.Fatalf("stopped MKS state = %#v, %v", state, err)
	}
	if _, err := provider.calculateMKSPricing(context.Background()); err != nil || presets.calls != 1 {
		t.Fatalf("preset cache calls=%d err=%v", presets.calls, err)
	}
}

func TestCalculateMKSPricingRejectsUnsupportedFractionAndMissingPrices(t *testing.T) {
	provider, _, clusters, _ := mksTestProvider(autoScaleCluster(k8s.Cluster_RUNNING, 1, 2, 8))
	clusters.cluster.Master.Resources.CoreFraction = 50
	if _, err := provider.calculateMKSPricing(context.Background()); err == nil || !strings.Contains(err.Error(), "only 100%") {
		t.Fatalf("unsupported-fraction error = %v", err)
	}
	clusters.cluster.Master.Resources.CoreFraction = 100
	provider.prices = map[string]unitPrice{}
	if _, err := provider.calculateMKSPricing(context.Background()); err == nil || !strings.Contains(err.Error(), "master prices are unavailable") {
		t.Fatalf("missing-price error = %v", err)
	}
}

func TestMKSPricingRetainsLastGoodValue(t *testing.T) {
	provider, _, clusters, _ := mksTestProvider(autoScaleCluster(k8s.Cluster_RUNNING, 1, 2, 8))
	if err := provider.refreshMKSPricing(context.Background()); err != nil {
		t.Fatal(err)
	}
	clusters.err = errors.New("temporary MKS failure")
	if err := provider.refreshMKSPricing(context.Background()); err == nil {
		t.Fatal("expected refresh error")
	}
	provisioner, price, err := provider.ClusterManagementPricing()
	if err != nil || provisioner != "mks" || math.Abs(price-7.2) > 1e-12 {
		t.Fatalf("last-good ClusterManagementPricing = %q, %g, %v", provisioner, price, err)
	}
	status := provider.PricingSourceStatus()[MKSPricingSourceName]
	if !status.Available || !strings.Contains(status.Error, "temporary MKS failure") {
		t.Fatalf("degraded MKS status = %#v", status)
	}
}

func TestClusterManagementPricingUnavailableBeforeRefresh(t *testing.T) {
	provider := &Yandex{}
	provisioner, price, err := provider.ClusterManagementPricing()
	if provisioner != "mks" || price != 0 || err == nil {
		t.Fatalf("unavailable ClusterManagementPricing = %q, %g, %v", provisioner, price, err)
	}
}

func TestLiveMKSPricing(t *testing.T) {
	if os.Getenv("YC_LIVE_TEST") != "1" {
		t.Skip("set YC_LIVE_TEST=1, YC_SERVICE_ACCOUNT_KEY_FILE, and YC_LIVE_MKS_NODE_GROUP_ID to run")
	}
	nodeGroupID := strings.TrimSpace(os.Getenv("YC_LIVE_MKS_NODE_GROUP_ID"))
	if os.Getenv("YC_SERVICE_ACCOUNT_KEY_FILE") == "" || nodeGroupID == "" {
		t.Fatal("YC_SERVICE_ACCOUNT_KEY_FILE and YC_LIVE_MKS_NODE_GROUP_ID are required for the live MKS test")
	}
	provider, err := New(&clustercache.MockClusterCache{Nodes: []*clustercache.Node{{
		Labels: map[string]string{nodeGroupIDLabel: nodeGroupID},
	}}}, nil, "ru-central1", "")
	if err != nil {
		t.Fatal(err)
	}
	provider.refreshInterval = 0
	provider.mksRefreshInterval = 0
	if err := provider.DownloadPricingData(); err != nil {
		t.Fatalf("live Billing refresh: %v", err)
	}
	if got := len(provider.prices); got != 70 {
		t.Fatalf("live mapped SKU count = %d, want 70", got)
	}
	if err := provider.refreshMKSPricing(context.Background()); err != nil {
		t.Fatalf("live MKS refresh: %v", err)
	}
	if provider.mksState.ClusterID == "" || provider.mksState.PresetID == "" || provider.mksState.HourlyCost <= 0 {
		t.Fatalf("incomplete live MKS pricing state: %#v", provider.mksState)
	}
}
