# v0.4.2

- `indent`, `nindent` and `replace` refuse an intermediate result larger than 4 MiB, even when the final rendered text
  is small, with `indent: the result is larger than 4194304 bytes`, `nindent: the result is larger than 4194304 bytes`
  or `replace: the result is larger than 4194304 bytes`. This applies to the controller and `kubocd render`, with no
  retry on the refusal in the controller.
- A negative indentation fails with `indent: the indentation must not be negative` or
  `nindent: the indentation must not be negative`, with no retry in the controller.
- `join`, `printf` and `regexReplaceAll` refuse an intermediate result when its size bound says it can exceed 4 MiB,
  even when the final rendered text is small, with `join: the result can exceed 4194304 bytes`,
  `printf: the result can exceed 4194304 bytes` or `regexReplaceAll: the result can exceed 4194304 bytes`.
  Conservative bounds can also refuse a smaller result: non-string join items and regex capture replacements can be
  over-counted. For `printf`, bounds can over-count quoted or hexadecimal strings (`%q`, `%x`, `%X`, and quoted strings
  in `%#v`), containers, numeric formatting (including precision) and invalid-format diagnostics. Plain `%s` and `%v`
  strings are counted exactly.
  The controller and `kubocd render` use the same bound, with no retry on the refusal in the controller.

# v0.4.1

- A refused update of the finalizer of a Release no longer quotes the answer of the API server.
- A Release computes why an output connection is not ready as its controller does, and never copies its stored message.
- The deletion of a Release logs the folder and the ClusterConnection it could not remove as values.
- An API error keeps a standard Kubernetes reason only, and a field only if a kind that KuboCD writes declares it.
- The controller logs the creation of an output connection once the API server has created it.
- A HelmRelease spec that does not decode blames `specPatch` only when the spec decodes without it.
- A failed list of a watch of the controller is logged without its message, which can quote the selector.
- A Context without `spec.context` is READY.
- A module type other than `HelmChart` or `Package` is refused, and fetching the chart of a module no longer panics.
- A Release names an output connection that no controller checked yet, and says when the `connections` gate is off.
- A missing parent Context is no longer reported as a failed read of it, nor the reverse.
- A Release waiting for a Flux source that is not ready shows the message of its Ready condition.
- A change of the Configs no longer crashes the controller, nor gives a Release a half-rebuilt configuration. Configs
  that fail to load leave the previous configuration in effect.
- A Config without `specPatch` no longer removes the `specPatch` of a Config sorted before it.
- A failed status update of a Release no longer changes how the next failure of another Release is reported.
- A Release names the Connection or the ClusterConnection of an output in its status from the pass that creates it.
- The controller stops when it cannot create an index of the Releases, instead of running without it.
- `kubocd package` reports a `manifest.json` that it cannot add to the image, instead of a later error.
- A Release is not READY while it cannot list its output connections, and retries.
- The error on the values of a connection of an input names the input as the other input errors do, `input#N` from 1.
- A Release leaves alone a ClusterConnection whose `parentRelease` names another Release, and reports it.
- A Release patches or deletes only the HelmReleases it controls. One of another Release, of a deleted Release of the
  same name, or without owner, is refused for an enabled module and left in place for a disabled one.
- A change of the Helm timeout or interval defaults of the Configs reaches, at their next reconciliation, the modules
  that take these defaults, also when their package is already loaded.
- The finalizer of a deleted Release stays until the API server accepts the deletion of each of its ClusterConnections.
- A cycle in the parents of a Context is reported as `parents form a cycle`, and the Context stays in ERROR until it is
  broken.
- A commit of `main` is published once its CI passed. A final version never moves its tag, nor deletes its release.
- The NetworkPolicy of the `kubocd-ctrl` chart opens source-controller to the pods of the KuboCD controller and of the
  Flux namespace, where it opened it to every pod of the cluster.
- A NetworkPolicy of the `kubocd-ctrl` chart lets only source-controller reach the chart server of the controller, and
  keeps the metrics port open when the metrics are on. The chart server no longer lists its folders.
- The `kubocd-wh` chart no longer installs mutating webhooks, and its validating webhooks check deletions only. Releases
  and Contexts can be created and updated, and a deleted Release loses its finalizer, while the webhook is down.
- The deletion of a Release checks `spec.protected` when it is set, before the protection in its status.
- A restarted controller keeps the HelmRepository of each Release whose package has not changed, where it deleted and
  recreated it. The first start of this version still recreates once the HelmRepositories that an earlier version
  created.
- One execution of a template renders at most 4 MiB of text, and fails past it with
  `the rendered text is larger than 4194304 bytes`. The controller schedules no retry on such an error, a change of what
  the Release reads reconciling it again.
- `until` builds at most 10000 items either way, and fails past it with `until: the count is beyond the limit of 10000`,
  with no retry either.
- The controller refuses a package archive larger than `--maxPackageArtifactSize` bytes (20 MiB by default), whose files
  hold more than `--maxPackageContentSize` bytes (40 MiB), or with more than `--maxPackageEntries` entries (1000), with
  no retry on the refusal. The folders that a name needs count as entries, and a name of more than 32 levels is refused.
  A fetch ends after 5 minutes. The flags go in the `controller.additionalArgs` of the `kubocd-ctrl` chart, for instance
  `--maxPackageArtifactSize=52428800`.
- The `original.yaml` and the `status.yaml` of a package are refused past `--maxPackageManifestSize` bytes each (1 MiB
  by default).
- The cache of loaded packages holds at most `--maxCachedPackages` packages (100 by default), and the packages of at
  most `--maxCachedPackagesSize` bytes of `original.yaml` and `status.yaml` (4 MiB). A package that left it is loaded
  again from its files.
- A field removed from a `specPatch`, or left by a change of failure strategy, leaves the existing HelmRelease, as it is
  absent from a new one. A field that the spec sets, `null` included, is written as before, and `spec.suspend` set on
  the HelmRelease, by `flux suspend` for instance, stays when the spec does not set it.
- A Release waits for its OCIRepository, its HelmRepository and its HelmReleases until Flux has reconciled their current
  generation, where it took a Ready condition of their previous spec.
- A package schema in OpenAPI form, with `$schema` set, keeps the `default` of an array parameter, where the parameter
  got an empty list.
- `kubocd package` refuses more than one manifest, where it packaged the first one only.
- An action in the `parameters` of a Release written as a map can quote a string, as in
  `{{ printf "%s.%s" .Context.a .Context.b }}`. Such parameters are parsed as before, then, when that fails, with their
  actions as written, unless an action holds, outside its comments, a backslash, a control character, or a tab or a
  newline in a string, which keeps the parse error. The text that an action renders is still inserted in the parameters
  as it is, and must leave them a valid document.
- `kubocd render` runs `helm template` with the namespace and the release name that Flux uses for the HelmRelease it
  writes, and `values.yaml` holds the values of that HelmRelease, patches included. It used the target namespace of the
  Release, the name of the HelmRelease and the values of the module before the patches. `valuesFrom` and the values
  files of a chart are not read.
- A Context with parents writes its status only when its phase, its parents or its values change.
- The CRD descriptions of `defaultHelmInterval` and `defaultPackageInterval` in the Config, and of
  `spec.package.interval` in the Release, give the defaults that apply: 30m for `defaultHelmInterval`, 5m for
  `spec.package.interval`, which the API server sets, and 30m for `defaultPackageInterval`, which applies to an interval
  of 0s.
- The deletion of a Release without `spec.protected` is refused until the controller has written its computed
  protection, which `status.printProtected` (column `PRT`) shows, with a message that says to set
  `spec.protected: false` to delete it anyway, as for a Release whose package never loads. A status without
  `printProtected` gets it at the next status write after the controller loads the package. A protection computed for an
  earlier package is not detected.
- The kustomize installer is removed: `make build-installer`, `config/_dist_/install.yaml` and the files of `config/`
  that only it read. It could not start the controller. The Helm charts install KuboCD.
- The Replication controller reports a refused request with the reason of the API server and without its message, in the
  status, the events and the log, as the other controllers do.
- The check of the dependencies of a Release reads one configuration, where it could accept one dependency by the
  previous configuration and another by the new one.
- A Release is reconciled when a Context it takes by default changes, also after a change of the default Contexts of the
  Configs, and the deletion of a Release no longer stops the reconciliation of the other Releases of its Contexts.
- The spec of a HelmRelease is built from one configuration, and a change of the Configs during the build no longer
  blames the `specPatch` of a module for an error of the configuration.
- Every Release is reconciled again once a change of the Configs is published, where a Release reconciled before the
  publication could keep the previous configuration until another change.
- `make docker-buildx` fails when the image build fails, and the publication of a commit with it.
- While the Ready condition of a HelmRelease is Unknown, as before an upgrade, the Release shows `Deploying...`, and no
  longer gives a Warning event with the message of the previous Helm action.
- Hardening: building the model of the templates of a Release returns an error, where it panicked, when the JSON of the
  Release does not convert to YAML.
- The `manifests.yaml` of `kubocd render` holds the manifests only, the debug lines of helm going to the standard error.
- The errors of the defaults of an OpenAPI schema name the property at fault from its own path.

# v0.4.0

- BREAKING: Package and Release templates can only call a fixed list of functions, whose result depends on their
  arguments only. The list is on the "Template functions" reference page of the documentation. Every other function is
  gone: the Sprig functions outside the list, among them `getHostByName`, `now`, `date`, `rand*`, `uuidv4`, `genCA`,
  `b64enc` and `contains`, the KuboCD functions `readFile`, `toToml`, `fromYamlArray` and `fromJsonArray`, and the
  `include`, `tpl` and `lookup` placeholders. A template calling one of them fails with `function "x" not defined`.
- BREAKING: `resolveDNS` is off by default, and only package templates can call it. The platform operator turns it on
  for every package of the cluster with the `controller.allowDNSLookups` value of the `kubocd-ctrl` chart, and
  `kubocd render` has the matching `--allowDNSLookups` flag. When off, a package calling it fails to render with
  `resolveDNS is disabled by platform policy`. The parameters of a Release never get it.
- BREAKING: A `${...}` sequence in a package or Release manifest stays literal. Neither the CLI nor the controller
  replaces it with an environment variable anymore. A CI pipeline prepares its substitutions before running
  `kubocd package`.
- BREAKING: Templates receive plain data only. `.ImageRedirector` is replaced by `.ImageRedirects`, the table of image
  redirects from the Configs. `imageRedirect` is called as before, with the root context (`.` or `$`).
- BREAKING: `keys` returns the keys sorted.
- BREAKING: `slice` is the text/template builtin, which takes integer indexes only, where the Sprig version converted
  them.
- Each template execution works on its own copy of the data. `set` and `sortAlpha` no longer change what another
  template, or another Release of the same package, sees.
- `printf` rejects the `%p` verb, which prints a memory address.
- A map with two keys that are the same once converted to strings (`1` and `"1"`, `true` and `"true"`) is an error in a
  manifest read by the CLI or the controller, in the rendered text of a template, and in a document read by `fromYaml`.
  One of the two values used to be kept at random. The keys are checked before the document is converted, so that the
  same document always gives the same result. A map key that cannot be converted to a string, such as `null`, gets a
  fixed message too.
- The value a Release gives to a `connectionRef` parameter is a connection name, never rendered again as a template.
  Only the templated default of the package schema is rendered.
- Template render errors no longer quote the data or the rendered text. They name the template, the place and the
  reason. A failing action is located by its line and column in the field, or in the `templateHeader`, which is then
  named. Invalid YAML is located by its line in the rendered text. A parse error keeps the message of text/template,
  which quotes the template only, and whose line counts from the first line of the `templateHeader`. The messages of
  text/template and of the Sprig functions at execution, which can print the value at fault, give way to a category such
  as `error calling regexReplaceAll`. The messages of the KuboCD functions, and those written with `fail` and
  `required`, are kept as written. A schema validation error on the parameters or the context no longer names a property
  the schema does not declare.
- Once the templates of a Release are rendered, the errors of building its HelmReleases (`specPatch`,
  `onFailureStrategy`), of reading, creating, updating or deleting them and the connections of its outputs, of reading
  or listing its input connections, and of checking its conditions no longer quote a rendered value either. They name
  the module, the field, or the index of the output, input or condition, and a category, such as
  `specPatch of module 'main' has a field that HelmRelease does not define`. From then on, a request refused by the API
  server gives the reason of the answer and the fields at fault, without its message, which can quote the value refused.
  This holds for the status, the events and the log of the controller, status updates of the Release included, for the
  input lookups of `kubocd render`, and for the deletion of the ClusterConnections of a deleted Release. A Release
  waiting for a condition still names the object it checks, but no longer quotes the CEL expression nor the message of
  the CEL evaluation, which can quote the object. A Release waiting for a connection or a role still names it. The
  messages copied from a HelmRelease (Helm) are shown as written.
- A Connection or a ClusterConnection whose values do not match its contract gets a message that names the place and the
  category of the failure, with the property names that the contract schema declares only:
  `validation error on values: (root): a property is not declared in the schema`. Values that do not decode and a
  missing contract get fixed messages, the latter naming `spec.contract`. When the API server refuses to read the
  contract or to update the status, their controllers give the reason and the fields of the answer only. A Release
  copies these messages into its status and events, as before.
- The images and the CLI binaries are built with Go 1.27.3 and `golang.org/x/net` v0.60.0, which fix the vulnerabilities
  that `govulncheck` reports in code called by KuboCD, such as HTTP/2, TLS and certificate verification. Building from
  source now requires Go 1.27.3 or later.
- `kubocd render`, `kubocd dump package`, `kubocd dump oci` and `kubocd dump helmRepository` refuse an archive entry
  whose name is not a relative path inside the target folder, or holds a backslash, and prevent archive paths from
  escaping that folder, including through existing symbolic links. Link entries are still skipped.
- The `controller.extraEnv` and `webhook.extraEnv` chart values add their entries to the `env` list of the container.
  Their indentation made the rendered Deployment invalid. The webhook pod now declares the volumes of
  `webhook.extraVolumes`, not only their mounts.

## Security

Before v0.4.0, KuboCD templates could disclose files and credentials readable by the controller or the host running
`kubocd render`. Manifest loading also expanded `${VAR}` from the controller or CLI environment. The v0.4.0 CLI also
refuses an archive entry that would be written outside the target folder, and v0.4.0 is built with the fixed
`golang.org/x/net` v0.60.0 and Go 1.27.3. Upgrade the controller and CLI to v0.4.0. If untrusted parties could create or
modify Releases, supply packages consumed by the controller, or provide manifests processed by the CLI, treat
credentials accessible through those paths as potentially exposed and rotate or revoke them.

## Upgrading from v0.3.x

- Before upgrading the controller, use the v0.4.0 CLI's `kubocd dump package <manifest>` to check package parsing
  without publishing an image. It rejects a template calling a function outside the list, and a manifest with map keys
  that are the same once converted to strings. This check does not execute templates or compile templated
  `connectionRef` defaults. Also test representative Releases with `kubocd render`. Upgrade consuming controllers before
  publishing packages that rely on the new literal `${...}` behaviour. Once the controller is upgraded, a Release whose
  package fails stays in error, its HelmReleases are left as they are, and the Releases depending on its roles wait.
- A package calling `resolveDNS` needs `controller.allowDNSLookups: true`, or the address written in its parameters or
  its context.
- A manifest relying on `${VAR}` substitution is prepared by the CI before `kubocd package`.
- A template reading `.ImageRedirector` reads `.ImageRedirects` instead. One calling `slice` with a non-integer index
  converts it with `int`.
- The message that v0.3.x wrote in the status of a Connection or a ClusterConnection, which can name a property of its
  values, is replaced when the connection controller of v0.4.0, which runs with the `connections` feature gate on,
  reconciles the connection and records its status. It reconciles every connection when it starts. A refused read of the
  contract or a refused patch of the status delays the replacement to a later reconciliation. The change of the
  connection triggers a reconciliation of its Release, which copies the new message once its HelmReleases are ready.
  Until then, the log of the controller at the V(1) level (the `debug` log level) can show the old message of the
  Release, and it shows it once more in the trace of the status update that replaces it. Events already emitted keep
  their text.
- Remove any top-level `extraVolumes` value used only to work around the webhook guard, keep volume definitions under
  `webhook.extraVolumes`, and remove any patches that corrected `extraEnv` indentation.

# v0.3.2

Add the connectionRef parameter

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
