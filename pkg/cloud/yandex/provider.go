package yandex

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/opencost/opencost/core/pkg/clustercache"
	coreenv "github.com/opencost/opencost/core/pkg/env"
	"github.com/opencost/opencost/core/pkg/log"
	"github.com/opencost/opencost/core/pkg/opencost"
	"github.com/opencost/opencost/core/pkg/util"
	corejson "github.com/opencost/opencost/core/pkg/util/json"
	"github.com/opencost/opencost/pkg/cloud/models"
	cloudutils "github.com/opencost/opencost/pkg/cloud/utils"
	"github.com/opencost/opencost/pkg/env"
	ycsdk "github.com/yandex-cloud/go-sdk"
	"github.com/yandex-cloud/go-sdk/iamkey"
)

const (
	PricingSourceName       = "Yandex Cloud Billing SKU API"
	defaultAPIEndpoint      = "api.cloud.yandex.net:443"
	defaultCurrency         = "RUB"
	defaultRefreshInterval  = 6 * time.Hour
	serviceAccountCheckName = "Yandex Cloud service account key"
	preemptibleLabel        = "yandex.cloud/preemptible"
	coreFractionLabel       = "yandex.cloud/core-fraction"
	nvidiaGPUResource       = "nvidia.com/gpu"
	nvidiaGPUCountLabel     = "nvidia.com/gpu.count"
	nvidiaGPUReplicasLabel  = "nvidia.com/gpu.replicas"
)

type Yandex struct {
	Clientset        clustercache.ClusterCache
	Config           models.ProviderConfig
	ClusterRegion    string
	ClusterAccountID string

	mu                 sync.RWMutex
	client             skuClient
	nodeGroups         nodeGroupClient
	clusters           clusterClient
	resourcePresets    resourcePresetClient
	sdk                *ycsdk.SDK
	mapping            SKUMapping
	prices             map[string]unitPrice
	lastRefresh        time.Time
	lastError          string
	refreshInterval    time.Duration
	refreshOnce        sync.Once
	mksRefreshInterval time.Duration
	mksRefreshOnce     sync.Once
	mksState           mksPricingState
	mksPresets         map[string]masterResources
	now                func() time.Time
}

func New(cache clustercache.ClusterCache, config models.ProviderConfig, region, accountID string) (*Yandex, error) {
	mapping, err := loadMapping(os.Getenv(env.YandexSKUMappingFileEnvVar))
	if err != nil {
		return nil, err
	}
	refreshInterval := defaultRefreshInterval
	if value := strings.TrimSpace(os.Getenv(env.YandexPricingRefreshIntervalEnvVar)); value != "" {
		refreshInterval, err = time.ParseDuration(value)
		if err != nil || refreshInterval <= 0 {
			return nil, fmt.Errorf("invalid %s %q", env.YandexPricingRefreshIntervalEnvVar, value)
		}
	}
	mksRefreshInterval := defaultMKSRefresh
	if value := strings.TrimSpace(os.Getenv(env.YandexMKSRefreshIntervalEnvVar)); value != "" {
		mksRefreshInterval, err = time.ParseDuration(value)
		if err != nil || mksRefreshInterval <= 0 {
			return nil, fmt.Errorf("invalid %s %q", env.YandexMKSRefreshIntervalEnvVar, value)
		}
	}
	return &Yandex{
		Clientset:          cache,
		Config:             config,
		ClusterRegion:      region,
		ClusterAccountID:   accountID,
		mapping:            mapping,
		prices:             map[string]unitPrice{},
		refreshInterval:    refreshInterval,
		mksRefreshInterval: mksRefreshInterval,
		mksPresets:         map[string]masterResources{},
		now:                time.Now,
	}, nil
}

func (y *Yandex) ensureClient(ctx context.Context) (skuClient, error) {
	y.mu.Lock()
	defer y.mu.Unlock()
	if y.client != nil {
		return y.client, nil
	}
	keyFile := strings.TrimSpace(os.Getenv(env.YandexServiceAccountKeyFileEnvVar))
	if keyFile == "" {
		return nil, fmt.Errorf("%s must point to an authorized service-account key", env.YandexServiceAccountKeyFileEnvVar)
	}
	key, err := iamkey.ReadFromJSONFile(keyFile)
	if err != nil {
		return nil, fmt.Errorf("read Yandex service-account key: %w", err)
	}
	credentials, err := ycsdk.ServiceAccountKey(key)
	if err != nil {
		return nil, fmt.Errorf("create Yandex service-account credentials: %w", err)
	}
	sdk, err := ycsdk.Build(ctx, ycsdk.Config{Credentials: credentials, Endpoint: apiEndpoint()})
	if err != nil {
		return nil, fmt.Errorf("initialize Yandex Cloud SDK: %w", err)
	}
	y.sdk = sdk
	y.client = sdk.Billing().Sku()
	y.nodeGroups = sdk.Kubernetes().NodeGroup()
	y.clusters = sdk.Kubernetes().Cluster()
	y.resourcePresets = sdk.Kubernetes().ResourcePreset()
	return y.client, nil
}

func (y *Yandex) DownloadPricingData() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	client, err := y.ensureClient(ctx)
	if err == nil {
		catalog, fetchErr := downloadCatalog(ctx, client, billingCurrency(), billingAccountID(), y.mapping, y.now())
		if fetchErr == nil {
			y.mu.Lock()
			y.prices = catalog
			y.lastRefresh = y.now()
			y.lastError = ""
			y.mu.Unlock()
			log.Infof("Yandex Cloud: loaded %d mapped Billing SKU prices in %s", len(catalog), billingCurrency())
		} else {
			err = fetchErr
		}
	}
	if err != nil {
		y.mu.Lock()
		y.lastError = err.Error()
		y.mu.Unlock()
	}
	y.startRefreshLoop()
	y.startMKSRefreshLoop()
	return err
}

func (y *Yandex) startRefreshLoop() {
	if y.refreshInterval <= 0 {
		return
	}
	y.refreshOnce.Do(func() {
		go func() {
			ticker := time.NewTicker(y.refreshInterval)
			defer ticker.Stop()
			for range ticker.C {
				if err := y.DownloadPricingData(); err != nil {
					log.Warnf("Yandex Cloud: Billing SKU refresh failed; retaining last successful prices: %v", err)
				}
			}
		}()
	})
}

type yandexKey struct {
	providerID   string
	instanceType string
	region       string
	platform     string
	coreFraction int
	preemptible  bool
	vcpu         float64
	ramBytes     int64
	gpuType      string
	gpuCount     int
}

func (k *yandexKey) ID() string      { return k.providerID }
func (k *yandexKey) GPUType() string { return k.gpuType }
func (k *yandexKey) GPUCount() int   { return k.gpuCount }
func (k *yandexKey) Features() string {
	return fmt.Sprintf("%s,%s,%d,%t", k.region, k.platform, k.coreFraction, k.preemptible)
}

func (y *Yandex) GetKey(labels map[string]string, node *clustercache.Node) models.Key {
	instanceType, _ := util.GetInstanceType(labels)
	if instanceType == "" && node != nil {
		instanceType, _ = util.GetInstanceType(node.Labels)
	}
	platform := y.mapping.Platforms[instanceType]
	coreFraction := platform.CoreFraction
	if coreFraction == 0 {
		coreFraction = 100
	}
	if raw := labels[coreFractionLabel]; raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			coreFraction = parsed
		}
	}
	region, _ := util.GetRegion(labels)
	if region == "" {
		zone, _ := util.GetZone(labels)
		region = RegionFromZone(zone)
	}
	key := &yandexKey{
		instanceType: instanceType,
		region:       region,
		platform:     platform.Platform,
		coreFraction: coreFraction,
		preemptible:  strings.EqualFold(labels[preemptibleLabel], "true"),
		gpuType:      platform.GPUType,
	}
	if node != nil {
		key.providerID = node.SpecProviderID
		key.vcpu = node.Status.Capacity.Cpu().AsApproximateFloat64()
		key.ramBytes = node.Status.Capacity.Memory().Value()
		key.gpuCount = physicalGPUCount(labels, node)
		if key.region == "" {
			zone, _ := util.GetZone(node.Labels)
			key.region = RegionFromZone(zone)
		}
	}
	return key
}

func (y *Yandex) NodePricing(key models.Key) (*models.Node, models.PricingMetadata, error) {
	yk, ok := key.(*yandexKey)
	meta := models.PricingMetadata{Currency: billingCurrency(), Source: PricingSourceName}
	if !ok {
		return nil, meta, fmt.Errorf("Yandex Cloud: unexpected node key type %T", key)
	}
	if yk.platform == "" {
		return nil, meta, fmt.Errorf("Yandex Cloud: unsupported instance type %q", yk.instanceType)
	}
	skus, ok := y.mapping.NodeSKUs[nodeSKUKey(yk.platform, yk.coreFraction, yk.preemptible)]
	if !ok {
		return nil, meta, fmt.Errorf("Yandex Cloud: no SKU mapping for platform=%s coreFraction=%d preemptible=%t", yk.platform, yk.coreFraction, yk.preemptible)
	}
	y.mu.RLock()
	cpuPrice, cpuOK := y.prices[skus.CPU]
	ramPrice, ramOK := y.prices[skus.RAM]
	gpuPrice, gpuOK := y.prices[skus.GPU]
	y.mu.RUnlock()
	if !cpuOK || !ramOK {
		return nil, meta, fmt.Errorf("Yandex Cloud: mapped node prices are unavailable; refresh the Billing SKU catalog")
	}
	if yk.gpuType != "" {
		if skus.GPU == "" {
			return nil, meta, fmt.Errorf("Yandex Cloud: GPU platform %s has no GPU SKU mapping", yk.platform)
		}
		if yk.gpuCount <= 0 {
			return nil, meta, fmt.Errorf("Yandex Cloud: GPU platform %s has no physical GPU capacity in Kubernetes metadata", yk.platform)
		}
		if !gpuOK {
			return nil, meta, fmt.Errorf("Yandex Cloud: mapped GPU price is unavailable; refresh the Billing SKU catalog")
		}
	}
	ramGiB := float64(yk.ramBytes) / (1024 * 1024 * 1024)
	total := yk.vcpu*cpuPrice.Hourly + ramGiB*ramPrice.Hourly
	if yk.gpuType != "" {
		total += float64(yk.gpuCount) * gpuPrice.Hourly
	}
	usageType := "regular"
	pricingType := models.Api
	if yk.preemptible {
		usageType = "preemptible"
		pricingType = models.Spot
	}
	result := &models.Node{
		Cost:         formatPrice(total),
		VCPU:         formatPrice(yk.vcpu),
		VCPUCost:     formatPrice(cpuPrice.Hourly),
		RAM:          formatPrice(ramGiB),
		RAMBytes:     strconv.FormatInt(yk.ramBytes, 10),
		RAMCost:      formatPrice(ramPrice.Hourly),
		UsageType:    usageType,
		InstanceType: yk.instanceType,
		Region:       yk.region,
		ProviderID:   yk.providerID,
		PricingType:  pricingType,
	}
	if yk.gpuType != "" {
		result.GPU = strconv.Itoa(yk.gpuCount)
		result.GPUName = yk.gpuType
		result.GPUCost = formatPrice(gpuPrice.Hourly)
	}
	return result, meta, nil
}

func physicalGPUCount(labels map[string]string, node *clustercache.Node) int {
	lookup := func(name string) string {
		if value := strings.TrimSpace(labels[name]); value != "" {
			return value
		}
		if node != nil {
			return strings.TrimSpace(node.Labels[name])
		}
		return ""
	}
	if lookup(nvidiaGPUReplicasLabel) != "" {
		count, err := strconv.Atoi(lookup(nvidiaGPUCountLabel))
		if err == nil && count > 0 {
			return count
		}
		return 0
	}
	if node == nil {
		return 0
	}
	if quantity, ok := node.Status.Capacity[nvidiaGPUResource]; ok && quantity.Value() > 0 {
		return int(quantity.Value())
	}
	return 0
}

type yandexPVKey struct {
	providerID   string
	storageClass string
	diskType     string
	region       string
	sizeGiB      float64
	parameters   map[string]string
}

func (k *yandexPVKey) ID() string              { return k.providerID }
func (k *yandexPVKey) GetStorageClass() string { return k.storageClass }
func (k *yandexPVKey) Features() string        { return k.region + "," + k.diskType }

func (y *Yandex) GetPVKey(pv *clustercache.PersistentVolume, parameters map[string]string, defaultRegion string) models.PVKey {
	key := &yandexPVKey{parameters: parameters, region: defaultRegion}
	if pv == nil {
		return key
	}
	key.storageClass = pv.Spec.StorageClassName
	key.diskType = parameters["type"]
	if quantity := pv.Spec.Capacity.Storage(); quantity != nil {
		key.sizeGiB = float64(quantity.Value()) / (1024 * 1024 * 1024)
	}
	if pv.Spec.CSI != nil {
		key.providerID = pv.Spec.CSI.VolumeHandle
		if key.diskType == "" {
			key.diskType = pv.Spec.CSI.VolumeAttributes["type"]
		}
	}
	if key.diskType == "" {
		key.diskType = y.mapping.StorageClasses[key.storageClass]
	}
	return key
}

func (y *Yandex) PVPricing(key models.PVKey) (*models.PV, error) {
	yk, ok := key.(*yandexPVKey)
	if !ok {
		return nil, fmt.Errorf("Yandex Cloud: unexpected PV key type %T", key)
	}
	skuID, ok := y.mapping.DiskSKUs[yk.diskType]
	if !ok {
		return nil, fmt.Errorf("Yandex Cloud: unsupported disk type %q for storage class %q", yk.diskType, yk.storageClass)
	}
	y.mu.RLock()
	price, ok := y.prices[skuID]
	y.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("Yandex Cloud: mapped disk price is unavailable; refresh the Billing SKU catalog")
	}
	return &models.PV{
		Cost:       formatPrice(price.Hourly),
		Class:      yk.storageClass,
		Size:       formatPrice(yk.sizeGiB),
		Region:     yk.region,
		ProviderID: yk.providerID,
		Parameters: yk.parameters,
	}, nil
}

func RegionFromZone(zone string) string {
	parts := strings.Split(strings.TrimSpace(zone), "-")
	if len(parts) < 2 {
		return zone
	}
	return strings.Join(parts[:len(parts)-1], "-")
}

func formatPrice(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}

func apiEndpoint() string {
	if value := strings.TrimSpace(os.Getenv(env.YandexAPIEndpointEnvVar)); value != "" {
		return value
	}
	return defaultAPIEndpoint
}

func billingCurrency() string {
	if value := strings.ToUpper(strings.TrimSpace(os.Getenv(env.YandexBillingCurrencyEnvVar))); value != "" {
		return value
	}
	return defaultCurrency
}

func billingAccountID() string { return strings.TrimSpace(os.Getenv(env.YandexBillingAccountIDEnvVar)) }

func (y *Yandex) AllNodePricing() (interface{}, error) {
	y.mu.RLock()
	defer y.mu.RUnlock()
	copy := make(map[string]unitPrice, len(y.prices))
	for id, price := range y.prices {
		copy[id] = price
	}
	return copy, nil
}

func (y *Yandex) PricingSourceSummary() interface{} {
	y.mu.RLock()
	defer y.mu.RUnlock()
	return map[string]interface{}{
		"prices": y.prices, "lastRefresh": y.lastRefresh, "currency": billingCurrency(), "lastError": y.lastError,
		"mks": y.mksState,
	}
}

func (y *Yandex) PricingSourceStatus() map[string]*models.PricingSource {
	y.mu.RLock()
	defer y.mu.RUnlock()
	return map[string]*models.PricingSource{
		PricingSourceName: {
			Name: PricingSourceName, Enabled: true, Available: len(y.prices) > 0, Error: y.lastError,
		},
		MKSPricingSourceName: {
			Name: MKSPricingSourceName, Enabled: true, Available: y.mksState.Available, Error: y.mksState.LastError,
		},
	}
}

func (y *Yandex) ServiceAccountStatus() *models.ServiceAccountStatus {
	y.mu.RLock()
	defer y.mu.RUnlock()
	return &models.ServiceAccountStatus{Checks: []*models.ServiceAccountCheck{
		{Message: serviceAccountCheckName + " (Billing)", Status: y.client != nil && y.lastError == "", AdditionalInfo: y.lastError},
		{Message: serviceAccountCheckName + " (MKS)", Status: y.mksState.Available && y.mksState.LastError == "", AdditionalInfo: y.mksState.LastError},
	}}
}

func (y *Yandex) ClusterInfo() (map[string]string, error) {
	name := "Yandex Cloud Cluster"
	config, err := y.GetConfig()
	if err != nil {
		return nil, err
	}
	if config.ClusterName != "" {
		name = config.ClusterName
	}
	return map[string]string{
		"name": name, "provider": opencost.YandexProvider, "region": y.ClusterRegion,
		"account": billingAccountID(), "remoteReadEnabled": strconv.FormatBool(env.IsRemoteEnabled()), "id": coreenv.GetClusterID(),
	}, nil
}

func (y *Yandex) GetConfig() (*models.CustomPricing, error) {
	config, err := y.Config.GetCustomPricingData()
	if err != nil {
		return nil, err
	}
	if config.Discount == "" {
		config.Discount = "0%"
	}
	if config.NegotiatedDiscount == "" {
		config.NegotiatedDiscount = "0%"
	}
	config.CurrencyCode = billingCurrency()
	return config, nil
}

func (y *Yandex) UpdateConfigFromConfigMap(values map[string]string) (*models.CustomPricing, error) {
	return y.Config.UpdateFromMap(values)
}

func (y *Yandex) UpdateConfig(reader io.Reader, updateType string) (*models.CustomPricing, error) {
	return y.Config.Update(func(config *models.CustomPricing) error {
		values := map[string]interface{}{}
		if err := corejson.NewDecoder(reader).Decode(&values); err != nil {
			return err
		}
		for key, value := range values {
			text, ok := value.(string)
			if !ok {
				return fmt.Errorf("type error while updating config for %s", key)
			}
			if err := models.SetCustomPricingField(config, cloudutils.ToTitle.String(key), text); err != nil {
				return err
			}
		}
		return nil
	})
}

func (y *Yandex) Regions() []string {
	if overrides := env.GetRegionOverrideList(); len(overrides) > 0 {
		return overrides
	}
	if y.ClusterRegion == "" {
		return []string{"ru-central1"}
	}
	return []string{y.ClusterRegion}
}

func (y *Yandex) GetManagementPlatform() (string, error) {
	if y.Clientset == nil {
		return "", nil
	}
	for _, node := range y.Clientset.GetAllNodes() {
		if strings.HasPrefix(strings.ToLower(node.SpecProviderID), "yandex://") {
			return "mks", nil
		}
	}
	return "", nil
}

func (*Yandex) ApplyReservedInstancePricing(map[string]*models.Node) {}
func (*Yandex) CombinedDiscountForNode(_ string, _ bool, defaultDiscount, negotiatedDiscount float64) float64 {
	return 1.0 - ((1.0 - defaultDiscount) * (1.0 - negotiatedDiscount))
}
func (y *Yandex) ClusterManagementPricing() (string, float64, error) {
	y.mu.RLock()
	defer y.mu.RUnlock()
	if !y.mksState.Available {
		if y.mksState.LastError != "" {
			return mksProvisioner, 0, errors.New(y.mksState.LastError)
		}
		return mksProvisioner, 0, errors.New("Yandex Cloud MKS master pricing is not available yet")
	}
	return mksProvisioner, y.mksState.HourlyCost, nil
}
func (y *Yandex) GpuPricing(labels map[string]string) (string, error) {
	instanceType, _ := util.GetInstanceType(labels)
	platform := y.mapping.Platforms[instanceType]
	if platform.GPUType == "" {
		return "", nil
	}
	coreFraction := platform.CoreFraction
	if coreFraction == 0 {
		coreFraction = 100
	}
	if raw := labels[coreFractionLabel]; raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			coreFraction = parsed
		}
	}
	preemptible := strings.EqualFold(labels[preemptibleLabel], "true")
	skus, ok := y.mapping.NodeSKUs[nodeSKUKey(platform.Platform, coreFraction, preemptible)]
	if !ok {
		return "", fmt.Errorf("Yandex Cloud: no SKU mapping for GPU platform=%s coreFraction=%d preemptible=%t", platform.Platform, coreFraction, preemptible)
	}
	if skus.GPU == "" {
		return "", fmt.Errorf("Yandex Cloud: GPU platform %s has no GPU SKU mapping", platform.Platform)
	}
	y.mu.RLock()
	price, ok := y.prices[skus.GPU]
	y.mu.RUnlock()
	if !ok {
		return "", fmt.Errorf("Yandex Cloud: mapped GPU price is unavailable; refresh the Billing SKU catalog")
	}
	return formatPrice(price.Hourly), nil
}
func (*Yandex) NetworkPricing() (*models.Network, error) {
	return nil, errors.New("Yandex Cloud network pricing is not implemented")
}
func (*Yandex) LoadBalancerPricing() (*models.LoadBalancer, error) {
	return nil, errors.New("Yandex Cloud load-balancer pricing is not implemented")
}
func (*Yandex) GetAddresses() ([]byte, error) {
	return nil, errors.New("Yandex Cloud address discovery is not implemented")
}
func (*Yandex) GetDisks() ([]byte, error) {
	return nil, errors.New("Yandex Cloud disk discovery is not implemented")
}
func (*Yandex) GetOrphanedResources() ([]models.OrphanedResource, error) {
	return nil, errors.New("Yandex Cloud orphaned resources are not implemented")
}
