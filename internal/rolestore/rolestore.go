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

package rolestore

import (
	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/types"
	"kubocd/internal/configstore"
	"sync"
)

type RoleStore interface {
	MissingDependency(requester types.NamespacedName, dependencies []string) string
	RegisterRelease(namespacedName types.NamespacedName, roles []string)
	UnRegisterRelease(namespacedName types.NamespacedName)
}

type roleStore struct {
	mutex          sync.Mutex
	rolesByRelease map[string][]string
	configStore    configstore.ConfigStore
	logger         logr.Logger
}

var _ RoleStore = &roleStore{}

func New(configStore configstore.ConfigStore, logger logr.Logger) RoleStore {
	return &roleStore{
		configStore:    configStore,
		logger:         logger,
		rolesByRelease: make(map[string][]string),
	}
}

func (store *roleStore) isDependencyOK(requester types.NamespacedName, dependency string) bool {
	if store.configStore.IsClusterRole(dependency) {
		store.logger.V(1).Info("dependency OK (cluster role)", "dependency", dependency, "requester", requester.String())
		return true
	}
	for rel, roles := range store.rolesByRelease {
		for _, r := range roles {
			if r == dependency {
				store.logger.V(1).Info("dependency OK", "dependency", dependency, "provider", rel, "requester", requester.String())
				return true
			}
		}
	}
	return false
}

func (store *roleStore) MissingDependency(requester types.NamespacedName, dependencies []string) string {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	for _, dependency := range dependencies {
		if !store.isDependencyOK(requester, dependency) {
			store.logger.V(1).Info("dependency missing", "dependency", dependency, "requester", requester.String())
			return dependency
		}
	}
	return ""
}

func (store *roleStore) RegisterRelease(namespacedName types.NamespacedName, roles []string) {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	store.rolesByRelease[namespacedName.String()] = roles
	store.logger.V(1).Info("registered release role(s)", "release", namespacedName, "roles", roles)
}

func (store *roleStore) UnRegisterRelease(namespacedName types.NamespacedName) {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	delete(store.rolesByRelease, namespacedName.String())
	store.logger.V(1).Info("un-registered release role(s)", "release", namespacedName)
}
