

# TODO

- Release description should default to the (templated) application description.  

- Translate all KAD components
- Doc
- Handle right management. See what fluxcd does (Impersonation)
- Improve usage on kubocd CLI (Add sample)

- Manage application in application.
  NB: This may seems redundant with the appsOfApps/stack pattern. 
  But handling this in an integrated way will allow better control of generated helm release. and an efficient 'render'
  cli command


- Implement protected fallback in case there is no webhook (break helmRelease ownership ?)

- A webhook to patch all image in pod manifests (https://slack.engineering/simple-kubernetes-webhook/)
- Embed application image to the package

- make kubocd pack *.yaml working (Loop on args). (May need to patch https://github.com/mittwald/go-helm-client. See note in cmd.package.go)

# Rejected
 
- Set Application in the model ?
- Dump chart content on kuboctl dump hr/oci ?

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
- Check path target a folder with Chart.yaml on module.source.git and module.source.local
- Make module.dependsOn a template
- Rename module.specAddon to module.specPatch
- Sandbox with bootstrap





