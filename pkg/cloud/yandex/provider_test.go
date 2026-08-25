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
