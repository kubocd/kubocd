/*
Copyright 2025 Kubotal

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package configstore

import (
	"context"
	"fmt"
	"kubocd/api/v1alpha1"
	"kubocd/internal/misc"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

type ConfigStore interface {
	Init(ctx context.Context, kubeClient client.Client, myPodNamespace string) error
	IsClusterRole(role string) bool
	GetPackageRedirect(oldUrl string) (packageRedirectSpec *v1alpha1.PackageRedirectSpec, newUrl string)
	GetImageRedirects() []*v1alpha1.ImageRedirectSpec
	GetDefaultContexts() []v1alpha1.NamespacedName
	AddConfigs(configs *v1alpha1.ConfigList, defaultNamespace string) error
	ObjectMap() map[string]interface{} // Get a map to dump as yaml in debug
	GetDefaultNamespaceContexts() []string
	GetOnFailureStrategy(name string) map[string]interface{}
	GetDefaultOnFailureStrategy() map[string]interface{}
	GetDefaultHelmTimeout() time.Duration
	GetDefaultHelmInterval() time.Duration
	GetSpecPatch() *apiextensionsv1.JSON
	GetDefaultPackageInterval() time.Duration
	// Version grows with each configuration that AddConfigs publishes.
	Version() uint64
}

// configuration holds what the Configs set. Once published, it is never
// modified: the readers return copies of its slices, maps and pointers.
type configuration struct {
	version                  uint64
	clusterRoles             map[string]bool
	packageRedirects         []*v1alpha1.PackageRedirectSpec
	imageRedirects           []*v1alpha1.ImageRedirectSpec
	defaultContexts          []v1alpha1.NamespacedName
	defaultNamespaceContexts []string
	defaultOnFailureStrategy string
	onFailureStrategyByName  map[string]map[string]interface{}
	defaultHelmTimeout       time.Duration
	defaultHelmInterval      time.Duration
	defaultPackageInterval   time.Duration
	specPatch                *apiextensionsv1.JSON
}

type configStore struct {
	addMutex sync.Mutex
	current  atomic.Pointer[configuration]
}

var _ ConfigStore = &configStore{}

func New() ConfigStore {
	store := &configStore{}
	store.current.Store(&configuration{})
	return store
}

func (c *configStore) IsClusterRole(role string) bool {
	return c.current.Load().clusterRoles[role]
}

// GetPackageRedirect return a redirected url
// oldUrl and NewUrl must be without scheme (oci:// or other)
func (c *configStore) GetPackageRedirect(oldUrl string) (packageRedirectSpec *v1alpha1.PackageRedirectSpec, newUrl string) {
	for _, redirect := range c.current.Load().packageRedirects {
		if strings.HasPrefix(oldUrl, redirect.OldPrefix) {
			if redirect.NewPrefix == "" {
				// We don't want to change the URL. Just add some addOns
				return redirect.DeepCopy(), oldUrl
			} else {
				return redirect.DeepCopy(), redirect.NewPrefix + oldUrl[len(redirect.OldPrefix):]
			}
		}
	}
	return nil, oldUrl
}

// GetImageRedirects returns a copy of the image redirects of all Configs, in
// Config name order.
func (c *configStore) GetImageRedirects() []*v1alpha1.ImageRedirectSpec {
	imageRedirects := c.current.Load().imageRedirects
	redirects := make([]*v1alpha1.ImageRedirectSpec, len(imageRedirects))
	for i, redirect := range imageRedirects {
		redirects[i] = redirect.DeepCopy()
	}
	return redirects
}

func (c *configStore) GetDefaultContexts() []v1alpha1.NamespacedName {
	return slices.Clone(c.current.Load().defaultContexts)
}

func (c *configStore) GetDefaultNamespaceContexts() []string {
	return slices.Clone(c.current.Load().defaultNamespaceContexts)
}

// AddConfigs replaces the configuration with the one that the Configs of
// configList set, applied in name order. On error, the previous configuration
// stays.
func (c *configStore) AddConfigs(configList *v1alpha1.ConfigList, defaultNamespace string) error {
	c.addMutex.Lock()
	defer c.addMutex.Unlock()
	cfg, err := newConfiguration(configList, defaultNamespace)
	if err != nil {
		return err
	}
	cfg.version = c.current.Load().version + 1
	c.current.Store(cfg)
	return nil
}

func newConfiguration(configList *v1alpha1.ConfigList, defaultNamespace string) (*configuration, error) {
	configs := configList.DeepCopy()
	sort.Slice(configs.Items, func(i, j int) bool {
		return configs.Items[i].Name < configs.Items[j].Name
	})
	cfg := &configuration{
		clusterRoles:             make(map[string]bool),
		packageRedirects:         make([]*v1alpha1.PackageRedirectSpec, 0, 10),
		imageRedirects:           make([]*v1alpha1.ImageRedirectSpec, 0, 10),
		defaultContexts:          make([]v1alpha1.NamespacedName, 0, 10),
		defaultNamespaceContexts: make([]string, 0, 10),
		onFailureStrategyByName:  make(map[string]map[string]interface{}),
		defaultHelmTimeout:       time.Minute * 2,
		defaultHelmInterval:      time.Minute * 30,
		defaultPackageInterval:   time.Minute * 30,
	}
	for _, config := range configs.Items {
		for _, role := range config.Spec.ClusterRoles {
			cfg.clusterRoles[role] = true
		}
		cfg.packageRedirects = append(cfg.packageRedirects, config.Spec.PackageRedirects...)
		cfg.imageRedirects = append(cfg.imageRedirects, config.Spec.ImageRedirects...)
		cfg.defaultContexts = append(cfg.defaultContexts, config.Spec.DefaultContexts...)
		cfg.defaultNamespaceContexts = append(cfg.defaultNamespaceContexts, config.Spec.DefaultNamespaceContexts...)
		if config.Spec.OnFailureStrategies != nil {
			for _, strategy := range config.Spec.OnFailureStrategies {
				if strategy.Strategy != nil {
					v := make(map[string]interface{})
					err := yaml.Unmarshal(strategy.Strategy.Raw, &v)
					if err != nil {
						return nil, fmt.Errorf("OnFailureStrategy '%s' failed to unmarshal value: %v", strategy.Name, err)
					}
					cfg.onFailureStrategyByName[strategy.Name] = v
				}
			}
		}
		if config.Spec.DefaultOnFailureStrategy != "" {
			if cfg.defaultOnFailureStrategy != "" && cfg.defaultOnFailureStrategy != config.Spec.DefaultOnFailureStrategy {
				return nil, fmt.Errorf("DefaultOnFailureStrategy is defined multiple times (%s and %s", cfg.defaultOnFailureStrategy, config.Spec.DefaultOnFailureStrategy)
			}
			cfg.defaultOnFailureStrategy = config.Spec.DefaultOnFailureStrategy
		}
		if config.Spec.DefaultHelmTimeout != nil {
			cfg.defaultHelmTimeout = config.Spec.DefaultHelmTimeout.Duration
		}
		if config.Spec.DefaultHelmInterval != nil {
			cfg.defaultHelmInterval = config.Spec.DefaultHelmInterval.Duration
		}
		if config.Spec.DefaultPackageInterval != nil {
			cfg.defaultPackageInterval = config.Spec.DefaultPackageInterval.Duration
		}
		if config.Spec.SpecPatch != nil {
			cfg.specPatch = config.Spec.SpecPatch
		}
	}
	for idx := range cfg.defaultContexts {
		if cfg.defaultContexts[idx].Namespace == "" {
			cfg.defaultContexts[idx].Namespace = defaultNamespace
		}
	}
	if cfg.defaultOnFailureStrategy != "" && cfg.onFailureStrategyByName[cfg.defaultOnFailureStrategy] == nil {
		return nil, fmt.Errorf("OnFailureStrategy '%s' is not defined while defined as defaut", cfg.defaultOnFailureStrategy)
	}
	return cfg, nil
}

func (c *configStore) ObjectMap() map[string]interface{} {
	cfg := c.current.Load()
	m := map[string]interface{}{
		"packageRedirects":         deepCopies(cfg.packageRedirects),
		"imageRedirects":           deepCopies(cfg.imageRedirects),
		"defaultContexts":          slices.Clone(cfg.defaultContexts),
		"defaultNamespaceContexts": slices.Clone(cfg.defaultNamespaceContexts),
		"defaultOnFailureStrategy": cfg.defaultOnFailureStrategy,
		"defaultHelmTimeout":       cfg.defaultHelmTimeout.String(),
		"defaultHelmInterval":      cfg.defaultHelmInterval.String(),
		"defaultPackageInterval":   cfg.defaultPackageInterval.String(),
		"specPatch":                cfg.specPatch.DeepCopy(),
	}
	// ---------------------------------------------------------
	onFailureStrategies := make([]map[string]interface{}, 0, len(cfg.onFailureStrategyByName))
	for name, strategy := range cfg.onFailureStrategyByName {
		item := make(map[string]interface{})
		item["name"] = name
		item["strategy"] = misc.DeepCopyMap(strategy)
		onFailureStrategies = append(onFailureStrategies, item)
	}
	m["onFailureStrategies"] = onFailureStrategies
	// ---------------------------------------------------------
	clusterRoles := make([]string, 0, len(cfg.clusterRoles))
	for role := range cfg.clusterRoles {
		clusterRoles = append(clusterRoles, role)
	}
	m["clusterRoles"] = clusterRoles
	return m
}

// deepCopies returns a deep copy of each item, and nil for nil.
func deepCopies[T interface{ DeepCopy() T }](items []T) []T {
	if items == nil {
		return nil
	}
	copies := make([]T, len(items))
	for i, item := range items {
		copies[i] = item.DeepCopy()
	}
	return copies
}

func (c *configStore) Init(ctx context.Context, kubeClient client.Client, myPodNamespace string) error {
	configs := &v1alpha1.ConfigList{}
	err := kubeClient.List(ctx, configs, client.InNamespace(myPodNamespace))
	if err != nil {
		return err
	}
	err = c.AddConfigs(configs, myPodNamespace)
	return err
}

func (c *configStore) GetOnFailureStrategy(name string) map[string]interface{} {
	// We need to DeepCopy the result, as this result may be modified by the caller. See release_helmrelease.deleteFailureConf()
	return misc.DeepCopyMap(c.current.Load().onFailureStrategyByName[name])
}

func (c *configStore) GetDefaultOnFailureStrategy() map[string]interface{} {
	cfg := c.current.Load()
	if cfg.defaultOnFailureStrategy == "" {
		return nil
	}
	return misc.DeepCopyMap(cfg.onFailureStrategyByName[cfg.defaultOnFailureStrategy])
}

func (c *configStore) GetDefaultHelmTimeout() time.Duration {
	return c.current.Load().defaultHelmTimeout
}

func (c *configStore) GetDefaultHelmInterval() time.Duration {
	return c.current.Load().defaultHelmInterval
}

func (c *configStore) GetDefaultPackageInterval() time.Duration {
	return c.current.Load().defaultPackageInterval
}

func (c *configStore) GetSpecPatch() *apiextensionsv1.JSON {
	return c.current.Load().specPatch.DeepCopy()
}

func (c *configStore) Version() uint64 {
	return c.current.Load().version
}
