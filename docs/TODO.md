

# TODO

- Handle context.protected
- Add a templateHeader in application
- Implements Roles dependencies 
  - Roles/DependsOn as template ?
  - Rename one of the dependsOn
  - Implementation
- Helm chart setup Config permissions
- rename SpecAddonByModule to PatchSpecByModule

- Translate all KAD components
- Sandbox with bootstrap
- Doc

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
- 



