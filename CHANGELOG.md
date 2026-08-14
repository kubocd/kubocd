# v0.4.0

## Upgrading from v0.3.x

- Four CRDs are added: `interfaces`, `clusterinterfaces`, `connections`, `clusterconnections`. They live in the chart
  `crds/` directory, which **Helm never updates on upgrade**. Apply them by hand before upgrading the controller,
  otherwise reconciliation fails on unknown kinds.

## Core

- Implementation of Connection sub system
- `Interface` is namespaced and `ClusterInterface` has been added. A Connection is validated against the schema its
  interface declares.
- Packages declare `outputs` to publish Connections, and `inputs` to consume them. Both are now a single template
  rendering a list.
- New `connectionRef` parameter type. A package parameter typed this way declares its own input, and the resolved values
  are substituted in place, so templates read `.Parameters.<name>.<field>` like any other parameter.
- A Release waits in `WAIT_ICNX` until its non optional inputs resolve, and the message reports the root cause from the
  producing Release rather than a bare "waiting".
- A Connection a Release does not own is never patched nor deleted.
- The Release.Status has been modified. Error message are reported in a single field, decreasing the need to dig inside
  child resources to retrieve errors.
- Internally now using helm v4 (Following up fluxCD)
- Updated go and go libraries dependencies to latest current version.

## Fixes

- Bump go-git to v5.19.2, addressing two advisories on worktree operations and reference names, reached through the
  `git` chart source.

## CLI

- New `kubocd connections` command, dumping the producer to consumer relationships across namespaces or cluster wide.

# v0.3.1

Core

- Empty entries are pruned from generated values object.

  ```
  global:
      stuff:
        {{ if .Parameters.myVar }}
        config:
          foo: bar
        {{ end }}
  ```

  result in empty string ("") if `.Parameters.myVar` is false

  Previously, was:

  ```
  global:
      stuff: null
  ```

CLI:

- On render command, the targetNamespace was not properly set in resulting 'manifests'. Fixed

# v0.3.0

Core

- A specific module name (`noname`) now allow the derivative components name to be build without the module name
  ([ref](https://www.kubocd.io/user-guide/140-under-the-hood/#naming_2))
- A mechanism to configure helmRelease deployment error has been implemented.
  ([ref](https://www.kubocd.io/user-guide/220-deployment-failure/))
- The `Release.spec.package.interval` is not mandatory anymore, with a default in global value.
- The `Release.spec.specPathByModule` attribute has bee removed, replaced by
  `Release.spec.moduleOverride[module].specPath`
  ([ref](https://www.kubocd.io/reference/510-release/#releasespecmoduleoverride)).
- Many default values are now configurable in a `config` resource
  ([ref](https://www.kubocd.io/reference/530-config/#configspec))

CLI

- Adding KCD*OCI*{REGISTRY}_USER and KCD_OCI_{REGISTRY}\_USER environment variable for alternate OCI registry
  authentification. ([ref](https://www.kubocd.io/user-guide/130-a-first-deployment/#package-build))
- When building a package with an helm chart from a Git repository, only charts related files are included (cf:
  https://helm.sh/docs/v3/topics/charts). A new `extraFilePrefixes` has been added to include other files if needed
  ([ref](https://www.kubocd.io/reference/500-package/#packagemodulesourcegit))
- A `helm dependency update` command is now performed during packaging for both local and git source.
- A new `kubocd dump config` has been added ([ref](https://www.kubocd.io/user-guide/180-kubocd-cli/#kubocd-dump-config))

Helm (`kubocd-ctrl` and `kubocd-wh`)

- Add `nodeSelector` and `tolerations`, to control deployment location.
- Add a `deployInControlPlane` shortcut, for cluster with standard layout.
- Change in `values.yaml` file layout: Configuration is now in `config.content` sub-element. And a `config.enabled` has
  been added. ([ref](https://www.kubocd.io/user-guide/170-context-and-config/#configuration-via-helm-chart))
