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

## Input refactoring

```
input:
  - interface: <template_string> # required
    kind: <template_string> # Connection or ClusterConnection. If "", then loopkup both
    interfaceLookup:
      namespace: <template_string> # Default to targetNamespace.
    namedConnection:
      name: <template_string>
      namespace: <template_string> # Default to targetNamespace. If "", then it is a clusterConnection
    release:
      name: <template_string>
      namespace: <template_string> # Default to targetNamespace
      outputName: <template_string>

    alias: <template_string> # optional. Default to interface
    optional: <template_bool> # Default: false. If true and the connection is missing, there is no error, and `.Inputs.<alias>` does not exists.
    allowMultiple: <bool> # Optional. If false, error in case of multiple providers on a binding. Default false

```

interfaceLookup, namedConnection and release are exclusive

If none, then:

```
input:
  - interface: <template_string> # required
    interfaceLookup:
      namespace: targetNamespace
```

## Misc

- Find a shortcut for ClusterInterface used only once
