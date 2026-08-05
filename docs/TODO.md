## Output namespace

Make the relationship between Release and output Connection using spec.Parent instead of owner reference.

Then, add a 'namespace' attribute on output. Default to targetNamespace (Which default to release namespace)

```
output:
  - name: <string>  # Required
    interface: <string> # required
    kind: <template_string> # Connection or ClusterConnection. Optional. Default to Connection
    namespace: <template_string> # Default to targetNamespace
    displayName: <template_string> # Optional. Default to .name
    priority: <template_int> # Optional. Default 200 for Connection and 100 for CLusterConnection
    description: <template_string> # Optional
    disabled: <templte_bool> # default false
    values: <template_map[string]interface{}>
```

## Misc

- Add a 'disabled' on input. (cf podinfo tls)

- Find a shortcut for ClusterInterface used only once

- Allow inputs and outputs chunk to be a complete template

- Pouvoir disabler une release
