Une nouvelle version de spec.md, après reflexion sur la gestion des namespaces.

```
apiVersion: kubocd.kubotal.io/v2alpha1
kind: Connection
metadata:
  name: <string>
  namespace: <string>
spec:
  contract: <string>  # Required
  priority: <int> # Optionnal
  values: <map[string]interface{}>
  disabled: <bool> # default false
  description: <string>
  syncedResources:
    - name: <string>
      inReleaseNamespace: bool  # Default: false. Normaly, syncedResource are fetched from targetNamespace.
  outputName: <string>
  parentRelease:
    name: <string>
    namespace: <string>

```

```
apiVersion: kubocd.kubotal.io/v2alpha1
kind: Replication
metadata:
  name: <string>
  namespace: <string>
spec:
  source:
    contract: <string>
    release:
      name: <string>
      namespace: <string>
      outputName: <string>
  target:
    releases:
      - name: <string>
        namespace: <string>
```

```
apiVersion: kubocd.kubotal.io/v2alpha1
kind: Replication
metadata:
  name: <string>
  namespace: <string>
spec:
  source:
    name: <string>
  target:
    name: <string>
    namespace: <string>

```
