# KuboCD

Most applications that can be deployed on Kubernetes come with a Helm chart. Moreover, this Helm chart is generally
highly flexible, designed to accommodate as many contexts as possible. This can make its configuration quite complex.

Furthermore, deploying an application on Kubernetes using an Helm Chart requires a deep understanding of the Kubernetes
ecosystem. As a result, application deployment is typically the responsibility of platform administrators or platform engineers.

And even for experienced administrators, the verbosity of Helm configurations, especially the repetition of variables,
can quickly become tedious and error-prone. Therefore, industrializing these configurations is crucial to improve
efficiency and reliability.

KuboCD is a tool that enables Platform Engineers to package applications in a way that simplifies deployment for other
technical users (such as Developers, AppOps, etc.) by abstracting most of the underlying infrastructure and environment complexities.

In addition to usual applications, KuboCD can also provision core system components (e.g., ingress controllers,
load balancers, Kubernetes operators, etc.), enabling fully automated bootstrapping of a production-ready cluster
from the ground up.

The documentation can be found [here](https://kubocd.github.io/kubocd-doc/)