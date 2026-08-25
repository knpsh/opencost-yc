# Yandex Cloud pricing provider

This package prices Yandex Cloud Kubernetes nodes and persistent volumes from
the Billing SKU API. Authentication uses an authorized service-account key;
the Yandex Cloud Go SDK creates and renews short-lived IAM tokens.

## Runtime settings

| Variable | Default | Description |
| --- | --- | --- |
| `YC_SERVICE_ACCOUNT_KEY_FILE` | none | Required path to the mounted authorized-key JSON file. |
| `YC_API_ENDPOINT` | `api.cloud.yandex.net:443` | Yandex Cloud API endpoint. |
| `YC_BILLING_CURRENCY` | `RUB` | Billing SKU currency. |
| `YC_BILLING_ACCOUNT_ID` | none | Optional billing account for contract prices. |
| `YC_SKU_MAPPING_FILE` | none | Optional partial YAML override of the built-in mapping. |
| `YC_PRICING_REFRESH_INTERVAL` | `6h` | Catalog refresh interval. |

The built-in version 1 mapping supports `standard-v3` (Intel Ice Lake) at 100%
core fraction, regular and preemptible CPU/RAM prices, and `network-hdd`
storage (`yc-network-hdd`). A missing core-fraction label is treated as 100%.
Unsupported platforms, fractions, disks, and non-flat Billing rates return an
explicit pricing error.

Mapping files are strict YAML and merge into the built-in mapping. For example:

```yaml
version: 1
platforms:
  standard-v4:
    platform: custom-platform
    coreFraction: 100
nodeSKUs:
  custom-platform/100/regular:
    cpu: cpu-sku-id
    ram: ram-sku-id
diskSKUs:
  network-ssd: disk-sku-id
storageClasses:
  yc-network-ssd: network-ssd
```
