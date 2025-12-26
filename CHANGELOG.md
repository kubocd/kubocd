# v0.3.0

Core

- A specific module name (`noname`) now allow the derivative components name to be build without the module name ([ref](https://www.kubocd.io/user-guide/140-under-the-hood/#naming_2))
- A mechanism to configure helmRelease deployment error has been implemented. ([ref](https://www.kubocd.io/user-guide/220-deployment-failure/))
- The `Release.spec.package.interval` is not mandatory anymore, with a default in global value.
- The `Release.spec.specPathByModule` attribute has bee removed, replaced by `Release.spec.moduleOverride[module].specPath` ([ref](https://www.kubocd.io/reference/510-release/#releasespecmoduleoverride)).
- Many default values are now configurable in a `config` resource ([ref](https://www.kubocd.io/reference/530-config/#configspec))

CLI

- Adding KCD_OCI_{REGISTRY}_USER and KCD_OCI_{REGISTRY}_USER environment variable for alternate OCI registry
  authentification. ([ref](https://www.kubocd.io/user-guide/130-a-first-deployment/#package-build))
- When building a package with an helm chart from a Git repository, only charts related files are included 
  (cf: https://helm.sh/docs/v3/topics/charts). A new `extraFilePrefixes` has been added to include other files if needed ([ref](https://www.kubocd.io/reference/500-package/#packagemodulesourcegit))
- A `helm dependency update` command is now performed during packaging for both local and git source.
- A new `kubocd dump config` has been added ([ref](https://www.kubocd.io/user-guide/180-kubocd-cli/#kubocd-dump-config))

Helm (`kubocd-ctrl` and `kubocd-wh`)

- Add `nodeSelector` and `tolerations`, to control deployment location.
- Add a `deployInControlPlane` shortcut, for cluster with standard layout.
