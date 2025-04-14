
# TODO

- On image and kuboApp redirection, a list of pass through (exception). Use case, initial cert-manager release, when there is still no ca.crt
- Improve usage on kubocd CLI (Add sample)


## Later

- Implement protected fallback in case there is no webhook (break helmRelease ownership ?)
- make kubocd pack *.yaml working (Loop on args). (May need to patch https://github.com/mittwald/go-helm-client. See note in cmd.package.go)


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
- On chart pack, perform an helm dependency to fetch inner charts
- A --chart option on kubocd dump helmRepository to dump the chart
- A --chart option on kubocd dump oci to dump the chart if one is present
- Make Release.parameters a template
- if a Context named 'context' exits in a namespace, it will be used in all Release, after a global default one. (Except SkipDefaultContext)
- dump context command
- Rename Application to Package
- Set the groomed application descriptor on manifest config
- Application layout should be more different of k8s resources
- schema.parameters and schema.context
- Usage is now a map, to have several context dependent version (ie: text, html,...)
- Release description should default to the (templated) application description.
- Debug 'on reconcile error:the object has been modified....'
- Make info message more relevant
- CLI - render command: Add helm templating (Or provide a script to launch helm template ?)

# Mid term roadmap 

- Doc

- Handle right management. See what fluxcd does (Impersonation)

- Manage application in application.
  NB: This may seems redundant with the appsOfApps/stack pattern.
  But handling this in an integrated way will allow better control of generated helm release. and an efficient 'render'
  cli command

- Embed application image to the package

- A webhook to patch all image in pod manifests (https://slack.engineering/simple-kubernetes-webhook/)



