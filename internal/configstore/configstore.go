package configstore

import (
	"kubocd/api/v1alpha1"
	"sort"
	"strings"
	"sync"
)

type ConfigStore interface {
	IsClusterRole(role string) bool
	GetKuboAppRedirect(oldUrl string) (kuboAppRedirectSpec *v1alpha1.KuboAppRedirectSpec, newUrl string)
	GetImageRedirect(oldUrl string) (imageRedirectSpec *v1alpha1.ImageRedirectSpec, newUrl string)
	AddConfigs(configs *v1alpha1.ConfigList)
	ObjectMap() map[string]interface{} // Get a map to dump as yaml in debug
}

type configStore struct {
	mutex            sync.Mutex
	clusterRoles     map[string]bool
	kuboAppRedirects []*v1alpha1.KuboAppRedirectSpec
	imageRedirects   []*v1alpha1.ImageRedirectSpec
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

// GetKuboAppRedirect return a redirected url
// oldUrl and NewUrl must be without scheme (oci:// or other)
func (c *configStore) GetKuboAppRedirect(oldUrl string) (kuboAppRedirectSpec *v1alpha1.KuboAppRedirectSpec, newUrl string) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	for _, redirect := range c.kuboAppRedirects {
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

func (c *configStore) AddConfigs(configList *v1alpha1.ConfigList) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	configs := configList.DeepCopy()
	sort.Slice(configs.Items, func(i, j int) bool {
		return configs.Items[i].Name < configs.Items[j].Name
	})
	c.clusterRoles = make(map[string]bool)
	c.kuboAppRedirects = make([]*v1alpha1.KuboAppRedirectSpec, 0, 10)
	c.imageRedirects = make([]*v1alpha1.ImageRedirectSpec, 0, 10)
	for _, config := range configs.Items {
		for _, role := range config.Spec.ClusterRoles {
			c.clusterRoles[role] = true
		}
		c.kuboAppRedirects = append(c.kuboAppRedirects, config.Spec.KuboAppRedirects...)
		c.imageRedirects = append(c.imageRedirects, config.Spec.ImageRedirects...)
	}
}

func (c *configStore) ObjectMap() map[string]interface{} {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	return map[string]interface{}{
		"clusterRoles":     c.clusterRoles,
		"kuboAppRedirects": c.kuboAppRedirects,
	}
}
