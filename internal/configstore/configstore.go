package configstore

import (
	"context"
	"kubocd/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sort"
	"strings"
	"sync"
)

type ConfigStore interface {
	Init(ctx context.Context, kubeClient client.Client, myPodNamespace string) error
	IsClusterRole(role string) bool
	GetPackageRedirect(oldUrl string) (packageRedirectSpec *v1alpha1.PackageRedirectSpec, newUrl string)
	GetImageRedirect(oldUrl string) (imageRedirectSpec *v1alpha1.ImageRedirectSpec, newUrl string)
	GetDefaultContexts() []v1alpha1.NamespacedName
	AddConfigs(configs *v1alpha1.ConfigList, defaultNamespace string)
	ObjectMap() map[string]interface{} // Get a map to dump as yaml in debug
	GetDefaultNamespaceContext() string
}

type configStore struct {
	mutex                   sync.Mutex
	clusterRoles            map[string]bool
	packageRedirects        []*v1alpha1.PackageRedirectSpec
	imageRedirects          []*v1alpha1.ImageRedirectSpec
	defaultContexts         []v1alpha1.NamespacedName
	defaultNamespaceContext string
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

func (c *configStore) GetDefaultNamespaceContext() string {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	return c.defaultNamespaceContext
}

func (c *configStore) AddConfigs(configList *v1alpha1.ConfigList, defaultNamespace string) {
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
	for _, config := range configs.Items {
		for _, role := range config.Spec.ClusterRoles {
			c.clusterRoles[role] = true
		}
		c.packageRedirects = append(c.packageRedirects, config.Spec.PackageRedirects...)
		c.imageRedirects = append(c.imageRedirects, config.Spec.ImageRedirects...)
		c.defaultContexts = append(c.defaultContexts, config.Spec.DefaultContexts...)
		c.defaultNamespaceContext = config.Spec.DefaultNamespaceContext
	}
	for idx := range c.defaultContexts {
		if c.defaultContexts[idx].Namespace == "" {
			c.defaultContexts[idx].Namespace = defaultNamespace
		}
	}
}

func (c *configStore) ObjectMap() map[string]interface{} {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	return map[string]interface{}{
		"clusterRoles":     c.clusterRoles,
		"packageRedirects": c.packageRedirects,
	}
}

func (c *configStore) Init(ctx context.Context, kubeClient client.Client, myPodNamespace string) error {
	configs := &v1alpha1.ConfigList{}
	err := kubeClient.List(ctx, configs, client.InNamespace(myPodNamespace))
	if err != nil {
		return err
	}
	c.AddConfigs(configs, myPodNamespace)
	return nil
}
