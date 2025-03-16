package controller

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"kubocd/internal/misc"
	"sigs.k8s.io/yaml"
)

func Merge(base map[string]interface{}, addon *apiextensionsv1.JSON) (map[string]interface{}, error) {
	if addon == nil {
		return base, nil
	}
	inc := make(map[string]interface{})
	err := yaml.Unmarshal(addon.Raw, &inc)
	if err != nil {
		return nil, err
	}
	return misc.MergeMaps(base, inc), nil
}
