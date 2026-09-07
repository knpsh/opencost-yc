package yandex

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/opencost/opencost/core/pkg/log"
	k8s "github.com/yandex-cloud/go-genproto/yandex/cloud/k8s/v1"
	"google.golang.org/grpc"
)

const (
	MKSPricingSourceName = "Yandex Cloud Managed Kubernetes API"
	mksProvisioner       = "mks"
	defaultMKSRefresh    = time.Minute
	nodeGroupIDLabel     = "yandex.cloud/node-group-id"
)

type nodeGroupClient interface {
	Get(context.Context, *k8s.GetNodeGroupRequest, ...grpc.CallOption) (*k8s.NodeGroup, error)
}

type clusterClient interface {
	Get(context.Context, *k8s.GetClusterRequest, ...grpc.CallOption) (*k8s.Cluster, error)
}

type resourcePresetClient interface {
	Get(context.Context, *k8s.GetResourcePresetRequest, ...grpc.CallOption) (*k8s.ResourcePreset, error)
}

type masterResources struct {
	Cores        int64 `json:"cores"`
	CoreFraction int64 `json:"coreFraction"`
	MemoryBytes  int64 `json:"memoryBytes"`
}

type mksPricingState struct {
	Available     bool            `json:"available"`
	ClusterID     string          `json:"clusterID"`
	ClusterStatus string          `json:"clusterStatus"`
	PresetID      string          `json:"presetID"`
	MasterCount   int64           `json:"masterCount"`
	Current       masterResources `json:"current"`
	Minimum       masterResources `json:"minimum"`
	Billable      masterResources `json:"billable"`
	HourlyCost    float64         `json:"hourlyCost"`
	LastRefresh   time.Time       `json:"lastRefresh"`
	LastError     string          `json:"lastError,omitempty"`
}

func (y *Yandex) startMKSRefreshLoop() {
	if y.Clientset == nil || y.mksRefreshInterval <= 0 {
		return
	}
	y.mksRefreshOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		if err := y.refreshMKSPricing(ctx); err != nil {
			log.Warnf("Yandex Cloud: initial MKS master pricing refresh failed: %v", err)
		}
		cancel()

		go func() {
			ticker := time.NewTicker(y.mksRefreshInterval)
			defer ticker.Stop()
			for range ticker.C {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				err := y.refreshMKSPricing(ctx)
				cancel()
				if err != nil {
					log.Warnf("Yandex Cloud: MKS master pricing refresh failed; retaining last successful price: %v", err)
				}
			}
		}()
	})
}

func (y *Yandex) refreshMKSPricing(ctx context.Context) error {
	state, err := y.calculateMKSPricing(ctx)
	y.mu.Lock()
	defer y.mu.Unlock()
	if err != nil {
		y.mksState.LastError = err.Error()
		return err
	}
	state.Available = true
	state.LastRefresh = y.now()
	state.LastError = ""
	y.mksState = state
	log.Infof("Yandex Cloud: MKS master pricing refreshed: cluster=%s status=%s masters=%d current=%dvCPU/%gGiB minimum=%s(%dvCPU/%gGiB) cost=%g %s/hour",
		state.ClusterID, state.ClusterStatus, state.MasterCount,
		state.Current.Cores, bytesToGiB(state.Current.MemoryBytes), state.PresetID,
		state.Minimum.Cores, bytesToGiB(state.Minimum.MemoryBytes), state.HourlyCost, billingCurrency())
	return nil
}

func (y *Yandex) calculateMKSPricing(ctx context.Context) (mksPricingState, error) {
	clusterID, err := y.discoverMKSClusterID(ctx)
	if err != nil {
		return mksPricingState{}, err
	}
	if y.clusters == nil || y.resourcePresets == nil {
		return mksPricingState{}, fmt.Errorf("Yandex Cloud: MKS API clients are unavailable")
	}
	cluster, err := y.clusters.Get(ctx, &k8s.GetClusterRequest{ClusterId: clusterID})
	if err != nil {
		return mksPricingState{}, fmt.Errorf("Yandex Cloud: get MKS cluster %s: %w", clusterID, err)
	}
	master := cluster.GetMaster()
	if master == nil || master.GetResources() == nil {
		return mksPricingState{}, fmt.Errorf("Yandex Cloud: MKS cluster %s has no master resource data", clusterID)
	}
	current := masterResources{
		Cores:        master.GetResources().GetCores(),
		CoreFraction: master.GetResources().GetCoreFraction(),
		MemoryBytes:  master.GetResources().GetMemory(),
	}
	if err := validateMasterResources("current", current); err != nil {
		return mksPricingState{}, err
	}
	if master.GetEtcdClusterSize() <= 0 {
		return mksPricingState{}, fmt.Errorf("Yandex Cloud: MKS cluster %s has invalid master count %d", clusterID, master.GetEtcdClusterSize())
	}

	presetID, err := masterPresetID(master.GetScalePolicy())
	if err != nil {
		return mksPricingState{}, err
	}
	minimum, err := y.getMasterPreset(ctx, presetID)
	if err != nil {
		return mksPricingState{}, err
	}
	if err := validateMasterResources("minimum preset", minimum); err != nil {
		return mksPricingState{}, err
	}

	billable := masterResources{
		Cores:        max(current.Cores, minimum.Cores),
		CoreFraction: 100,
		MemoryBytes:  max(current.MemoryBytes, minimum.MemoryBytes),
	}
	state := mksPricingState{
		ClusterID: clusterID, ClusterStatus: cluster.GetStatus().String(), PresetID: presetID,
		MasterCount: master.GetEtcdClusterSize(), Current: current, Minimum: minimum, Billable: billable,
	}
	if cluster.GetStatus() == k8s.Cluster_STOPPED {
		return state, nil
	}

	y.mu.RLock()
	cpuPrice, cpuOK := y.prices[y.mapping.MasterSKUs.CPU]
	ramPrice, ramOK := y.prices[y.mapping.MasterSKUs.RAM]
	y.mu.RUnlock()
	if !cpuOK || !ramOK {
		return mksPricingState{}, fmt.Errorf("Yandex Cloud: mapped MKS master prices are unavailable; refresh the Billing SKU catalog")
	}
	state.HourlyCost = float64(state.MasterCount) * (float64(billable.Cores)*cpuPrice.Hourly + bytesToGiB(billable.MemoryBytes)*ramPrice.Hourly)
	return state, nil
}

func (y *Yandex) discoverMKSClusterID(ctx context.Context) (string, error) {
	y.mu.RLock()
	clusterID := y.mksState.ClusterID
	y.mu.RUnlock()
	if clusterID != "" {
		return clusterID, nil
	}
	if y.Clientset == nil {
		return "", fmt.Errorf("Yandex Cloud: Kubernetes cluster cache is unavailable for MKS discovery")
	}
	if y.nodeGroups == nil {
		return "", fmt.Errorf("Yandex Cloud: MKS node-group client is unavailable")
	}

	ids := map[string]struct{}{}
	for _, node := range y.Clientset.GetAllNodes() {
		if node == nil {
			continue
		}
		if id := strings.TrimSpace(node.Labels[nodeGroupIDLabel]); id != "" {
			ids[id] = struct{}{}
		}
	}
	if len(ids) == 0 {
		return "", fmt.Errorf("Yandex Cloud: no %s labels found for MKS cluster discovery", nodeGroupIDLabel)
	}
	ordered := make([]string, 0, len(ids))
	for id := range ids {
		ordered = append(ordered, id)
	}
	sort.Strings(ordered)
	for _, id := range ordered {
		nodeGroup, err := y.nodeGroups.Get(ctx, &k8s.GetNodeGroupRequest{NodeGroupId: id})
		if err != nil {
			return "", fmt.Errorf("Yandex Cloud: get MKS node group %s: %w", id, err)
		}
		resolved := strings.TrimSpace(nodeGroup.GetClusterId())
		if resolved == "" {
			return "", fmt.Errorf("Yandex Cloud: MKS node group %s has no cluster ID", id)
		}
		if clusterID != "" && clusterID != resolved {
			return "", fmt.Errorf("Yandex Cloud: node groups resolve to multiple MKS clusters: %s and %s", clusterID, resolved)
		}
		clusterID = resolved
	}
	y.mu.Lock()
	y.mksState.ClusterID = clusterID
	y.mu.Unlock()
	return clusterID, nil
}

func (y *Yandex) getMasterPreset(ctx context.Context, id string) (masterResources, error) {
	y.mu.RLock()
	preset, ok := y.mksPresets[id]
	y.mu.RUnlock()
	if ok {
		return preset, nil
	}
	resourcePreset, err := y.resourcePresets.Get(ctx, &k8s.GetResourcePresetRequest{ResourcePresetId: id})
	if err != nil {
		return masterResources{}, fmt.Errorf("Yandex Cloud: get MKS master resource preset %s: %w", id, err)
	}
	preset = masterResources{Cores: resourcePreset.GetCores(), CoreFraction: resourcePreset.GetCoreFraction(), MemoryBytes: resourcePreset.GetMemory()}
	y.mu.Lock()
	if y.mksPresets == nil {
		y.mksPresets = map[string]masterResources{}
	}
	y.mksPresets[id] = preset
	y.mu.Unlock()
	return preset, nil
}

func masterPresetID(policy *k8s.MasterScalePolicy) (string, error) {
	if policy == nil {
		return "", fmt.Errorf("Yandex Cloud: MKS master scale policy is unavailable")
	}
	id := ""
	if fixed := policy.GetFixedScale(); fixed != nil {
		id = fixed.GetResourcePresetId()
	} else if auto := policy.GetAutoScale(); auto != nil {
		id = auto.GetMinResourcePresetId()
	}
	if strings.TrimSpace(id) == "" {
		return "", fmt.Errorf("Yandex Cloud: MKS master scale policy has no resource preset ID")
	}
	return id, nil
}

func validateMasterResources(name string, resources masterResources) error {
	if resources.Cores <= 0 || resources.MemoryBytes <= 0 {
		return fmt.Errorf("Yandex Cloud: MKS %s resources are invalid: cores=%d memoryBytes=%d", name, resources.Cores, resources.MemoryBytes)
	}
	if resources.CoreFraction != 100 {
		return fmt.Errorf("Yandex Cloud: MKS %s core fraction %d is unsupported; only 100%% is supported", name, resources.CoreFraction)
	}
	return nil
}

func bytesToGiB(value int64) float64 {
	return float64(value) / (1024 * 1024 * 1024)
}
