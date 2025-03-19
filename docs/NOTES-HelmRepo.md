
# Target

KuboCD main object is a "Application". 

An Application is made of one or several Helm Chart sharing some configuration and with eventual dependencies.

Aim is to make an Application an autonomous object, stored in a tar.gz file and an OCI image, with an adhoc format.

This means this OCI package will contains several HelmCharts.

This OCI package will be deployed in a cluster by a `Release` object. This `Release` will generate an `OCIRepository` 
FluxCD object to fetch the Application OCI image. 

The main issue is how to connect this `Release` to FluxCD. 

In practice, how to expose the KuboCD OCI package in a form which will allow the `HelmRelease` flux object 
to fetch  an Helm Chart embedded in the release ?

# Solution #1:

Make the `Release` object an 'artifact' server. And reference each artifact using  the `.spec.chartRef` of the HelmRelease.

Unfortunately, this will not work, as the `.spec.chartRef` of the HelmRelease is limited to reference only 
`OCIRepository` or `HelmChart` Flux CD object.

# Solution #2

In the KuboCD OCI image, we store each Helm chart in a separated layer. And, on `Release` deployment, we create an 
`OCIRepository` object for each chart. And then create also an `HelmRelease` for each chart, connected to these `OCIRepositories`.

Unfortunately, we got this message:

```
artifact revision X.Y.Z does not match chart version A.B.C
```

The problem is there is only one revision/version for the OCI image. And the `HelmRelease` want this to match the Chart 
version. This is not compatible with embedding several Chart in a single image.

# Solution #3

Our `Release` object act as an Helm Repository server, and an objet fluxCD `HelmRepository` is created, which than can 
be referenced by all generated `HelmRelease` 
