

# TODO


- Check path target a folder with Chart.yaml on module.source.git and module.source.local
- Dump chart content on kuboctl dump hr/oci ?
- make kubocd pack *.yaml working (Loop on args)

- Translate all KAD components
- Sandbox with bootstrap
- Doc
- A system to restrain targetNamespace to the one of the release. Or forbid createNamespace (/!\ appOfApp)

- Set Application in the model ?

- Implement protected fallback in case there is no webhook (break helmRelease ownership ?)
- Manage application in application

- A webhook to patch all image in pod manifests (https://slack.engineering/simple-kubernetes-webhook/)
- Embed application image to the package

# DONE

- Implement Application.module[X].enabled
- Implements Usage in Release.Status
- Helm release reporting in Release.status
- Implement Protected in status and in the webhook.
- Implement Release.suspended
- Implement intra-module dependencies
- Implement config resources
- Config in helm chart
- Context in helm chart
- A tool to fetch helm charts
  - 'dump app' optionally expands charts
- Rework -o on render and dump
- A template function to redirect images
- Add a templateHeader in application.module
- Store parameters in status
- rename SpecAddonByModule to SpecPatchByModule
- Implements Roles dependencies
  - Roles/DependsOn as template
  - Rename one of the dependsOn
  - Implementation
- Helm chart: Set the contexts in a specific namespace
- Handle context.protected
- Helm chart setup Config permissions
- Default context in global config
- A 'Waiting' column in release for dependencies




