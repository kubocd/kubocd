# v0.3.1

WARNING: All feature tagged as EXPERIMENTAL are under active development. Their resources, fields and behavior may
change, or be removed, in any future release without prior notice and without a migration path. Do not use for
production and be ready to modify any application using one of these features.

Tu use them, such feature must be explicitly enabled when deploying the Helm chart. See `controller.featureGates` in the
`values.yaml` file.

## Controller:

- EXPERIMENTAL: Implementation of Replication resources
- EXPERIMENTAL: Implementation of Connection sub system
- The Release.Status has been modified. Error message are reported in a single field, decreasing the need to dig inside
  child resources to retrieve errors.
- Internally now using helm v4 (Following up fluxCD)
- Updated go and go libraries dependencies to latest current version.

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

  NOTE: The pruning also removes entries explicitly set to `null`, as well as empty maps (`{}`), including inside a
  list. As a consequence, the Helm idiom `key: null`, which removes a value defined in the chart's `values.yaml` file,
  is not effective anymore. The chart default value is kept.

## CLI:

- On render command, the targetNamespace was not properly set in resulting 'manifests'. Fixed

## Upgrading from v0.3.0

**The CRDs must be applied manually, before upgrading the Helm chart.** They are shipped in the `crds/` folder of the
`kubocd-ctrl` chart, which Helm applies on `install`, but never on `upgrade`. As v0.3.1 both modifies the `Release` CRD
and adds new ones, a plain `helm upgrade` leaves the controller unable to work.

```
helm pull oci://quay.io/kubocd/charts/kubocd-ctrl --version v0.3.1 --untar
kubectl apply --server-side -f kubocd-ctrl/crds/crds.yaml
```

Skipping this step leads to one of the following:

- The controller exits at startup with `no matches for kind "Connection" in version "kubocd.kubotal.io/v1alpha1"`. The
  new `Contract`, `ClusterContract`, `Connection`, `ClusterConnection` and `Replication` CRDs must be present. Note the
  controller sets up watches and indexes on `Connection` and `ClusterConnection` whatever the value of
  `controller.featureGates`. So, having the `connections` feature gate off does NOT make these CRDs optional.
- Every `Release` status update is rejected with `Release "xxx" is invalid: status.missingDependency: Required value`,
  and no `Release` is reconciled anymore. This is because `Release.status.missingDependency` has been removed (replaced
  by `Release.status.message`) while it is a required field of the v0.3.0 CRD.

Other points to be aware of when upgrading:

- The `kubectl get releases` columns have changed: `Ready` is now `Rel.`, `Wait` is now `Message`, and a new `Out`
  column has been added. Scripts parsing this output must be adjusted.
- Explicit `null` values are now pruned from the generated values object (see below). A package relying on the Helm
  idiom `key: null` to remove a value defined in the chart's `values.yaml` will now keep the chart default. This may
  change the manifests generated for an already deployed `Release`.
- The `image.repository` Helm value does not default to `quay.io/kubocd/kubocd` anymore. The controller and webhook
  images are now published under `<registry>/exec/kubocd`, and the chart default is provided by the
  `kubotal_image_repository` annotation of the `Chart.yaml` file. A deployment pinning `image.repository` to the old
  value in its own values file must drop or update this setting.

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
