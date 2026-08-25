package yandex

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	billing "github.com/yandex-cloud/go-genproto/yandex/cloud/billing/v1"
	"google.golang.org/grpc"
)

const hoursPerMonth = 730.0

type skuClient interface {
	Get(context.Context, *billing.GetSkuRequest, ...grpc.CallOption) (*billing.Sku, error)
	List(context.Context, *billing.ListSkusRequest, ...grpc.CallOption) (*billing.ListSkusResponse, error)
}

type unitPrice struct {
	SKU      string  `json:"sku"`
	Name     string  `json:"name"`
	Unit     string  `json:"unit"`
	Currency string  `json:"currency"`
	Hourly   float64 `json:"hourly"`
}

func downloadCatalog(ctx context.Context, client skuClient, currency, billingAccountID string, mapping SKUMapping, now time.Time) (map[string]unitPrice, error) {
	wanted := mappedSKUIDs(mapping)
	found := make(map[string]unitPrice, len(wanted))
	pageToken := ""
	for {
		page, err := client.List(ctx, &billing.ListSkusRequest{
			Currency:         currency,
			BillingAccountId: billingAccountID,
			PageSize:         1000,
			PageToken:        pageToken,
		})
		if err != nil {
			return nil, fmt.Errorf("list Billing SKUs: %w", err)
		}
		for _, sku := range page.Skus {
			if _, ok := wanted[sku.Id]; !ok {
				continue
			}
			price, err := priceForSKU(sku, currency, billingAccountID != "", now)
			if err != nil {
				return nil, err
			}
			found[sku.Id] = price
		}
		pageToken = page.NextPageToken
		if pageToken == "" {
			break
		}
	}
	if len(found) != len(wanted) {
		missing := make([]string, 0)
		for id := range wanted {
			if _, ok := found[id]; !ok {
				missing = append(missing, id)
			}
		}
		sort.Strings(missing)
		return nil, fmt.Errorf("Billing catalog did not contain mapped SKU IDs: %s", strings.Join(missing, ", "))
	}
	return found, nil
}

func priceForSKU(sku *billing.Sku, currency string, preferContract bool, now time.Time) (unitPrice, error) {
	version := activePricingVersion(sku.PricingVersions, preferContract, now)
	if version == nil {
		return unitPrice{}, fmt.Errorf("Yandex SKU %s has no active pricing version", sku.Id)
	}
	if len(version.PricingExpressions) != 1 || len(version.PricingExpressions[0].Rates) != 1 {
		return unitPrice{}, fmt.Errorf("Yandex SKU %s uses tiered or ambiguous pricing, which is unsupported", sku.Id)
	}
	rate := version.PricingExpressions[0].Rates[0]
	start, err := strconv.ParseFloat(rate.StartPricingQuantity, 64)
	if err != nil || start != 0 {
		return unitPrice{}, fmt.Errorf("Yandex SKU %s has a non-flat starting quantity %q", sku.Id, rate.StartPricingQuantity)
	}
	if !strings.EqualFold(rate.Currency, currency) {
		return unitPrice{}, fmt.Errorf("Yandex SKU %s returned currency %q, expected %q", sku.Id, rate.Currency, currency)
	}
	amount, err := strconv.ParseFloat(rate.UnitPrice, 64)
	if err != nil {
		return unitPrice{}, fmt.Errorf("parse price %q for Yandex SKU %s: %w", rate.UnitPrice, sku.Id, err)
	}
	hourly, err := normalizeHourlyPrice(amount, sku.PricingUnit)
	if err != nil {
		return unitPrice{}, fmt.Errorf("Yandex SKU %s: %w", sku.Id, err)
	}
	return unitPrice{SKU: sku.Id, Name: sku.Name, Unit: sku.PricingUnit, Currency: rate.Currency, Hourly: hourly}, nil
}

func activePricingVersion(versions []*billing.PricingVersion, preferContract bool, now time.Time) *billing.PricingVersion {
	type candidate struct {
		version *billing.PricingVersion
		weight  int
	}
	candidates := make([]candidate, 0, len(versions))
	for _, version := range versions {
		if version == nil || version.EffectiveTime == nil || version.EffectiveTime.AsTime().After(now) {
			continue
		}
		if !preferContract && version.Type == billing.PricingVersionType_CONTRACT_PRICE {
			continue
		}
		weight := 0
		if preferContract && version.Type == billing.PricingVersionType_CONTRACT_PRICE {
			weight = 1
		}
		candidates = append(candidates, candidate{version: version, weight: weight})
	}
	if len(candidates) == 0 {
		return nil
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].weight != candidates[j].weight {
			return candidates[i].weight < candidates[j].weight
		}
		return candidates[i].version.EffectiveTime.AsTime().Before(candidates[j].version.EffectiveTime.AsTime())
	})
	return candidates[len(candidates)-1].version
}

func normalizeHourlyPrice(amount float64, unit string) (float64, error) {
	switch strings.ToLower(strings.TrimSpace(unit)) {
	case "core*hour", "gbyte*hour", "gb*hour", "gibibyte*hour":
		return amount, nil
	case "gbyte*month", "gb*month", "gibibyte*month":
		return amount / hoursPerMonth, nil
	default:
		return 0, fmt.Errorf("unsupported pricing unit %q", unit)
	}
}
