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
	"sort"
	"strings"
	"sync"
	"time"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

type ConfigStore interface {
	Init(ctx context.Context, kubeClient client.Client, myPodNamespace string) error
	IsClusterRole(role string) bool
	GetPackageRedirect(oldUrl string) (packageRedirectSpec *v1alpha1.PackageRedirectSpec, newUrl string)
	GetImageRedirect(oldUrl string) (imageRedirectSpec *v1alpha1.ImageRedirectSpec, newUrl string)
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
}

type configStore struct {
	mutex                    sync.Mutex
	clusterRoles             map[string]bool
	packageRedirects         []*v1alpha1.PackageRedirectSpec
	imageRedirects           []*v1alpha1.ImageRedirectSpec
	defaultContexts          []v1alpha1.NamespacedName
	defaultNamespaceContexts []string
	DefaultOnFailureStrategy string
	OnFailureStrategyByName  map[string]map[string]interface{} `json:"onFailureStrategyByName,omitempty"`
	DefaultHelmTimeout       time.Duration
	DefaultHelmInterval      time.Duration
	DefaultPackageInterval   time.Duration
	SpecPatch                *apiextensionsv1.JSON
}

var _ ConfigStore = &configStore{}

func New() ConfigStore {
	return &configStore{}
}

func (c *configStore) IsClusterRole(role string) bool {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	return c.clusterRoles[role]
}

// GetPackageRedirect return a redirected url
// oldUrl and NewUrl must be without scheme (oci:// or other)
func (c *configStore) GetPackageRedirect(oldUrl string) (packageRedirectSpec *v1alpha1.PackageRedirectSpec, newUrl string) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	for _, redirect := range c.packageRedirects {
		if strings.HasPrefix(oldUrl, redirect.OldPrefix) {
			if redirect.NewPrefix == "" {
				// We don't want to change the URL. Just add some addOns
				return redirect, oldUrl
			} else {
				return redirect, redirect.NewPrefix + oldUrl[len(redirect.OldPrefix):]
			}
		}
	}
	return nil, oldUrl
}

func (c *configStore) GetImageRedirect(oldUrl string) (imageRedirectSpec *v1alpha1.ImageRedirectSpec, newUrl string) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	for _, redirect := range c.imageRedirects {
		if strings.HasPrefix(oldUrl, redirect.OldPrefix) {
			if redirect.NewPrefix == "" {
				return redirect, oldUrl
			} else {
				// We don't want to change the URL. Just add some addOns
				return redirect, redirect.NewPrefix + oldUrl[len(redirect.OldPrefix):]
			}
		}
	}
	return nil, oldUrl
}

func (c *configStore) GetDefaultContexts() []v1alpha1.NamespacedName {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	return c.defaultContexts
}

func (c *configStore) GetDefaultNamespaceContexts() []string {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	return c.defaultNamespaceContexts
}

func (c *configStore) AddConfigs(configList *v1alpha1.ConfigList, defaultNamespace string) error {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	configs := configList.DeepCopy()
	sort.Slice(configs.Items, func(i, j int) bool {
		return configs.Items[i].Name < configs.Items[j].Name
	})
	c.clusterRoles = make(map[string]bool)
	c.packageRedirects = make([]*v1alpha1.PackageRedirectSpec, 0, 10)
	c.imageRedirects = make([]*v1alpha1.ImageRedirectSpec, 0, 10)
	c.defaultContexts = make([]v1alpha1.NamespacedName, 0, 10)
	c.defaultNamespaceContexts = make([]string, 0, 10)
	c.OnFailureStrategyByName = make(map[string]map[string]interface{})
	c.DefaultOnFailureStrategy = ""
	c.DefaultHelmTimeout = time.Minute * 2
	c.DefaultHelmInterval = time.Minute * 30
	c.DefaultPackageInterval = time.Minute * 30
	c.SpecPatch = nil
	for _, config := range configs.Items {
		for _, role := range config.Spec.ClusterRoles {
			c.clusterRoles[role] = true
		}
		c.packageRedirects = append(c.packageRedirects, config.Spec.PackageRedirects...)
		c.imageRedirects = append(c.imageRedirects, config.Spec.ImageRedirects...)
		c.defaultContexts = append(c.defaultContexts, config.Spec.DefaultContexts...)
		c.defaultNamespaceContexts = append(c.defaultNamespaceContexts, config.Spec.DefaultNamespaceContexts...)
		if config.Spec.OnFailureStrategies != nil {
			for _, strategy := range config.Spec.OnFailureStrategies {
				v := make(map[string]interface{})
				err := yaml.Unmarshal(strategy.Values.Raw, &v)
				if err != nil {
					return fmt.Errorf("OnFailureStrategy '%s' failed to unmarshal value: %v", strategy.Name, err)
				}
				c.OnFailureStrategyByName[strategy.Name] = v
			}
		}
		if config.Spec.DefaultOnFailureStrategy != "" {
			if c.DefaultOnFailureStrategy != "" && c.DefaultOnFailureStrategy != config.Spec.DefaultOnFailureStrategy {
				return fmt.Errorf("DefaultOnFailureStrategy is defined multiple times (%s and %s", c.DefaultOnFailureStrategy, config.Spec.DefaultOnFailureStrategy)
			}
			c.DefaultOnFailureStrategy = config.Spec.DefaultOnFailureStrategy
		}
		if config.Spec.DefaultHelmTimeout != nil {
			c.DefaultHelmTimeout = config.Spec.DefaultHelmTimeout.Duration
		}
		if config.Spec.DefaultHelmInterval != nil {
			c.DefaultHelmInterval = config.Spec.DefaultHelmInterval.Duration
		}
		if config.Spec.DefaultPackageInterval != nil {
			c.DefaultPackageInterval = config.Spec.DefaultPackageInterval.Duration
		}
		c.SpecPatch = config.Spec.SpecPatch
	}
	for idx := range c.defaultContexts {
		if c.defaultContexts[idx].Namespace == "" {
			c.defaultContexts[idx].Namespace = defaultNamespace
		}
	}
	if c.DefaultOnFailureStrategy != "" && c.OnFailureStrategyByName[c.DefaultOnFailureStrategy] == nil {
		return fmt.Errorf("OnFailureStrategy '%s' is not defined while defined as defaut", c.DefaultOnFailureStrategy)
	}
	return nil
}

func (c *configStore) ObjectMap() map[string]interface{} {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	return map[string]interface{}{
		"clusterRoles":             c.clusterRoles,
		"packageRedirects":         c.packageRedirects,
		"imageRedirects":           c.imageRedirects,
		"defaultContexts":          c.defaultContexts,
		"defaultNamespaceContexts": c.defaultNamespaceContexts,
		"defaultOnFailureStrategy": c.DefaultOnFailureStrategy,
		"onFailureStrategyByName":  c.OnFailureStrategyByName,
	}
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
	return misc.DeepCopyMap(c.OnFailureStrategyByName[name])
}

func (c *configStore) GetDefaultOnFailureStrategy() map[string]interface{} {
	if c.DefaultOnFailureStrategy == "" {
		return nil
	}
	return c.GetOnFailureStrategy(c.DefaultOnFailureStrategy)
}

func (c *configStore) GetDefaultHelmTimeout() time.Duration {
	return c.DefaultHelmTimeout
}

func (c *configStore) GetDefaultHelmInterval() time.Duration {
	return c.DefaultHelmInterval
}

func (c *configStore) GetDefaultPackageInterval() time.Duration {
	return c.DefaultPackageInterval
}

func (c *configStore) GetSpecPatch() *apiextensionsv1.JSON {
	return c.SpecPatch
}
