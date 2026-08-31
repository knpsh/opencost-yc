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
	GPUType      string `json:"gpuType,omitempty" yaml:"gpuType,omitempty"`
}

type NodeSKUs struct {
	CPU string `json:"cpu" yaml:"cpu"`
	RAM string `json:"ram" yaml:"ram"`
	GPU string `json:"gpu,omitempty" yaml:"gpu,omitempty"`
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
			"standard-v1":      {Platform: "broadwell", CoreFraction: 100},
			"standard-v2":      {Platform: "cascade-lake", CoreFraction: 100},
			"standard-v3":      {Platform: "ice-lake", CoreFraction: 100},
			"highfreq-v3":      {Platform: "ice-lake-compute-optimized", CoreFraction: 100},
			"standard-v4":      {Platform: "zen-4", CoreFraction: 100},
			"highfreq-v4":      {Platform: "zen-4-compute-optimized", CoreFraction: 100},
			"gpu-standard-v1":  {Platform: "broadwell-v100", CoreFraction: 100, GPUType: "NVIDIA V100"},
			"gpu-standard-v2":  {Platform: "cascade-lake-v100", CoreFraction: 100, GPUType: "NVIDIA V100"},
			"gpu-standard-v3":  {Platform: "amd-epyc-a100", CoreFraction: 100, GPUType: "NVIDIA A100"},
			"gpu-standard-v3i": {Platform: "gen2", CoreFraction: 100, GPUType: "Gen2"},
			"gpu-standard-v4":  {Platform: "gpu-platform-v4", CoreFraction: 100, GPUType: "GPU PLATFORM V4"},
			"standard-v3-t4":   {Platform: "ice-lake-t4", CoreFraction: 100, GPUType: "NVIDIA T4"},
			"standard-v3-t4i":  {Platform: "ice-lake-t4i", CoreFraction: 100, GPUType: "NVIDIA T4i"},
		},
		NodeSKUs: map[string]NodeSKUs{
			"broadwell/100/regular": {
				CPU: "dn299ll54t5jt2gojh7e",
				RAM: "dn2dka206olokggsieuu",
			},
			"broadwell/100/preemptible": {
				CPU: "dn247qigcq66fq6t3tk5",
				RAM: "dn2u497ok1kl70on0ta2",
			},
			"cascade-lake/100/regular": {
				CPU: "dn218a07u143r9v1r5ms",
				RAM: "dn2fhtcoocq50j1uj4tg",
			},
			"cascade-lake/100/preemptible": {
				CPU: "dn2ipnaa10sls6i7osfv",
				RAM: "dn2sp66rt1l381la6q7p",
			},
			"ice-lake/100/regular": {
				CPU: "dn2k3vqlk9snp1jv351u",
				RAM: "dn2ilq72mjc3bej6j74p",
			},
			"ice-lake/100/preemptible": {
				CPU: "dn2e2fphfupugm21k4hv",
				RAM: "dn26ur5frjbgdek2a0g5",
			},
			"ice-lake-compute-optimized/100/regular": {
				CPU: "dn2lag3718gm9oq8dus2",
				RAM: "dn23hq90a5khr3o6fivm",
			},
			"zen-4/100/regular": {
				CPU: "dn28kn5h601tc7lk5fbu",
				RAM: "dn29sa4d441spg8aokdn",
			},
			"zen-4/100/preemptible": {
				CPU: "dn2p24jgv2e06fqoc480",
				RAM: "dn2vulur9lrq1m6phk7l",
			},
			"zen-4-compute-optimized/100/regular": {
				CPU: "dn2bnom85ie58bpvmtmn",
				RAM: "dn2dq9mqiklrm87pc1h2",
			},
			"zen-4-compute-optimized/100/preemptible": {
				CPU: "dn2i8hbduq7pn24un5n4",
				RAM: "dn2tqniq656s6p8nlv0j",
			},
			"amd-epyc-a100/100/regular": {
				CPU: "dn28c1erut6m9f9uem08",
				RAM: "dn21jcm82510bfa6is22",
				GPU: "dn2395q10bihjmm2b0v6",
			},
			"amd-epyc-a100/100/preemptible": {
				CPU: "dn2tvs05nnrib706hgnt",
				RAM: "dn2m4gusa7m7t4hl6vo2",
				GPU: "dn211dses9ju3abvq0bs",
			},
			"gpu-platform-v4/100/regular": {
				CPU: "dn2shelhculi2g5o5ogy",
				RAM: "dn2pvxnj2udp3unyhz2b",
				GPU: "dn2qtqtmybqbpyihfxri",
			},
			"gpu-platform-v4/100/preemptible": {
				CPU: "dn2kllf2dqxte3qnj7vq",
				RAM: "dn2puxcaylhhmezidpko",
				GPU: "dn2azjbhk7j6jwi5agdv",
			},
			"gen2/100/regular": {
				CPU: "dn2fd3g50rub98vfprlt",
				RAM: "dn2h5gi2u2l3bdclrput",
				GPU: "dn2jfrjoic5h3nh7e6jh",
			},
			"gen2/100/preemptible": {
				CPU: "dn2o9fiqemifmch1dq7c",
				RAM: "dn2mgiub24223fh5mvgv",
				GPU: "dn2qvcfe8i5vqlrvterc",
			},
			"ice-lake-t4/100/regular": {
				CPU: "dn24b7m6qol7tb7tukga",
				RAM: "dn2lg2hrvbn5b8lm7em4",
				GPU: "dn20ml8ifdps6m7048an",
			},
			"ice-lake-t4/100/preemptible": {
				CPU: "dn2lsfskfirek2985fnd",
				RAM: "dn2im0g43iedeohe4sac",
				GPU: "dn2cpk4mc82b1vib72e5",
			},
			"ice-lake-t4i/100/regular": {
				CPU: "dn242l2ivnhdd5so2oga",
				RAM: "dn290pbmohupnus9ajb7",
				GPU: "dn2hql9evci880d8jq7i",
			},
			"ice-lake-t4i/100/preemptible": {
				CPU: "dn2960mi7268n67o8iae",
				RAM: "dn25rffeums4j1ku5649",
				GPU: "dn2qlml2u48bng4jgilh",
			},
			"broadwell-v100/100/regular": {
				CPU: "dn2sfcnkn3jlhmq568ac",
				RAM: "dn2nccae8nra81iqphdn",
				GPU: "dn2oroscvvtb6sqtt83i",
			},
			"broadwell-v100/100/preemptible": {
				CPU: "dn2t7aa68lehsmvo5mss",
				RAM: "dn2k0omvmglh857u60vu",
				GPU: "dn2lov15qqamcimfv84q",
			},
			"cascade-lake-v100/100/regular": {
				CPU: "dn2udmu2aa9jm5a8f4ug",
				RAM: "dn2qtp90p3r8l8vakmm6",
				GPU: "dn2dlvuk2ecf6hu0kjtl",
			},
			"cascade-lake-v100/100/preemptible": {
				CPU: "dn2h4u30djq3jhh8dqh8",
				RAM: "dn2hotj7skno0turhbq1",
				GPU: "dn23ppvthcls7rjt5pol",
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
	gpuPlatforms := map[string]string{}
	for instanceType, platform := range mapping.Platforms {
		if strings.TrimSpace(instanceType) == "" || strings.TrimSpace(platform.Platform) == "" {
			return fmt.Errorf("Yandex platform mappings require non-empty instance type and platform")
		}
		if platform.CoreFraction <= 0 || platform.CoreFraction > 100 {
			return fmt.Errorf("Yandex platform %q has invalid core fraction %d", instanceType, platform.CoreFraction)
		}
		if strings.TrimSpace(platform.GPUType) != "" {
			gpuPlatforms[strings.ToLower(platform.Platform)] = instanceType
		}
	}
	for key, skus := range mapping.NodeSKUs {
		if strings.TrimSpace(key) == "" || strings.TrimSpace(skus.CPU) == "" || strings.TrimSpace(skus.RAM) == "" {
			return fmt.Errorf("Yandex node SKU mapping %q requires CPU and RAM SKU IDs", key)
		}
		platformName := strings.ToLower(strings.SplitN(key, "/", 2)[0])
		instanceType, isGPUPlatform := gpuPlatforms[platformName]
		if isGPUPlatform && strings.TrimSpace(skus.GPU) == "" {
			return fmt.Errorf("Yandex GPU platform %q node SKU mapping %q requires a GPU SKU ID", instanceType, key)
		}
		if !isGPUPlatform && strings.TrimSpace(skus.GPU) != "" {
			return fmt.Errorf("Yandex node SKU mapping %q has a GPU SKU but its platform has no GPU type", key)
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
		if node.GPU != "" {
			ids[node.GPU] = struct{}{}
		}
	}
	for _, id := range mapping.DiskSKUs {
		ids[id] = struct{}{}
	}
	return ids
}
