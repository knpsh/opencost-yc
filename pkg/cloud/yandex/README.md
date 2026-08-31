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

The built-in version 1 mapping supports 100% core fraction for the following
Compute Cloud platforms. A missing core-fraction label is treated as 100%.

| Instance type | Billing platform | Pricing modes |
| --- | --- | --- |
| `standard-v1` | Intel Broadwell | regular, preemptible |
| `standard-v2` | Intel Cascade Lake | regular, preemptible |
| `standard-v3` | Intel Ice Lake | regular, preemptible |
| `highfreq-v3` | Intel Ice Lake (Compute Optimized) | regular only |
| `standard-v4` | AMD Zen 4 | regular, preemptible |
| `highfreq-v4` | AMD Zen 4 (Compute Optimized) | regular, preemptible |
| `gpu-standard-v1` | Intel Broadwell with NVIDIA V100 | regular, preemptible |
| `gpu-standard-v2` | Intel Cascade Lake with NVIDIA V100 | regular, preemptible |
| `gpu-standard-v3` | AMD EPYC with NVIDIA A100 | regular, preemptible |
| `gpu-standard-v3i` | Gen2 | regular, preemptible |
| `gpu-standard-v4` | GPU PLATFORM V4 | regular, preemptible |
| `standard-v3-t4` | Intel Ice Lake with NVIDIA T4 | regular, preemptible |
| `standard-v3-t4i` | Intel Ice Lake with NVIDIA T4i | regular, preemptible |

GPU prices are loaded as RUB per GPU-hour. Physical GPU count comes from the
Kubernetes `nvidia.com/gpu` capacity; with NVIDIA time slicing, the provider
uses `nvidia.com/gpu.count` instead of charging for every replica. GPU node
hourly cost is CPU + RAM + physical GPU count times the per-GPU price. A GPU
platform without physical GPU capacity fails explicitly rather than returning
an incomplete CPU/RAM-only price. High-speed GPU interconnect pricing is not
included.

The built-in mapping also supports all four persistent disk types exposed by
the Yandex Cloud CSI driver:

| Disk type | StorageClass | Billing SKU ID |
| --- | --- | --- |
| `network-ssd` | `yc-network-ssd` | `dn27ajm6m8mnfcshbi61` |
| `network-hdd` | `yc-network-hdd` | `dn2al287u6jr3a710u8g` |
| `network-ssd-nonreplicated` | `yc-network-ssd-nonreplicated` | `dn24kdllggk8ahsol15g` |
| `network-ssd-io-m3` | `yc-network-ssd-io-m3` | `dn2bl3v71k1mej7andmc` |

Unsupported platforms, fractions, disks, and non-flat Billing rates return an explicit pricing error.
The SKU IDs are stable mapping inputs; prices are always read from the active
Billing catalog rather than embedded in the image.

Mapping files are strict YAML and merge into the built-in mapping. For example:

```yaml
version: 1
platforms:
  standard-v4:
    platform: custom-platform
    coreFraction: 100
    gpuType: NVIDIA Example
nodeSKUs:
  custom-platform/100/regular:
    cpu: cpu-sku-id
    ram: ram-sku-id
    gpu: gpu-sku-id
diskSKUs:
  network-ssd: disk-sku-id
storageClasses:
  yc-network-ssd: network-ssd
```
