
# TODO


- pack on docker hub (docker.io) does not works.

- On image and kuboApp redirection, a list of pass through (exception). Use case, initial cert-manager release, when there is still no ca.crt

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
- CLI - render command: Add helm templating
- BUG: if a parameter is required and there is no parameters in release => no error generated (spec.parameters: {} fix it)
- BUG If schema.parameters is not defined, this means the Release should not accept parameters. But it does ! (cf kubocd-workbench/m48/kubo1/project1/cnpg.yaml)
- Add kcd- prefix as helmRelease name
- specPatch.timeout => timeout. And set default to 2mn
- defaultNamespaceContext should be a list
- kubocd render display expand chart 'podinfo' in .....
- hide kubocd dump oci
- kubocd dump package --charts: display expand chart 'podinfo' in .dump/podinfo/charts/main
- kubocd dump package podinfo-p01.yaml -> status.yaml is meaningless. Remove it
- Improve usage on kubocd CLI (Add sample)


# Mid term roadmap 

- Doc

- Handle right management. See what fluxcd does (Impersonation)

- Manage application in application.
  NB: This may seems redundant with the appsOfApps/stack pattern.
  But handling this in an integrated way will allow better control of generated helm release. and an efficient 'render'
  cli command
  Also, stack pattern issue: In case of deletion of child release, there is no auto-resync. Need to 

- Embed application image to the package

- A webhook to patch all image in pod manifests (https://slack.engineering/simple-kubernetes-webhook/)

# Useless idea

- A mode where helm chart is not embedded, referencing original. (Usage ?)
- Add package info in model


