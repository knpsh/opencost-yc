package yandex

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	billing "github.com/yandex-cloud/go-genproto/yandex/cloud/billing/v1"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/opencost/opencost/core/pkg/clustercache"
)

type fakeSKUClient struct {
	pages []*billing.ListSkusResponse
	err   error
	calls int
}

func (f *fakeSKUClient) Get(context.Context, *billing.GetSkuRequest, ...grpc.CallOption) (*billing.Sku, error) {
	return nil, errors.New("not implemented")
}

func (f *fakeSKUClient) List(context.Context, *billing.ListSkusRequest, ...grpc.CallOption) (*billing.ListSkusResponse, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.calls >= len(f.pages) {
		return &billing.ListSkusResponse{}, nil
	}
	page := f.pages[f.calls]
	f.calls++
	return page, nil
}

func flatSKU(id, unit, currency, amount string, effective time.Time) *billing.Sku {
	return &billing.Sku{
		Id: id, Name: id, PricingUnit: unit,
		PricingVersions: []*billing.PricingVersion{{
			Type: billing.PricingVersionType_STREET_PRICE, EffectiveTime: timestamppb.New(effective),
			PricingExpressions: []*billing.PricingExpression{{Rates: []*billing.Rate{{
				StartPricingQuantity: "0", UnitPrice: amount, Currency: currency,
			}}}},
		}},
	}
}

func testMapping() SKUMapping {
	return SKUMapping{
		Version:        mappingVersion,
		Platforms:      map[string]PlatformMapping{"standard-v3": {Platform: "ice-lake", CoreFraction: 100}},
		NodeSKUs:       map[string]NodeSKUs{"ice-lake/100/regular": {CPU: "cpu", RAM: "ram"}},
		DiskSKUs:       map[string]string{"network-hdd": "disk"},
		StorageClasses: map[string]string{"yc-network-hdd": "network-hdd"},
	}
}

func TestDownloadCatalogPaginatesAndNormalizes(t *testing.T) {
	now := time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)
	client := &fakeSKUClient{pages: []*billing.ListSkusResponse{
		{Skus: []*billing.Sku{flatSKU("cpu", "core*hour", "RUB", "1.24", now.Add(-time.Hour))}, NextPageToken: "next"},
		{Skus: []*billing.Sku{flatSKU("ram", "gbyte*hour", "RUB", "0.33", now.Add(-time.Hour)), flatSKU("disk", "gbyte*month", "RUB", "3.65", now.Add(-time.Hour))}},
	}}
	prices, err := downloadCatalog(context.Background(), client, "RUB", "", testMapping(), now)
	if err != nil {
		t.Fatalf("downloadCatalog: %v", err)
	}
	if client.calls != 2 {
		t.Fatalf("List calls = %d, want 2", client.calls)
	}
	if got := prices["disk"].Hourly; math.Abs(got-0.005) > 1e-12 {
		t.Fatalf("monthly disk hourly price = %g, want 0.005", got)
	}
}

func TestPriceForSKURejectsTieredPricing(t *testing.T) {
	now := time.Now()
	sku := flatSKU("tiered", "core*hour", "RUB", "1", now.Add(-time.Hour))
	sku.PricingVersions[0].PricingExpressions[0].Rates = append(sku.PricingVersions[0].PricingExpressions[0].Rates,
		&billing.Rate{StartPricingQuantity: "10", UnitPrice: "0.5", Currency: "RUB"})
	if _, err := priceForSKU(sku, "RUB", false, now); err == nil {
		t.Fatal("expected tiered pricing error")
	}
}

func TestActivePricingVersionPrefersContractOnlyWhenRequested(t *testing.T) {
	now := time.Now()
	street := &billing.PricingVersion{Type: billing.PricingVersionType_STREET_PRICE, EffectiveTime: timestamppb.New(now.Add(-2 * time.Hour))}
	contract := &billing.PricingVersion{Type: billing.PricingVersionType_CONTRACT_PRICE, EffectiveTime: timestamppb.New(now.Add(-time.Hour))}
	if got := activePricingVersion([]*billing.PricingVersion{street, contract}, false, now); got != street {
		t.Fatal("street lookup selected a contract version")
	}
	if got := activePricingVersion([]*billing.PricingVersion{street, contract}, true, now); got != contract {
		t.Fatal("contract lookup did not prefer contract pricing")
	}
}

func TestLoadMappingMergesOverride(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mapping.yaml")
	contents := []byte("version: 1\nplatforms:\n  custom-v1:\n    platform: custom\n    coreFraction: 50\n")
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	mapping, err := loadMapping(path)
	if err != nil {
		t.Fatalf("loadMapping: %v", err)
	}
	if mapping.Platforms["standard-v3"].Platform != "ice-lake" || mapping.Platforms["custom-v1"].CoreFraction != 50 {
		t.Fatalf("mapping override was not merged: %#v", mapping.Platforms)
	}
}

func TestBuiltInDiskMappings(t *testing.T) {
	tests := []struct {
		diskType     string
		storageClass string
		skuID        string
	}{
		{diskType: "network-ssd", storageClass: "yc-network-ssd", skuID: "dn27ajm6m8mnfcshbi61"},
		{diskType: "network-hdd", storageClass: "yc-network-hdd", skuID: "dn2al287u6jr3a710u8g"},
		{diskType: "network-ssd-nonreplicated", storageClass: "yc-network-ssd-nonreplicated", skuID: "dn24kdllggk8ahsol15g"},
		{diskType: "network-ssd-io-m3", storageClass: "yc-network-ssd-io-m3", skuID: "dn2bl3v71k1mej7andmc"},
	}

	mapping := builtInMapping()
	provider := &Yandex{mapping: mapping}
	for _, tt := range tests {
		t.Run(tt.diskType, func(t *testing.T) {
			if got := mapping.DiskSKUs[tt.diskType]; got != tt.skuID {
				t.Fatalf("disk SKU = %q, want %q", got, tt.skuID)
			}
			if got := mapping.StorageClasses[tt.storageClass]; got != tt.diskType {
				t.Fatalf("storage class disk type = %q, want %q", got, tt.diskType)
			}

			pv := &clustercache.PersistentVolume{
				Spec: v1.PersistentVolumeSpec{
					StorageClassName: tt.storageClass,
					PersistentVolumeSource: v1.PersistentVolumeSource{CSI: &v1.CSIPersistentVolumeSource{
						VolumeHandle: "disk-id",
					}},
				},
			}
			fromStorageClass := provider.GetPVKey(pv, nil, "ru-central1").(*yandexPVKey)
			if fromStorageClass.diskType != tt.diskType {
				t.Fatalf("storage class resolved disk type = %q, want %q", fromStorageClass.diskType, tt.diskType)
			}

			fromParameters := provider.GetPVKey(pv, map[string]string{"type": tt.diskType}, "ru-central1").(*yandexPVKey)
			if fromParameters.diskType != tt.diskType {
				t.Fatalf("CSI parameter resolved disk type = %q, want %q", fromParameters.diskType, tt.diskType)
			}
		})
	}
}

func TestBuiltInPlatformMappings(t *testing.T) {
	tests := []struct {
		instanceType string
		platform     string
		gpuType      string
		regular      NodeSKUs
		preemptible  NodeSKUs
	}{
		{"standard-v1", "broadwell", "", NodeSKUs{CPU: "dn299ll54t5jt2gojh7e", RAM: "dn2dka206olokggsieuu"}, NodeSKUs{CPU: "dn247qigcq66fq6t3tk5", RAM: "dn2u497ok1kl70on0ta2"}},
		{"standard-v2", "cascade-lake", "", NodeSKUs{CPU: "dn218a07u143r9v1r5ms", RAM: "dn2fhtcoocq50j1uj4tg"}, NodeSKUs{CPU: "dn2ipnaa10sls6i7osfv", RAM: "dn2sp66rt1l381la6q7p"}},
		{"standard-v3", "ice-lake", "", NodeSKUs{CPU: "dn2k3vqlk9snp1jv351u", RAM: "dn2ilq72mjc3bej6j74p"}, NodeSKUs{CPU: "dn2e2fphfupugm21k4hv", RAM: "dn26ur5frjbgdek2a0g5"}},
		{"highfreq-v3", "ice-lake-compute-optimized", "", NodeSKUs{CPU: "dn2lag3718gm9oq8dus2", RAM: "dn23hq90a5khr3o6fivm"}, NodeSKUs{}},
		{"standard-v4", "zen-4", "", NodeSKUs{CPU: "dn28kn5h601tc7lk5fbu", RAM: "dn29sa4d441spg8aokdn"}, NodeSKUs{CPU: "dn2p24jgv2e06fqoc480", RAM: "dn2vulur9lrq1m6phk7l"}},
		{"highfreq-v4", "zen-4-compute-optimized", "", NodeSKUs{CPU: "dn2bnom85ie58bpvmtmn", RAM: "dn2dq9mqiklrm87pc1h2"}, NodeSKUs{CPU: "dn2i8hbduq7pn24un5n4", RAM: "dn2tqniq656s6p8nlv0j"}},
		{"gpu-standard-v1", "broadwell-v100", "NVIDIA V100", NodeSKUs{CPU: "dn2sfcnkn3jlhmq568ac", RAM: "dn2nccae8nra81iqphdn", GPU: "dn2oroscvvtb6sqtt83i"}, NodeSKUs{CPU: "dn2t7aa68lehsmvo5mss", RAM: "dn2k0omvmglh857u60vu", GPU: "dn2lov15qqamcimfv84q"}},
		{"gpu-standard-v2", "cascade-lake-v100", "NVIDIA V100", NodeSKUs{CPU: "dn2udmu2aa9jm5a8f4ug", RAM: "dn2qtp90p3r8l8vakmm6", GPU: "dn2dlvuk2ecf6hu0kjtl"}, NodeSKUs{CPU: "dn2h4u30djq3jhh8dqh8", RAM: "dn2hotj7skno0turhbq1", GPU: "dn23ppvthcls7rjt5pol"}},
		{"gpu-standard-v3", "amd-epyc-a100", "NVIDIA A100", NodeSKUs{CPU: "dn28c1erut6m9f9uem08", RAM: "dn21jcm82510bfa6is22", GPU: "dn2395q10bihjmm2b0v6"}, NodeSKUs{CPU: "dn2tvs05nnrib706hgnt", RAM: "dn2m4gusa7m7t4hl6vo2", GPU: "dn211dses9ju3abvq0bs"}},
		{"gpu-standard-v3i", "gen2", "Gen2", NodeSKUs{CPU: "dn2fd3g50rub98vfprlt", RAM: "dn2h5gi2u2l3bdclrput", GPU: "dn2jfrjoic5h3nh7e6jh"}, NodeSKUs{CPU: "dn2o9fiqemifmch1dq7c", RAM: "dn2mgiub24223fh5mvgv", GPU: "dn2qvcfe8i5vqlrvterc"}},
		{"gpu-standard-v4", "gpu-platform-v4", "GPU PLATFORM V4", NodeSKUs{CPU: "dn2shelhculi2g5o5ogy", RAM: "dn2pvxnj2udp3unyhz2b", GPU: "dn2qtqtmybqbpyihfxri"}, NodeSKUs{CPU: "dn2kllf2dqxte3qnj7vq", RAM: "dn2puxcaylhhmezidpko", GPU: "dn2azjbhk7j6jwi5agdv"}},
		{"standard-v3-t4", "ice-lake-t4", "NVIDIA T4", NodeSKUs{CPU: "dn24b7m6qol7tb7tukga", RAM: "dn2lg2hrvbn5b8lm7em4", GPU: "dn20ml8ifdps6m7048an"}, NodeSKUs{CPU: "dn2lsfskfirek2985fnd", RAM: "dn2im0g43iedeohe4sac", GPU: "dn2cpk4mc82b1vib72e5"}},
		{"standard-v3-t4i", "ice-lake-t4i", "NVIDIA T4i", NodeSKUs{CPU: "dn242l2ivnhdd5so2oga", RAM: "dn290pbmohupnus9ajb7", GPU: "dn2hql9evci880d8jq7i"}, NodeSKUs{CPU: "dn2960mi7268n67o8iae", RAM: "dn25rffeums4j1ku5649", GPU: "dn2qlml2u48bng4jgilh"}},
	}

	mapping := builtInMapping()
	if err := validateMapping(mapping); err != nil {
		t.Fatalf("built-in mapping is invalid: %v", err)
	}
	for _, tt := range tests {
		t.Run(tt.instanceType, func(t *testing.T) {
			platform := mapping.Platforms[tt.instanceType]
			if platform.Platform != tt.platform || platform.CoreFraction != 100 || platform.GPUType != tt.gpuType {
				t.Fatalf("platform mapping = %#v", platform)
			}
			if got := mapping.NodeSKUs[nodeSKUKey(tt.platform, 100, false)]; got != tt.regular {
				t.Fatalf("regular SKUs = %#v, want %#v", got, tt.regular)
			}
			if got := mapping.NodeSKUs[nodeSKUKey(tt.platform, 100, true)]; got != tt.preemptible {
				t.Fatalf("preemptible SKUs = %#v, want %#v", got, tt.preemptible)
			}
		})
	}
	if got := len(mappedSKUIDs(mapping)); got != 68 {
		t.Fatalf("mapped SKU count = %d, want 68", got)
	}
}

func TestNormalizeHourlyGPUPrice(t *testing.T) {
	got, err := normalizeHourlyPrice(408.12, "gpu*hour")
	if err != nil || got != 408.12 {
		t.Fatalf("normalizeHourlyPrice = %g, %v", got, err)
	}
}

func gpuTestMapping() SKUMapping {
	return SKUMapping{
		Version: mappingVersion,
		Platforms: map[string]PlatformMapping{
			"gpu-standard-v3": {Platform: "amd-epyc-a100", CoreFraction: 100, GPUType: "NVIDIA A100"},
		},
		NodeSKUs: map[string]NodeSKUs{
			"amd-epyc-a100/100/regular":     {CPU: "cpu", RAM: "ram", GPU: "gpu"},
			"amd-epyc-a100/100/preemptible": {CPU: "spot-cpu", RAM: "spot-ram", GPU: "spot-gpu"},
		},
		DiskSKUs:       map[string]string{},
		StorageClasses: map[string]string{},
	}
}

func TestGPUNodePricing(t *testing.T) {
	provider := &Yandex{
		mapping: gpuTestMapping(),
		prices: map[string]unitPrice{
			"cpu": {Hourly: 1}, "ram": {Hourly: 2}, "gpu": {Hourly: 10},
		},
	}
	node := &clustercache.Node{
		Labels:         map[string]string{"node.kubernetes.io/instance-type": "gpu-standard-v3"},
		SpecProviderID: "yandex://gpu-node",
		Status: v1.NodeStatus{Capacity: v1.ResourceList{
			v1.ResourceCPU: resource.MustParse("4"), v1.ResourceMemory: resource.MustParse("8Gi"),
			v1.ResourceName(nvidiaGPUResource): resource.MustParse("2"),
		}},
	}
	key := provider.GetKey(node.Labels, node)
	if key.GPUType() != "NVIDIA A100" || key.GPUCount() != 2 {
		t.Fatalf("GPU key = type %q count %d", key.GPUType(), key.GPUCount())
	}
	price, _, err := provider.NodePricing(key)
	if err != nil {
		t.Fatalf("NodePricing: %v", err)
	}
	if price.Cost != "40" || price.GPU != "2" || price.GPUName != "NVIDIA A100" || price.GPUCost != "10" {
		t.Fatalf("GPU node price = %#v", price)
	}
}

func TestTimeSlicedGPUUsesPhysicalCount(t *testing.T) {
	provider := &Yandex{mapping: gpuTestMapping()}
	node := &clustercache.Node{
		Labels: map[string]string{
			"node.kubernetes.io/instance-type": "gpu-standard-v3",
			nvidiaGPUReplicasLabel:             "8",
			nvidiaGPUCountLabel:                "2",
		},
		Status: v1.NodeStatus{Capacity: v1.ResourceList{
			v1.ResourceName(nvidiaGPUResource): resource.MustParse("16"),
		}},
	}
	if got := provider.GetKey(node.Labels, node).GPUCount(); got != 2 {
		t.Fatalf("physical GPU count = %d, want 2", got)
	}
}

func TestGPUNodePricingRejectsMissingCapacity(t *testing.T) {
	provider := &Yandex{
		mapping: gpuTestMapping(),
		prices: map[string]unitPrice{
			"cpu": {Hourly: 1}, "ram": {Hourly: 2}, "gpu": {Hourly: 10},
		},
	}
	node := &clustercache.Node{
		Labels: map[string]string{"node.kubernetes.io/instance-type": "gpu-standard-v3"},
		Status: v1.NodeStatus{Capacity: v1.ResourceList{
			v1.ResourceCPU: resource.MustParse("4"), v1.ResourceMemory: resource.MustParse("8Gi"),
		}},
	}
	if _, _, err := provider.NodePricing(provider.GetKey(node.Labels, node)); err == nil {
		t.Fatal("expected missing GPU capacity error")
	}
}

func TestGpuPricing(t *testing.T) {
	provider := &Yandex{
		mapping: gpuTestMapping(),
		prices:  map[string]unitPrice{"gpu": {Hourly: 10}, "spot-gpu": {Hourly: 4}},
	}
	labels := map[string]string{"node.kubernetes.io/instance-type": "gpu-standard-v3"}
	if got, err := provider.GpuPricing(labels); err != nil || got != "10" {
		t.Fatalf("regular GpuPricing = %q, %v", got, err)
	}
	labels[preemptibleLabel] = "true"
	if got, err := provider.GpuPricing(labels); err != nil || got != "4" {
		t.Fatalf("preemptible GpuPricing = %q, %v", got, err)
	}
	if got, err := provider.GpuPricing(map[string]string{"node.kubernetes.io/instance-type": "standard-v3"}); err != nil || got != "" {
		t.Fatalf("CPU-only GpuPricing = %q, %v", got, err)
	}
}

func TestValidateMappingRejectsIncompleteGPU(t *testing.T) {
	mapping := gpuTestMapping()
	node := mapping.NodeSKUs["amd-epyc-a100/100/regular"]
	node.GPU = ""
	mapping.NodeSKUs["amd-epyc-a100/100/regular"] = node
	if err := validateMapping(mapping); err == nil {
		t.Fatal("expected missing GPU SKU validation error")
	}
}

func TestNodeAndPVPricing(t *testing.T) {
	provider := &Yandex{
		mapping: testMapping(),
		prices: map[string]unitPrice{
			"cpu":  {Hourly: 1.24, Currency: "RUB"},
			"ram":  {Hourly: 0.33, Currency: "RUB"},
			"disk": {Hourly: 0.0048, Currency: "RUB"},
		},
	}
	node := &clustercache.Node{
		Labels: map[string]string{
			"node.kubernetes.io/instance-type": "standard-v3",
			"topology.kubernetes.io/zone":      "ru-central1-a",
			preemptibleLabel:                   "false",
		},
		SpecProviderID: "yandex://node-id",
		Status: v1.NodeStatus{Capacity: v1.ResourceList{
			v1.ResourceCPU: resource.MustParse("4"), v1.ResourceMemory: resource.MustParse("8Gi"),
		}},
	}
	key := provider.GetKey(node.Labels, node)
	price, meta, err := provider.NodePricing(key)
	if err != nil {
		t.Fatalf("NodePricing: %v", err)
	}
	if price.Cost != "7.6" || price.VCPUCost != "1.24" || price.RAMCost != "0.33" {
		t.Fatalf("unexpected node price: %#v", price)
	}
	if price.Region != "ru-central1" || price.ProviderID != "yandex://node-id" || meta.Currency != "RUB" {
		t.Fatalf("unexpected node metadata: %#v %#v", price, meta)
	}

	pv := &clustercache.PersistentVolume{
		Name: "pv", Spec: v1.PersistentVolumeSpec{
			StorageClassName: "yc-network-hdd",
			Capacity:         v1.ResourceList{v1.ResourceStorage: resource.MustParse("10Gi")},
			PersistentVolumeSource: v1.PersistentVolumeSource{CSI: &v1.CSIPersistentVolumeSource{
				Driver: "disk-csi-driver.mks.ycloud.io", VolumeHandle: "disk-id",
			}},
		},
	}
	pvPrice, err := provider.PVPricing(provider.GetPVKey(pv, map[string]string{"type": "network-hdd"}, "ru-central1"))
	if err != nil {
		t.Fatalf("PVPricing: %v", err)
	}
	if pvPrice.Cost != "0.0048" || pvPrice.ProviderID != "disk-id" || pvPrice.Size != "10" {
		t.Fatalf("unexpected PV price: %#v", pvPrice)
	}

	pv.Spec.CSI.VolumeAttributes = map[string]string{"type": "network-hdd"}
	pvKey := provider.GetPVKey(pv, nil, "ru-central1").(*yandexPVKey)
	if pvKey.diskType != "network-hdd" {
		t.Fatalf("CSI disk type = %q, want network-hdd", pvKey.diskType)
	}
}

func TestCombinedDiscountForNode(t *testing.T) {
	provider := &Yandex{}
	if got := provider.CombinedDiscountForNode("", false, 0.1, 0.2); math.Abs(got-0.28) > 1e-12 {
		t.Fatalf("combined discount = %g, want 0.28", got)
	}
}

func TestDownloadPricingDataRetainsLastGoodCatalog(t *testing.T) {
	now := time.Now()
	mapping := testMapping()
	client := &fakeSKUClient{pages: []*billing.ListSkusResponse{{Skus: []*billing.Sku{
		flatSKU("cpu", "core*hour", "RUB", "1", now.Add(-time.Hour)),
		flatSKU("ram", "gbyte*hour", "RUB", "1", now.Add(-time.Hour)),
		flatSKU("disk", "gbyte*hour", "RUB", "1", now.Add(-time.Hour)),
	}}}}
	provider := &Yandex{client: client, mapping: mapping, prices: map[string]unitPrice{}, now: func() time.Time { return now }}
	if err := provider.DownloadPricingData(); err != nil {
		t.Fatalf("initial refresh: %v", err)
	}
	client.err = errors.New("temporary failure")
	if err := provider.DownloadPricingData(); err == nil {
		t.Fatal("expected refresh failure")
	}
	if len(provider.prices) != 3 || provider.PricingSourceStatus()[PricingSourceName].Available != true {
		t.Fatalf("last good catalog was not retained: %#v", provider.prices)
	}
}

func TestLiveBillingCatalog(t *testing.T) {
	if os.Getenv("YC_LIVE_TEST") != "1" {
		t.Skip("set YC_LIVE_TEST=1 and YC_SERVICE_ACCOUNT_KEY_FILE to run")
	}
	if os.Getenv("YC_SERVICE_ACCOUNT_KEY_FILE") == "" {
		t.Fatal("YC_SERVICE_ACCOUNT_KEY_FILE is required for the live Billing test")
	}
	mapping := builtInMapping()
	provider := &Yandex{
		mapping: mapping, prices: map[string]unitPrice{}, refreshInterval: 0, now: time.Now,
	}
	if err := provider.DownloadPricingData(); err != nil {
		t.Fatalf("live Billing catalog refresh: %v", err)
	}
	if got, want := len(provider.prices), len(mappedSKUIDs(mapping)); got != want {
		t.Fatalf("live mapped SKU count = %d, want %d", got, want)
	}
}
