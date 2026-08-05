# Types de paramètres connectionRef et connectionSelector

Deux types de schéma génèrent les entrées d'input à la place de la stanza `inputs:` et reçoivent l'objet résolu SUR
PLACE, au point de déclaration. Les templates ne manipulent que `.Parameters` / `.Context`, jamais `.Inputs` (réservé à
la stanza).

## connectionRef : câblage nommé

```yaml
schema:
  parameters:
    properties:
      metadataDb: { type: connectionRef, interface: database-server, required: true }
# Release :
#   parameters:
#     metadataDb: kcd-postgres-superset
# Template : {{ .Parameters.metadataDb.host }}   (les values, à plat)
```

- Dans un array d'items : une entrée d'input générée PAR ÉLÉMENT, résolution sur place dans l'élément
  (`range .Parameters.datasources` puis `.trino.host`, les champs voisins restent intacts).
- Dans `schema.context` : variable de plateforme. Le NOM vient du Context, la Release n'écrit rien et ne peut pas
  surcharger. Template : `{{ .Context.platform.connections.oidc.issuerUri }}`.
- `default` : uniquement une string templatée, rendue contre le Context
  (`default: "{{ .Context.platform.defaults.oidc }}"`). Un défaut littéral est rejeté au groom. Interdit en
  `schema.context`.
- Gating : une connexion nommée absente ou non-READY met la release en WAIT_ICNX, réveil à sa création (watch). Une
  interface qui ne correspond pas est une erreur.

## connectionSelector : requête

```yaml
schema:
  parameters:
    properties:
      databases:
        type: connectionSelector
        interface: database-server # requis
        matchLabels: { backup: enabled } # optionnel, labels k8s des Connections
        kind: Connection # optionnel
# Release : rien, la requête vit dans le package (une valeur fournie = erreur)
# Template : {{ range .Parameters.databases }}{{ .host }}{{ end }}
```

- La liste résolue est triée par `priority` décroissante puis nom croissant.
- `required: true` = la liste ne doit pas être vide (sinon WAIT_ICNX). `required: false` + aucun match = liste vide
  substituée.
- Interdit dans les arrays et dans `schema.context`.

## Identité et statut

L'alias interne d'un input généré est le chemin de sa déclaration (`parameters.datasources[1].trino`,
`context.platform.connections.oidc`), visible dans `status.watchedInputConnections`, `status.effectiveInputConnections`
et les messages WAIT_ICNX. Une collision avec un alias de la stanza `inputs:` est une erreur. Les entrées générées
n'apparaissent pas dans `.Inputs`/`.InputLists`.
