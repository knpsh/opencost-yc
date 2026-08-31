package yandex

import (
	"fmt"
	"os"
	"strings"

	"sigs.k8s.io/yaml"
)

const mappingVersion = 1

type PlatformMapping struct {
	Platform     string `json:"platform" yaml:"platform"`
	CoreFraction int    `json:"coreFraction" yaml:"coreFraction"`
}

type NodeSKUs struct {
	CPU string `json:"cpu" yaml:"cpu"`
	RAM string `json:"ram" yaml:"ram"`
}

type SKUMapping struct {
	Version        int                        `json:"version" yaml:"version"`
	Platforms      map[string]PlatformMapping `json:"platforms" yaml:"platforms"`
	NodeSKUs       map[string]NodeSKUs        `json:"nodeSKUs" yaml:"nodeSKUs"`
	DiskSKUs       map[string]string          `json:"diskSKUs" yaml:"diskSKUs"`
	StorageClasses map[string]string          `json:"storageClasses" yaml:"storageClasses"`
}

func builtInMapping() SKUMapping {
	return SKUMapping{
		Version: mappingVersion,
		Platforms: map[string]PlatformMapping{
			"standard-v3": {Platform: "ice-lake", CoreFraction: 100},
		},
		NodeSKUs: map[string]NodeSKUs{
			"ice-lake/100/regular": {
				CPU: "dn2k3vqlk9snp1jv351u",
				RAM: "dn2ilq72mjc3bej6j74p",
			},
			"ice-lake/100/preemptible": {
				CPU: "dn2e2fphfupugm21k4hv",
				RAM: "dn26ur5frjbgdek2a0g5",
			},
		},
		DiskSKUs: map[string]string{
			"network-ssd":               "dn27ajm6m8mnfcshbi61",
			"network-hdd":               "dn2al287u6jr3a710u8g",
			"network-ssd-nonreplicated": "dn24kdllggk8ahsol15g",
			"network-ssd-io-m3":         "dn2bl3v71k1mej7andmc",
		},
		StorageClasses: map[string]string{
			"yc-network-ssd":               "network-ssd",
			"yc-network-hdd":               "network-hdd",
			"yc-network-ssd-nonreplicated": "network-ssd-nonreplicated",
			"yc-network-ssd-io-m3":         "network-ssd-io-m3",
		},
	}
}

func loadMapping(path string) (SKUMapping, error) {
	mapping := builtInMapping()
	if strings.TrimSpace(path) == "" {
		return mapping, validateMapping(mapping)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return SKUMapping{}, fmt.Errorf("read Yandex SKU mapping %q: %w", path, err)
	}
	var override SKUMapping
	if err := yaml.UnmarshalStrict(data, &override); err != nil {
		return SKUMapping{}, fmt.Errorf("parse Yandex SKU mapping %q: %w", path, err)
	}
	if override.Version != 0 && override.Version != mappingVersion {
		return SKUMapping{}, fmt.Errorf("unsupported Yandex SKU mapping version %d", override.Version)
	}
	mergeMapping(&mapping, override)
	return mapping, validateMapping(mapping)
}

func mergeMapping(target *SKUMapping, override SKUMapping) {
	for key, value := range override.Platforms {
		target.Platforms[key] = value
	}
	for key, value := range override.NodeSKUs {
		target.NodeSKUs[key] = value
	}
	for key, value := range override.DiskSKUs {
		target.DiskSKUs[key] = value
	}
	for key, value := range override.StorageClasses {
		target.StorageClasses[key] = value
	}
}

func validateMapping(mapping SKUMapping) error {
	if mapping.Version != mappingVersion {
		return fmt.Errorf("Yandex SKU mapping version must be %d", mappingVersion)
	}
	for instanceType, platform := range mapping.Platforms {
		if strings.TrimSpace(instanceType) == "" || strings.TrimSpace(platform.Platform) == "" {
			return fmt.Errorf("Yandex platform mappings require non-empty instance type and platform")
		}
		if platform.CoreFraction <= 0 || platform.CoreFraction > 100 {
			return fmt.Errorf("Yandex platform %q has invalid core fraction %d", instanceType, platform.CoreFraction)
		}
	}
	for key, skus := range mapping.NodeSKUs {
		if strings.TrimSpace(key) == "" || strings.TrimSpace(skus.CPU) == "" || strings.TrimSpace(skus.RAM) == "" {
			return fmt.Errorf("Yandex node SKU mapping %q requires CPU and RAM SKU IDs", key)
		}
	}
	for diskType, skuID := range mapping.DiskSKUs {
		if strings.TrimSpace(diskType) == "" || strings.TrimSpace(skuID) == "" {
			return fmt.Errorf("Yandex disk SKU mappings require non-empty disk type and SKU ID")
		}
	}
	return nil
}

func nodeSKUKey(platform string, coreFraction int, preemptible bool) string {
	usage := "regular"
	if preemptible {
		usage = "preemptible"
	}
	return fmt.Sprintf("%s/%d/%s", strings.ToLower(platform), coreFraction, usage)
}

func mappedSKUIDs(mapping SKUMapping) map[string]struct{} {
	ids := map[string]struct{}{}
	for _, node := range mapping.NodeSKUs {
		ids[node.CPU] = struct{}{}
		ids[node.RAM] = struct{}{}
	}
	for _, id := range mapping.DiskSKUs {
		ids[id] = struct{}{}
	}
	return ids
}
