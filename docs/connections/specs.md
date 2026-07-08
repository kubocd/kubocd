# KuboCD Connections

<!-- START doctoc generated TOC please keep comment here to allow auto update -->
<!-- DON'T EDIT THIS SECTION, INSTEAD RE-RUN doctoc TO UPDATE -->

## Index

- [Introduction](#introduction)
- [Resources](#resources)
  - [Connection.kubocd.kubotal.io](#connectionkubocdkubotalio)
  - [ClusterConnection.kubocd.kubotal.io](#clusterconnectionkubocdkubotalio)
  - [Interface](#interface)
  - [output](#output)
  - [input](#input)
  - [NamespacedInterface](#namespacedinterface)
  - [Usage](#usage)
  - [Connection Binding](#connection-binding)
  - [Namespaces, RBAC et multitenancy](#namespaces-rbac-et-multitenancy)
  - [Connections managées vs autonomes](#connections-manag%C3%A9es-vs-autonomes)
  - [Connection validation](#connection-validation)
  - [DisplayName](#displayname)
  - [Outillage](#outillage)
- [Examples](#examples)
  - [Exemple 1: Traefik](#exemple-1-traefik)
  - [Example 2 : Connexion SGBD](#example-2--connexion-sgbd)
  - [Example 3 : Multi connexions](#example-3--multi-connexions)
  - [Example 4 : DEX](#example-4--dex)
  - [Example 5 : Backup](#example-5--backup)
- [Still TODO](#still-todo)

<!-- END doctoc generated TOC please keep comment here to allow auto update -->

## Introduction

Une connection est un ensemble de données qu'expose une application à l'attention d'une ou plusieurs autres
applications.

Généralement, une Connection est un objet créé par une application offrant un service (Service Provider) et utilisé par
une ou plusieurs applications consommatrices (Service Consumer). La Connection définit le type de service offert et les
paramètres d'accès.

A l'inverse, une Connection peut définir une demande de service. Par exemple, une demande de backup de données, avec les
paramètres permettant d'y accéder. Charge à l'application de backup d'intégrer cette demande.

Le type de service définit un schéma de données, qui en assure la bonne interopérabilité entre des services
indépendants.

Les différents constituants du système sont décrits ci-après de manière 'brut'. L'usage des différents attributs est
ensuite détaillé par aspect.

## Resources

### Connection.kubocd.kubotal.io

```
apiVersion: kubocd.kubotal.io/v2alpha1
kind: Connection
metadata:
  name: <string>
  namespace: <string>
spec:
  interface: <string>  # Required
  priority: <int> # Optionnal
  values: <map[string]interface{}>
  enabled: <bool> # default true
  description: <string>

```

- Une Connection est une resource K8S namespaced.
- Le type de service fourni est défini par référence a une 'Interface'. Cette interface défine le schémas des 'values'
  fournis.
- Une Connection peut être créée explicitement, ou bien automatiquement, lors du déploiement d'une Instance KuboCD

### ClusterConnection.kubocd.kubotal.io

```
apiVersion: kubocd.kubotal.io/v2alpha1
kind: ClusterConnection
metadata:
  name: <string>
spec:
  interface: <string>  # Required
  priority: <int> # Optionnal
  values: <map[string]interface{}
  enabled: <bool> # default true
  description: <string>

```

- Une ClusterConnection est analogue à une Connection, mais elle permet de décrire un service disponible par tous les
  namespaces du cluster.
- Le type de service fourni est défini par référence a une 'Interface'. Cette interface défine le schémas des 'values'
  fournis.
- Une ClusterConnection peut être définie explicitement, ou bien lors du déploiement d'une Instance KuboCD

### Interface

Le Interface permet de décrire le schéma auquel doit se conformer l'attribut 'values' d'un `output`. An Interface is a
cluster resource (non namespaced)

```
apiVersion: kubocd.kubotal.io/v2alpha1
kind: Interface
metadata:
  name: <string>  # Le nom k8s est le nom de l'interface
  namespace: <string>
spec:
  allowMultiple: <bool> # Optional. If false, error in case of multiple providers on a binding. Default false
  description: <string>
  schema: # <schema_json_or_kubocd>
    properties:
      ....
```

### output

Il s'agit d'un élément de définition d'un package, au même titre que `components`, ou `schemas`.

Il permet de définir une ou plusieurs Connections qui seront générées lors du déploiement.

```
output:
  - name: <string>  # Required
    interface: <string> # required
    displayName: <template_string> # Optional. Default to .name
    namespace: <template_string>  # Optional. Default to <instance_namespace>
    priority: <template_int> # Optional. Default 100
    kind: <template_string> # Optional. Default: 'Connection'. May be 'Connection' ou 'ClusterConnection'
    description: <template_string> # Optional
    enabled: <templte_bool> # default true
    values: <template_map[string]interface{}>
```

NB: 'output' Pourrait aussi être nommé 'connectionTemplate'

### input

Il s'agit d'un élément de définition d'un package, au même titre que `components`, `schemas` ou `output`.

Il permet d'insérer dans le data model utilisé pour la résolution des 'values' les paramètres d'une connection
existante.

```
input:
  - interface: <template_string> # required
    connection:
      namespace: <template_string> # Default to release namespace
      fullName: <template_string> # k8s connection name. Mainly for unmanaged connection
      release: <template_string>   # The release managing this connection.
      outputName: <template_string> # Used if the release manage several connection with the same interface
      kind: <template_string> # Connection or ClusterConnection
    alias: <template_string> # optional. Default to interface

```

### NamespacedInterface

NB: Si le besoin s'en fait sentir, il serait possible de définir une alternative namespaced pour les Interface
(NamespacedInterface ?)

Dans ce cas, elle devra être référencée avec son namespace:

```
output:
  - name: <string>  # Required
    interface:  # Empty or ommited
    namespacedInterface:
      name: <string>
      namepace: <string>  # Default to referering Connection namespace
```

### Usage

### Connection Binding

Lors du déploiement d'une instance, les éléments de la liste 'input' sont insérés dans le data model, à l'emplacement
`.Input.<alias>.*`

C'est en fait un filtre permettant de sélectionner une ou plusieurs Connections. Seul l'élément 'interface' est
obligatoire.

- Une liste de connections est donc retournée, même si dans la plupart des cas, elle se réduira à un seul élément.
- Les ClusterConnections sont incluses
- Si input.namespaces est défini, les Connections trouvées dans ces namespace sont incluses
- Si input.namespaces n'est pas défini, les connection trouvées dans le namespace de la Release sont incluses.
- Les Connections dépendant d'une Instance (Connections Managées) sont éliminées si l'Instance n'est pas dans l'état
  READY.
- Les Connections restantes sont triées par priorité (alphabétique si même priorité), et celle ayant la plus haute est
  sélectionnée pour apparaitre dans le data model.
- Si la liste résultante est vide, alors le déploiement est mis en stand-bye (Ceci correspondant à la gestion des
  dépendances par les roles de KuboCD V1)

Pour des cas plus sophistiqués, le data model intègre aussi une entrée '.InputList.<alias>' qui comprend la liste de
toutes les Connections éligibles. Voir les examples.

### Namespaces, RBAC et multitenancy

Le multi-tenancy implique que les déploiements d'Instances soient effectués avec un ServiceAccount spécifique.

Un projet A porte des données et veut les rendre accessibles au projet B et C, mais pas aux autres projets:

2 Solutions:

- Le projet A génère des Connections dans les namespaces B et C. Ce qui implique que les namespaces B et C intègrent un
  ClusterRole permettant la création de Connections et lié au ServiceAccount de déploiement du projet A

- Le projet A génère sa Connection dans son namespace. Et aussi un ClusterRole permettant l'accès à cette connection (on
  pourra utiliser l'attribut 'resourceName'). Il faudra ensuite définir des binding sur les ServiceAccount des
  applications B et C

Il est aussi possible d'utiliser une ClusterConnection, mais au prix d'une perte de contrôle par le projet A de ses
accès.

### Connections managées vs autonomes

Les Connections (Ou ClusterConnections) peuvent être classées en deux catégories:

- Les connections autonomes, définies explicitement.
- Les connections managées, créées par les éléments de `output` des déploiements d'Instance.

Les secondes se caractérisent par

- L'attribut OwnerReference, pointant sur l'Instance ayant donné lieu à la création.
- Un nom intégrant le nom de la release, le nom de la connection et une valeur aléatoire, avec le pattern traditionnel
  K8S

Concernant les connections autonomes, il appartient aux différents administrateurs de s'assurer de l'unicité du nom.

### Connection validation

L'attribut value d'une Connection ou d'une ClusterConnection est donc validé par un schéma porté par un Interface.

Cette validation intervient :

- Pour les connections managées, lors du déploiement de l'instance, à la création des 'output'
- Dans un webhook pour les connections autonomes

La validation peut être supprimée par l'utilisation du flag 'skipValidation'. Dans ce cas, l'existence même d'une
resource Interface n'est pas requise. Le interface devient implicite.

### DisplayName

Chaque Connection/ClusterConnection peut optionnellement être dotée d'un attribut 'displayName'. Celui-ci est à l'usage
d'un éventuel Front End utilisateur gérant les déploiements, Il pourra ainsi présenter une liste de choix, le choix
sélectionné étant ensuite passé en paramètre du déploiement de l'Instance.

### Outillage

Il apparait nécéssaire de fournir une vision synthétique des connexions entre applications, des cas d'erreurs détectés,
etc... Ceci pouvant être obtenu de deux manières :

- Une API REST, permettant d'examiner la structure des objets tels qu'ils sont représentés en mémoire.
- Un outil CLI reconstituant les informations à partir des objets K8S.

La première solution nécéssite la mise en place d'une authentification et surtout d'un système de gestion des
habilitations, dupliquant RBAC.

La seconde solution, parce qu'elle s'appuie directement sur RBAC K8S parait à la fois plus simple et plus sure. (Elle
implique du partage de code entre CLI et controlleur, comme KuboCD)

## Examples

### Exemple 1: Traefik

Un déploiement de 'traefik', offrant un service 'IngressController' et un service 'Gateway API'.

En premier lieu, une ClusterConnection permettant de définir un environment global

```
---
apiVersion: kubocd.kubotal.io/v2alpha1
kind: Interface
metadata:
  name: environment
spec:
  schema:
    properties:
      domain: { type: string, required: true }


---
apiVersion: kubocd.kubotal.io/v2alpha1
kind: ClusterConnection
metadata:
  name: environment
spec:
  interface: environement
  values:
    domain: mycluster.mycompany.com

```

Le package de 'traefik'

```
apiVersion: v2alpha1
name: traefik
tag: 1.1.0-p01
schema:
  parameters:
    ...

components:
  - name: ......

output:

  - name: ingress
    displayName: Treafik ingress controller
    interface: ingress
    type: ClusterConnection
    values:
      domain: ingress.{{ .Input.env.domain }}
      className: traefik

  - name: gatewayApi
    interface: gateway-api
    displayName: Treafik gateway
    type: ClusterConnection
    values:
      domain: gapi.{{ .Input.env.domain }}
      parentRefs:
        name: traefik-gateway
        namespace: {{ .Instance.targetNamespace }}
        sectionNames:
          http: web
          https: webSecure
          passthrough: passthrough

input:
  - interface: environement
    alias: env

```

Les Interface correspondant

```
---
apiVersion: kubocd.kubotal.io/v2alpha1
kind: Interface
metadata:
  name: ingress
spec:
  schema:
    properties:
      domain: { type: string, required: true }
      className: { type: string, required: true }

---
apiVersion: kubocd.kubotal.io/v2alpha1
kind: Interface
metadata:
  name: gateway-api
spec:
  schema:
    properties:
      domain: { type: string, required: true }
      parentRefs:
        properties:
          name: { type: string, required: true }
          namespace: { type: string, required: true }
          sectionNames:
            properties:
              http: { type: string }
              https: { type: string }
              passthrough: { type: string }

```

Un package de podinfo utilisant l'ingress :

```
apiVersion: v2alpha1
type: Package
name: podinfo-ingress
tag: 6.7.1-p04
schema:
  parameters:
    properties:
      host: { type: string, required: true }
components:
  - name: main
    type: helm
    source:
      helmRepository:
        url: https://stefanprodan.github.io/podinfo
        chart: podinfo
        version: 6.7.1
    values: |
      ingress:
        enabled: true
        className: {{ .Input.ingress.className  }}
        hosts:
          - host: {{ .Parameters.host }}.{{ .Input.ingress.domain }}
            paths:
              - path: /
                pathType: ImplementationSpecific

input:
  - interface: ingress
```

Et une version utilisant GatewayApi

```
apiVersion: v2alpha1
type: Package
name: podinfo-gwapi
tag: 6.7.1-p04
schema:
  parameters:
    properties:
      host: { type: string, required: true }
components:
  - name: main
    type: helm
    source:
      helmRepository:
        url: https://stefanprodan.github.io/podinfo
        chart: podinfo
        version: 6.7.1
    values: |
      ingress:
        enabled: false

  -name: route
   type: manifest
   body:
    apiVersion: gateway.networking.k8s.io/v1
    kind: HTTPRoute
    metadata:
      name: {{ .Instance.metadat.name }}-route
    spec:
      parentRefs:
        - name: {{ .Input.gateway-api.parentRefs.name }}
          namespace: {{ .Input.gateway-api.parentRefs.namespace }}
          sectionName: {{ .Input.gateway-api.parentRefs.sectionNames.http }}
      hostnames:
        - {{ .Parameters.host }}.{{ .Input.gw.domain }}
      rules:
        - matches:
            - path:
                type: PathPrefix
                value: /
          backendRefs:
            - kind: Service
              name: podinfo
              port: 4040

input:
  - interface: gateway-api
```

### Example 2 : Connexion SGBD

Une application connectée à une DB PostgreSQL.

2 DB sont déployées, et le choix de la db est un paramètre de l'Instance

Les 2 DB et l'application sont déployées dans le même namespace (projet)

Pas de Interface pour cet exemple

```
---
apiVersion: v2alpha1
type: Package
name: pg-database
tag: 0.1.1-p1
schema:
  parameters:
    displayName: { type: string }
    password: { type: string, required: true }
  ....
components:
  .....

output:
  - name: db
    interface: database-pg
    displayName: Database {{ .Parameter.displayName | default .Instance.metadata.name }}
    values:
      host: {{ .Instance.targetNamespace }}-svc
      port: 5432
      dbName: app
      user: app-user
      password: {{ .Parameters.password }}
      skipValidation: true    # No Interface. Too lazy !!

```

Application utilisatrice (Consumer) :

```
apiVersion: v2alpha1
type: Package
name: app
tag: 0.1.1-p1
schema:
  parameters:
    database: { type: string }
  ....
components:
  type: helm
  .....
  values:
    db:
      host: {{ .Input.db.port }}
      dbName: {{ .Input.db.dbName }}
      user: {{ .Input.db.user }}
      password: {{ .Input.db.password }}

input:
  - connectionName: {{ .Parameters.database }}
    interface: database-pg
    alias: db

```

### Example 3 : Multi connexions

Pour cet example, imaginons que l'application utilisatrice de l'exemple précédant gère elle-même le choix par
l'utilisateur de la DB cible. Il faut alors lui fournir une liste de DB candidates.

```
apiVersion: v2alpha1
type: Package
name: app
tag: 0.1.1-p1
schema:
  parameters:
  ....
components:
  type: helm
  .....
  values:
    databases:
    {{- range .InputList.db}}
      - host: {{ .port }}
        dbName: {{ .dbName }}
        user: {{ .user }}
        password: {{ .password }}
    {{- end }}
input:
  - interface: database-pg
    alias: db

```

### Example 4 : DEX

Configuration de DEX

Ce Interface permet de définir ce qu'est un connecteur DEX:

```
---
apiVersion: kubocd.kubotal.io/v2alpha1
kind: Interface
metadata:
  name: dex-connector
spec:
  schema:
    properties:
      type: { type: string, required: true }
      name: { type: string, required: true }
      config:
        required: true
        additionalProperties: true
        properties: {}

```

Ce dessous, son utilisation dans le Package DEX:

```
---
apiVersion: v2alpha1
type: package
name: dex
tag: 0.23.0-p01
schema:
  ...
components:
  - name: main
    source:
      helmRepository:
        url: https://charts.dexidp.io
        chart: dex
        version: 0.23.0
    values: |
      ingress:
        ....
      config:
        issuer: ....
        .....
        connectors:
          {{- range $idx, $item := .InputList.dexConnector }}
          - type: {{ $item.type }}
            id: conn{{ $idx }}
            name: {{ $item.name }}
            config:
              {{ -toYaml $item.config | nindent 12 }}
          {{- end }}

input:
  - interface: dex-connector
    alias: dexConnector
```

Et un example de package Openldap, pour une configuration automatique de DEX:

```
---
apiVersion: v2alpha1
type: package
name: openldap
tag: 4.3.3-p02
protected: true
schema:
  .....
components:
  ......
output:
  - interface: dex-connector
    type: ClusterConnection
    values:
      type: ldap
      name: {{ .Release.metadata.name }}-ldap
      config:
        host: openldap.{{ .Release.targetNamespace}}.svc
        insecureNoSSL: false
        rootCaSecret: certs-bundle
        bindDN: cn=admin,dc=mycompany,dc=com
        bindPW: admin123
        groupSearch:
          baseDN: ou=Groups,dc=mycompany,dc=com
          filter: (objectClass=posixgroup)
          linkGroupAttr: memberUid
          linkUserAttr: uid
          nameAttr: cn
        timeoutSec: 10
        userSearch:
          baseDN: ou=Users,dc=mycompany,dc=com
          cnAttr: cn
          emailAttr: mail
          filter: (objectClass=inetOrgPerson)
          loginAttr: uid
          numericalIdAttr: uidNumber
```

### Example 5 : Backup

Une application de backup S3 est déployée. Les applications qui le souhaitent peuvent lui soumettre leur demande.
Celle-ci est structurée par un Interface:

```
---
apiVersion: kubocd.kubotal.io/v2alpha1
kind: Interface
metadata:
  name: backup-s3-request
spec:
  schema:
    properties:
      baseURL: { type: string, required: true }
      bucket: { type: string, required: true }
      path: { type: string }
      schedule: { type: string, enum: ["hourly", "dayly", .... ]
```

Un backup request sera donc défini sous forme de 'output'. Un example d'application ayant des données à sauvegarder:

```
apiVersion: v2alpha1
type: Package
name: app
tag: 0.1.1-p1
schema:
  parameters:
  ....
components:
  .....


output:
  - name: backup-request
    interface: backup-s3
    type: ClusterConnection
    values:
      baseURL: https://s3.mycompany.com
      bucket: bucket1
      path: # Empty: Full bucket
      schedule: dayly

```

Et un squelette de l'application de backup:

```
apiVersion: v2alpha1
type: Package
name: backup-app
tag: 0.1.1-p1
schema:
  parameters:
  ....
components:
  .....
  - name: main
    type: helm
    values:
      ,,,
      backupSpec:
      {{- range .InputList.bcks }}
        - baseURL: {{ .baseURL }}
          buckey: {{ .bucket }}
          path: {{ .path }}
          schedule: {{ .sc}}
      {{- end }}


input:
  - interface: backup-s3-request
    alias: bcks

```

## Still TODO

SECRET MANAGEMENT
